package evidenceingestion

import (
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
