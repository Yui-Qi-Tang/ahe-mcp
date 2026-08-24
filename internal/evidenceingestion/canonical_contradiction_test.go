package evidenceingestion

import (
	"context"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestPrepareCanonicalContradictionProposalCanonicalizesSymmetricEndpoints(t *testing.T) {
	input := CanonicalContradictionProposalInput{
		RequestID:          "relation-request-1",
		NodeAID:            "canon-node:z",
		NodeBID:            "canon-node:a",
		Rationale:          "The two admitted claims specify incompatible limits.",
		ProducerName:       "claude-code",
		ProducerVersion:    "workflow-v1",
		ProducerSessionRef: "session:debug-1",
	}

	forward, err := prepareCanonicalContradictionProposal(input)
	if err != nil {
		t.Fatalf("prepareCanonicalContradictionProposal() error = %v", err)
	}
	if forward.input.NodeAID != "canon-node:a" || forward.input.NodeBID != "canon-node:z" {
		t.Fatalf("canonical endpoints = %s/%s", forward.input.NodeAID, forward.input.NodeBID)
	}
	if !strings.HasPrefix(forward.proposalID, CanonicalContradictionProposalIDPrefix) || forward.fingerprint == "" || forward.payloadHash == "" {
		t.Fatalf("prepared identity is incomplete: %+v", forward)
	}

	reversed := input
	reversed.NodeAID, reversed.NodeBID = input.NodeBID, input.NodeAID
	reverseResult, err := prepareCanonicalContradictionProposal(reversed)
	if err != nil {
		t.Fatalf("prepare reversed proposal: %v", err)
	}
	if reverseResult.proposalID != forward.proposalID || reverseResult.fingerprint != forward.fingerprint || reverseResult.payloadHash != forward.payloadHash {
		t.Fatalf("symmetric identity differs: forward %+v reverse %+v", forward, reverseResult)
	}
}

func TestPrepareCanonicalContradictionProposalRejectsInvalidInput(t *testing.T) {
	valid := CanonicalContradictionProposalInput{
		RequestID:       "relation-request-1",
		NodeAID:         "canon-node:a",
		NodeBID:         "canon-node:b",
		Rationale:       "The claims are incompatible.",
		ProducerName:    "claude-code",
		ProducerVersion: "workflow-v1",
	}
	tests := []struct {
		name   string
		mutate func(*CanonicalContradictionProposalInput)
	}{
		{name: "same endpoint", mutate: func(input *CanonicalContradictionProposalInput) { input.NodeBID = input.NodeAID }},
		{name: "bad endpoint prefix", mutate: func(input *CanonicalContradictionProposalInput) { input.NodeAID = "node:a" }},
		{name: "missing rationale", mutate: func(input *CanonicalContradictionProposalInput) { input.Rationale = "" }},
		{name: "missing producer", mutate: func(input *CanonicalContradictionProposalInput) { input.ProducerName = "" }},
		{name: "oversized session", mutate: func(input *CanonicalContradictionProposalInput) {
			input.ProducerSessionRef = strings.Repeat("x", ProducerSessionRefMaxBytes+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := prepareCanonicalContradictionProposal(input); err == nil {
				t.Fatal("error = nil")
			}
		})
	}
}

func TestBuildCanonicalContradictionEdgeIsOrderedAndAuditable(t *testing.T) {
	proposal := CanonicalContradictionProposal{
		ID:           "contradiction-proposal:1",
		NodeAID:      "canon-node:a",
		NodeBID:      "canon-node:b",
		Relation:     evidencegraph.CanonicalContradicts,
		ProducerName: "claude-code",
	}
	edge := buildCanonicalContradictionEdge(proposal, "contradiction-adm:1")
	if edge.From != proposal.NodeAID || edge.To != proposal.NodeBID || edge.Relation != evidencegraph.CanonicalContradicts {
		t.Fatalf("edge = %+v", edge)
	}
	if edge.OriginContradictionProposalID != proposal.ID || edge.OriginProposalOccurrenceID != "" {
		t.Fatalf("edge origins = %+v", edge)
	}
	if edge.Provenance.TraceRef != proposal.ID || edge.Provenance.ReviewRef != "contradiction-adm:1" || len(edge.Provenance.OriginRefs) != 2 {
		t.Fatalf("edge provenance = %+v", edge.Provenance)
	}
}

func TestLoadCanonicalContradictionProposalForUpdateRefreshesDecisionSnapshot(t *testing.T) {
	db := &scriptedCanonicalContradictionQueryer{
		rows: []sqlRow{
			mockRow{values: []any{"contradiction-proposal:1"}},
			mockRow{values: []any{
				"contradiction-proposal:1",
				"request:1",
				"payload-hash",
				"proposal-fingerprint",
				"canon-node:a",
				"canon-node:b",
				"contradicts",
				"The two grounded claims are mutually incompatible.",
				"claude-code",
				"workflow-v1",
				"session:debug-1",
				admissionOutcomeAdmitted,
				"canon-edge:1",
				"contradiction-adm:1",
				admissionOutcomeAdmitted,
				"canon-edge:1",
				"reviewer@example.test",
				"The contradiction was confirmed.",
			}},
		},
	}

	proposal, decision, err := loadCanonicalContradictionProposalByID(
		context.Background(),
		db,
		"contradiction-proposal:1",
		true,
	)
	if err != nil {
		t.Fatalf("loadCanonicalContradictionProposalByID() error = %v", err)
	}
	if len(db.queries) != 2 {
		t.Fatalf("query count = %d, want 2", len(db.queries))
	}
	if !strings.Contains(db.queries[0], "FOR UPDATE") || strings.Contains(db.queries[0], "LEFT JOIN") {
		t.Fatalf("lock query must lock only the contradiction proposal row:\n%s", db.queries[0])
	}
	if !strings.Contains(db.queries[1], "LEFT JOIN canonical_contradiction_admission_decisions") || strings.Contains(db.queries[1], "FOR UPDATE") {
		t.Fatalf("refresh query must read the contradiction proposal and decision in a new statement:\n%s", db.queries[1])
	}
	if proposal.AdmissionOutcome != admissionOutcomeAdmitted || proposal.CanonicalEdgeID != "canon-edge:1" {
		t.Fatalf("proposal terminal state = %+v", proposal)
	}
	if decision == nil || decision.ID != "contradiction-adm:1" || decision.ProposalID != proposal.ID || decision.CanonicalEdgeID != proposal.CanonicalEdgeID {
		t.Fatalf("decision = %+v", decision)
	}
}

type scriptedCanonicalContradictionQueryer struct {
	queries []string
	rows    []sqlRow
}

func (*scriptedCanonicalContradictionQueryer) query(context.Context, string, ...any) (sqlRows, error) {
	panic("unexpected query")
}

func (db *scriptedCanonicalContradictionQueryer) queryRow(_ context.Context, query string, _ ...any) sqlRow {
	db.queries = append(db.queries, query)
	if len(db.rows) == 0 {
		panic("unexpected queryRow")
	}
	row := db.rows[0]
	db.rows = db.rows[1:]
	return row
}
