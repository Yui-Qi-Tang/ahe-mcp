//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type liveClient struct {
	process *authorityProcess
	name    string
	tools   map[string]mcpstdio.Tool
}

type liveReply struct {
	IsError bool            `json:"isError"`
	Content json.RawMessage `json:"structuredContent"`
}

type liveWitness struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	trace []any
}

// begin writes every request before any response is consumed. Each client has
// its own stdio transport, MCP process and LOGIN-bound PG sessions.
func (w *liveWitness) begin(t *testing.T, clients []*liveClient, tool string, args []any) {
	t.Helper()
	if len(args) != len(clients) {
		t.Fatal("one argument set is required per client")
	}
	for i, c := range clients {
		if _, ok := c.tools[tool]; !ok {
			t.Fatalf("client %s did not advertise %s", c.name, tool)
		}
		c.process.nextID++
		request := map[string]any{"jsonrpc": "2.0", "id": c.process.nextID, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args[i]}}
		w.trace = append(w.trace, map[string]any{"client": c.name, "request": request})
		if err := c.process.input.Encode(request); err != nil {
			t.Fatal("cannot send live client request")
		}
	}
}

func (w *liveWitness) receive(t *testing.T, clients []*liveClient) []liveReply {
	t.Helper()
	var replies []liveReply
	for _, c := range clients {
		select {
		case body, ok := <-c.process.responses:
			var response authorityProcessResponse
			if !ok || json.Unmarshal(body, &response) != nil || response.JSONRPC != "2.0" || response.ID != c.process.nextID || response.Error != nil {
				t.Fatalf("client %s transport failed", c.name)
			}
			var reply liveReply
			if json.Unmarshal(response.Result, &reply) != nil || len(reply.Content) == 0 {
				t.Fatal("missing structured tool response")
			}
			w.trace = append(w.trace, map[string]any{"client": c.name, "response": response})
			replies = append(replies, reply)
		case <-w.ctx.Done():
			t.Fatal("live client response exceeded workflow deadline")
		}
	}
	return replies
}

func liveValue[T any](t *testing.T, r liveReply) T {
	t.Helper()
	var out T
	if r.IsError || json.Unmarshal(r.Content, &out) != nil {
		t.Fatalf("unexpected tool failure or invalid response: %s", r.Content)
	}
	return out
}

func liveRepeated(value any, n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = value
	}
	return args
}

func (w *liveWitness) call(t *testing.T, c *liveClient, tool string, args any) liveReply {
	t.Helper()
	w.begin(t, []*liveClient{c}, tool, []any{args})
	return w.receive(t, []*liveClient{c})[0]
}

