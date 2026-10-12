package evidenceimplements

import (
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestReviewedAdmissionEnvelopeVersionByteBounds(t *testing.T) {
	input := DerivedAdmissionInput{RequestID: "synthetic", Decision: "approved",
		Review: DerivedReviewRequest{SpecificationNodeID: "canon-node:s", ImplementationNodeID: "canon-node:i"},
		Receipt: DerivedReviewReceipt{ReviewerID: "synthetic", DecisionReason: "TEST APPROVAL STUB",
			Mapping: ReviewMapping{ProposalSentence: "Synthetic mapping.", Coverage: "Synthetic coordinates only.",
				Limitations: []string{"No real evidence or human decision."},
				Witnesses: []Witness{{Kind: ReviewedBehavior, EndpointNodeIDs: []string{"canon-node:s", "canon-node:i"},
					SourceTitle: "Synthetic", SourceLocation: "synthetic:source", ExactExcerpt: "x", ExcerptHash: ExcerptHash("x")}}}}}
	for _, tc := range []struct {
		name    string
		payload string
		v2, bad bool
	}{
		{"v1_limit", strings.Repeat("a", 128<<10), false, false},
		{"v1_over", strings.Repeat("a", (128<<10)+1), false, true},
		{"v2_limit", strings.Repeat("a", 512<<10), true, false},
		{"v2_over", strings.Repeat("a", (512<<10)+1), true, true},
		{"v2_receipt_over", strings.Repeat("\"", 512<<10), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := input
			value.Receipt.Display.PayloadUTF8 = tc.payload
			var err error
			if tc.v2 {
				err = validateReviewedAdmissionEnvelopeWithBounds(value, MaxRecursiveReviewDisplayBytes, evidenceingestion.RecursiveImplementsReceiptMaxBytes)
			} else {
				err = validateReviewedAdmissionEnvelope(value)
			}
			if (err != nil) != tc.bad {
				t.Fatalf("version-specific envelope bound: %v", err)
			}
		})
	}
}
