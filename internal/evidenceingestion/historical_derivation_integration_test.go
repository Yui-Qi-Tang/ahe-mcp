//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// All fixtures and approvals in this file are TEST APPROVAL STUB, synthetic only.
func historicalProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string, parents ...string) HistoricalDerivationRequest {
	t.Helper()
	n, err := GetCanonicalEvidenceByID(ctx, pool, parents[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err := submitHistoricalFixtureOutput(ctx, pool, ExtractorOutputInput{RequestID: label, SourceSnapshotID: n.OriginProposal.SourceSnapshotID, ExtractionViewID: n.OriginProposal.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{Name: "synthetic-induction", Version: "v1"},
		Output:              FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "J", StatementText: "Hypothesis: contact information may be required; bounded synthetic sample, no control, other causes unknown. " + label, EvidenceRefs: []string{"span:S1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	parents = slices.Clone(parents)
	slices.Sort(parents)
	return HistoricalDerivationRequest{ProposalOccurrenceID: p.ProposalOccurrenceID, Derivation: HistoricalDerivation{ParentNodeIDs: parents, Method: "Induction over the declared samples; no causal proof or exhaustive coverage", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:" + label}}
}

func historicalDecision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, req HistoricalDerivationRequest, id string) HistoricalDerivationDecision {
	t.Helper()
	r, err := LoadHistoricalDerivationReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	return HistoricalDerivationDecision{RequestID: id, Review: req, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: historical trace only", ReviewerID: "TEST APPROVAL STUB"}
}

func historicalSources(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int, current bool) []string {
	t.Helper()
	parents := make([]string, count)
	for i := range parents {
		label := fmt.Sprintf("historical-source-%d", i)
		if current {
			in := registrationNativeFixture(t, ctx, pool, label, "HISTORICAL-"+label)
			r, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
			if err != nil {
				t.Fatal(err)
			}
			in.ExpectedSubject = r.Subject
			if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
				t.Fatal(err)
			}
			parents[i] = in.Review.ClaimID
		} else {
			p := createIntegrationClosureExternalProposal(t, ctx, pool, label, "HISTORICAL-"+label, "r1", fmt.Sprintf("Application %d lacked contact information and was returned.", i))
			a, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic source"})
			if err != nil {
				t.Fatal(err)
			}
			parents[i] = a.CanonicalRef
		}
	}
	return parents
}

func assertHistoricalBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) {
	t.Helper()
	for _, policy := range []evidenceuse.Policy{evidenceuse.CurrentActionV1, evidenceuse.GroundedActionV1} {
		p, err := ReadEvidenceUseProjectionForPolicy(ctx, pool, policy, []string{id})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range p.Assessment.Nodes {
			if n.NodeID == id {
				found = true
				if n.Outcome == evidenceuse.Eligible {
					t.Fatal("historical cleared", p)
				}
			}
		}
		if !found {
			t.Fatal("missing assessment")
		}
		plan, err := CheckCurrentEvidencePlan(ctx, pool, policy, []evidenceuse.ActionBasis{{ActionID: "act", BasisIDs: []string{id}}})
		if err != nil || plan.Accepted {
			t.Fatal("historical action cleared", err)
		}
	}
}

func TestIntegrationHistoricalDerivationInductionAndReplay(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 3, false)
	req := historicalProposal(t, ctx, pool, "induction", parents...)
	r, err := LoadHistoricalDerivationReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Display.Basis.SourceLeaves) != 3 || r.Display.Basis.CodeFile != nil {
		t.Fatal("incomplete textual review")
	}
	for _, leaf := range r.Display.Basis.SourceLeaves {
		if strings.Contains(leaf.RawText, "Hypothesis:") {
			t.Fatal("invented J source")
		}
	}
	in := historicalDecision(t, ctx, pool, req, "induction-admit")
	got, err := ApplyHistoricalDerivationDecision(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoricalBlocked(t, ctx, pool, got.Admission.CanonicalRef)
	view, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{RootNodeIDs: []string{got.Admission.CanonicalRef}, Relations: []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom}, MaxDepth: 3, MaxNodes: 64, MaxEdges: 128})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Artifact.Derivations) != 1 || view.Artifact.Derivations[0].UseClass != evidencegraph.DerivationHistoricalOnly || view.Artifact.SchemaVersion != evidencegraph.CanonicalHistoricalSchemaVersion {
		t.Fatal("query lost purpose", view)
	}
	if !slices.Equal(view.Artifact.Derivations[0].Parents, req.Derivation.ParentNodeIDs) {
		t.Fatal("query lost parents")
	}
	portGovernanceDecisionIn := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parents[0], Operation: "withdraw"}, "withdraw-parent")
	if _, err := ApplyGovernanceDecision(ctx, pool, portGovernanceDecisionIn); err != nil {
		t.Fatal(err)
	}
	replay, err := ApplyHistoricalDerivationDecision(ctx, pool, in)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Admission, got.Admission) {
		t.Fatal("saved replay", err)
	}
	saved, err := LoadHistoricalDerivationReview(ctx, pool, req)
	if err != nil || !reflect.DeepEqual(saved.Display, r.Display) {
		t.Fatal("saved display changed", err)
	}
	for _, mutate := range []func(*HistoricalDerivationDecision){func(x *HistoricalDerivationDecision) { x.ReviewerID = "other" }, func(x *HistoricalDerivationDecision) { x.DecisionReason = "changed" }, func(x *HistoricalDerivationDecision) { x.RequestID = "changed" }, func(x *HistoricalDerivationDecision) { x.Review.Derivation.Method = "changed" }} {
		bad := in
		mutate(&bad)
		if _, err := ApplyHistoricalDerivationDecision(ctx, pool, bad); err == nil {
			t.Fatal("changed replay accepted")
		}
	}
	nested := historicalProposal(t, ctx, pool, "nested", got.Admission.CanonicalRef)
	nestedReceipt, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, nested, "nested-admit"))
	if err != nil {
		t.Fatal("historical ancestor", err)
	}
	assertHistoricalBlocked(t, ctx, pool, nestedReceipt.Admission.CanonicalRef)
	if _, err := AdmitPendingProposal(ctx, pool, historicalAdmissionInput(req, in.ReviewerID, in.DecisionReason)); err == nil {
		t.Fatal("generic historical replay")
	}
	raw, err := os.ReadFile("../../migrations/000061_historical_derivation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(raw)); err == nil {
		t.Fatal("rollback discarded history")
	}
}

