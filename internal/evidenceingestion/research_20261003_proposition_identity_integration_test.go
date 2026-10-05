//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These types and the adapter are Lab-only. A membership records an externally
// declared identity, not a discovered equivalence or a new canonical relation.
type propositionLabKey struct {
	Namespace string `json:"namespace"`
	LocalID   string `json:"local_id"`
	ScopeRef  string `json:"scope_ref"`
	Revision  string `json:"revision"`
}

type propositionLabInput struct {
	RequestID      string            `json:"request_id"`
	Key            propositionLabKey `json:"key"`
	Definition     string            `json:"definition"`
	NodeID         string            `json:"node_id"`
	DecisionBy     string            `json:"decision_by"`
	DecisionReason string            `json:"decision_reason"`
}

type propositionLabResult struct {
	PropositionID string `json:"proposition_id"`
	Replayed      bool   `json:"replayed"`
}

type propositionLabMember struct {
	RequestID      string                          `json:"request_id"`
	NodeID         string                          `json:"node_id"`
	DecisionBy     string                          `json:"decision_by"`
	DecisionReason string                          `json:"decision_reason"`
	Derivation     *evidencegraph.DerivationRecord `json:"derivation,omitempty"`
}

type propositionLabView struct {
	PropositionID    string                          `json:"proposition_id"`
	Key              propositionLabKey               `json:"key"`
	Definition       string                          `json:"definition"`
	Snapshot         string                          `json:"postgres_snapshot"`
	Members          []propositionLabMember          `json:"members"`
	MembersTruncated bool                            `json:"members_truncated"`
	GraphTruncated   bool                            `json:"graph_truncated"`
	GraphMaxDepth    int                             `json:"graph_max_depth"`
	GraphMaxNodes    int                             `json:"graph_max_nodes"`
	GraphMaxEdges    int                             `json:"graph_max_edges"`
	Artifact         evidencegraph.CanonicalArtifact `json:"artifact"`
}

func propositionLabDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		// Length framing preserves boundaries even when values contain colons.
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (k propositionLabKey) id() string {
	return "prop:sha256:" + propositionLabDigest("proposition-identity/v1", k.Namespace, k.LocalID, k.ScopeRef, k.Revision)
}

func (in propositionLabInput) hash() string {
	return propositionLabDigest("proposition-membership-lab/v1", in.RequestID,
		in.Key.Namespace, in.Key.LocalID, in.Key.ScopeRef, in.Key.Revision,
		in.Definition, in.NodeID, in.DecisionBy, in.DecisionReason)
}

func (in propositionLabInput) validate() error {
	fields := []struct {
		name  string
		value string
		max   int
	}{
		{"namespace", in.Key.Namespace, 512}, {"local_id", in.Key.LocalID, 512},
		{"scope_ref", in.Key.ScopeRef, 512}, {"revision", in.Key.Revision, 512},
		{"definition", in.Definition, 8192}, {"request_id", in.RequestID, 512},
		{"node_id", in.NodeID, 512}, {"decision_by", in.DecisionBy, 512},
		{"decision_reason", in.DecisionReason, 2048},
	}
	for _, field := range fields {
		if len(field.value) == 0 || len(field.value) > field.max ||
			!utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) {
			return fmt.Errorf("invalid lab %s", field.name)
		}
	}
	return nil
}

func propositionLabLoadRequest(ctx context.Context, tx pgx.Tx, requestID string) (propositionLabInput, error) {
	var in propositionLabInput
	err := tx.QueryRow(ctx, `
		SELECT m.request_id, p.namespace_id, p.local_id, p.scope_ref, p.revision,
		       p.definition, m.canonical_node_id, m.decision_by, m.decision_reason
		FROM canonical_proposition_bindings m JOIN canonical_propositions p USING (proposition_id)
		WHERE m.request_id = $1`, requestID).Scan(&in.RequestID,
		&in.Key.Namespace, &in.Key.LocalID, &in.Key.ScopeRef, &in.Key.Revision,
		&in.Definition, &in.NodeID, &in.DecisionBy, &in.DecisionReason)
	return in, err
}

