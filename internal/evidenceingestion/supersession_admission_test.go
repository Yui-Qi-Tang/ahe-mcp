package evidenceingestion

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRunSupersessionAdmissionAttemptsRetriesOneSerializationFailure(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := runSupersessionAdmissionAttempts(func() error {
		attempts++
		if attempts == 1 {
			return &pgconn.PgError{Code: "40001", Message: "injected serialization failure"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("runSupersessionAdmissionAttempts() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRunSupersessionAdmissionAttemptsStopsAtBound(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := runSupersessionAdmissionAttempts(func() error {
		attempts++
		return &pgconn.PgError{Code: "40001", Message: "injected serialization failure"}
	})
	if err == nil || !isSupersessionSerializationFailure(err) ||
		!strings.Contains(err.Error(), "exceeded 3 serialization attempts") {
		t.Fatalf("runSupersessionAdmissionAttempts() error = %v, want bounded serialization failure", err)
	}
	if attempts != supersessionAdmissionSerializationAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, supersessionAdmissionSerializationAttempts)
	}
}

func TestRunSupersessionAdmissionAttemptsDoesNotRetryOtherFailures(t *testing.T) {
	t.Parallel()

	want := errors.New("injected non-serialization failure")
	attempts := 0
	err := runSupersessionAdmissionAttempts(func() error {
		attempts++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("runSupersessionAdmissionAttempts() error = %v, want %v", err, want)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestNormalizeSupersessionAdmissionInputCanonicalizesReviewedCommand(t *testing.T) {
	input, basis, err := normalizeSupersessionAdmissionInput(SupersessionAdmissionInput{
		ProposalOccurrenceID: " occ:proposal ",
		DecisionBy:           " yuki ",
		DecisionReason:       " reviewed replacement ",
		Basis: SupersessionLineageBasis{
			SourceSystem:    " jira ",
			SourceNamespace: " tenant:acme ",
			ObjectType:      " issue ",
			ObjectID:        " PAY-42 ",
			SlotKind:        " field ",
			SlotID:          " description ",
		},
		TargetNodeIDs: []string{"canon-node:old-b", " canon-node:old-a "},
	})
	if err != nil {
		t.Fatalf("normalizeSupersessionAdmissionInput() error = %v", err)
	}
	if input.ProposalOccurrenceID != "occ:proposal" ||
		input.DecisionBy != "yuki" ||
		input.DecisionReason != "reviewed replacement" ||
		basis.SourceSystem != "jira" ||
		input.Basis.SlotID != "description" ||
		!slices.Equal(input.TargetNodeIDs, []string{"canon-node:old-a", "canon-node:old-b"}) {
		t.Fatalf("normalized input = %+v, basis = %+v", input, basis)
	}
}

func TestNormalizeSupersessionAdmissionInputRejectsIncompleteAuthority(t *testing.T) {
	valid := SupersessionAdmissionInput{
		ProposalOccurrenceID: "occ:proposal",
		DecisionBy:           "yuki",
		DecisionReason:       "reviewed replacement",
		Basis: SupersessionLineageBasis{
			SourceSystem:    "jira",
			SourceNamespace: "tenant:acme",
			ObjectType:      "issue",
			ObjectID:        "PAY-42",
			SlotKind:        "field",
			SlotID:          "description",
		},
		TargetNodeIDs: []string{"canon-node:old"},
	}
	tests := []struct {
		name   string
		mutate func(*SupersessionAdmissionInput)
	}{
		{name: "missing reviewer", mutate: func(value *SupersessionAdmissionInput) { value.DecisionBy = "" }},
		{name: "missing reason", mutate: func(value *SupersessionAdmissionInput) { value.DecisionReason = "" }},
		{name: "missing slot", mutate: func(value *SupersessionAdmissionInput) { value.Basis.SlotID = "" }},
		{name: "duplicate target", mutate: func(value *SupersessionAdmissionInput) {
			value.TargetNodeIDs = []string{"canon-node:old", " canon-node:old "}
		}},
		{name: "head without revision", mutate: func(value *SupersessionAdmissionInput) {
			value.ExpectedHeadEventID = "admission-event:v2:sha256:" + strings.Repeat("a", 64)
		}},
		{name: "revision without head", mutate: func(value *SupersessionAdmissionInput) { value.ExpectedRevision = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.TargetNodeIDs = append([]string(nil), valid.TargetNodeIDs...)
			test.mutate(&input)
			_, _, err := normalizeSupersessionAdmissionInput(input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestBuildSupersedesEdgesUsesNewToOldDirectionAndReviewAudit(t *testing.T) {
	proposal := ProposalQueryResult{
		ProposalOccurrenceID: "occ:proposal",
		ProposalFingerprint:  "proposal-fingerprint",
		ExtractorName:        "external-agent",
		SourceSnapshotID:     "srcsnap:revision-2",
		ExtractionViewID:     "view:revision-2",
	}
	edges := buildSupersedesEdges(
		proposal,
		"canon-node:new",
		[]string{"canon-node:old-a", "canon-node:old-b"},
		"adm:decision",
		"admission-event:v2:sha256:"+strings.Repeat("a", 64),
	)
	if len(edges) != 2 {
		t.Fatalf("len(edges) = %d, want 2", len(edges))
	}
	for index, edge := range edges {
		if edge.From != "canon-node:new" ||
			edge.To != []string{"canon-node:old-a", "canon-node:old-b"}[index] ||
			edge.Relation != evidencegraph.CanonicalSupersedes ||
			edge.Provenance.ReviewRef != "adm:decision" ||
			edge.Provenance.Method != "supersession_admission_edge" ||
			edge.OriginProposalOccurrenceID != proposal.ProposalOccurrenceID {
			t.Fatalf("edge %d = %+v", index, edge)
		}
	}
}

func TestGenericAdmissionReplayRejectsSupersessionDecision(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	ingested, err := ingestManualText(ctx, db, testManualInput("generic-replay-supersession"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	admitted, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}
	db.supersessionEvents[ingested.ProposalOccurrenceID] = struct{}{}

	// The event row is the authority discriminator. Corrupting the decision's
	// edge list must not make the generic replay path accept a supersession.
	decision := db.admissionDecisions[admitted.AdmissionDecisionID]
	decision.canonicalEdgeIDs = nil
	db.admissionDecisions[admitted.AdmissionDecisionID] = decision

	_, err = admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID})
	assertKind(t, err, ErrorAdmissionStateConflict)
}
