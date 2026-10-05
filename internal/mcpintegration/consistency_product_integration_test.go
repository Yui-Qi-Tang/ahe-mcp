//go:build integration && consistencylab

package mcpintegration

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: only synthetic claims; actual launcher/MCP, SQL roles,
// compiled worker and pinned real SAT/DRAT processes execute the whole flow.
func TestIntegrationConsistencyProductRoundTrip(t *testing.T) {
	for _, key := range []string{"AHE_DBROLE_ACCEPTANCE_DATABASE_DSN", "AHE_CONSISTENCY_CADICAL", "AHE_CONSISTENCY_DRAT", "AHE_CORE_LAB_REPORT_DIR"} {
		if os.Getenv(key) == "" {
			t.Fatalf("prepared product suite requires %s", key)
		}
	}
	s := newCoreLabSession(t)
	defer s.finish(t)
	directory := provisioningProtectedDirectory(t, os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN"))
	worker := filepath.Join(directory, "ahe-consistency-worker")
	admin := filepath.Join(directory, "ahe-runtime-admin")
	for _, binary := range []string{worker, admin} {
		cmd := exec.CommandContext(s.ctx, "go", "build", "-o", binary, "../../cmd/"+filepath.Base(binary))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building product command: %v %s", err, output)
		}
	}
	artifactRoot := filepath.Join(os.Getenv("AHE_CORE_LAB_REPORT_DIR"), "worker-sessions")
	if err := os.Mkdir(artifactRoot, 0700); err != nil {
		t.Fatal(err)
	}
	writer := s.fixture.identities[dbrole.ProfileCoreRecords]
	env := []string{"PATH=" + os.Getenv("PATH"), "DATABASE_DSN=" + writer.dsn, "AHE_DATABASE_NAME=" + s.fixture.database, "AHE_DATABASE_LOGIN=" + writer.login, "AHE_DATABASE_ROLE=" + writer.group, "AHE_DATABASE_SCHEMA=" + s.fixture.schema, "GORACE=halt_on_error=1",
		"AHE_CONSISTENCY_CADICAL=" + os.Getenv("AHE_CONSISTENCY_CADICAL"), "AHE_CONSISTENCY_DRAT=" + os.Getenv("AHE_CONSISTENCY_DRAT"), "AHE_CONSISTENCY_CADICAL_SHA256=338a9bec6d24cdd04b117b63504e804f7ce4c0605370fe21cc5b8a98c25073df", "AHE_CONSISTENCY_DRAT_SHA256=6dfac4c795691b620c0d8adf359f67232e65e27782c9cd88e4d4d4d28342dc62", "AHE_CONSISTENCY_ARTIFACT_DIR=" + artifactRoot, "AHE_CONSISTENCY_INTERVAL=50ms"}
	calls := 0
	command := func(t *testing.T, operation string, wantOK bool, selected []string) []byte {
		t.Helper()
		calls++
		cmd := exec.CommandContext(s.ctx, worker, operation)
		cmd.Env = selected
		var out bytes.Buffer
		race := &authorityRaceOutput{}
		cmd.Stdout, cmd.Stderr = &out, race
		err := cmd.Run()
		if race.detected {
			t.Fatal("worker reported data race")
		}
		if (err == nil) != wantOK {
			t.Fatalf("worker %s success=%v, want %v (credential output suppressed)", operation, err == nil, wantOK)
		}
		// Only successful credential-free structured stdout is retained.
		if wantOK {
			if err := os.WriteFile(filepath.Join(artifactRoot, "command-"+stdioContentHash([]byte(out.String()))+".jsonl"), out.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return out.Bytes()
	}
	var identity struct {
		EngineID string `json:"engine_id"`
	}
	if err := json.Unmarshal(command(t, "identity", true, env), &identity); err != nil || identity.EngineID == "" {
		t.Fatal("worker identity missing")
	}
	scope := evidenceingestion.ConsistencyScope{Namespace: "product-consistency", ScopeRef: "door", Policy: evidenceingestion.ConsistencyProfile, MaxNodes: 256, MaxEdges: 4096}
	var nodes []string
	var keys []evidenceingestion.PropositionKey
	bind := func(t *testing.T, id string) string {
		t.Helper()
		node := s.claim(t, id)
		key := evidenceingestion.PropositionKey{Namespace: scope.Namespace, ScopeRef: scope.ScopeRef, LocalID: id, Revision: "1"}
		sixCaseCall[evidenceingestion.PropositionBindingReceipt](t, s.recorder, &s.trace, "bind_canonical_proposition", map[string]any{"request_id": "bind-" + id, "node_id": node, "key": key, "definition": "Synthetic exact Boolean declaration " + id, "decision_reason": "TEST APPROVAL STUB"})
		keys = append(keys, key)
		return node
	}
	for _, id := range []string{"door-open", "door-locked", "not-both"} {
		nodes = append(nodes, bind(t, id))
	}
	atom := func(property string, negated bool) evidenceingestion.ConsistencyLiteral {
		return evidenceingestion.ConsistencyLiteral{Atom: evidenceingestion.ConsistencyAtom{Subject: "door", Property: property, Context: "same-instant"}, Negated: negated}
	}
	evidence := []evidenceingestion.ConsistencyCondition{
		{ID: nodes[0], Clauses: [][]evidenceingestion.ConsistencyLiteral{{atom("open", false)}}},
		{ID: nodes[1], Clauses: [][]evidenceingestion.ConsistencyLiteral{{atom("locked", false)}}},
		{ID: nodes[2], Clauses: [][]evidenceingestion.ConsistencyLiteral{{atom("open", true), atom("locked", true)}}},
	}
	watch := map[string]any{"contract": "consistency-watch/v1", "watch_id": "door-watch", "request_id": "config-1", "expected_revision": 0, "engine_id": identity.EngineID, "max_localization_calls": 64, "paused": false, "request": map[string]any{"scope": scope, "rule_version": "frozen-1", "rules": []evidenceingestion.ConsistencyCondition{}, "evidence": evidence}}
	register := func(t *testing.T) evidenceingestion.ConsistencyWatchReceipt {
		return sixCaseCall[evidenceingestion.ConsistencyWatchReceipt](t, s.recorder, &s.trace, "register_consistency_watch", watch)
	}
	events := func(t *testing.T, after int64, limit int) evidenceingestion.ConsistencyEvents {
		return sixCaseCall[evidenceingestion.ConsistencyEvents](t, s.query, &s.trace, "get_consistency_events", map[string]any{"watch_id": "door-watch", "after": after, "limit": limit})
	}
	read := func(t *testing.T, id string) evidenceingestion.ConsistencyRunRead {
		return sixCaseCall[evidenceingestion.ConsistencyRunRead](t, s.query, &s.trace, "get_consistency_run", map[string]any{"run_id": id})
	}
	latest := func(t *testing.T) evidenceingestion.ConsistencyRunRead {
		t.Helper()
		es := events(t, 0, 256)
		for i := len(es.Events) - 1; i >= 0; i-- {
			if es.Events[i].Kind == "completed" {
				return read(t, es.Events[i].RunID)
			}
		}
		t.Fatal("no persisted completion")
		return evidenceingestion.ConsistencyRunRead{}
	}
	var initial evidenceingestion.ConsistencyRunRead
	t.Run("MCP_scope_registration_and_authority", func(t *testing.T) {
		view := sixCaseCall[evidenceingestion.ConsistencyView](t, s.query, &s.trace, "get_consistency_scope", scope)
		if len(view.Members) != 3 {
			t.Fatal("scope incomplete")
		}
		s.query.assertDenied(t, "register_consistency_watch", watch)
		s.intake.assertDenied(t, "register_consistency_watch", watch)
		receipt := register(t)
		if receipt.Revision != 1 || receipt.Replayed {
			t.Fatal("initial revision")
		}
		forged := map[string]any{}
		for k, v := range watch {
			forged[k] = v
		}
		forged["recorded_by"] = "forged"
		s.rejected(t, "register_consistency_watch", forged)
		got := sixCaseCall[evidenceingestion.ConsistencyWatchRead](t, s.query, &s.trace, "get_consistency_watch", map[string]any{"watch_id": "door-watch", "revision": -1})
		if got.Configuration.RecordedBy != "mock:core-lab:core-records" {
			t.Fatal("launcher identity lost")
		}
	})
	t.Run("compiled_worker_detects_three_way_conflict", func(t *testing.T) {
		command(t, "once", true, env)
		initial = latest(t)
		if initial.Run.Diagnosis.Outcome != evidenceingestion.ConsistencyConflict || initial.Freshness != "matches_snapshot" {
			t.Fatal("conflict missing", initial.Run.Diagnosis.Outcome, initial.Freshness)
		}
		before := events(t, 0, 256)
		command(t, "once", true, env)
		after := events(t, 0, 256)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("unchanged restart created another result")
		}
	})
	t.Run("event_pagination_and_immutable_artifact_chunks", func(t *testing.T) {
		all := events(t, 0, 256)
		var joined []evidenceingestion.ConsistencyEvent
		cursor := int64(0)
		for {
			page := events(t, cursor, 1)
			joined = append(joined, page.Events...)
			cursor = page.NextCursor
			if !page.Truncated {
				break
			}
		}
		if !reflect.DeepEqual(joined, all.Events) {
			t.Fatal("pagination lost events")
		}
		if len(initial.Run.Artifacts) == 0 {
			t.Fatal("no persisted solver artifacts")
		}
		a := initial.Run.Artifacts[0]
		var data []byte
		offset := int64(0)
		for {
			chunk := sixCaseCall[struct {
				SHA256 string `json:"sha256"`
				Data   []byte `json:"data_base64"`
				Next   int64  `json:"next_offset"`
				Done   bool   `json:"done"`
			}](t, s.query, &s.trace, "get_consistency_artifact", map[string]any{"run_id": initial.ID, "artifact_id": a.ID, "offset": offset, "limit": 17})
			if chunk.SHA256 != a.SHA256 {
				t.Fatal("digest identity drift")
			}
			data = append(data, chunk.Data...)
			offset = chunk.Next
			if chunk.Done {
				break
			}
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			t.Fatal("artifact reassembly invalid")
		}
	})
	t.Run("withdrawal_marks_old_run_stale_and_recomputes", func(t *testing.T) {
		sixCaseCall[evidenceingestion.PropositionChangeReceipt](t, s.recorder, &s.trace, "change_proposition_binding", map[string]any{"request_id": "withdraw-not-both", "node_id": nodes[2], "expected_revision": 0, "previous_ref": "membership:bind-not-both", "from_id": keys[2].ID(), "operation": "withdraw", "reason": "TEST APPROVAL STUB", "evidence_ref": "synthetic:withdraw"})
		if read(t, initial.ID).Freshness != "stale" {
			t.Fatal("old diagnosis remained current")
		}
		command(t, "once", true, env)
		if latest(t).Run.Diagnosis.Outcome != evidenceingestion.ConsistencyCompatible {
			t.Fatal("withdrawal not recomputed")
		}
	})
	t.Run("new_evidence_blocks_until_MCP_configuration_supplies_it", func(t *testing.T) {
		extra := bind(t, "window-open")
		command(t, "once", true, env)
		if latest(t).Run.Diagnosis.Reason != "missing_normalization" {
			t.Fatal("missing input treated as compatible")
		}
		evidence = append(evidence, evidenceingestion.ConsistencyCondition{ID: extra, Clauses: [][]evidenceingestion.ConsistencyLiteral{{atom("window", false)}}})
		watch["request"].(map[string]any)["evidence"] = evidence
		watch["expected_revision"] = 1
		watch["request_id"] = "config-2"
		if register(t).Revision != 2 {
			t.Fatal("revision not advanced")
		}
		command(t, "once", true, env)
		if latest(t).Run.Diagnosis.Outcome != evidenceingestion.ConsistencyCompatible {
			t.Fatal("supplement did not recover")
		}
	})
	t.Run("bad_engine_nonzero_pause_and_public_recovery", func(t *testing.T) {
		watch["expected_revision"] = 2
		watch["request_id"] = "config-bad-engine"
		watch["engine_id"] = "wrong-engine"
		register(t)
		command(t, "once", false, env)
		watch["expected_revision"] = 3
		watch["request_id"] = "config-paused"
		watch["paused"] = true
		register(t)
		command(t, "once", true, env)
		watch["expected_revision"] = 4
		watch["request_id"] = "config-recovered"
		watch["paused"] = false
		watch["engine_id"] = identity.EngineID
		register(t)
		command(t, "once", true, env)
		if latest(t).Run.Revision != 5 {
			t.Fatal("public recovery failed")
		}
		old := sixCaseCall[evidenceingestion.ConsistencyWatchRead](t, s.query, &s.trace, "get_consistency_watch", map[string]any{"watch_id": "door-watch", "revision": 1})
		if old.Receipt.Revision != 1 || old.CurrentRevision != 5 {
			t.Fatal("configuration history lost")
		}
		changed := map[string]any{}
		for k, v := range watch {
			changed[k] = v
		}
		changed["expected_revision"] = 0
		changed["request_id"] = "stale-write"
		s.rejected(t, "register_consistency_watch", changed)
	})
	t.Run("worker_rejects_query_role", func(t *testing.T) {
		query := s.fixture.identities[dbrole.ProfileQuery]
		other := append([]string(nil), env...)
		for i, v := range other {
			switch {
			case len(v) >= 13 && v[:13] == "DATABASE_DSN=":
				other[i] = "DATABASE_DSN=" + query.dsn
			case len(v) >= 19 && v[:19] == "AHE_DATABASE_LOGIN=":
				other[i] = "AHE_DATABASE_LOGIN=" + query.login
			case len(v) >= 18 && v[:18] == "AHE_DATABASE_ROLE=":
				other[i] = "AHE_DATABASE_ROLE=" + query.group
			}
		}
		command(t, "once", false, other)
	})
	t.Run("long_running_worker_SIGTERM_exits_cleanly", func(t *testing.T) {
		cmd := exec.CommandContext(s.ctx, worker, "run")
		cmd.Env = env
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		race := &authorityRaceOutput{}
		cmd.Stderr = race
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		scanner := bufio.NewScanner(pipe)
		if !scanner.Scan() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("worker did not report startup")
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		go func() {
			for scanner.Scan() {
			}
			done <- cmd.Wait()
		}()
		select {
		case err := <-done:
			if err != nil || race.detected {
				t.Fatal("worker did not stop cleanly")
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Fatal("worker cancellation did not join")
		}
	})
	t.Run("admin_upgrade_preserves_roles_and_rejects_wrong_pair", func(t *testing.T) {
		cfg, err := pgxpool.ParseConfig(os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN"))
		if err != nil {
			t.Fatal(err)
		}
		operator := provisioningExplicitURL(cfg, s.fixture.database, cfg.ConnConfig.User, cfg.ConnConfig.Password)
		ids := []string{writer.group, writer.login}
		snapshot := func() string {
			var body string
			if err := s.fixture.pool.QueryRow(s.ctx, `SELECT json_agg(r ORDER BY oid)::text FROM (SELECT oid,rolname,rolcanlogin,rolinherit,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls FROM pg_roles WHERE rolname=ANY($1::text[])) r`, ids).Scan(&body); err != nil {
				t.Fatal(err)
			}
			return body
		}
		before := snapshot()
		if _, err := s.fixture.pool.Exec(s.ctx, "REVOKE SELECT ON consistency_events FROM "+pgx.Identifier{writer.group}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		wrong := writer
		wrong.login = s.fixture.identities[dbrole.ProfileQuery].login
		runProvisioningAdmin(t, s.ctx, admin, "upgrade", operator, s.fixture.database, s.fixture.schema, wrong, false)
		var granted bool
		if err := s.fixture.pool.QueryRow(s.ctx, `SELECT has_table_privilege($1,'consistency_events','SELECT')`, writer.group).Scan(&granted); err != nil || granted {
			t.Fatal("wrong pair changed ACL")
		}
		for i := 0; i < 2; i++ {
			runProvisioningAdmin(t, s.ctx, admin, "upgrade", operator, s.fixture.database, s.fixture.schema, writer, true)
			runProvisioningAdmin(t, s.ctx, admin, "verify", writer.dsn, s.fixture.database, s.fixture.schema, writer, true)
		}
		if snapshot() != before {
			t.Fatal("upgrade replaced or mutated identities")
		}
	})
}
