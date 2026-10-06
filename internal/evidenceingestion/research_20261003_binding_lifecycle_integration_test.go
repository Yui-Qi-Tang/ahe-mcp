//go:build integration && labreplay

package evidenceingestion

import (
	"context"
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
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Test-only lifecycle. A receipt describes an applied historical request, never
// the node's current state. The latter must be read in a named snapshot.
type bindingLabRequest struct {
	RequestID        string            `json:"request_id"`
	NodeID           string            `json:"node_id"`
	ExpectedRevision int64             `json:"expected_revision"`
	PreviousRef      string            `json:"previous_ref"`
	FromID           string            `json:"from_id"`
	Operation        string            `json:"operation"`
	Target           propositionLabKey `json:"target"`
	Definition       string            `json:"definition"`
	DecisionBy       string            `json:"decision_by"`
	Reason           string            `json:"reason"`
	EvidenceRef      string            `json:"evidence_ref"`
}

type bindingLabEvent struct {
	Request    bindingLabRequest `json:"request"`
	Revision   int64             `json:"applied_revision"`
	TargetID   string            `json:"target_id"`
	RecordedAt time.Time         `json:"recorded_at"`
}

type bindingLabReceipt struct {
	Event    bindingLabEvent `json:"historical_event"`
	Replayed bool            `json:"replayed"`
}

type bindingLabHistory struct {
	Initial          propositionLabInput `json:"initial_receipt"`
	HeadRevision     int64               `json:"head_revision"`
	SelectedRevision int64               `json:"selected_revision"`
	PropositionID    string              `json:"proposition_id"`
	HeadRef          string              `json:"selected_ref"`
	Active           bool                `json:"active"`
	Events           []bindingLabEvent   `json:"events"`
	HistoryTruncated bool                `json:"history_truncated"`
	Snapshot         string              `json:"postgres_snapshot"`
}

type bindingLabInventory struct {
	PropositionID  string                          `json:"proposition_id"`
	Nodes          []string                        `json:"current_nodes"`
	Truncated      bool                            `json:"members_truncated"`
	Snapshot       string                          `json:"postgres_snapshot"`
	Graph          evidencegraph.CanonicalArtifact `json:"graph"`
	GraphTruncated bool                            `json:"graph_truncated"`
	GraphMaxDepth  int                             `json:"graph_max_depth"`
	GraphMaxNodes  int                             `json:"graph_max_nodes"`
	GraphMaxEdges  int                             `json:"graph_max_edges"`
}

var errBindingLabConflict = errors.New("lab binding request or definition conflict")

func (in bindingLabRequest) validate() error {
	if in.ExpectedRevision < 0 || in.ExpectedRevision > 1000000 {
		return errors.New("invalid lab expected revision")
	}
	for _, f := range []struct {
		value string
		max   int
	}{
		{in.RequestID, 512}, {in.NodeID, 512}, {in.PreviousRef, 1024},
		{in.DecisionBy, 512}, {in.Reason, 2048}, {in.EvidenceRef, 2048},
	} {
		if f.value == "" || len(f.value) > f.max || !utf8.ValidString(f.value) || strings.ContainsRune(f.value, 0) {
			return errors.New("invalid lab event field")
		}
	}
	switch in.Operation {
	case "withdraw":
		if in.Target != (propositionLabKey{}) || in.Definition != "" {
			return errors.New("withdrawal cannot supply a target")
		}
	case "correct", "restore":
		return (propositionLabInput{RequestID: in.RequestID, NodeID: in.NodeID, Key: in.Target,
			Definition: in.Definition, DecisionBy: in.DecisionBy, DecisionReason: in.Reason}).validate()
	default:
		return errors.New("invalid lab operation")
	}
	return nil
}

const bindingLabEventSelect = `SELECT e.request_id,e.canonical_node_id,e.revision-1,e.previous_ref,
	coalesce(e.from_id,''),e.operation,coalesce(p.namespace_id,''),coalesce(p.local_id,''),
	coalesce(p.scope_ref,''),coalesce(p.revision,''),coalesce(p.definition,''),
	e.decision_by,e.decision_reason,e.evidence_ref,e.revision,coalesce(e.target_id,''),e.recorded_at
	FROM canonical_proposition_binding_events e LEFT JOIN canonical_propositions p ON p.proposition_id=e.target_id `

func bindingLabScan(row pgx.Row) (bindingLabEvent, error) {
	var event bindingLabEvent
	in := &event.Request
	err := row.Scan(&in.RequestID, &in.NodeID, &in.ExpectedRevision, &in.PreviousRef, &in.FromID, &in.Operation,
		&in.Target.Namespace, &in.Target.LocalID, &in.Target.ScopeRef, &in.Target.Revision, &in.Definition,
		&in.DecisionBy, &in.Reason, &in.EvidenceRef, &event.Revision, &event.TargetID, &event.RecordedAt)
	return event, err
}

func bindingLabApply(ctx context.Context, pool *pgxpool.Pool, in bindingLabRequest, stopAfter string) (result bindingLabReceipt, err error) {
	cleanup, err := fullLabFault(ctx, pool, stopAfter)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	got, err := ChangePropositionBinding(ctx, pool, PropositionBindingChange{
		RequestID: in.RequestID, NodeID: in.NodeID, ExpectedRevision: in.ExpectedRevision, PreviousRef: in.PreviousRef,
		FromID: in.FromID, Operation: in.Operation, Target: PropositionKey(in.Target), Definition: in.Definition,
		DecisionBy: in.DecisionBy, Reason: in.Reason, EvidenceRef: in.EvidenceRef})
	if err != nil {
		return result, err
	}
	return fullLabConvert[bindingLabReceipt](got)
}

func bindingLabHistoryTx(ctx context.Context, tx pgx.Tx, node string, revision int64, limit int) (bindingLabHistory, error) {
	got, err := readPropositionBindingHistoryTx(ctx, tx, node, revision, limit)
	if err != nil {
		return bindingLabHistory{}, err
	}
	return fullLabConvert[bindingLabHistory](got)
}

func bindingLabHistoryRead(ctx context.Context, pool *pgxpool.Pool, node string, revision int64, limit int) (bindingLabHistory, error) {
	got, err := ReadPropositionBindingHistory(ctx, pool, node, revision, limit)
	if err != nil {
		return bindingLabHistory{}, err
	}
	return fullLabConvert[bindingLabHistory](got)
}

func bindingLabInventoryTx(ctx context.Context, tx pgx.Tx, key propositionLabKey, limit int) (bindingLabInventory, error) {
	got, err := readPropositionMembersTx(ctx, tx, PropositionKey(key), limit)
	return bindingLabInventory(got), err
}

func bindingLabInventoryRead(ctx context.Context, pool *pgxpool.Pool, key propositionLabKey, limit int) (bindingLabInventory, error) {
	got, err := ReadPropositionMembers(ctx, pool, PropositionKey(key), limit)
	return bindingLabInventory(got), err
}

func bindingLabTables(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"canonical_propositions", "canonical_proposition_bindings", "canonical_proposition_binding_events"} {
		var value string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		result[table] = value
	}
	return result
}

