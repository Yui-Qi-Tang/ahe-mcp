package evidenceingestion

import (
	"context"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestPrepareCanonicalSupersessionProposalPreservesDirection(t *testing.T) {
	input := canonicalSupersessionTestInput()
	forward, err := prepareCanonicalSupersessionProposal(input)
	if err != nil {
		t.Fatalf("prepareCanonicalSupersessionProposal() error = %v", err)
	}
	if forward.input.FromNodeID != input.FromNodeID || forward.input.ToNodeID != input.ToNodeID {
		t.Fatalf("directed endpoints = %s -> %s", forward.input.FromNodeID, forward.input.ToNodeID)
	}
	if !strings.HasPrefix(forward.proposalID, CanonicalSupersessionProposalIDPrefix) || forward.fingerprint == "" || forward.payloadHash == "" {
		t.Fatalf("prepared identity is incomplete: %+v", forward)
	}

	reversedInput := input
	reversedInput.FromNodeID, reversedInput.ToNodeID = input.ToNodeID, input.FromNodeID
	reversed, err := prepareCanonicalSupersessionProposal(reversedInput)
	if err != nil {
		t.Fatalf("prepare reversed proposal: %v", err)
	}
	if reversed.proposalID == forward.proposalID || reversed.fingerprint == forward.fingerprint {
		t.Fatalf("directed identity was canonicalized: forward %+v reverse %+v", forward, reversed)
	}
}

func TestPrepareCanonicalSupersessionProposalSeparatesSessionFromFingerprint(t *testing.T) {
	input := canonicalSupersessionTestInput()
	first, err := prepareCanonicalSupersessionProposal(input)
	if err != nil {
		t.Fatalf("prepare first proposal: %v", err)
	}
	input.ProducerSessionRef = "session:debug-2"
	second, err := prepareCanonicalSupersessionProposal(input)
	if err != nil {
		t.Fatalf("prepare second proposal: %v", err)
	}
	if first.proposalID != second.proposalID || first.fingerprint != second.fingerprint {
		t.Fatalf("debug session changed semantic identity: first %+v second %+v", first, second)
	}
	if first.payloadHash == second.payloadHash {
		t.Fatal("debug session did not change exact request payload hash")
	}
}

func TestPrepareCanonicalSupersessionProposalRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CanonicalSupersessionProposalInput)
	}{
		{name: "same endpoint", mutate: func(input *CanonicalSupersessionProposalInput) { input.ToNodeID = input.FromNodeID }},
		{name: "bad from prefix", mutate: func(input *CanonicalSupersessionProposalInput) { input.FromNodeID = "node:current" }},
		{name: "missing proposal sentence", mutate: func(input *CanonicalSupersessionProposalInput) { input.ProposalSentence = "" }},
		{name: "missing rationale", mutate: func(input *CanonicalSupersessionProposalInput) { input.Rationale = "" }},
		{name: "missing version difference", mutate: func(input *CanonicalSupersessionProposalInput) { input.VersionDifference = "" }},
		{name: "empty limitation", mutate: func(input *CanonicalSupersessionProposalInput) { input.Limitations = []string{" "} }},
		{name: "duplicate limitation", mutate: func(input *CanonicalSupersessionProposalInput) {
			input.Limitations = []string{"partial excerpt", "partial excerpt"}
		}},
		{name: "too many limitations", mutate: func(input *CanonicalSupersessionProposalInput) {
			input.Limitations = make([]string, CanonicalSupersessionLimitationsMaxEntries+1)
			for index := range input.Limitations {
				input.Limitations[index] = string(rune('a' + index))
			}
		}},
		{name: "oversized limitation", mutate: func(input *CanonicalSupersessionProposalInput) {
			input.Limitations = []string{strings.Repeat("x", CanonicalSupersessionLimitationMaxBytes+1)}
		}},
		{name: "missing producer", mutate: func(input *CanonicalSupersessionProposalInput) { input.ProducerName = "" }},
		{name: "oversized session", mutate: func(input *CanonicalSupersessionProposalInput) {
			input.ProducerSessionRef = strings.Repeat("x", ProducerSessionRefMaxBytes+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := canonicalSupersessionTestInput()
			test.mutate(&input)
			if _, err := prepareCanonicalSupersessionProposal(input); err == nil {
				t.Fatal("error = nil")
			}
		})
	}
}

