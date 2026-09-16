package evidenceimplements

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

// All inputs are synthetic assertions. No fixture is evidence of DB admission.
func relationFixture(t *testing.T) (AdmittedCut, Input) {
	t.Helper()
	specID := evidencegraph.StableCanonicalID("canon-node", "synthetic-spec")
	codeID := evidencegraph.StableCanonicalID("canon-node", "synthetic-code")
	claim := "refund.Validate rejects a negative amount."
	code := "func Validate(amount int) bool { return amount >= 0 }"
	cut, err := NewAdmittedCut([]SpecificationInput{{
		NodeID: specID, NodeKind: evidencegraph.CanonicalSourceClaim,
		SourceTitle: "Synthetic refund requirement", SourceLocation: "fixture:requirements#refund",
		SourceRevision: "fixture-v1", ClaimText: claim, ClaimHash: ExcerptHash(claim),
	}}, []ImplementationInput{{
		NodeID: codeID, NodeKind: evidencegraph.CanonicalSourceClaim,
		RepositoryID: "fixture:repo", SourceTitle: "Synthetic refund implementation",
		SourceLocation: "fixture:repo/refund.go", Revision: strings.Repeat("a", 40),
		Path: "refund.go", Span: "L1", SymbolKind: "function", QualifiedName: "refund.Validate",
		ExactExcerpt: code, ExcerptHash: ExcerptHash(code),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return cut, Input{
		AdmittedCutID: cut.ID, SpecificationNodeID: specID, ImplementationNodeID: codeID,
		ProposalSentence: "refund.Validate implements the negative-amount rejection requirement.",
		Witnesses: []Witness{{Kind: ReviewedBehavior, EndpointNodeIDs: []string{specID, codeID},
			SourceTitle: "Synthetic implementation", SourceLocation: "fixture:repo/refund.go",
			ExactExcerpt: code, ExcerptHash: ExcerptHash(code)}},
		Coverage:    "Only the negative-amount condition.",
		Limitations: []string{"No claim about deployment or other requirements."},
		ReviewerID:  "synthetic-reviewer", DecisionReason: "Synthetic contract test, not human approval.",
	}
}

func TestRelationReceiptDirectionBindingAndReplay(t *testing.T) {
	cut, input := relationFixture(t)
	receipt, err := NewReceipt(cut, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReceipt(cut, receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ProjectedEdge.From != input.SpecificationNodeID ||
		receipt.ProjectedEdge.To != input.ImplementationNodeID ||
		receipt.ProjectedEdge.Relation != evidencegraph.CanonicalImplements ||
		receipt.Authority != Authority || receipt.SemanticEffect != Effect || receipt.PolicyEffect != PolicyEffect {
		t.Fatal("direction or authority boundary changed")
	}
	if err := ValidateReplay(cut, receipt, input); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.DecisionReason = "A different synthetic review."
	other, err := NewReceipt(cut, changed)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProjectedEdge.ID != other.ProjectedEdge.ID || receipt.ID == other.ID {
		t.Fatal("structural pair identity and review identity must remain separate")
	}
	if !errors.Is(ValidateReplay(cut, receipt, changed), ErrReplayConflict) {
		t.Fatal("changed decision was accepted as exact replay")
	}
}

func TestRelationRejectsMissingOrChangedEvidence(t *testing.T) {
	tests := map[string]func(*AdmittedCut, *Input){
		"reversed endpoints": func(_ *AdmittedCut, in *Input) {
			in.SpecificationNodeID, in.ImplementationNodeID = in.ImplementationNodeID, in.SpecificationNodeID
		},
		"stale cut":              func(_ *AdmittedCut, in *Input) { in.AdmittedCutID += "changed" },
		"missing witness":        func(_ *AdmittedCut, in *Input) { in.Witnesses = nil },
		"wrong witness endpoint": func(_ *AdmittedCut, in *Input) { in.Witnesses[0].EndpointNodeIDs[0] = "other" },
		"changed quote":          func(_ *AdmittedCut, in *Input) { in.Witnesses[0].ExactExcerpt += "changed" },
		"duplicate witness":      func(_ *AdmittedCut, in *Input) { in.Witnesses = append(in.Witnesses, in.Witnesses[0]) },
		"missing reviewer":       func(_ *AdmittedCut, in *Input) { in.ReviewerID = "" },
		"missing reason":         func(_ *AdmittedCut, in *Input) { in.DecisionReason = "" },
		"missing limitations":    func(_ *AdmittedCut, in *Input) { in.Limitations = nil },
		"changed source":         func(cut *AdmittedCut, _ *Input) { cut.Implementations[0].ExactExcerpt += "changed" },
		"mutable revision":       func(cut *AdmittedCut, _ *Input) { cut.Implementations[0].Revision = "main" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cut, input := relationFixture(t)
			mutate(&cut, &input)
			got, err := NewReceipt(cut, input)
			if err == nil || !reflect.DeepEqual(got, Receipt{}) {
				t.Fatal("invalid evidence produced a receipt")
			}
		})
	}
}

func TestRelationCandidateDiscoveryDoesNotApprove(t *testing.T) {
	cut, input := relationFixture(t)
	report, err := ProposeCandidates(cut)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 1 || report.PolicyEffect != CandidatePolicyEffect ||
		report.Candidates[0].PolicyEffect != CandidatePolicyEffect ||
		report.Candidates[0].Specification.NodeID != input.SpecificationNodeID {
		t.Fatalf("unexpected candidate report: %+v", report)
	}
	again, err := ProposeCandidates(cut)
	if err != nil || !reflect.DeepEqual(report, again) {
		t.Fatal("candidate report is not deterministic")
	}
	input.ReviewerID, input.DecisionReason = "", ""
	if _, err := NewReceipt(cut, input); err == nil {
		t.Fatal("candidate discovery substituted for review")
	}
}

func TestRelationStoredReceiptRejectsTampering(t *testing.T) {
	for name, mutate := range map[string]func(*Receipt){
		"edge direction":   func(r *Receipt) { r.ProjectedEdge.From, r.ProjectedEdge.To = r.ProjectedEdge.To, r.ProjectedEdge.From },
		"authority":        func(r *Receipt) { r.Authority = "model_approved" },
		"semantic effect":  func(r *Receipt) { r.SemanticEffect = "truth_support" },
		"receipt identity": func(r *Receipt) { r.ID += "changed" },
		"provenance":       func(r *Receipt) { r.ProjectedEdge.ProvenanceReceiptID += "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			cut, in := relationFixture(t)
			receipt, err := NewReceipt(cut, in)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&receipt)
			if err := ValidateReceipt(cut, receipt); err == nil {
				t.Fatal("tampered receipt accepted")
			}
		})
	}
}