func TestIntegrationHistoricalDerivationAllCurrentAndBothGates(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 2, true)
	projection, err := ReadEvidenceUseProjection(ctx, pool, parents)
	if err != nil {
		t.Fatal(err)
	}
	for id, source := range projection.Snapshot.Sources {
		checks := []evidenceuse.SourceCheck{}
		for _, criterion := range []string{evidenceuse.RetentionV1, evidenceuse.CorrespondenceV1, evidenceuse.CoverageV1} {
			checks = append(checks, evidenceuse.SourceCheck{SourceSnapshotID: id, ContentHash: source.ContentHash, Scope: evidenceuse.RetainedSnapshotScope, Criterion: criterion, Result: evidenceuse.CheckPass, ComparedRevision: source.ProviderRevision, MaterialRef: id})
		}
		assessment := GovernanceRequest{Kind: "source_assessment", TargetID: id, Operation: "assess", MaterialIDs: []string{id}, Method: "synthetic comparison", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"synthetic checks only"}, Checks: checks}
		if _, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, assessment, "grounded-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	grounded, err := ReadEvidenceUseProjectionForPolicy(ctx, pool, evidenceuse.GroundedActionV1, parents)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range grounded.Assessment.Nodes {
		if node.Outcome != evidenceuse.Eligible {
			t.Fatal("positive grounded parent control", node)
		}
	}

	req := historicalProposal(t, ctx, pool, "all-current", parents...)
	in := historicalDecision(t, ctx, pool, req, "all-current-admit")
	currentDerivation := EndpointDerivation{UsePolicy: evidenceuse.CurrentActionV1, ParentNodeIDs: req.Derivation.ParentNodeIDs, Method: req.Derivation.Method, Producer: req.Derivation.Producer, TraceRef: req.Derivation.TraceRef}
	currentReq := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: req.ProposalOccurrenceID, Derivation: &currentDerivation}
	currentReview, err := LoadEndpointReview(ctx, pool, currentReq)
	if err != nil {
		t.Fatal(err)
	}
	reverse := in
	reverse.ExpectedSubject = currentReview.Subject
	if _, err := ApplyHistoricalDerivationDecision(ctx, pool, reverse); err == nil {
		t.Fatal("current subject accepted by historical entry")
	}
	p, err := GetProposalByOccurrenceID(ctx, pool, req.ProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := buildCanonicalAdmissionMutation(p, historicalAdmissionInput(req, in.ReviewerID, in.DecisionReason))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := guardDerivedCurrentUse(ctx, &endpointUseCut{tx: pgxTx{tx: tx}}, mutation, evidenceuse.CurrentActionV1); err == nil {
		tx.Rollback(ctx)
		t.Fatal("virtual historical mutation treated as ordinary")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitReviewedEndpoint(ctx, pool, ReviewedEndpointAdmissionInput{RequestID: in.RequestID, Review: currentReq, ExpectedSubject: in.ExpectedSubject, Decision: in.Decision, DecisionReason: in.DecisionReason, ReviewerID: in.ReviewerID}); err == nil {
		t.Fatal("historical subject accepted by current")
	}
	got, err := ApplyHistoricalDerivationDecision(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoricalBlocked(t, ctx, pool, got.Admission.CanonicalRef)
	occ := historicalPending(t, ctx, pool, "current-child-endpoint")
	childReq := registrationCurrentEndpointRequest(occ, got.Admission.CanonicalRef)
	childReview, err := LoadEndpointReview(ctx, pool, childReq)
	if err != nil {
		t.Fatal("complete current child review", err)
	}
	if _, err := AdmitReviewedEndpoint(ctx, pool, ReviewedEndpointAdmissionInput{RequestID: "endpoint", Review: childReq, ExpectedSubject: childReview.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}); err == nil {
		t.Fatal("current endpoint laundered historical parent")
	}
}

func TestIntegrationHistoricalDerivationChangedSubjectAndDisposition(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 1, false)
	req := historicalProposal(t, ctx, pool, "changed-subject", parents...)
	in := historicalDecision(t, ctx, pool, req, "changed-subject-decision")
	unrelated := historicalPending(t, ctx, pool, "unrelated-historical-review-control")
	if _, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: unrelated}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := LoadHistoricalDerivationReview(ctx, pool, req)
	if err != nil || unchanged.Subject != in.ExpectedSubject {
		t.Fatal("unrelated write invalidated scoped historical review", err)
	}
	if _, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parents[0], Operation: "withdraw"}, "changed-state")); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyHistoricalDerivationDecision(ctx, pool, in); err == nil {
		t.Fatal("stale historical subject accepted")
	}
	in = historicalDecision(t, ctx, pool, req, in.RequestID)
	in.Decision = "audit_only"
	got, err := ApplyHistoricalDerivationDecision(ctx, pool, in)
	if err != nil || got.Disposition == nil || got.Admission != nil {
		t.Fatal("disposition", err)
	}
	again, err := ApplyHistoricalDerivationDecision(ctx, pool, in)
	if err != nil || !again.Replayed {
		t.Fatal("disposition replay", err)
	}
	if _, err := RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{ProposalOccurrenceID: req.ProposalOccurrenceID, Outcome: "audit_only", DecisionBy: in.ReviewerID, DecisionReason: in.DecisionReason}); err == nil {
		t.Fatal("generic disposition replay")
	}
	bad := in
	bad.Decision = "approved"
	if _, err := ApplyHistoricalDerivationDecision(ctx, pool, bad); err == nil {
		t.Fatal("terminal reopened")
	}
}

