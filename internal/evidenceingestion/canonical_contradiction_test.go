package evidenceingestion

import (
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