// block holds only a test lock, never writes evidence. The caller waits for a
// distinct backend bearing each exact client's label before releasing it.
func (w *liveWitness) block(t *testing.T, table string) (uint32, func(bool)) {
	t.Helper()
	conn, err := w.pool.Acquire(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(w.ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	released := false
	release := func(kill bool) {
		if released {
			return
		}
		released = true
		defer conn.Release()
		if kill {
			var stopped bool
			if err := w.pool.QueryRow(w.ctx, "SELECT pg_terminate_backend($1)", conn.Conn().PgConn().PID()).Scan(&stopped); err != nil || !stopped {
				t.Fatal("cannot terminate owned barrier backend")
			}
			return // Server termination rolls back the lock-only transaction.
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(ctx); err != nil {
			t.Error("cannot release owned barrier transaction")
		}
	}
	t.Cleanup(func() { release(false) })
	if _, err := tx.Exec(w.ctx, "LOCK TABLE "+pgx.Identifier{table}.Sanitize()+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		release(false)
		t.Fatal(err)
	}
	return conn.Conn().PgConn().PID(), release
}

func (w *liveWitness) waiting(t *testing.T, clients []*liveClient, blocker uint32) map[string]uint32 {
	t.Helper()
	labels := make([]string, len(clients))
	for i, c := range clients {
		labels[i] = c.name
	}
	ctx, cancel := context.WithTimeout(w.ctx, 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Exact replays may queue behind a peer's advisory lock. Follow the
		// wait chain back to our barrier instead of requiring a direct wait.
		rows, err := w.pool.Query(ctx, `WITH RECURSIVE blocked(pid) AS (
			SELECT $2::int UNION SELECT a.pid FROM pg_stat_activity a
			JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
		) SELECT usename,pid,pg_blocking_pids(pid) FROM pg_stat_activity
			WHERE datname=current_database() AND usename=ANY($1::text[])
			AND state='active' AND wait_event_type='Lock' AND pid IN (SELECT pid FROM blocked)`, labels, blocker)
		if err != nil {
			t.Fatal("cannot observe expected concurrent waiters")
		}
		pids := make(map[string]uint32)
		chains := make(map[string][]int32)
		for rows.Next() {
			var name string
			var pid uint32
			var blockers []int32
			if err := rows.Scan(&name, &pid, &blockers); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if _, duplicate := pids[name]; duplicate {
				rows.Close()
				t.Fatal("more than one waiting request for a client")
			}
			pids[name] = pid
			chains[name] = blockers
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(pids) == len(clients) {
			w.trace = append(w.trace, map[string]any{"barrier": t.Name(), "blocker": blocker, "client_backends": pids, "waiting_on": chains})
			t.Logf("observed %d distinct client backends waiting together", len(pids))
			return pids
		}
		select {
		case <-ctx.Done():
			t.Fatalf("only %d/%d clients reached the PG barrier", len(pids), len(clients))
		case <-ticker.C:
		}
	}
}

func (w *liveWitness) concurrent(t *testing.T, clients []*liveClient, table, tool string, args []any) []liveReply {
	t.Helper()
	pid, release := w.block(t, table)
	defer release(false)
	w.begin(t, clients, tool, args)
	w.waiting(t, clients, pid)
	release(false)
	return w.receive(t, clients)
}

// TEST APPROVAL STUB: synthetic claims only. The standard shipping launcher,
// profiles and native exact-review flow run in a separately selected database.
func TestIntegrationMultiClientLiveWorkflow(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	dir := provisioningProtectedDirectory(t, dsn)
	binaries := buildProvisioningCommands(t, ctx, dir)
	fixture := newProvisioningLauncherFixture(t, ctx, dsn, binaries["ahe-runtime-admin"])
	w := &liveWitness{ctx: ctx, pool: fixture.pool}
	t.Cleanup(func() {
		if out := os.Getenv("AHE_MULTICLIENT_REPORT_DIR"); out != "" {
			body, err := json.MarshalIndent(w.trace, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(filepath.Join(out, fixture.schema+".json"), body, 0600); err != nil {
				t.Error(err)
			}
		}
	})
	start := func(profile dbrole.Profile, index int) *liveClient {
		identity := fixture.identities[profile]
		name := fmt.Sprintf("live_%s_%d_%s", strings.ReplaceAll(string(profile), "-", "_"), index, strings.TrimPrefix(fixture.schema, "ahe_launch_"))
		// Separate bounded LOGINs map database waiters to clients without
		// changing the launcher's closed credential-parameter contract.
		role := pgx.Identifier{name}.Sanitize()
		if _, err := fixture.pool.Exec(ctx, "CREATE ROLE "+role+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"); err != nil {
			t.Fatal("cannot create owned client login")
		}
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if _, err := fixture.pool.Exec(cleanupCtx, "DROP ROLE "+role); err != nil {
				t.Error("cannot remove owned client login")
			}
		})
		if _, err := fixture.pool.Exec(ctx, "GRANT "+pgx.Identifier{identity.group}.Sanitize()+" TO "+role+" WITH INHERIT FALSE, SET TRUE"); err != nil {
			t.Fatal("cannot bind client to its existing bounded profile")
		}
		parsed, err := url.Parse(identity.dsn)
		if err != nil {
			t.Fatal("invalid test credential")
		}
		parsed.User = url.UserPassword(name, stdioRandomHex(t, 24))
		credential, config := filepath.Join(dir, name+".dsn"), filepath.Join(dir, name+".json")
		writeProvisioningProtectedFile(t, credential, []byte(parsed.String()))
		command := "ahe-ingest-mcp"
		if profile == dbrole.ProfileQuery {
			command = "ahe-query-mcp"
		}
		writeProvisioningConfig(t, config, provisioningLauncherConfig{
			SchemaVersion: "ahe-mcp-launcher/v1", BinaryPath: binaries[command], DatabaseDNSFile: credential,
			Database: fixture.database, SessionUser: name, Schema: fixture.schema,
			Role: identity.group, Profile: string(profile), PrincipalID: "mock:multiclient:" + string(profile),
		})
		c := &liveClient{process: startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], config, command), name: name, tools: make(map[string]mcpstdio.Tool)}
		listed := c.process.request(t, "tools/list", map[string]any{})
		var result struct {
			Tools []mcpstdio.Tool `json:"tools"`
		}
		if listed.Error != nil || json.Unmarshal(listed.Result, &result) != nil || len(result.Tools) == 0 {
			t.Fatal("cannot discover live client schemas")
		}
		for _, tool := range result.Tools {
			if tool.InputSchema == nil {
				t.Fatal("missing live input schema")
			}
			c.tools[tool.Name] = tool
		}
		w.trace = append(w.trace, map[string]any{"client": name, "profile": profile, "tools": result.Tools})
		return c
	}
	const clients = 4
	groups := make(map[dbrole.Profile][]*liveClient)
	for _, profile := range []dbrole.Profile{dbrole.ProfileIntake, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileCoreRecords, dbrole.ProfileQuery} {
		for i := range clients {
			groups[profile] = append(groups[profile], start(profile, i))
		}
	}
	intakes, reviewers, recorders, queries := groups[dbrole.ProfileIntake], groups[dbrole.ProfileSourceClaimReviewer], groups[dbrole.ProfileCoreRecords], groups[dbrole.ProfileQuery]
	check := func(name string, f func(*testing.T)) {
		if !t.Run(name, f) {
			t.FailNow()
		}
	}
	const text = "Synthetic evidence: refunds must retain the original receipt."
	var source evidenceingestionmcp.SubmitTextSourceResponse
	var proposals []evidenceingestionmcp.SubmitExtractorOutputResponse
	var admission evidenceingestionmcp.AdmitReviewedSourceClaimResponse
	check("fresh_source_and_concurrent_extractor_registration", func(t *testing.T) {
		source = liveValue[evidenceingestionmcp.SubmitTextSourceResponse](t, w.call(t, intakes[0], "submit_text_source", map[string]any{"request_id": "live-source", "source_id": "mock:live-refunds", "source_version": "1", "raw_text": text}))
		args := make([]any, clients)
		for i, c := range intakes {
			input := liveValue[evidenceingestionmcp.GetExtractorInputResponse](t, w.call(t, c, "get_extractor_input", map[string]any{"extraction_view_id": source.ExtractionViewID}))
			if input.RenderedText != text || input.RawContentHash != stdioContentHash([]byte(text)) || len(input.Spans) != 1 || input.Spans[0].Text != text {
				t.Fatal("source bytes or spans drifted")
			}
			args[i] = map[string]any{"request_id": fmt.Sprintf("live-output-%d", i), "source_snapshot_id": source.SourceSnapshotID, "extraction_view_id": source.ExtractionViewID,
				"extractor_definition": map[string]any{"name": "mock-multiclient", "version": "1", "config": map[string]string{}},
				"extractor_output":     map[string]any{"proposals": []any{map[string]any{"proposal_local_id": "claim", "statement_text": text, "evidence_refs": []string{input.Spans[0].SpanID}}}}}
		}
		seen := make(map[string]bool)
		for _, reply := range w.concurrent(t, intakes, "extractor_definitions", "submit_extractor_output", args) {
			p := liveValue[evidenceingestionmcp.SubmitExtractorOutputResponse](t, reply)
			if p.Status != "pending" || p.Replayed || p.ProposalCount != 1 || p.ProposalOccurrenceID == "" || seen[p.ProposalOccurrenceID] {
				t.Fatal("independent pending requests merged or failed")
			}
			seen[p.ProposalOccurrenceID] = true
			proposals = append(proposals, p)
		}
		for table, count := range map[string]int{"source_snapshots": 1, "extractor_definitions": 1, "extraction_runs": clients, "proposal_occurrences": clients, "canonical_graph_nodes": 0} {
			stdioAssertTableCount(t, ctx, fixture.pool, table, count)
		}
	})
	check("exact_review_concurrent_admission_commits_once", func(t *testing.T) {
		p := proposals[0]
		var args []any
		var expected evidenceingestionmcp.GetSourceClaimReviewResponse
		for i, c := range reviewers {
			r := liveValue[evidenceingestionmcp.GetSourceClaimReviewResponse](t, w.call(t, c, "get_source_claim_review", evidenceingestionmcp.GetSourceClaimReviewRequest{ExtractionAttemptID: p.ExtractionAttemptID, ProposalOccurrenceID: p.ProposalOccurrenceID}))
			if i == 0 {
				expected = r
			}
			if !reflect.DeepEqual(r.Subject, expected.Subject) || r.Display.PayloadUTF8 != expected.Display.PayloadUTF8 || !strings.Contains(r.Display.PayloadUTF8, text) {
				t.Fatal("review subject or source changed across clients")
			}
			args = append(args, evidenceingestionmcp.AdmitReviewedSourceClaimRequest{ExtractionAttemptID: p.ExtractionAttemptID, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: synthetic multi-client witness"})
		}
		fresh := 0
		for i, reply := range w.concurrent(t, reviewers, "proposal_occurrences", "admit_reviewed_source_claim", args) {
			a := liveValue[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reply)
			if !a.Replayed {
				fresh++
			}
			a.Replayed = false
			if i == 0 {
				admission = a
			}
			if a.CanonicalRef == "" || a.AdmissionOutcome != "admitted" || !reflect.DeepEqual(a, admission) {
				t.Fatal("exact admission identity differs across clients")
			}
		}
		if fresh != 1 {
			t.Fatal("expected exactly one fresh admission")
		}
		for table, count := range map[string]int{"canonical_graph_nodes": 2, "canonical_graph_edges": 1, "admission_decisions": 1, "canonical_source_claim_review_bindings": 1} {
			stdioAssertTableCount(t, ctx, fixture.pool, table, count)
		}
	})
	readArgs := map[string]any{"canonical_id": admission.CanonicalRef}
	assertRecord := func(t *testing.T, reply liveReply) {
		r := liveValue[evidencequerymcp.GetEvidenceRecordResponse](t, reply)
		if r.RecordRef.ID != admission.CanonicalRef || r.StatementText != text || r.ProposalOriginRef == nil || r.ProposalOriginRef.ID != proposals[0].ProposalOccurrenceID || len(r.SourceRefs) != 1 || r.SourceRefs[0].QuotedText != text || r.SourceRefs[0].QuotedTextHash != stdioContentHash([]byte(text)) {
			t.Fatal("Query lost exact admitted source or provenance")
		}
	}
	check("four_readers_observe_committed_source", func(t *testing.T) {
		for _, r := range w.concurrent(t, queries, "canonical_graph_nodes", "get_evidence_record", liveRepeated(readArgs, clients)) {
			assertRecord(t, r)
		}
	})
	key := evidenceingestion.PropositionKey{Namespace: "mock", LocalID: "refund-rule", ScopeRef: "live", Revision: "1"}
	bind := map[string]any{"request_id": "live-bind", "node_id": admission.CanonicalRef, "key": key, "definition": "Synthetic identity", "decision_reason": "TEST APPROVAL STUB"}
	check("concurrent_binding_replay", func(t *testing.T) {
		fresh := 0
		for _, r := range w.concurrent(t, recorders, "canonical_proposition_bindings", "bind_canonical_proposition", liveRepeated(bind, clients)) {
			v := liveValue[evidenceingestion.PropositionBindingReceipt](t, r)
			if !v.Replayed {
				fresh++
			}
			if v.PropositionID != key.ID() {
				t.Fatal("binding identity changed")
			}
		}
		if fresh != 1 {
			t.Fatal("binding replay did not converge")
		}
		stdioAssertTableCount(t, ctx, fixture.pool, "canonical_proposition_bindings", 1)
	})
	check("competing_withdrawals_have_one_revision_winner", func(t *testing.T) {
		var args []any
		for i := range clients {
			args = append(args, map[string]any{"request_id": fmt.Sprintf("live-withdraw-%d", i), "node_id": admission.CanonicalRef, "expected_revision": 0, "previous_ref": "membership:live-bind", "from_id": key.ID(), "operation": "withdraw", "reason": fmt.Sprintf("TEST APPROVAL STUB correction by contender %d", i), "evidence_ref": fmt.Sprintf("mock:correction-%d", i)})
		}
		winner := -1
		for i, r := range w.concurrent(t, recorders, "canonical_proposition_bindings", "change_proposition_binding", args) {
			if r.IsError {
				var e struct{ Code, Message string }
				if json.Unmarshal(r.Content, &e) != nil || e.Code != "tool_error" || !strings.Contains(e.Message, evidenceingestion.ErrPropositionBindingConflict.Error()) {
					t.Fatalf("unexpected contender error: %s", r.Content)
				}
				continue
			}
			v := liveValue[evidenceingestion.PropositionChangeReceipt](t, r)
			if winner != -1 || v.Replayed || v.Event.Revision != 1 || v.Event.Request.RequestID != fmt.Sprintf("live-withdraw-%d", i) {
				t.Fatal("more than one winner or incorrect event")
			}
			winner = i
		}
		if winner == -1 {
			t.Fatal("no valid contender committed")
		}
		for _, c := range recorders {
			if !liveValue[evidenceingestion.PropositionBindingReceipt](t, w.call(t, c, "bind_canonical_proposition", bind)).Replayed {
				t.Fatal("old binding receipt did not replay")
			}
		}
		for _, q := range queries {
			h := liveValue[evidenceingestion.PropositionBindingHistory](t, w.call(t, q, "get_proposition_binding_history", map[string]any{"node_id": admission.CanonicalRef, "revision": -1, "limit": 10}))
			if h.Active || h.PropositionID != "" || h.HeadRevision != 1 || len(h.Events) != 1 || h.HistoryTruncated || h.Events[0].Request.RequestID != fmt.Sprintf("live-withdraw-%d", winner) || h.Events[0].Request.Reason != fmt.Sprintf("TEST APPROVAL STUB correction by contender %d", winner) || h.Events[0].Request.EvidenceRef != fmt.Sprintf("mock:correction-%d", winner) {
				t.Fatal("withdrawal history was mixed or revived")
			}
			old := liveValue[evidenceingestion.PropositionBindingHistory](t, w.call(t, q, "get_proposition_binding_history", map[string]any{"node_id": admission.CanonicalRef, "revision": 0, "limit": 10}))
			if !old.Active || old.PropositionID != key.ID() || old.HeadRevision != 1 {
				t.Fatal("original binding history lost")
			}
			members := liveValue[evidenceingestion.PropositionMembers](t, w.call(t, q, "get_proposition_members", map[string]any{"key": key, "limit": 10}))
			if len(members.Nodes) != 0 || members.Truncated {
				t.Fatal("withdrawn binding remained current")
			}
		}
		stdioAssertTableCount(t, ctx, fixture.pool, "canonical_proposition_binding_events", 1)
	})
	check("lost_reader_connection_fails_then_same_transport_recovers", func(t *testing.T) {
		pid, release := w.block(t, "canonical_graph_nodes")
		defer release(false)
		w.begin(t, queries, "get_evidence_record", liveRepeated(readArgs, clients))
		backends := w.waiting(t, queries, pid)
		victim := backends[queries[0].name]
		var killed bool
		if err := fixture.pool.QueryRow(ctx, "SELECT pg_terminate_backend($1)", victim).Scan(&killed); err != nil || !killed {
			t.Fatal("cannot terminate owned reader backend")
		}
		w.trace = append(w.trace, map[string]any{"injected_reader_disconnect": queries[0].name, "backend": victim})
		release(false)
		for i, r := range w.receive(t, queries) {
			if i == 0 {
				if !r.IsError {
					t.Fatal("killed in-flight read reported success")
				}
				continue
			}
			assertRecord(t, r)
		}
		// This is an explicit new read on the same initialized stdio transports,
		// not a transparent retry, new launcher or replacement MCP process.
		for _, q := range queries {
			assertRecord(t, w.call(t, q, "get_evidence_record", readArgs))
		}
		pid, releaseAgain := w.block(t, "canonical_graph_nodes")
		defer releaseAgain(false)
		w.begin(t, queries, "get_evidence_record", liveRepeated(readArgs, clients))
		reopened := w.waiting(t, queries, pid)
		if reopened[queries[0].name] == victim {
			t.Fatal("terminated backend was not replaced")
		}
		releaseAgain(false)
		for _, r := range w.receive(t, queries) {
			assertRecord(t, r)
		}
	})
	check("lost_lock_holder_releases_all_readers", func(t *testing.T) {
		pid, release := w.block(t, "canonical_graph_nodes")
		defer release(false)
		w.begin(t, queries, "get_evidence_record", liveRepeated(readArgs, clients))
		w.waiting(t, queries, pid)
		w.trace = append(w.trace, map[string]any{"injected_lock_holder_disconnect": pid})
		release(true)
		for _, r := range w.receive(t, queries) {
			assertRecord(t, r)
		}
	})
	check("new_client_reads_committed_evidence_and_withdrawal", func(t *testing.T) {
		q := start(dbrole.ProfileQuery, clients)
		assertRecord(t, w.call(t, q, "get_evidence_record", readArgs))
		h := liveValue[evidenceingestion.PropositionBindingHistory](t, w.call(t, q, "get_proposition_binding_history", map[string]any{"node_id": admission.CanonicalRef, "revision": -1, "limit": 10}))
		if h.Active || h.HeadRevision != 1 || len(h.Events) != 1 {
			t.Fatal("new client lost committed withdrawal")
		}
		q.process.finish(t)
		for table, count := range map[string]int{"canonical_graph_nodes": 2, "canonical_graph_edges": 1, "admission_decisions": 1, "canonical_proposition_bindings": 1, "canonical_proposition_binding_events": 1} {
			stdioAssertTableCount(t, ctx, fixture.pool, table, count)
		}
	})
	for _, group := range groups {
		for _, c := range group {
			c.process.finish(t)
		}
	}
}