func TestIntegrationHistoricalManualAndMultiClaimSource(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	input, fixture := integrationInputFixture(t, "historical-manual")
	input.ManualReviewProfile = manualReviewProfileFixture()
	captured, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := submitHistoricalFixtureOutput(ctx, pool, ExtractorOutputInput{RequestID: "historical-manual-extract", SourceSnapshotID: captured.SourceSnapshotID, ExtractionViewID: captured.ExtractionViewID, Output: fixture})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: manual.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "recorded manual rule"})
	if err != nil {
		t.Fatal(err)
	}
	envelope := validExternalSourceEnvelope("historical-multi")
	envelope.ObjectID = "HISTORICAL-MULTI"
	envelope.Content = "First recorded observation.\nSecond recorded observation."
	source, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := submitHistoricalFixtureOutput(ctx, pool, ExtractorOutputInput{RequestID: "historical-multi-extract", SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID, Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "first", StatementText: "First recorded observation.", EvidenceRefs: []string{"span:S1"}}, {ProposalLocalID: "second", StatementText: "Second recorded observation.", EvidenceRefs: []string{"span:S1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var occurrence string
	if err := pool.QueryRow(ctx, `SELECT proposal_occurrence_id FROM proposal_occurrences WHERE extraction_attempt_id=$1 ORDER BY proposal_occurrence_id LIMIT 1`, batch.ExtractionAttemptID).Scan(&occurrence); err != nil {
		t.Fatal(err)
	}
	statement, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: occurrence, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic external statement"})
	if err != nil {
		t.Fatal(err)
	}
	req := historicalProposal(t, ctx, pool, "manual-multi-judgment", rule.CanonicalRef, statement.CanonicalRef)
	result, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, req, "manual-multi-decision"))
	if err != nil {
		t.Fatal(err)
	}
	assertHistoricalBlocked(t, ctx, pool, result.Admission.CanonicalRef)
}

