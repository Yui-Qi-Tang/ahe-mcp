//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationAdmitPendingSupersessionAtomicSuccessAndExactReplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()

	oldProposal := createSupersessionExternalProposal(t, ctx, pool, "old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: oldProposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial source-backed claim",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(old) error = %v", err)
	}
	oldTemporal := canonicalTemporalJSON(t, ctx, pool, oldAdmission.CanonicalRef)

	newProposal := createSupersessionExternalProposal(t, ctx, pool, "new", "r2", "Policy value is v2.")
	input := SupersessionAdmissionInput{
		ProposalOccurrenceID: newProposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "reviewed exact source replacement",
		Basis:                basis,
		TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
	}
	result, err := AdmitPendingSupersession(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitPendingSupersession() error = %v", err)
	}
	if result.EventRevision != 1 || result.PreviousHeadEventID != "" ||
		!slices.Equal(result.TargetNodeIDs, []string{oldAdmission.CanonicalRef}) ||
		!slices.Equal(result.BootstrappedTargetNodeIDs, []string{oldAdmission.CanonicalRef}) ||
		len(result.SupersedesEdgeIDs) != 1 || len(result.CanonicalEdgeIDs) != 2 {
		t.Fatalf("supersession result = %+v", result)
	}

	var from, to, relation, originProposal string
	if err := pool.QueryRow(ctx, `
		SELECT from_node_id, to_node_id, relation, origin_proposal_occurrence_id
		FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
	`, result.SupersedesEdgeIDs[0]).Scan(&from, &to, &relation, &originProposal); err != nil {
		t.Fatalf("load supersedes edge: %v", err)
	}
	if from != result.CanonicalRef || to != oldAdmission.CanonicalRef ||
		relation != string(evidencegraph.CanonicalSupersedes) || originProposal != newProposal.ProposalOccurrenceID {
		t.Fatalf("supersedes edge = %s -> %s / %s / %s", from, to, relation, originProposal)
	}
	if got := canonicalTemporalJSON(t, ctx, pool, oldAdmission.CanonicalRef); got != oldTemporal {
		t.Fatalf("old temporal changed from %s to %s", oldTemporal, got)
	}
	assertCanonicalTemporalStatus(t, ctx, pool, result.CanonicalRef, evidencegraph.TemporalUnknown)

	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
	}
	if head.Revision != 1 || head.HeadEventID != result.AdmissionEventID {
		t.Fatalf("head = %+v, want revision 1 event %s", head, result.AdmissionEventID)
	}
	assertSupersessionIntegrationCounts(t, ctx, pool, 1, 2, 1)

	replay, err := AdmitPendingSupersession(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(replay) error = %v", err)
	}
	if !replay.Replayed || replay.AdmissionEventID != result.AdmissionEventID || replay.CanonicalRef != result.CanonicalRef {
		t.Fatalf("replay = %+v, want event/ref from %+v", replay, result)
	}
	assertSupersessionIntegrationCounts(t, ctx, pool, 1, 2, 1)

	changed := input
	changed.DecisionReason = "different reason"
	_, err = AdmitPendingSupersession(ctx, pool, changed)
	assertKind(t, err, ErrorSupersessionReplayConflict)
	changed = input
	changed.Basis.SlotID = "summary"
	_, err = AdmitPendingSupersession(ctx, pool, changed)
	assertKind(t, err, ErrorSupersessionReplayConflict)
	changed = input
	changed.ExpectedRevision = 1
	changed.ExpectedHeadEventID = result.AdmissionEventID
	_, err = AdmitPendingSupersession(ctx, pool, changed)
	assertKind(t, err, ErrorSupersessionReplayConflict)

	_, err = AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: newProposal.ProposalOccurrenceID})
	assertKind(t, err, ErrorAdmissionStateConflict)
}

func TestIntegrationSupersessionReplaySurvivesLaterEvent(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()
	old := createSupersessionExternalProposal(t, ctx, pool, "history-old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: old.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit old: %v", err)
	}
	v2 := createSupersessionExternalProposal(t, ctx, pool, "history-v2", "r2", "Policy value is v2.")
	firstInput := SupersessionAdmissionInput{
		ProposalOccurrenceID: v2.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v2 replaces v1",
		Basis:                basis,
		TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
	}
	first, err := AdmitPendingSupersession(ctx, pool, firstInput)
	if err != nil {
		t.Fatalf("admit v2: %v", err)
	}
	v3 := createSupersessionExternalProposal(t, ctx, pool, "history-v3", "r3", "Policy value is v3.")
	second, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v3.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v3 replaces v2",
		Basis:                basis,
		TargetNodeIDs:        []string{first.CanonicalRef},
		ExpectedRevision:     first.EventRevision,
		ExpectedHeadEventID:  first.AdmissionEventID,
	})
	if err != nil {
		t.Fatalf("admit v3: %v", err)
	}
	if second.EventRevision != 2 || len(second.BootstrappedTargetNodeIDs) != 0 {
		t.Fatalf("second result = %+v", second)
	}
	replay, err := AdmitPendingSupersession(ctx, pool, firstInput)
	if err != nil {
		t.Fatalf("replay first after second event: %v", err)
	}
	if !replay.Replayed || replay.AdmissionEventID != first.AdmissionEventID {
		t.Fatalf("old replay = %+v", replay)
	}
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head: %v", err)
	}
	if head.Revision != 2 || head.HeadEventID != second.AdmissionEventID {
		t.Fatalf("replay moved head: %+v", head)
	}
}

