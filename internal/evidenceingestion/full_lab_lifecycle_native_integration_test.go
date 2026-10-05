//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceprojection"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/jackc/pgx/v5/pgxpool"
)

type lifecycleScenario struct {
	Name    string
	A, B    [3]string
	R       string
	Reverse bool
}

var lifecycleScenarios = []lifecycleScenario{
	{Name: "policy", A: [3]string{"Keep release audit evidence for 30 days.", "Keep release audit evidence for 60 days.", "Keep release audit evidence for 90 days."}, B: [3]string{"A release needs one reviewer.", "A release needs two independent reviewers.", "A release needs two reviewers and an incident owner."}, R: "The recorded release policy combines the retained-evidence rule and independent-review rule."},
	{Name: "api", A: [3]string{"POST /v1/orders requires a signed request body.", "POST /v2/orders requires a signed request body and timestamp.", "POST /v2/orders requires a signed body, timestamp, and key identifier."}, B: [3]string{"Reject reused nonces within 60 seconds.", "Reject reused nonces within 120 seconds.", "Reject reused nonces within 300 seconds."}, R: "The recorded order endpoint contract combines request authentication and replay rejection.", Reverse: true},
	{Name: "deployment", A: [3]string{"Run two service replicas in one zone.", "Run three service replicas across two zones.", "Run four service replicas across three zones."}, B: [3]string{"Promotion needs 30 seconds of successful health checks.", "Promotion needs 60 seconds of successful health checks and no active incident.", "Promotion needs 120 seconds of successful health checks and no active incident."}, R: "The recorded deployment gate combines placement capacity and healthy promotion conditions."},
}

type lifecycleFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	dir      string
	scenario lifecycleScenario
	sequence int
}

// TestLifecycleLabNative runs only synthetic, explicitly authorized test approvals.
// It never writes canonical tables directly; SQL below is read-only export.
func TestLifecycleLabNative(t *testing.T) {
	root := os.Getenv("LIFECYCLE_LAB_OUTPUT")
	if root == "" {
		t.Fatal("LIFECYCLE_LAB_OUTPUT is required")
	}
	states := []string{"both_current", "a_superseded", "b_superseded", "unrelated_changed", "a_pending", "a_unclassified"}
	for _, scenario := range lifecycleScenarios {
		for _, state := range states {
			if !t.Run(scenario.Name+"/"+state, func(t *testing.T) {
				dir := filepath.Join(root, scenario.Name, state)
				if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal("refuse existing native attempt: ", err)
				}
				ctx, pool := integrationPool(t)
				f := lifecycleFixture{t: t, ctx: ctx, pool: pool, dir: dir, scenario: scenario}
				f.run(state)
			}) {
				return
			}
		}
	}
}

func (f *lifecycleFixture) save(name string, value any) {
	f.t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		f.t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(f.dir, name+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		f.t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *lifecycleFixture) call(name string, input, output any, err error) {
	f.t.Helper()
	f.sequence++
	message := ""
	if err != nil {
		message = err.Error()
	}
	f.save(fmt.Sprintf("operation-%03d", f.sequence), map[string]any{"operation": name, "input": input, "output": output, "error": message, "approval": "TEST APPROVAL STUB"})
	if err != nil {
		f.t.Fatalf("native gate failed in %s: %v", name, err)
	}
}

func (f *lifecycleFixture) basis(role string) SupersessionLineageBasis {
	return SupersessionLineageBasis{SourceSystem: "synthetic-lab", SourceNamespace: "and-lifecycle-state-v1", ObjectType: "document", ObjectID: f.scenario.Name + "-" + role, SlotKind: "field", SlotID: "rule"}
}

func (f *lifecycleFixture) proposal(role, revision, statement string) IngestResult {
	f.t.Helper()
	basis := f.basis(role)
	envelope := ExternalSourceEnvelopeV1{
		SchemaVersion: ExternalSourceEnvelopeSchemaV1, RequestID: f.scenario.Name + "-" + role + "-" + revision,
		SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID,
		Revision: revision, SourceLocation: "https://fixture.example/" + basis.ObjectID, Title: "Synthetic " + f.scenario.Name + " " + role,
		ContentFormat: ExternalSourceContentFormatMarkdown, ContentFidelity: ExternalSourceContentFidelityVerbatim,
		Content: statement + "\n", Coverage: ExternalSourceCoverageFullDocument, CollectorID: "lifecycle-lab", ConnectorID: "synthetic-fixture",
		ObservedAt: "2026-09-26T00:00:00Z",
	}
	source, err := CaptureExternalSource(f.ctx, f.pool, envelope)
	f.call("CaptureExternalSource", envelope, source, err)
	input := ExtractorOutputInput{RequestID: envelope.RequestID + "-extract", SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{Name: "lifecycle-lab-verbatim", Version: "v1"},
		Output:              FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "rule", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}},
	}
	result, err := SubmitExtractorOutput(f.ctx, f.pool, input)
	f.call("SubmitExtractorOutput", input, result, err)
	return result
}