func TestIntegrationHistoricalParentRegistrationAndNested32(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	registration := registrationNativeFixture(t, ctx, pool, "historical-register", "HISTORICAL-REGISTER")
	req := historicalProposal(t, ctx, pool, "before-registration", registration.Review.ClaimID)
	h, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, req, "before-registration-decision"))
	if err != nil {
		t.Fatal(err)
	}
	freshRegistration, err := LoadInitialLineageRegistrationReview(ctx, pool, registration.Review)
	if err != nil {
		t.Fatal(err)
	}
	registration.ExpectedSubject = freshRegistration.Subject
	if _, err := ApplyInitialLineageRegistration(ctx, pool, registration); err != nil {
		t.Fatal(err)
	}
	assertHistoricalBlocked(t, ctx, pool, h.Admission.CanonicalRef)
	parents := historicalSources(t, ctx, pool, 32, false)
	wide := historicalProposal(t, ctx, pool, "wide", parents...)
	w, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, wide, "wide-decision"))
	if err != nil {
		t.Fatal(err)
	}
	for level := range 2 {
		nested := historicalProposal(t, ctx, pool, fmt.Sprintf("wide-nested-%d", level), w.Admission.CanonicalRef, h.Admission.CanonicalRef)
		w, err = ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, nested, fmt.Sprintf("wide-nested-decision-%d", level)))
		if err != nil {
			t.Fatal(err)
		}
		assertHistoricalBlocked(t, ctx, pool, w.Admission.CanonicalRef)
	}
}

func historicalCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []int {
	t.Helper()
	out := []int{}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "canonical_derivations", "canonical_derivation_parents", "admission_decisions", "canonical_historical_derivation_reviews"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func TestIntegrationHistoricalBindingFailuresAndAtomicRollback(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 1, false)
	req := historicalProposal(t, ctx, pool, "atomic", parents...)
	in := historicalDecision(t, ctx, pool, req, "atomic-decision")
	before := historicalCounts(t, ctx, pool)
	// The internal generic materializer cannot create the historical class
	// without the independent historical approval/binding authority.
	if _, err := AdmitPendingProposal(ctx, pool, historicalAdmissionInput(req, in.ReviewerID, in.DecisionReason)); err == nil {
		t.Fatal("historical class accepted without its review authority")
	}
	if !reflect.DeepEqual(before, historicalCounts(t, ctx, pool)) {
		t.Fatal("missing authority partially persisted")
	}

	bad := req
	bad.Derivation.ParentNodeIDs = []string{"canon-node:missing"}
	if _, err := LoadHistoricalDerivationReview(ctx, pool, bad); err == nil {
		t.Fatal("missing parent accepted")
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_history_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'TEST APPROVAL STUB: force receipt failure'; END $$; CREATE TRIGGER reject_history_fixture BEFORE INSERT ON canonical_historical_derivation_reviews FOR EACH ROW EXECUTE FUNCTION reject_history_fixture()`); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyHistoricalDerivationDecision(ctx, pool, in); err == nil {
		t.Fatal("forced receipt failure accepted")
	}
	if !reflect.DeepEqual(before, historicalCounts(t, ctx, pool)) {
		t.Fatal("partial historical mutation")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_history_fixture ON canonical_historical_derivation_reviews; DROP FUNCTION reject_history_fixture()`); err != nil {
		t.Fatal(err)
	}
	got, err := ApplyHistoricalDerivationDecision(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE canonical_derivations SET use_class='ordinary' WHERE node_id=$1`, got.Admission.CanonicalRef); err == nil {
		t.Fatal("historical classification mutable")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM canonical_historical_derivation_reviews`); err == nil {
		t.Fatal("historical authority deletable")
	}
	forged, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forged.Exec(ctx, `ALTER TABLE canonical_historical_derivation_reviews DISABLE TRIGGER historical_derivation_immutable_rows; UPDATE canonical_historical_derivation_reviews SET receipt_payload=jsonb_set(receipt_payload::jsonb,'{admission,CanonicalEdgeIDs}','[]'::jsonb)::text`); err != nil {
		forged.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := forged.Exec(ctx, `SELECT historical_derivation_assert_v1($1)`, got.Admission.AdmissionDecisionID); err == nil {
		forged.Rollback(ctx)
		t.Fatal("receipt edge manifest can differ from authority")
	}
	if err := forged.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Corruption is confined to a rolled-back owner transaction in this disposable schema.
	next := historicalProposal(t, ctx, pool, "wrong-bytes", parents...)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `ALTER TABLE source_blobs DISABLE TRIGGER USER; UPDATE source_blobs SET raw_content=convert_to('changed fixture bytes','UTF8')`); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHistoricalDerivationReview(ctx, pgxTx{tx: tx}, next); err == nil {
		t.Fatal("wrong source binding accepted")
	}
}