func TestIntegrationSupersessionHeadCASAllowsOneParallelWriter(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()
	old := createSupersessionExternalProposal(t, ctx, pool, "parallel-old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: old.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit old: %v", err)
	}
	candidates := []IngestResult{
		createSupersessionExternalProposal(t, ctx, pool, "parallel-a", "r2", "Policy branch A."),
		createSupersessionExternalProposal(t, ctx, pool, "parallel-b", "r3", "Policy branch B."),
	}

	type outcome struct {
		index  int
		result SupersessionAdmissionResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(candidates))
	var wg sync.WaitGroup
	for index, candidate := range candidates {
		wg.Add(1)
		go func(index int, candidate IngestResult) {
			defer wg.Done()
			<-start
			result, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
				ProposalOccurrenceID: candidate.ProposalOccurrenceID,
				DecisionBy:           "yuki",
				DecisionReason:       fmt.Sprintf("parallel branch %d", index),
				Basis:                basis,
				TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
			})
			outcomes <- outcome{index: index, result: result, err: err}
		}(index, candidate)
	}
	close(start)
	wg.Wait()
	close(outcomes)

	successes := 0
	conflicts := 0
	loser := -1
	for outcome := range outcomes {
		switch {
		case outcome.err == nil:
			successes++
		case supersessionHasErrorKind(outcome.err, ErrorSupersessionHeadConflict):
			conflicts++
			loser = outcome.index
		default:
			t.Fatalf("parallel writer %d error = %v", outcome.index, outcome.err)
		}
	}
	if successes != 1 || conflicts != 1 || loser < 0 {
		t.Fatalf("parallel outcomes successes=%d conflicts=%d loser=%d", successes, conflicts, loser)
	}
	loserProposal, err := GetProposalByOccurrenceID(ctx, pool, candidates[loser].ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("load loser proposal: %v", err)
	}
	if loserProposal.AdmissionOutcome != admissionOutcomePending || loserProposal.CanonicalRef != "" {
		t.Fatalf("loser proposal mutated = %+v", loserProposal)
	}
	assertSupersessionIntegrationCounts(t, ctx, pool, 1, 2, 1)
}

func TestIntegrationSupersessionRejectsInvalidTargetsWithoutMutation(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()
	old := createSupersessionExternalProposal(t, ctx, pool, "invalid-old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: old.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit old: %v", err)
	}
	candidate := createSupersessionExternalProposal(t, ctx, pool, "invalid-new", "r2", "Policy value is v2.")
	base := SupersessionAdmissionInput{
		ProposalOccurrenceID: candidate.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "negative target validation",
		Basis:                basis,
		TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
	}

	missing := base
	missing.TargetNodeIDs = []string{"canon-node:missing"}
	_, err = AdmitPendingSupersession(ctx, pool, missing)
	assertKind(t, err, ErrorSupersessionInvariant)

	duplicate := base
	duplicate.TargetNodeIDs = []string{oldAdmission.CanonicalRef, " " + oldAdmission.CanonicalRef + " "}
	_, err = AdmitPendingSupersession(ctx, pool, duplicate)
	assertKind(t, err, ErrorInvalidInput)

	rawTarget := base
	rawTarget.TargetNodeIDs = []string{oldAdmission.RawEvidenceNodeIDs[0]}
	_, err = AdmitPendingSupersession(ctx, pool, rawTarget)
	assertKind(t, err, ErrorSupersessionInvariant)

	wrongLineage := base
	wrongLineage.Basis.ObjectID = "AHE-OTHER"
	_, err = AdmitPendingSupersession(ctx, pool, wrongLineage)
	assertKind(t, err, ErrorSupersessionInvariant)

	proposal, err := GetProposalByOccurrenceID(ctx, pool, candidate.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("invalid commands mutated proposal = %+v", proposal)
	}
	assertSupersessionIntegrationCounts(t, ctx, pool, 0, 0, 0)
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head: %v", err)
	}
	if head.Revision != 0 || head.HeadEventID != "" {
		t.Fatalf("invalid commands moved head = %+v", head)
	}
}