func propositionLabBind(ctx context.Context, pool *pgxpool.Pool, in propositionLabInput, stopAfter string) (result propositionLabResult, err error) {
	cleanup, err := fullLabFault(ctx, pool, stopAfter)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	got, err := BindCanonicalProposition(ctx, pool, PropositionBindingInput{
		RequestID: in.RequestID, Key: PropositionKey(in.Key), Definition: in.Definition,
		NodeID: in.NodeID, DecisionBy: in.DecisionBy, DecisionReason: in.DecisionReason,
	})
	return propositionLabResult{PropositionID: got.PropositionID, Replayed: got.Replayed}, err
}

func propositionLabReadTx(ctx context.Context, tx pgx.Tx, key propositionLabKey, limit int) (propositionLabView, error) {
	got, err := readPropositionMembersTx(ctx, tx, PropositionKey(key), limit)
	if err != nil {
		return propositionLabView{}, err
	}
	view := propositionLabView{PropositionID: got.PropositionID, Key: key, Snapshot: got.Snapshot,
		MembersTruncated: got.Truncated, GraphTruncated: got.GraphTruncated,
		GraphMaxDepth: got.GraphMaxDepth, GraphMaxNodes: got.GraphMaxNodes, GraphMaxEdges: got.GraphMaxEdges, Artifact: got.Graph}
	if err := tx.QueryRow(ctx, "SELECT definition FROM canonical_propositions WHERE proposition_id=$1", got.PropositionID).Scan(&view.Definition); err != nil {
		return view, err
	}
	for _, node := range got.Nodes {
		member := propositionLabMember{NodeID: node}
		if err := tx.QueryRow(ctx, "SELECT request_id,decision_by,decision_reason FROM canonical_proposition_bindings WHERE canonical_node_id=$1", node).Scan(&member.RequestID, &member.DecisionBy, &member.DecisionReason); err != nil {
			return view, err
		}
		for _, d := range got.Graph.Derivations {
			if d.NodeID == node {
				copy := d
				member.Derivation = &copy
			}
		}
		view.Members = append(view.Members, member)
	}
	return view, nil
}

func propositionLabRead(ctx context.Context, pool *pgxpool.Pool, key propositionLabKey, limit int) (propositionLabView, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return propositionLabView{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	view, err := propositionLabReadTx(ctx, tx, key, limit)
	if err != nil {
		return propositionLabView{}, err
	}
	return view, tx.Commit(ctx)
}

func propositionLabWrite(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("save lab artifact: %v / %v", writeErr, closeErr)
	}
}

func propositionLabCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) [2]int {
	t.Helper()
	var counts [2]int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_propositions),
		(SELECT count(*) FROM canonical_proposition_bindings)`).Scan(&counts[0], &counts[1]); err != nil {
		t.Fatal(err)
	}
	return counts
}

func propositionLabGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "canonical_derivations", "canonical_derivation_parents"} {
		var text string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t`).Scan(&text); err != nil {
			t.Fatal(err)
		}
		out[table] = text
	}
	return out
}