func TestIntegrationHistoricalGovernanceStatesRemainVisible(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 3, false)
	if _, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parents[0], Operation: "withdraw"}, "historical-withdrawn")); err != nil {
		t.Fatal(err)
	}
	q := ContradictionReviewRequest{ProposalRequestID: "historical-conflict", NodeAID: parents[1], NodeBID: parents[2], Rationale: "TEST APPROVAL STUB", Scope: "synthetic", Conditions: []string{}, ProducerName: "synthetic", ProducerVersion: "1", Relation: "contradicts"}
	cr, err := LoadContradictionReview(ctx, pool, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyReviewedContradiction(ctx, pool, ReviewedContradictionInput{RequestID: "historical-conflict-decision", Review: cr.Display.Request, ExpectedSubject: cr.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}); err != nil {
		t.Fatal(err)
	}
	n, err := GetCanonicalEvidenceByID(ctx, pool, parents[0])
	if err != nil {
		t.Fatal(err)
	}
	finding := GovernanceRequest{Kind: "source_assessment", TargetID: n.OriginProposal.SourceSnapshotID, Operation: "assess", MaterialIDs: []string{n.OriginProposal.SourceSnapshotID}, Method: "synthetic disclosure", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"coverage unknown"}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: n.OriginProposal.SourceSnapshotID, ContentHash: n.OriginProposal.RawContentHash, Scope: evidenceuse.RetainedSnapshotScope, Criterion: evidenceuse.CoverageV1, Result: evidenceuse.CheckInconclusive, ComparedRevision: "r1", MaterialRef: n.OriginProposal.SourceSnapshotID}}}
	if _, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, finding, "historical-finding")); err != nil {
		t.Fatal(err)
	}
	previous := createSupersessionExternalProposal(t, ctx, pool, "history-superseded", "r1", "Earlier synthetic policy.")
	old, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: previous.ProposalOccurrenceID})
	if err != nil {
		t.Fatal(err)
	}
	next := createSupersessionExternalProposal(t, ctx, pool, "history-replacement", "r2", "Replacement synthetic policy.")
	if _, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: next.ProposalOccurrenceID, Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{old.CanonicalRef}, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic replacement"}); err != nil {
		t.Fatal(err)
	}
	parents = append(parents, old.CanonicalRef)
	req := historicalProposal(t, ctx, pool, "governance-context", parents...)
	r, err := LoadHistoricalDerivationReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	if r.Display.Basis.UseReview.States[old.CanonicalRef].Currentness.Status != "superseded" || r.Display.Basis.UseReview.States[parents[0]].Withdrawal.Status != evidenceuse.WithdrawalWithdrawn || len(r.Display.Basis.UseReview.Contradictions) != 1 {
		t.Fatal("governance hidden")
	}
	if _, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, req, "governance-context-decision")); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationHistoricalWaitsForGovernanceAndConcurrentReplay(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 1, false)
	req := historicalProposal(t, ctx, pool, "concurrent", parents...)
	in := historicalDecision(t, ctx, pool, req, "concurrent-decision")
	w := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parents[0], Operation: "withdraw"}, "historical-concurrent-withdraw")
	r, err := LoadGovernanceReview(ctx, pool, w.Review)
	if err != nil {
		t.Fatal(err)
	}
	display, err := json.Marshal(r.Display)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(governanceEventBody{Contract: GovernanceEventV1, RequestID: w.RequestID, Subject: w.ExpectedSubject, Display: string(display), Decision: w.Decision, Reason: w.Reason, ReviewerID: w.ReviewerID})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ApplyHistoricalDerivationDecision(ctx, pool, in); done <- err }()
	waitInitialRegistrationLocks(t, ctx, pool, 1)
	if _, err := blocker.Exec(ctx, `INSERT INTO canonical_withdrawal_events(event_id,request_id,target_id,revision,previous_event_id,body) VALUES($1,$2,$3,$4,NULLIF($5,''),$6)`, governanceDigest("governance:sha256:", body), w.RequestID, w.Review.TargetID, w.Review.ExpectedRevision+1, w.Review.PreviousEventID, string(body)); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale concurrent review accepted")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	in = historicalDecision(t, ctx, pool, req, in.RequestID)
	type result struct {
		receipt HistoricalDerivationReceipt
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() { r, e := ApplyHistoricalDerivationDecision(ctx, pool, in); results <- result{r, e} }()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.receipt.Replayed == second.receipt.Replayed {
		t.Fatal("concurrent exact replay", first.err, second.err)
	}
}

func TestIntegrationReviewedDerivationsDoNotHoldHeadWhileWaitingForDerivation(t *testing.T) {
	for _, route := range []string{"historical", "endpoint"} {
		t.Run(route, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			parents := historicalSources(t, ctx, pool, 1, true)
			req := historicalProposal(t, ctx, pool, "lock-order-"+route, parents...)
			var apply func() error
			switch route {
			case "historical":
				in := historicalDecision(t, ctx, pool, req, "lock-order-history")
				apply = func() error { _, err := ApplyHistoricalDerivationDecision(ctx, pool, in); return err }
			case "endpoint":
				q := registrationCurrentEndpointRequest(req.ProposalOccurrenceID, parents...)
				r, err := LoadEndpointReview(ctx, pool, q)
				if err != nil {
					t.Fatal(err)
				}
				in := ReviewedEndpointAdmissionInput{RequestID: "lock-order-endpoint", Review: q, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
				apply = func() error { _, err := AdmitReviewedEndpoint(ctx, pool, in); return err }
			}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, derivationAdmissionLockKey); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- apply() }()
			waitHistoricalDerivationAdvisory(t, ctx, pool)
			probe, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := probe.Exec(ctx, `SET LOCAL lock_timeout='200ms'`); err != nil {
				probe.Rollback(ctx)
				t.Fatal(err)
			}
			_, probeErr := probe.Exec(ctx, `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)`)
			if err := probe.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("writer failed after released blocker", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if probeErr != nil {
				t.Fatal("writer held governance head while waiting for derivation lock", probeErr)
			}
		})
	}
}

func waitHistoricalDerivationAdvisory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks w JOIN pg_locks p ON p.pid=w.pid WHERE w.locktype='advisory' AND NOT w.granted AND p.locktype='relation' AND p.relation='proposal_occurrences'::regclass AND p.mode='RowShareLock' AND p.database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 1 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("derivation advisory barrier not observed")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