func TestIntegrationSupersessionEventFailureRollsBackEntireAdmission(t *testing.T) {
	ctx, pool := integrationPool(t)
	old := createSupersessionExternalProposal(t, ctx, pool, "rollback-old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: old.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit old: %v", err)
	}
	candidate := createSupersessionExternalProposal(t, ctx, pool, "rollback-new", "r2", "Policy value is v2.")
	before := supersessionAuthorityCounts(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION fail_supersession_event_insert()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			RAISE EXCEPTION 'forced supersession event failure';
		END;
		$$;

		CREATE TRIGGER fail_supersession_event_insert
		BEFORE INSERT ON canonical_supersession_admission_events
		FOR EACH ROW EXECUTE FUNCTION fail_supersession_event_insert();
	`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}

	_, err = AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: candidate.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "forced rollback",
		Basis:                supersessionIntegrationBasis(),
		TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
	})
	if err == nil {
		t.Fatal("AdmitPendingSupersession() error = nil, want forced event failure")
	}
	after := supersessionAuthorityCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("failed writer changed authority counts from %+v to %+v", before, after)
	}
	proposal, err := GetProposalByOccurrenceID(ctx, pool, candidate.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("failed writer mutated proposal = %+v", proposal)
	}
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head: %v", err)
	}
	if head.Revision != 0 || head.HeadEventID != "" {
		t.Fatalf("failed writer moved head = %+v", head)
	}
}

func createSupersessionExternalProposal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	requestSuffix string,
	revision string,
	statement string,
) IngestResult {
	t.Helper()
	envelope := validExternalSourceEnvelope("supersession-source-" + requestSuffix)
	envelope.Revision = revision
	envelope.Content = statement + "\n"
	envelope.ObservedAt = "2026-08-23T02:03:04Z"
	envelope.SourceUpdatedAt = ""
	source, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil {
		t.Fatalf("CaptureExternalSource(%s) error = %v", requestSuffix, err)
	}
	result, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID:        "supersession-extractor-" + requestSuffix,
		SourceSnapshotID: source.SourceSnapshotID,
		ExtractionViewID: source.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{
			Name:    "supersession-integration-extractor",
			Version: "v1",
		},
		Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "replacement",
			StatementText:   statement,
			EvidenceRefs:    []string{"span:S1"},
		}}},
	})
	if err != nil {
		t.Fatalf("SubmitExtractorOutput(%s) error = %v", requestSuffix, err)
	}
	return result
}

func supersessionIntegrationBasis() SupersessionLineageBasis {
	return SupersessionLineageBasis{
		SourceSystem:    "jira",
		SourceNamespace: "acme/eng",
		ObjectType:      "issue",
		ObjectID:        "AHE-42",
		SlotKind:        "field",
		SlotID:          "description",
	}
}

func canonicalTemporalJSON(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nodeID string) string {
	t.Helper()
	var temporal string
	if err := pool.QueryRow(ctx, `
		SELECT temporal::text
		FROM canonical_graph_nodes
		WHERE canonical_node_id = $1
	`, nodeID).Scan(&temporal); err != nil {
		t.Fatalf("load temporal for %s: %v", nodeID, err)
	}
	return temporal
}

func assertCanonicalTemporalStatus(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	nodeID string,
	want evidencegraph.TemporalStatus,
) {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT temporal ->> 'status'
		FROM canonical_graph_nodes
		WHERE canonical_node_id = $1
	`, nodeID).Scan(&status); err != nil {
		t.Fatalf("load temporal status for %s: %v", nodeID, err)
	}
	if status != string(want) {
		t.Fatalf("temporal status for %s = %q, want %q", nodeID, status, want)
	}
}

func assertSupersessionIntegrationCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	events int,
	members int,
	targets int,
) {
	t.Helper()
	for table, want := range map[string]int{
		"canonical_supersession_admission_events":    events,
		"canonical_supersession_members":             members,
		"canonical_supersession_replacement_targets": targets,
	} {
		var got int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}

type supersessionIntegrationAuthorityCounts struct {
	CanonicalNodes     int
	CanonicalEdges     int
	AdmissionDecisions int
	Lineages           int
	Events             int
	Members            int
	ReplacementTargets int
}

func supersessionAuthorityCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) supersessionIntegrationAuthorityCounts {
	t.Helper()
	var result supersessionIntegrationAuthorityCounts
	queries := []struct {
		table string
		out   *int
	}{
		{table: "canonical_graph_nodes", out: &result.CanonicalNodes},
		{table: "canonical_graph_edges", out: &result.CanonicalEdges},
		{table: "admission_decisions", out: &result.AdmissionDecisions},
		{table: "canonical_supersession_lineages", out: &result.Lineages},
		{table: "canonical_supersession_admission_events", out: &result.Events},
		{table: "canonical_supersession_members", out: &result.Members},
		{table: "canonical_supersession_replacement_targets", out: &result.ReplacementTargets},
	}
	for _, query := range queries {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+query.table).Scan(query.out); err != nil {
			t.Fatalf("count %s: %v", query.table, err)
		}
	}
	return result
}

func supersessionHasErrorKind(err error, want ErrorKind) bool {
	var domainErr *DomainError
	return errors.As(err, &domainErr) && domainErr.Kind == want
}
