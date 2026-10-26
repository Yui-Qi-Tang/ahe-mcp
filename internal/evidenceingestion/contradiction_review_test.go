package evidenceingestion

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestContradictionDisplaySizeRetainsFullSources(t *testing.T) {
	for _, size := range []int{124103, 125000} {
		display := ContradictionReviewDisplay{
			Proposal: contradictionSizeProposal(),
			Sources: []GovernanceSource{
				{RetainedBytes: bytes.Repeat([]byte("a"), 2400)},
				{RetainedBytes: bytes.Repeat([]byte("b"), size)},
			},
		}
		raw, err := json.Marshal(display)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw)*6+32768 <= governanceEventMaxBytes {
			t.Fatal("fixture must exceed the former reservation")
		}
		original := bytes.Clone(raw)
		if err := validateContradictionDisplaySize(raw, display.Proposal); err != nil {
			t.Fatalf("%d-byte source refused: %v", size, err)
		}
		if !bytes.Equal(raw, original) {
			t.Fatal("size check changed the reviewed bytes")
		}
	}
}

func TestContradictionDisplaySizeBoundary(t *testing.T) {
	proposal := contradictionSizeProposal()
	display := func(padding string) []byte {
		t.Helper()
		raw, err := json.Marshal(struct {
			Padding string `json:"padding"`
			Escaped string `json:"escaped"`
		}{padding, "繁體中文 <>& \" \\ \n \x01 \u2028 \u2029"})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	padding := strings.Repeat("x", governanceEventMaxBytes-len(contradictionSizeBody(t, display(""), proposal, "admitted")))
	raw := display(padding)
	if size := len(contradictionSizeBody(t, raw, proposal, "admitted")); size != governanceEventMaxBytes {
		t.Fatalf("boundary receipt size = %d", size)
	}
	if err := validateContradictionDisplaySize(raw, proposal); err != nil {
		t.Fatalf("exact boundary refused: %v", err)
	}
	for _, outcome := range []string{"admitted", "rejected", "audit_only"} {
		if size := len(contradictionSizeBody(t, raw, proposal, outcome)); size > governanceEventMaxBytes {
			t.Fatalf("%s decision exceeds reserved envelope: %d", outcome, size)
		}
	}
	if err := validateContradictionDisplaySize(display(padding+"x"), proposal); err == nil {
		t.Fatal("receipt one byte above the bound accepted")
	}

	// These controls are escaped once in the display and again in the receipt.
	// The raw display fits, but its nested representation does not.
	raw = display(strings.Repeat("\x01", 150000))
	if len(raw) >= governanceEventMaxBytes || len(contradictionSizeBody(t, raw, proposal, "admitted")) <= governanceEventMaxBytes {
		t.Fatal("fixture does not exercise nested JSON escaping")
	}
	if err := validateContradictionDisplaySize(raw, proposal); err == nil {
		t.Fatal("oversized escaped display accepted")
	}
}

func contradictionSizeProposal() CanonicalContradictionProposal {
	return CanonicalContradictionProposal{
		ID: "contradiction-proposal:synthetic-size-check", NodeAID: "canon-node:a", NodeBID: "canon-node:b",
		ProducerName: "synthetic \"producer\" <>& \x01",
	}
}

// Construct each supported decision as the apply path does, using the largest
// legal unknown fields. This checks that the getter reserves every outcome.
func contradictionSizeBody(t *testing.T, raw []byte, proposal CanonicalContradictionProposal, outcome string) []byte {
	t.Helper()
	id, err := canonicalContradictionDecisionID(proposal.ID, outcome)
	if err != nil {
		t.Fatal(err)
	}
	decision := CanonicalContradictionDecision{ID: id, ProposalID: proposal.ID, Outcome: outcome,
		DecisionBy: strings.Repeat("\x01", 200), DecisionReason: strings.Repeat("\x01", 2000)}
	edge := CanonicalGraphEdge{}
	if outcome == "admitted" {
		edge = buildCanonicalContradictionEdge(proposal, id)
		decision.CanonicalEdgeID = edge.ID
	}
	requestID := strings.Repeat("\x01", 200)
	if !validateEndpointText(requestID, 200) || !validateEndpointText(decision.DecisionBy, 200) || !validateEndpointText(decision.DecisionReason, 2000) {
		t.Fatal("reservation uses an invalid decision field")
	}
	body, err := json.Marshal(contradictionReviewBody{
		Contract: ReviewedContradictionV1, RequestID: requestID,
		Subject: governanceDigest("contradiction-review:sha256:", raw), Display: string(raw), Decision: decision, Edge: edge,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