func TestResearch20261003PropositionIdentity(t *testing.T) {
	out := os.Getenv("AHE_PROPOSITION_LAB_OUT")
	if out == "" {
		t.Skip("explicit disposable proposition Lab output required")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, pool := integrationPool(t)
	var databaseVersion string
	if err := pool.QueryRow(ctx, "SELECT version()").Scan(&databaseVersion); err != nil {
		t.Fatal(err)
	}
	propositionLabWrite(t, filepath.Join(out, "database.json"), map[string]string{"version": databaseVersion})

	// All native graph creation precedes the preservation baseline. Registry
	// operations below never call a native writer or modify a canonical row.
	admit := func(name, statement string, parents []string, candidate bool) AdmissionResult {
		t.Helper()
		if candidate {
			return AdmissionResult{}
		} // Product has no candidate admission contract.
		input := ManualTextInput{SourceID: "proposition-lab-" + name, SourceVersion: "v1",
			Raw: []byte(statement + "\n"), RequestID: "proposition-source-" + name, AttemptNumber: 1,
			OriginMetadata: map[string]string{"fixture": "synthetic-proposition-identity"}}
		fixture := FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "assertion", StatementText: statement, EvidenceRefs: []string{"span:S1"},
		}}}
		ingested, err := IngestManualText(ctx, pool, input, fixture)
		if err != nil {
			t.Fatalf("ingest %s: %v", name, err)
		}
		request := AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID,
			DecisionBy: "synthetic-lab", DecisionReason: "frozen fixture, not human authority"}
		if len(parents) > 0 {
			d := DerivationAdmissionInput{ParentNodeIDs: parents, Method: "declared-lab-rule",
				Producer: "main-agent-fixture", TraceRef: "lab-trace:" + name}
			request.Derivation = &d
		}
		result, err := AdmitPendingProposal(ctx, pool, request)
		if err != nil {
			t.Fatalf("admit %s: %v", name, err)
		}
		return result
	}
	a := admit("A", "A@prod is supplied.", nil, false)
	b := admit("B", "B@prod is supplied.", nil, false)
	c := admit("C", "C@prod is supplied.", nil, false)
	ab := admit("Q-AB", "Q@prod holds.", []string{b.CanonicalRef, a.CanonicalRef}, false)
	qc := admit("Q-C", "Q@prod holds.", []string{c.CanonicalRef}, false)
	paraphrase := admit("Q-paraphrase", "The prod proposition Q holds.", []string{c.CanonicalRef}, false)
	duplicate := admit("Q-copy", "Q@prod holds.", []string{a.CanonicalRef, b.CanonicalRef}, false)
	n := admit("not-Q", "not-Q@prod holds.", nil, false)
	candidate := admit("candidate", "Q@prod is a candidate.", []string{c.CanonicalRef}, true)
	spares := make([]AdmissionResult, 18)
	for i := range spares {
		spares[i] = admit(fmt.Sprintf("spare-%02d", i), "Q holds in the externally declared scope.", nil, false)
	}
	proposal := fullLabContradictionProposal(t, ctx, pool,
		"proposition-lab-conflict", ab.CanonicalRef, n.CanonicalRef, "The supplied Q_AB and not-Q conflict.")
	conflict, err := AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{
		ProposalID: proposal.Proposal.ID, DecisionBy: "synthetic-lab", DecisionReason: "explicit fixture conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline := propositionLabGraph(t, ctx, pool)
	propositionLabWrite(t, filepath.Join(out, "canonical-before.json"), baseline)
	qkey := propositionLabKey{Namespace: "ahe-lab", LocalID: "Q", ScopeRef: "prod", Revision: "v1"}
	request := func(id string, node AdmissionResult, key propositionLabKey) propositionLabInput {
		return propositionLabInput{RequestID: id, Key: key, Definition: "Externally declared Q in this scope and revision.",
			NodeID: node.CanonicalRef, DecisionBy: "synthetic-binding-review", DecisionReason: "identity stipulated by frozen fixture"}
	}
	abRequest := request("bind-AB", ab, qkey)
	cRequest := request("bind-C", qc, qkey)
	bind := func(t *testing.T, in propositionLabInput) propositionLabResult {
		t.Helper()
		result, err := propositionLabBind(ctx, pool, in, "")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	read := func(t *testing.T, key propositionLabKey, limit int) propositionLabView {
		t.Helper()
		view, err := propositionLabRead(ctx, pool, key, limit)
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	reject := func(t *testing.T, in propositionLabInput, stopAfter string) string {
		t.Helper()
		before := propositionLabCounts(t, ctx, pool)
		_, err := propositionLabBind(ctx, pool, in, stopAfter)
		if err == nil {
			t.Fatal("unexpectedly accepted invalid registration")
		}
		if stopAfter != "" && !strings.Contains(err.Error(), "FULL LAB INJECTED") {
			t.Fatalf("fault point was not reached: %v", err)
		}
		if after := propositionLabCounts(t, ctx, pool); after != before {
			t.Fatalf("rejection leaked rows: %v -> %v", before, after)
		}
		return err.Error()
	}
	var cases []map[string]any
	run := func(id string, test func(*testing.T) any) {
		var detail any
		pass := t.Run(id, func(t *testing.T) { detail = test(t) })
		cases = append(cases, map[string]any{"id": id, "pass": pass, "detail": detail})
	}
	run("01-native-baseline", func(t *testing.T) any {
		if ab.CanonicalRef == qc.CanonicalRef || ab.DerivationID == qc.DerivationID {
			t.Fatal("native writer collapsed independent derivations")
		}
		view, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{
			RootNodeIDs: []string{ab.CanonicalRef, qc.CanonicalRef}, MaxDepth: 1, MaxNodes: 128, MaxEdges: 256,
		})
		if err != nil {
			t.Fatal(err)
		}
		if view.Truncated {
			t.Fatal("native positive control was truncated")
		}
		for _, target := range []AdmissionResult{ab, qc} {
			found := false
			for _, d := range view.Artifact.Derivations {
				if d.NodeID == target.CanonicalRef {
					found = d.ID == target.DerivationID && slices.Equal(d.Parents, target.ParentNodeIDs)
				}
			}
			if !found {
				t.Fatal("native read lost an exact parent group")
			}
		}
		return view
	})
	run("02-alternative-memberships", func(t *testing.T) any {
		first, second := bind(t, abRequest), bind(t, cRequest)
		view := read(t, qkey, 32)
		if first.PropositionID != second.PropositionID || len(view.Members) != 2 || view.MembersTruncated || view.GraphTruncated {
			t.Fatal("two independent derivations did not round trip under one identity")
		}
		for _, m := range view.Members {
			want := ab
			if m.NodeID == qc.CanonicalRef {
				want = qc
			}
			if m.Derivation == nil || m.Derivation.ID != want.DerivationID || !slices.Equal(m.Derivation.Parents, want.ParentNodeIDs) {
				t.Fatal("membership parent grouping changed")
			}
		}
		propositionLabWrite(t, filepath.Join(out, "alternative-view.json"), view)
		return view
	})
	run("03-identity-boundaries", func(t *testing.T) any {
		keys := []propositionLabKey{qkey, qkey, qkey, qkey}
		keys[0].Namespace = "another-namespace"
		keys[1].LocalID = "Q-other-slot"
		keys[2].ScopeRef = "staging"
		keys[3].Revision = "v2"
		ids := map[string]bool{qkey.id(): true}
		var views []propositionLabView
		for i, key := range keys {
			in := request(fmt.Sprintf("boundary-%d", i), spares[i], key)
			bind(t, in)
			if ids[key.id()] {
				t.Fatal("distinct scope/key/revision collapsed")
			}
			ids[key.id()] = true
			view := read(t, key, 32)
			if len(view.Members) != 1 || view.Members[0].NodeID != spares[i].CanonicalRef {
				t.Fatal("cross-identity membership leak")
			}
			views = append(views, view)
		}
		return views
	})
	run("04-explicit-paraphrase", func(t *testing.T) any {
		bind(t, request("paraphrase", paraphrase, qkey))
		view := read(t, qkey, 32)
		if len(view.Members) != 3 {
			t.Fatal("explicit identity depended on statement spelling")
		}
		return view
	})
	run("05-definition-drift", func(t *testing.T) any {
		in := request("definition-drift", spares[4], qkey)
		in.Definition = "A changed proposition definition."
		return reject(t, in, "")
	})
	run("06-request-drift", func(t *testing.T) any {
		mutations := []func(*propositionLabInput){
			func(in *propositionLabInput) { in.Key.Namespace += "changed" },
			func(in *propositionLabInput) { in.Key.LocalID += "changed" },
			func(in *propositionLabInput) { in.Key.ScopeRef += "changed" },
			func(in *propositionLabInput) { in.Key.Revision += "changed" },
			func(in *propositionLabInput) { in.Definition += "changed" },
			func(in *propositionLabInput) { in.NodeID = qc.CanonicalRef },
			func(in *propositionLabInput) { in.DecisionBy += "changed" },
			func(in *propositionLabInput) { in.DecisionReason += "changed" },
		}
		var rejected []string
		for _, mutate := range mutations {
			in := abRequest
			mutate(&in)
			rejected = append(rejected, reject(t, in, ""))
		}
		return rejected
	})
	run("07-exact-replay", func(t *testing.T) any {
		before := propositionLabCounts(t, ctx, pool)
		result := bind(t, abRequest)
		if !result.Replayed || propositionLabCounts(t, ctx, pool) != before {
			t.Fatal("exact replay duplicated membership")
		}
		return result
	})
	run("08-new-request-same-member", func(t *testing.T) any {
		in := abRequest
		in.RequestID = "another-receipt"
		return reject(t, in, "")
	})
	run("09-member-rebinding", func(t *testing.T) any {
		in := abRequest
		in.RequestID, in.Key.LocalID = "rebind", "another-proposition"
		return reject(t, in, "")
	})
	run("10-missing-node", func(t *testing.T) any {
		in := request("missing-node", spares[4], qkey)
		in.Key.LocalID, in.NodeID = "missing-node", "canon-node:absent"
		return reject(t, in, "")
	})
	run("11-raw-node", func(t *testing.T) any {
		in := request("raw-node", a, qkey)
		in.Key.LocalID, in.NodeID = "raw-node", a.RawEvidenceNodeIDs[0]
		return reject(t, in, "")
	})
	run("12-candidate-node", func(t *testing.T) any {
		if candidate.CanonicalRef == "" {
			t.Error("PRODUCT_CAPABILITY_MISSING: original candidate-node fixture cannot be created through product API")
			return map[string]any{"status": "unavailable", "reason": "product has no candidate admission contract"}
		}
		in := request("candidate-node", candidate, qkey)
		in.Key.LocalID = "candidate-node"
		return reject(t, in, "")
	})
	for _, stage := range []struct{ id, stop string }{{"13-rollback-definition", "definition"}, {"14-rollback-membership", "membership"}} {
		run(stage.id, func(t *testing.T) any {
			in := request(stage.id, spares[4], qkey)
			in.Key.LocalID = stage.id
			return reject(t, in, stage.stop)
		})
	}
	run("15-immutable-records", func(t *testing.T) any {
		before := propositionLabCounts(t, ctx, pool)
		commands := []string{
			"UPDATE canonical_propositions SET definition=definition", "DELETE FROM canonical_propositions",
			"TRUNCATE canonical_propositions CASCADE", "UPDATE canonical_proposition_bindings SET decision_reason=decision_reason",
			"DELETE FROM canonical_proposition_bindings", "TRUNCATE canonical_proposition_bindings",
		}
		var rejected []string
		for _, command := range commands {
			_, err := pool.Exec(ctx, command)
			if err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Fatalf("immutability guard: %s: %v", command, err)
			}
			rejected = append(rejected, err.Error())
		}
		if propositionLabCounts(t, ctx, pool) != before {
			t.Fatal("mutation changed registry counts")
		}
		return rejected
	})
	// All five contention cases use two independent SQL transactions. The start
	// barrier is recorded as a bounded concurrent probe, not schedule coverage.
	concurrent := func(t *testing.T, inputs []propositionLabInput, wantSuccess, wantReplay int) any {
		t.Helper()
		start := make(chan struct{})
		var wait sync.WaitGroup
		results := make([]propositionLabResult, len(inputs))
		errs := make([]error, len(inputs))
		for i := range inputs {
			wait.Add(1)
			go func(i int) {
				defer wait.Done()
				<-start
				results[i], errs[i] = propositionLabBind(ctx, pool, inputs[i], "")
			}(i)
		}
		close(start)
		wait.Wait()
		success, replay := 0, 0
		var messages []string
		for i, err := range errs {
			if err == nil {
				success++
				if results[i].Replayed {
					replay++
				}
			}
			messages = append(messages, fmt.Sprint(err))
		}
		if success != wantSuccess || replay != wantReplay {
			t.Fatalf("concurrent success/replay=%d/%d want %d/%d: %v", success, replay, wantSuccess, wantReplay, messages)
		}
		return map[string]any{"inputs": inputs, "results": results, "errors": messages}
	}
	run("16-concurrent-same-proposition", func(t *testing.T) any {
		key := qkey
		key.LocalID = "concurrent-proposition"
		inputs := []propositionLabInput{request("concurrent-a", spares[4], key), request("concurrent-b", spares[5], key)}
		before := propositionLabCounts(t, ctx, pool)
		detail := concurrent(t, inputs, 2, 0)
		if after := propositionLabCounts(t, ctx, pool); after != [2]int{before[0] + 1, before[1] + 2} {
			t.Fatal("concurrent different members were not both retained")
		}
		if len(read(t, key, 32).Members) != 2 {
			t.Fatal("concurrent member read lost a record")
		}
		return detail
	})
	run("17-concurrent-exact-request", func(t *testing.T) any {
		key := qkey
		key.LocalID = "concurrent-replay"
		in := request("concurrent-replay", spares[6], key)
		before := propositionLabCounts(t, ctx, pool)
		detail := concurrent(t, []propositionLabInput{in, in}, 2, 1)
		if after := propositionLabCounts(t, ctx, pool); after != [2]int{before[0] + 1, before[1] + 1} {
			t.Fatal("concurrent exact replay duplicated rows")
		}
		return detail
	})
	for _, trial := range []struct {
		id    string
		nodes [2]int
	}{
		{"18-concurrent-request-conflict", [2]int{7, 8}},
		{"19-concurrent-member-conflict", [2]int{9, 9}},
		{"20-concurrent-definition-conflict", [2]int{10, 11}},
	} {
		run(trial.id, func(t *testing.T) any {
			keyA, keyB := qkey, qkey
			keyA.LocalID, keyB.LocalID = trial.id+"-a", trial.id+"-b"
			inputs := []propositionLabInput{request(trial.id+"-a", spares[trial.nodes[0]], keyA), request(trial.id+"-b", spares[trial.nodes[1]], keyB)}
			if strings.HasPrefix(trial.id, "18") {
				inputs[1].RequestID = inputs[0].RequestID
			}
			if strings.HasPrefix(trial.id, "20") {
				inputs[1].Key = inputs[0].Key
				inputs[1].Definition = "Different definition competing for the exact key."
			}
			before := propositionLabCounts(t, ctx, pool)
			detail := concurrent(t, inputs, 1, 0)
			if after := propositionLabCounts(t, ctx, pool); after != [2]int{before[0] + 1, before[1] + 1} {
				t.Fatal("losing concurrent request leaked definition or membership")
			}
			return detail
		})
	}
	run("21-repeatable-read-membership", func(t *testing.T) any {
		key := qkey
		key.LocalID = "snapshot"
		bind(t, request("snapshot-a", spares[12], key))
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		before, err := propositionLabReadTx(ctx, tx, key, 32)
		if err != nil {
			t.Fatal(err)
		}
		bind(t, request("snapshot-b", spares[13], key))
		during, err := propositionLabReadTx(ctx, tx, key, 32)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after := read(t, key, 32)
		if !reflect.DeepEqual(before, during) || len(before.Members) != 1 || len(after.Members) != 2 {
			t.Fatal("read mixed membership snapshots")
		}
		return map[string]any{"before": before, "during": during, "after": after}
	})
	run("22-bounded-inventory", func(t *testing.T) any {
		limited, all := read(t, qkey, 1), read(t, qkey, 32)
		if !limited.MembersTruncated || len(limited.Members) != 1 || all.MembersTruncated || len(all.Members) != 3 {
			t.Fatal("bounded inventory did not declare truncation")
		}
		return map[string]any{"limited": limited, "full_in_lab_registry": all}
	})
	run("23-reconnect", func(t *testing.T) any {
		before := read(t, qkey, 32)
		fresh, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
		if err != nil {
			t.Fatal(err)
		}
		first, err := propositionLabRead(ctx, fresh, qkey, 32)
		fresh.Close()
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		after, err := propositionLabRead(ctx, reopened, qkey, 32)
		if err != nil {
			t.Fatal(err)
		}
		before.Snapshot, first.Snapshot, after.Snapshot = "", "", ""
		if !reflect.DeepEqual(before, first) || !reflect.DeepEqual(first, after) {
			t.Fatal("reconnect changed committed inventory or native graph")
		}
		return after
	})
	run("24-canonical-preservation", func(t *testing.T) any {
		after := propositionLabGraph(t, ctx, pool)
		propositionLabWrite(t, filepath.Join(out, "canonical-after.json"), after)
		if !reflect.DeepEqual(baseline, after) {
			t.Fatal("registry operations mutated the native graph")
		}
		return map[string]any{"all_four_tables_exactly_equal": true}
	})
	run("25-conflict-no-propagation", func(t *testing.T) any {
		view := read(t, qkey, 32)
		found := false
		for _, edge := range view.Artifact.Edges {
			if edge.Relation != evidencegraph.CanonicalContradicts {
				continue
			}
			if edge.ID != conflict.Decision.CanonicalEdgeID || edge.From == qc.CanonicalRef || edge.To == qc.CanonicalRef {
				t.Fatal("membership expanded a conflict to another derivation")
			}
			found = true
		}
		if !found {
			t.Fatal("original member conflict was lost")
		}
		return view
	})
	run("26-shared-origin", func(t *testing.T) any {
		bind(t, request("same-origin-copy", duplicate, qkey))
		view := read(t, qkey, 32)
		if len(view.Members) != 4 {
			t.Fatal("independent derivation identity disappeared")
		}
		provenance := map[string]evidencegraph.ProvenanceRecord{}
		for _, p := range view.Artifact.Provenance {
			provenance[p.ID] = p
		}
		var first, second evidencegraph.ProvenanceRecord
		for _, node := range view.Artifact.Nodes {
			if node.ID == ab.CanonicalRef {
				first = provenance[node.ProvenanceRef]
			}
			if node.ID == duplicate.CanonicalRef {
				second = provenance[node.ProvenanceRef]
			}
		}
		if first.OriginGroupID == "" || first.OriginGroupID != second.OriginGroupID || !slices.Equal(first.OriginRefs, second.OriginRefs) {
			t.Fatal("same-source derivation lost shared origin information")
		}
		if !reflect.DeepEqual(baseline, propositionLabGraph(t, ctx, pool)) {
			t.Fatal("late membership mutated the native graph")
		}
		return map[string]any{"first": first, "copy": second, "view": view}
	})
	run("27-key-encoding-and-strings", func(t *testing.T) any {
		left, right := qkey, qkey
		left.Namespace, left.LocalID = "ab", "c"
		right.Namespace, right.LocalID = "a", "bc"
		if left.id() == right.id() || propositionLabDigest("a:b", "c") == propositionLabDigest("a", "b:c") {
			t.Fatal("ambiguous tuple encoding")
		}
		left.LocalID, right.LocalID = "é", "e\u0301"
		left.Namespace, right.Namespace = "same", "same"
		if left.id() == right.id() {
			t.Fatal("undeclared Unicode normalization")
		}
		for _, key := range []propositionLabKey{left, right, {Namespace: "命題", LocalID: "Q:1", ScopeRef: "prod", Revision: "v1"}} {
			var sqlDigest string
			if err := pool.QueryRow(ctx, `SELECT proposition_digest('proposition-identity/v1',$1,$2,$3,$4)`,
				key.Namespace, key.LocalID, key.ScopeRef, key.Revision).Scan(&sqlDigest); err != nil {
				t.Fatal(err)
			}
			if "prop:sha256:"+sqlDigest != key.id() {
				t.Fatal("Go/PostgreSQL identity disagreement")
			}
		}
		var rejected []string
		for _, bad := range []string{"", "a\x00b", string([]byte{0xff}), strings.Repeat("x", 513)} {
			in := request("invalid-key", spares[14], qkey)
			in.Key.LocalID = bad
			rejected = append(rejected, reject(t, in, ""))
		}
		for _, bad := range []string{"a\x00b", string([]byte{0xff}), strings.Repeat("x", 8193)} {
			in := request("invalid-definition", spares[14], qkey)
			in.Definition = bad
			rejected = append(rejected, reject(t, in, ""))
		}
		return map[string]any{"boundary_tuple_ids": []string{left.id(), right.id()}, "rejections": rejected}
	})
	propositionLabWrite(t, filepath.Join(out, "cases.json"), cases)
	propositionLabWrite(t, filepath.Join(out, "final-counts.json"), propositionLabCounts(t, ctx, pool))
	if len(cases) != 27 {
		t.Fatalf("executed %d named cases, want 27", len(cases))
	}
}