func TestBuildCanonicalSupersessionEdgeIsDirectedAndAuditable(t *testing.T) {
	proposal := CanonicalSupersessionProposal{
		ID:           "supersession-proposal:1",
		FromNodeID:   "canon-node:current",
		ToNodeID:     "canon-node:replaced",
		Relation:     evidencegraph.CanonicalSupersedes,
		ProducerName: "claude-code",
	}
	edge := buildCanonicalSupersessionEdge(proposal, "supersession-adm:1")
	if edge.From != proposal.FromNodeID || edge.To != proposal.ToNodeID || edge.Relation != evidencegraph.CanonicalSupersedes {
		t.Fatalf("edge = %+v", edge)
	}
	if edge.OriginSupersessionProposalID != proposal.ID || edge.OriginProposalOccurrenceID != "" || edge.OriginContradictionProposalID != "" {
		t.Fatalf("edge origins = %+v", edge)
	}
	if edge.Provenance.TraceRef != proposal.ID || edge.Provenance.ReviewRef != "supersession-adm:1" || edge.Provenance.Method != "human_admitted_canonical_supersession" {
		t.Fatalf("edge provenance = %+v", edge.Provenance)
	}
}

func TestLoadCanonicalSupersessionProposalForUpdateRefreshesDecisionSnapshot(t *testing.T) {
	db := &scriptedCanonicalSupersessionQueryer{
		rows: []sqlRow{
			mockRow{values: []any{"supersession-proposal:1"}},
			mockRow{values: []any{
				"supersession-proposal:1",
				"request:1",
				"payload-hash",
				"proposal-fingerprint",
				"canon-node:current",
				"canon-node:replaced",
				"supersedes",
				"The current claim supersedes the previous claim.",
				"The source explicitly replaces the previous revision.",
				"The timeout changes from 60 seconds to 30 seconds.",
				[]byte(`["Only the supplied policy scope was compared."]`),
				"claude-code",
				"workflow-v1",
				"session:debug-1",
				admissionOutcomeAdmitted,
				"canon-edge:1",
				"supersession-adm:1",
				admissionOutcomeAdmitted,
				"canon-edge:1",
				"reviewer@example.test",
				"The version transition was confirmed.",
			}},
		},
	}

	proposal, decision, err := loadCanonicalSupersessionProposalByID(
		context.Background(),
		db,
		"supersession-proposal:1",
		true,
	)
	if err != nil {
		t.Fatalf("loadCanonicalSupersessionProposalByID() error = %v", err)
	}
	if len(db.queries) != 2 {
		t.Fatalf("query count = %d, want 2", len(db.queries))
	}
	if !strings.Contains(db.queries[0], "FOR UPDATE") || strings.Contains(db.queries[0], "LEFT JOIN") {
		t.Fatalf("lock query must lock only the proposal row:\n%s", db.queries[0])
	}
	if !strings.Contains(db.queries[1], "LEFT JOIN canonical_supersession_admission_decisions") || strings.Contains(db.queries[1], "FOR UPDATE") {
		t.Fatalf("refresh query must read the proposal and decision without reusing the locking statement:\n%s", db.queries[1])
	}
	if proposal.AdmissionOutcome != admissionOutcomeAdmitted || proposal.CanonicalEdgeID != "canon-edge:1" {
		t.Fatalf("proposal terminal state = %+v", proposal)
	}
	if decision == nil || decision.ID != "supersession-adm:1" || decision.ProposalID != proposal.ID || decision.CanonicalEdgeID != proposal.CanonicalEdgeID {
		t.Fatalf("decision = %+v", decision)
	}
}

type scriptedCanonicalSupersessionQueryer struct {
	queries []string
	rows    []sqlRow
}

func (*scriptedCanonicalSupersessionQueryer) query(context.Context, string, ...any) (sqlRows, error) {
	panic("unexpected query")
}

func (db *scriptedCanonicalSupersessionQueryer) queryRow(_ context.Context, query string, _ ...any) sqlRow {
	db.queries = append(db.queries, query)
	if len(db.rows) == 0 {
		panic("unexpected queryRow")
	}
	row := db.rows[0]
	db.rows = db.rows[1:]
	return row
}

func canonicalSupersessionTestInput() CanonicalSupersessionProposalInput {
	return CanonicalSupersessionProposalInput{
		RequestID:          "supersession-request-1",
		FromNodeID:         "canon-node:current",
		ToNodeID:           "canon-node:replaced",
		ProposalSentence:   "The current timeout claim supersedes the previous timeout claim.",
		Rationale:          "The source explicitly says that the current revision replaces the previous revision.",
		VersionDifference:  "The timeout changes from 60 seconds to 30 seconds.",
		Limitations:        []string{"Only the supplied policy scope was compared."},
		ProducerName:       "claude-code",
		ProducerVersion:    "workflow-v1",
		ProducerSessionRef: "session:debug-1",
	}
}