func (f *lifecycleFixture) admit(proposal IngestResult, parents []string) AdmissionResult {
	f.t.Helper()
	input := AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "Synthetic lifecycle experiment; not human-reviewed evidence"}
	if len(parents) > 0 {
		input.Derivation = &DerivationAdmissionInput{ParentNodeIDs: parents, Method: "synthetic_conjunction", Producer: "lifecycle-lab", TraceRef: "test-approval-stub:historical-and"}
	}
	result, err := AdmitPendingProposal(f.ctx, f.pool, input)
	f.call("AdmitPendingProposal", input, result, err)
	return result
}

func (f *lifecycleFixture) supersede(role string, proposal IngestResult, target string) SupersessionAdmissionResult {
	f.t.Helper()
	head, err := GetCanonicalSupersessionHead(f.ctx, f.pool)
	f.call("GetCanonicalSupersessionHead", nil, head, err)
	input := SupersessionAdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "Synthetic reviewed replacement for this exact source slot", Basis: f.basis(role), TargetNodeIDs: []string{target}, ExpectedRevision: head.Revision, ExpectedHeadEventID: head.HeadEventID}
	result, err := AdmitPendingSupersession(f.ctx, f.pool, input)
	f.call("AdmitPendingSupersession", input, result, err)
	return result
}

func (f *lifecycleFixture) bootstrap(role string, values [3]string) SupersessionAdmissionResult {
	old := f.admit(f.proposal(role, "r0", values[0]), nil)
	return f.supersede(role, f.proposal(role, "r1", values[1]), old.CanonicalRef)
}