func TestResearch20261003BindingLifecycle(t *testing.T) {
	out := os.Getenv("AHE_BINDING_LAB_OUT")
	if out == "" {
		t.Skip("explicit disposable binding lifecycle Lab output required")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, pool := integrationPool(t)
	// Reconnect swaps this variable; close its final value after the whole suite,
	// not when the reconnect subtest ends.
	t.Cleanup(func() { pool.Close() })
	var version string
	if err := pool.QueryRow(ctx, `SELECT version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	propositionLabWrite(t, filepath.Join(out, "database.json"), map[string]string{"version": version})
	admit := func(name string, parents []string, candidate bool) AdmissionResult {
		t.Helper()
		statement := "Synthetic assertion " + name + "."
		input := ManualTextInput{SourceID: "binding-lab-" + name, SourceVersion: "v1", Raw: []byte(statement + "\n"), RequestID: "source-" + name, AttemptNumber: 1}
		fixture := FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "assertion", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}}
		ingested, err := IngestManualText(ctx, pool, input, fixture)
		if err != nil {
			t.Fatal(err)
		}
		in := AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID, DecisionBy: "synthetic-lab", DecisionReason: "frozen input, not human authorization"}
		if len(parents) > 0 {
			d := DerivationAdmissionInput{ParentNodeIDs: parents, Method: "declared-lab-rule", Producer: "main-agent-fixture", TraceRef: "binding-trace:" + name}
			if candidate {
				c := CandidateAdmissionInput(d)
				in.Candidate = &c
			} else {
				in.Derivation = &d
			}
		}
		got, err := AdmitPendingProposal(ctx, pool, in)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a, b, c := admit("A", nil, false), admit("B", nil, false), admit("C", nil, false)
	ab := admit("Q-AB", []string{a.CanonicalRef, b.CanonicalRef}, false)
	qc := admit("Q-C", []string{c.CanonicalRef}, false)
	candidate := admit("candidate", []string{c.CanonicalRef}, true)
	persistedCandidate, err := GetCanonicalEvidenceByID(ctx, pool, candidate.CanonicalRef)
	if err != nil || persistedCandidate.NodeKind != evidencegraph.CanonicalCandidate {
		t.Fatalf("candidate fixture not durably recorded: %+v %v", persistedCandidate, err)
	}
	unbound := admit("unbound", nil, false)
	spares := make([]AdmissionResult, 10)
	for i := range spares {
		spares[i] = admit(fmt.Sprintf("control-%02d", i), nil, false)
	}
	proposal := fullLabContradictionProposal(t, ctx, pool, "binding-conflict", ab.CanonicalRef, unbound.CanonicalRef, "Explicit fixture conflict remains attached to Q_AB.")
	if _, err := AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{ProposalID: proposal.Proposal.ID, DecisionBy: "synthetic-lab", DecisionReason: "fixture conflict"}); err != nil {
		t.Fatal(err)
	}
	key := func(id string) propositionLabKey {
		return propositionLabKey{Namespace: "binding-lab", LocalID: id, ScopeRef: "prod", Revision: "v1"}
	}
	wrong, correct, third, control := key("P-original-wrong"), key("P-corrected"), key("P-third"), key("control-base")
	definition := "Externally stipulated proposition definition."
	initials := map[string]propositionLabInput{}
	initialBind := func(node AdmissionResult, k propositionLabKey, id string) {
		in := propositionLabInput{RequestID: id, NodeID: node.CanonicalRef, Key: k, Definition: definition, DecisionBy: "synthetic-initial", DecisionReason: "initial fixture assignment"}
		if _, err := propositionLabBind(ctx, pool, in, ""); err != nil {
			t.Fatal(err)
		}
		initials[node.CanonicalRef] = in
	}
	initialBind(ab, wrong, "initial-AB")
	initialBind(qc, wrong, "initial-C")
	for i, node := range spares {
		initialBind(node, control, fmt.Sprintf("initial-control-%02d", i))
	}
	beforeGraph := propositionLabGraph(t, ctx, pool)
	beforeTables := bindingLabTables(t, ctx, pool)
	propositionLabWrite(t, filepath.Join(out, "canonical-before.json"), beforeGraph)
	propositionLabWrite(t, filepath.Join(out, "registry-before.json"), beforeTables)
	history := func(t *testing.T, node string, rev int64, limit int) bindingLabHistory {
		t.Helper()
		v, err := bindingLabHistoryRead(ctx, pool, node, rev, limit)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	inventory := func(t *testing.T, k propositionLabKey, limit int) bindingLabInventory {
		t.Helper()
		v, err := bindingLabInventoryRead(ctx, pool, k, limit)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	request := func(id, node, op string, target propositionLabKey) bindingLabRequest {
		v := history(t, node, -1, 100)
		in := bindingLabRequest{RequestID: id, NodeID: node, ExpectedRevision: v.HeadRevision, PreviousRef: v.HeadRef, FromID: v.PropositionID, Operation: op,
			Target: target, Definition: definition, DecisionBy: "synthetic-correction-review", Reason: "external fixture correction", EvidenceRef: "fixture-evidence:" + id}
		if op == "withdraw" {
			in.Target = propositionLabKey{}
			in.Definition = ""
		}
		return in
	}
	apply := func(t *testing.T, in bindingLabRequest) bindingLabReceipt {
		t.Helper()
		v, err := bindingLabApply(ctx, pool, in, "")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	reject := func(t *testing.T, in bindingLabRequest, stop string) string {
		t.Helper()
		before := bindingLabTables(t, ctx, pool)
		_, err := bindingLabApply(ctx, pool, in, stop)
		if err == nil {
			t.Fatal("unexpected acceptance")
		}
		if stop != "" && !strings.Contains(err.Error(), "FULL LAB INJECTED") {
			t.Fatalf("fault point was not reached: %v", err)
		}
		if !reflect.DeepEqual(before, bindingLabTables(t, ctx, pool)) {
			t.Fatal("rejection leaked registry rows")
		}
		return err.Error()
	}
	parallel := func(t *testing.T, inputs []bindingLabRequest) ([]bindingLabReceipt, []string) {
		t.Helper()
		result := make([]bindingLabReceipt, len(inputs))
		errs := make([]string, len(inputs))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range inputs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				var err error
				result[i], err = bindingLabApply(ctx, pool, inputs[i], "")
				errs[i] = fmt.Sprint(err)
			}(i)
		}
		close(start)
		wg.Wait()
		return result, errs
	}
	var cases []map[string]any
	run := func(id string, fn func(*testing.T) any) {
		var detail any
		pass := t.Run(id, func(t *testing.T) { detail = fn(t) })
		cases = append(cases, map[string]any{"id": id, "pass": pass, "detail": detail})
		propositionLabWrite(t, filepath.Join(out, id+".json"), cases[len(cases)-1])
	}
	firstCorrection := request("correct-AB", ab.CanonicalRef, "correct", correct)
	var firstWithdrawal bindingLabRequest
	run("01-initial-alternatives", func(t *testing.T) any {
		v := inventory(t, wrong, 32)
		if len(v.Nodes) != 2 || v.Truncated {
			t.Fatal("initial alternatives missing")
		}
		for _, n := range []AdmissionResult{ab, qc} {
			found := false
			for _, d := range v.Graph.Derivations {
				if d.NodeID == n.CanonicalRef {
					found = d.ID == n.DerivationID && slices.Equal(d.Parents, n.ParentNodeIDs)
				}
			}
			if !found {
				t.Fatal("initial derivation mismatch")
			}
		}
		return v
	})
	run("02-correct-one-member", func(t *testing.T) any {
		r := apply(t, firstCorrection)
		old, new := inventory(t, wrong, 32), inventory(t, correct, 32)
		if !slices.Equal(old.Nodes, []string{qc.CanonicalRef}) || !slices.Equal(new.Nodes, []string{ab.CanonicalRef}) {
			t.Fatal("correction inventory mismatch")
		}
		return map[string]any{"receipt": r, "old": old, "new": new}
	})
	run("03-historical-revisions", func(t *testing.T) any {
		v0, v1 := history(t, ab.CanonicalRef, 0, 100), history(t, ab.CanonicalRef, 1, 100)
		if v0.PropositionID != wrong.id() || v1.PropositionID != correct.id() || v0.Initial != v1.Initial {
			t.Fatal("historical identity mismatch")
		}
		for _, rev := range []int64{-2, 2} {
			if _, err := bindingLabHistoryRead(ctx, pool, ab.CanonicalRef, rev, 100); err == nil {
				t.Fatal("accepted unavailable revision")
			}
		}
		return []bindingLabHistory{v0, v1}
	})
	run("04-withdraw-retains-history", func(t *testing.T) any {
		firstWithdrawal = request("withdraw-AB", ab.CanonicalRef, "withdraw", propositionLabKey{})
		apply(t, firstWithdrawal)
		v := history(t, ab.CanonicalRef, -1, 100)
		if v.Active || v.PropositionID != "" || len(v.Events) != 2 {
			t.Fatal("withdrawal state mismatch")
		}
		for _, k := range []propositionLabKey{wrong, correct} {
			if slices.Contains(inventory(t, k, 32).Nodes, ab.CanonicalRef) {
				t.Fatal("withdrawn member remains current")
			}
		}
		return v
	})
	run("05-naive-coalesce-negative-control", func(t *testing.T) any {
		var naive string
		if err := pool.QueryRow(ctx, `SELECT coalesce(e.target_id,m.proposition_id) FROM canonical_proposition_bindings m LEFT JOIN LATERAL
			(SELECT target_id FROM canonical_proposition_binding_events WHERE canonical_node_id=m.canonical_node_id ORDER BY revision DESC LIMIT 1)e ON true
			WHERE m.canonical_node_id=$1`, ab.CanonicalRef).Scan(&naive); err != nil {
			t.Fatal(err)
		}
		v := history(t, ab.CanonicalRef, -1, 100)
		if naive != wrong.id() || v.Active {
			t.Fatal("negative control did not expose resurrection")
		}
		return map[string]any{"expected_unsafe": true, "naive_resurrected": naive, "correct_view": v}
	})
	run("06-explicit-restore-and-corrections", func(t *testing.T) any {
		for i, target := range []propositionLabKey{correct, third, correct} {
			op := "correct"
			if i == 0 {
				op = "restore"
			}
			apply(t, request(fmt.Sprintf("chain-%d", i), ab.CanonicalRef, op, target))
		}
		v := history(t, ab.CanonicalRef, -1, 100)
		if v.HeadRevision != 5 || v.PropositionID != correct.id() {
			t.Fatal("append chain mismatch")
		}
		return v
	})
	run("07-old-receipts-not-current", func(t *testing.T) any {
		apply(t, request("withdraw-again", ab.CanonicalRef, "withdraw", propositionLabKey{}))
		before := bindingLabTables(t, ctx, pool)
		r1, r2 := apply(t, firstCorrection), apply(t, firstWithdrawal)
		v := history(t, ab.CanonicalRef, -1, 100)
		if !r1.Replayed || !r2.Replayed || r1.Event.Revision != 1 || r2.Event.Revision != 2 || v.Active || v.HeadRevision != 6 || !reflect.DeepEqual(before, bindingLabTables(t, ctx, pool)) {
			t.Fatal("historical replay changed current state")
		}
		return map[string]any{"historical_receipts": []bindingLabReceipt{r1, r2}, "current": v}
	})
	run("08-request-content-drift", func(t *testing.T) any {
		mutations := []func(*bindingLabRequest){func(v *bindingLabRequest) { v.NodeID = qc.CanonicalRef }, func(v *bindingLabRequest) { v.ExpectedRevision++ }, func(v *bindingLabRequest) { v.PreviousRef = "other" }, func(v *bindingLabRequest) { v.FromID = third.id() }, func(v *bindingLabRequest) { v.Operation = "restore" }, func(v *bindingLabRequest) { v.Target.LocalID += "x" }, func(v *bindingLabRequest) { v.Definition += "x" }, func(v *bindingLabRequest) { v.DecisionBy += "x" }, func(v *bindingLabRequest) { v.Reason += "x" }, func(v *bindingLabRequest) { v.EvidenceRef += "x" }}
		var errs []string
		for _, mutate := range mutations {
			in := firstCorrection
			mutate(&in)
			errs = append(errs, reject(t, in, ""))
		}
		return errs
	})
	run("09-complete-prestate-cas", func(t *testing.T) any {
		base := request("bad-head", qc.CanonicalRef, "correct", correct)
		mutations := []func(*bindingLabRequest){func(v *bindingLabRequest) { v.ExpectedRevision = 2 }, func(v *bindingLabRequest) { v.PreviousRef = "membership:initial-AB" }, func(v *bindingLabRequest) { v.FromID = correct.id() }}
		var errs []string
		for _, mutate := range mutations {
			in := base
			mutate(&in)
			errs = append(errs, reject(t, in, ""))
		}
		stale := firstCorrection
		stale.RequestID = "stale"
		errs = append(errs, reject(t, stale, ""))
		return errs
	})
	run("10-invalid-transitions", func(t *testing.T) any {
		inputs := []bindingLabRequest{request("noop", qc.CanonicalRef, "correct", wrong), request("active-restore", qc.CanonicalRef, "restore", wrong), request("double-withdraw", ab.CanonicalRef, "withdraw", propositionLabKey{}), request("wrong-restore", ab.CanonicalRef, "restore", third), request("inactive-correct", ab.CanonicalRef, "correct", third), request("bad-op", qc.CanonicalRef, "erase", correct)}
		var errs []string
		for _, in := range inputs {
			errs = append(errs, reject(t, in, ""))
		}
		return errs
	})
	run("11-invalid-or-unbound-node", func(t *testing.T) any {
		var raw string
		if err := pool.QueryRow(ctx, `SELECT canonical_node_id FROM canonical_graph_nodes WHERE node_kind='raw_evidence' LIMIT 1`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var errs []string
		for _, node := range []string{"absent", raw, candidate.CanonicalRef, unbound.CanonicalRef} {
			if node == "" {
				t.Error("PRODUCT_CAPABILITY_MISSING: original candidate-node fixture cannot be created through product API")
				continue
			}
			in := firstCorrection
			in.RequestID = "invalid-" + node
			in.NodeID = node
			errs = append(errs, reject(t, in, ""))
		}
		return errs
	})
	run("12-target-definition-drift", func(t *testing.T) any {
		in := request("definition-drift", qc.CanonicalRef, "correct", correct)
		in.Definition += " changed"
		return reject(t, in, "")
	})
	run("13-required-audit-fields", func(t *testing.T) any {
		base := request("audit-invalid", qc.CanonicalRef, "correct", correct)
		mutations := []func(*bindingLabRequest){func(v *bindingLabRequest) { v.DecisionBy = "" }, func(v *bindingLabRequest) { v.Reason = "" }, func(v *bindingLabRequest) { v.EvidenceRef = "" }, func(v *bindingLabRequest) { v.EvidenceRef = "bad\x00ref" }, func(v *bindingLabRequest) { v.Reason = string([]byte{0xff}) }, func(v *bindingLabRequest) { v.Target.LocalID = "" }}
		var errs []string
		for _, mutate := range mutations {
			in := base
			mutate(&in)
			errs = append(errs, reject(t, in, ""))
		}
		return errs
	})
	for _, point := range []struct{ id, stop string }{{"14-rollback-definition", "definition"}, {"15-rollback-event", "event"}} {
		run(point.id, func(t *testing.T) any {
			return reject(t, request(point.id, qc.CanonicalRef, "correct", key(point.id)), point.stop)
		})
	}
	run("16-event-immutability", func(t *testing.T) any {
		before := bindingLabTables(t, ctx, pool)
		var errs []string
		for _, sql := range []string{`UPDATE canonical_proposition_binding_events SET decision_reason='rewrite'`, `DELETE FROM canonical_proposition_binding_events`, `TRUNCATE canonical_proposition_binding_events`} {
			_, err := pool.Exec(ctx, sql)
			if err == nil {
				t.Fatal("event mutation accepted")
			}
			errs = append(errs, err.Error())
		}
		if !reflect.DeepEqual(before, bindingLabTables(t, ctx, pool)) {
			t.Fatal("event history changed")
		}
		return errs
	})
	for index, label := range []string{"17-concurrent-corrections", "18-concurrent-correct-withdraw", "19-concurrent-exact-replay"} {
		run(label, func(t *testing.T) any {
			node := spares[index].CanonicalRef
			in1 := request(label+"-a", node, "correct", key(label+"-target-a"))
			in2 := request(label+"-b", node, "correct", key(label+"-target-b"))
			if index == 1 {
				in2 = request(label+"-b", node, "withdraw", propositionLabKey{})
			}
			if index == 2 {
				in2 = in1
			}
			before := propositionLabCounts(t, ctx, pool)
			receipts, errs := parallel(t, []bindingLabRequest{in1, in2})
			success := 0
			replays := 0
			for i, e := range errs {
				if e == "<nil>" {
					success++
					if receipts[i].Replayed {
						replays++
					}
				}
			}
			want := 1
			if index == 2 {
				want = 2
			}
			if success != want || (index == 2 && replays != 1) {
				t.Fatalf("unexpected concurrent outcomes: %v", errs)
			}
			v := history(t, node, -1, 100)
			if v.HeadRevision != 1 || len(v.Events) != 1 {
				t.Fatal("concurrent chain forked")
			}
			newDefinitions := 1
			if v.Events[0].Request.Operation == "withdraw" {
				newDefinitions = 0
			}
			after := propositionLabCounts(t, ctx, pool)
			if after[0] != before[0]+newDefinitions || after[1] != before[1] {
				t.Fatal("concurrent loser leaked definition")
			}
			return map[string]any{"requests": []bindingLabRequest{in1, in2}, "receipts": receipts, "errors": errs, "history": v}
		})
	}
	run("20-concurrent-distinct-nodes", func(t *testing.T) any {
		target := key("shared-corrected-target")
		inputs := []bindingLabRequest{request("shared-a", spares[3].CanonicalRef, "correct", target), request("shared-b", spares[4].CanonicalRef, "correct", target)}
		receipts, errs := parallel(t, inputs)
		if !slices.Equal(errs, []string{"<nil>", "<nil>"}) {
			t.Fatal(errs)
		}
		v := inventory(t, target, 32)
		if len(v.Nodes) != 2 {
			t.Fatal("lost one independent correction")
		}
		return map[string]any{"receipts": receipts, "errors": errs, "view": v}
	})
	run("21-repeatable-read", func(t *testing.T) any {
		node := spares[5].CanonicalRef
		in := request("rr-correct", node, "correct", correct)
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		before, err := bindingLabHistoryTx(ctx, tx, node, -1, 100)
		if err != nil {
			t.Fatal(err)
		}
		oldInv, err := bindingLabInventoryTx(ctx, tx, control, 32)
		if err != nil {
			t.Fatal(err)
		}
		apply(t, in)
		during, err := bindingLabHistoryTx(ctx, tx, node, -1, 100)
		if err != nil {
			t.Fatal(err)
		}
		duringInv, err := bindingLabInventoryTx(ctx, tx, control, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, during) || !reflect.DeepEqual(oldInv, duringInv) {
			t.Fatal("RR snapshot mixed states")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after := history(t, node, -1, 100)
		if after.HeadRevision != 1 || after.PropositionID != correct.id() {
			t.Fatal("new snapshot missed correction")
		}
		return map[string]any{"before": before, "during": during, "after": after, "inventory_before": oldInv, "inventory_during": duringInv}
	})
	run("22-truncated-history-state", func(t *testing.T) any {
		v := history(t, ab.CanonicalRef, -1, 1)
		past := history(t, ab.CanonicalRef, 4, 1)
		if !v.HistoryTruncated || len(v.Events) != 1 || v.Active || v.SelectedRevision != 6 || past.PropositionID != third.id() || !past.HistoryTruncated {
			t.Fatal("state was inferred from incomplete history")
		}
		return []bindingLabHistory{v, past}
	})
	run("23-bounded-and-empty-inventory", func(t *testing.T) any {
		limited, full, empty := inventory(t, control, 1), inventory(t, control, 32), inventory(t, third, 32)
		if !limited.Truncated || len(limited.Nodes) != 1 || full.Truncated || len(full.Nodes) < 2 || len(empty.Nodes) != 0 || empty.Truncated {
			t.Fatal("inventory bounds incorrect")
		}
		return map[string]any{"limited": limited, "full": full, "empty": empty}
	})
	run("24-reconnect", func(t *testing.T) any {
		before := history(t, ab.CanonicalRef, -1, 100)
		cfg := pool.Config()
		pool.Close()
		var err error
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		after := history(t, ab.CanonicalRef, -1, 100)
		before.Snapshot = ""
		after.Snapshot = ""
		if !reflect.DeepEqual(before, after) {
			t.Fatal("reconnect changed history")
		}
		return after
	})
	run("25-canonical-and-initial-preservation", func(t *testing.T) any {
		after := propositionLabGraph(t, ctx, pool)
		tables := bindingLabTables(t, ctx, pool)
		if !reflect.DeepEqual(beforeGraph, after) || beforeTables["canonical_proposition_bindings"] != tables["canonical_proposition_bindings"] {
			t.Fatal("immutable base was modified")
		}
		v, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{RootNodeIDs: []string{ab.CanonicalRef}, MaxDepth: 1, MaxNodes: 128, MaxEdges: 256})
		if err != nil {
			t.Fatal(err)
		}
		conflicts := 0
		for _, e := range v.Artifact.Edges {
			if e.Relation == evidencegraph.CanonicalEdgeRelation("contradicts") {
				conflicts++
			}
		}
		if conflicts != 1 {
			t.Fatal("conflict lost")
		}
		return map[string]any{"four_tables_equal": true, "initial_memberships_equal": true, "original_conflict_count": conflicts}
	})
	run("26-independent-history-fold", func(t *testing.T) any {
		node := spares[6].CanonicalRef
		state := control.id()
		prev := "membership:" + initials[node].RequestID
		var prefixes []bindingLabHistory
		for cycle := 0; cycle < 4; cycle++ {
			for j, op := range []string{"correct", "withdraw", "restore"} {
				target := key(fmt.Sprintf("fold-target-%d", cycle))
				id := fmt.Sprintf("fold-%d-%d", cycle, j)
				apply(t, request(id, node, op, target))
				v := history(t, node, -1, 100)
				last := v.Events[len(v.Events)-1]
				if last.Request.FromID != state || last.Request.PreviousRef != prev || last.Revision != int64(len(prefixes)+1) {
					t.Fatal("history chain discontinuity")
				}
				if op == "withdraw" {
					state = ""
				} else {
					state = target.id()
				}
				prev = "event:" + id
				if v.PropositionID != state || v.Active != (state != "") {
					t.Fatal("reference transition differs")
				}
				prefixes = append(prefixes, v)
			}
		}
		// Requery every historical prefix after all later writes are committed.
		for i, old := range prefixes {
			got := history(t, node, int64(i+1), 100)
			if got.PropositionID != old.PropositionID || got.HeadRef != old.HeadRef || !reflect.DeepEqual(got.Events, old.Events) {
				t.Fatal("later writes changed historical prefix")
			}
		}
		return prefixes
	})
	run("27-direct-sql-transition-guards", func(t *testing.T) any {
		node := spares[8].CanonicalRef
		before := bindingLabTables(t, ctx, pool)
		var errs []string
		for i, sql := range []string{
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-gap',$1,2,'membership:initial-control-08',$2,$3,'correct','actor','reason','ref',now()`,
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-ref',$1,1,'membership:initial-control-09',$2,$3,'correct','actor','reason','ref',now()`,
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-from',$1,1,'membership:initial-control-08',$3,$2,'correct','actor','reason','ref',now()`,
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-null',$1,1,'membership:initial-control-08',$2,NULL,'correct','actor','reason','ref',now() WHERE $3<>''`,
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-op',$1,1,'membership:initial-control-08',$2,$3,'invented','actor','reason','ref',now()`,
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-audit',$1,1,'membership:initial-control-08',$2,$3,'correct','','reason','ref',now()`,
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-missing-node','absent',1,'membership:initial-control-08',$2,$3,'correct','actor','reason','ref',now() WHERE $1<>''`,
			`INSERT INTO canonical_proposition_binding_events SELECT 'direct-missing-target',$1,1,'membership:initial-control-08',$2,'absent-proposition','correct','actor','reason','ref',now() WHERE $3<>''`,
		} {
			_, err := pool.Exec(ctx, sql, node, control.id(), correct.id())
			if err == nil {
				t.Fatalf("direct invalid event accepted %d", i)
			}
			errs = append(errs, err.Error())
		}
		if !reflect.DeepEqual(before, bindingLabTables(t, ctx, pool)) {
			t.Fatal("direct rejection leaked changes")
		}
		return errs
	})
	run("28-old-bind-cannot-restore", func(t *testing.T) any {
		node := spares[7].CanonicalRef
		apply(t, request("withdraw-old-api", node, "withdraw", propositionLabKey{}))
		before := bindingLabTables(t, ctx, pool)
		in := initials[node]
		replayed, err := propositionLabBind(ctx, pool, in, "")
		if err != nil || !replayed.Replayed {
			t.Fatalf("initial replay: %v", err)
		}
		in.RequestID = "new-initial-receipt"
		_, err = propositionLabBind(ctx, pool, in, "")
		if err == nil {
			t.Fatal("old binder replaced initial receipt")
		}
		v := history(t, node, -1, 100)
		if v.Active || v.HeadRevision != 1 || !reflect.DeepEqual(before, bindingLabTables(t, ctx, pool)) {
			t.Fatal("old API resurrected membership")
		}
		return map[string]any{"initial_receipt_replay": replayed, "current": v, "new_request_error": err.Error()}
	})
	finalGraph := propositionLabGraph(t, ctx, pool)
	finalTables := bindingLabTables(t, ctx, pool)
	if !reflect.DeepEqual(beforeGraph, finalGraph) || beforeTables["canonical_proposition_bindings"] != finalTables["canonical_proposition_bindings"] {
		t.Error("final base preservation failed")
	}
	propositionLabWrite(t, filepath.Join(out, "canonical-after.json"), finalGraph)
	propositionLabWrite(t, filepath.Join(out, "registry-after.json"), finalTables)
	propositionLabWrite(t, filepath.Join(out, "cases.json"), cases)
	// Save raw table JSON as objects too, for independent offline transition fold.
	var events any
	if err := json.Unmarshal([]byte(finalTables["canonical_proposition_binding_events"]), &events); err != nil {
		t.Fatal(err)
	}
	propositionLabWrite(t, filepath.Join(out, "events.json"), events)
}