func (f *lifecycleFixture) run(state string) {
	var a, b SupersessionAdmissionResult
	if f.scenario.Reverse {
		b = f.bootstrap("B", f.scenario.B)
		a = f.bootstrap("A", f.scenario.A)
	} else {
		a = f.bootstrap("A", f.scenario.A)
		b = f.bootstrap("B", f.scenario.B)
	}
	r := f.admit(f.proposal("R", "r1", f.scenario.R), []string{a.CanonicalRef, b.CanonicalRef})
	f.save("roles", map[string]any{"A": a.CanonicalRef, "B": b.CanonicalRef, "R": r.CanonicalRef, "A_lineage": a.LineageKey, "B_lineage": b.LineageKey})
	f.save("source", map[string]string{"A": f.scenario.A[1], "B": f.scenario.B[1], "R": f.scenario.R})
	expectedA, expectedB := "current", "current"
	switch state {
	case "both_current":
	case "a_superseded":
		f.supersede("A", f.proposal("A", "r2", f.scenario.A[2]), a.CanonicalRef)
		expectedA = "superseded"
	case "b_superseded":
		f.supersede("B", f.proposal("B", "r2", f.scenario.B[2]), b.CanonicalRef)
		expectedB = "superseded"
	case "unrelated_changed":
		f.bootstrap("U", [3]string{"Unrelated log format is version one.", "Unrelated log format is version two.", "Unused."})
	case "a_pending":
		pending := f.proposal("A", "r2", f.scenario.A[2])
		readback, err := GetProposalByOccurrenceID(f.ctx, f.pool, pending.ProposalOccurrenceID)
		f.call("GetProposalByOccurrenceID", map[string]string{"proposal_occurrence_id": pending.ProposalOccurrenceID}, readback, err)
		if readback.AdmissionOutcome != "pending" || readback.CanonicalRef != "" {
			f.t.Fatal("replacement proposal is not pending")
		}
		f.save("pending", readback)
	case "a_unclassified":
		f.admit(f.proposal("A", "r2", f.scenario.A[2]), nil)
		expectedA = "unknown"
	default:
		f.t.Fatal("unknown planned state")
	}
	// From this point no evidence writes occur until the schema is disposed.
	headBefore, err := GetCanonicalSupersessionHead(f.ctx, f.pool)
	f.call("GetCanonicalSupersessionHead", nil, headBefore, err)
	readInput := CanonicalReadInput{RootNodeIDs: []string{r.CanonicalRef}, Relations: []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom}, MaxDepth: 1, MaxNodes: 3, MaxEdges: 2}
	view, err := ReadCanonicalGraphView(f.ctx, f.pool, readInput)
	f.call("ReadCanonicalGraphView", readInput, view, err)
	if view.Truncated || len(view.Artifact.Nodes) != 3 || len(view.Artifact.Edges) != 2 || len(view.Artifact.Derivations) != 1 {
		f.t.Fatal("historical structure changed")
	}
	parents := []string{a.CanonicalRef, b.CanonicalRef}
	slices.Sort(parents)
	if !slices.Equal(parents, view.Artifact.Derivations[0].Parents) {
		f.t.Fatal("wrong declared parents")
	}
	prepared, err := evidenceprojection.PrepareTopology(view.Artifact)
	f.call("PrepareTopology", map[string]string{"artifact": "canonical-view.json"}, map[string]bool{"prepared": err == nil}, err)
	query := evidenceprojection.PathQuery{FromNodeID: a.CanonicalRef, ToNodeID: r.CanonicalRef, Relations: []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom}}
	path, err := prepared.FindPath(query)
	f.call("FindPath", query, path, err)
	if !path.Found {
		f.t.Fatal("historical path missing")
	}
	f.save("canonical-view", view)
	f.save("path", path)
	for _, item := range []struct{ role, key, node, want string }{{"A", a.LineageKey, a.CanonicalRef, expectedA}, {"B", b.LineageKey, b.CanonicalRef, expectedB}} {
		var raw evidencesupersession.ClosureCutInput
		err = withReadOnlyTx(f.ctx, pgxDB{pool: f.pool}, func(tx sqlTx) error {
			var e error
			raw, e = loadCanonicalSupersessionClosureInput(f.ctx, tx, item.key)
			return e
		})
		f.call("ExportCurrentnessStaticRecords", map[string]string{"lineage_key": item.key}, raw, err)
		f.save("static-"+item.role, raw)
		result, e := GetCanonicalSupersessionCurrentness(f.ctx, f.pool, item.key)
		f.call("GetCanonicalSupersessionCurrentness", map[string]string{"lineage_key": item.key}, result, e)
		f.save("currentness-"+item.role, result)
		if raw.Head.Revision != headBefore.Revision || raw.Head.HeadEventID != headBefore.HeadEventID || raw.Head != result.Projection.Head {
			f.t.Fatal("snapshot head mismatch")
		}
		status := ""
		for _, node := range result.Projection.Nodes {
			if node.NodeID == item.node {
				status = string(node.Status)
			}
		}
		// A separately implemented record-level check does not call ProjectCurrentness.
		independent := "current"
		for _, claim := range raw.ObjectClaims {
			if claim.LineageKey == "" {
				independent = "unknown"
			}
		}
		for _, edge := range raw.Edges {
			if edge.ToNodeID == item.node && edge.Relation == "supersedes" {
				independent = "superseded"
			}
		}
		if status != item.want || independent != item.want {
			f.t.Fatalf("native state mismatch for %s: API=%s record-check=%s plan=%s", item.role, status, independent, item.want)
		}
		var second evidencesupersession.ClosureCutInput
		err = withReadOnlyTx(f.ctx, pgxDB{pool: f.pool}, func(tx sqlTx) error {
			var e error
			second, e = loadCanonicalSupersessionClosureInput(f.ctx, tx, item.key)
			return e
		})
		f.call("RecheckStaticRecords", map[string]string{"lineage_key": item.key}, second, err)
		// The native loader does not promise row order for its edge relation.
		// Raw exports remain unchanged; compare all row values after ID sorting.
		slices.SortFunc(raw.Edges, func(a, b evidencesupersession.ClosureEdgeRecord) int { return strings.Compare(a.ID, b.ID) })
		slices.SortFunc(second.Edges, func(a, b evidencesupersession.ClosureEdgeRecord) int { return strings.Compare(a.ID, b.ID) })
		if !reflect.DeepEqual(raw, second) {
			f.t.Fatal("state records changed across reads")
		}
	}
	headAfter, err := GetCanonicalSupersessionHead(f.ctx, f.pool)
	f.call("GetCanonicalSupersessionHead", nil, headAfter, err)
	if headBefore != headAfter {
		f.t.Fatal("head changed after freezing writes")
	}
	f.save("native-gate", map[string]any{"passed": true, "planned_state": state, "expected_A": expectedA, "expected_B": expectedB, "structural_path": true, "declared_integrity": true, "same_head": headBefore, "static_records_stable": true, "approval": "TEST APPROVAL STUB", "transport": "native domain functions, not MCP RPC"})
}
