package evidenceimplements

import (
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

const (
	// RecursiveBasisVersion binds a complete bounded recursive AND ancestor cut.
	RecursiveBasisVersion = "canonical-implements-recursive-review-basis/v2"
	// RecursiveDisplayVersion binds the exact display of that cut and every rule.
	RecursiveDisplayVersion = "canonical-implements-recursive-review-display/v2"
	// RecursiveReceiptVersion distinguishes recursive approval from depth-one v1.
	RecursiveReceiptVersion = "canonical-implements-recursive-review-receipt/v2"
	// MaxRecursiveDepth bounds the number of derived layers on every root path.
	MaxRecursiveDepth = 8
	// MaxRecursiveNodes bounds distinct ancestor nodes, including the root.
	MaxRecursiveNodes = 64
	// MaxRecursiveEdges bounds all parent-to-derived edges in the ancestor cut.
	MaxRecursiveEdges = 128
	// MaxRecursiveParents bounds each derivation's complete AND parent set.
	MaxRecursiveParents = 8
)

// RecursiveDerivationRule binds an explicit reviewed semantic assertion to one
// native derivation. Method, producer and trace metadata do not supply this rule.
type RecursiveDerivationRule struct {
	NodeID        string `json:"node_id"`
	DerivationID  string `json:"derivation_id"`
	RuleStatement string `json:"rule_statement"`
}

// RecursiveReviewBasis retains every reachable derivation and source leaf.
// Constructing this pure value does not establish database admission authority.
type RecursiveReviewBasis struct {
	ContractVersion string                          `json:"contract_version"`
	ID              string                          `json:"id"`
	AdmittedCutID   string                          `json:"admitted_cut_id"`
	RootNodeID      string                          `json:"root_node_id"`
	Ancestors       evidencegraph.CanonicalArtifact `json:"ancestors"`
	SourceLeaves    []ReviewEvidenceSnapshot        `json:"source_leaves"`
	Rules           []RecursiveDerivationRule       `json:"rules"`
}

// RecursiveReviewPackage makes every layer's AND dependencies and asserted rule
// visible together with original source reviews and the direct relation mapping.
type RecursiveReviewPackage struct {
	ContractVersion string                          `json:"contract_version"`
	ID              string                          `json:"id"`
	ReportID        string                          `json:"report_id"`
	Candidate       Candidate                       `json:"candidate"`
	BasisID         string                          `json:"basis_id"`
	Ancestors       evidencegraph.CanonicalArtifact `json:"ancestors"`
	Rules           []RecursiveDerivationRule       `json:"rules"`
	SourceDisplays  []DerivedSourceDisplay          `json:"source_displays"`
	Mapping         ReviewMapping                   `json:"mapping"`
	Limitations     []string                        `json:"limitations"`
}

// NewRecursiveReviewBasis checks a complete root-reachable AND closure, rejects
// cycles and bounds, then owns and deterministically seals the supplied records.
func NewRecursiveReviewBasis(cut AdmittedCut, input RecursiveReviewBasis) (RecursiveReviewBasis, error) {
	if err := ValidateRecursiveBounds(input); err != nil {
		return RecursiveReviewBasis{}, err
	}
	if len(cut.Specifications) > MaxCutNodes || len(cut.Implementations) > MaxCutNodes {
		return RecursiveReviewBasis{}, ErrInvalid
	}
	if err := ValidateAdmittedCut(cut); err != nil {
		return RecursiveReviewBasis{}, err
	}
	root, found := FindSpecification(cut.Specifications, input.RootNodeID)
	if !found || input.AdmittedCutID != cut.ID || root.NodeKind != evidencegraph.CanonicalDerivedClaim {
		return RecursiveReviewBasis{}, ErrEndpointMissing
	}
	if err := validateRecursiveClosure(cut, input); err != nil {
		return RecursiveReviewBasis{}, err
	}
	input.ContractVersion, input.ID = RecursiveBasisVersion, ""
	data, err := json.Marshal(input)
	if err != nil || len(data) > MaxDerivedBasisBytes {
		return RecursiveReviewBasis{}, fmt.Errorf("%w: recursive basis byte bound", ErrInvalid)
	}
	// Copy before sorting: callers retain ownership of all native sidecars and
	// historical source-review slices. Never mutate them while sealing a receipt.
	var result RecursiveReviewBasis
	if err := json.Unmarshal(data, &result); err != nil {
		return RecursiveReviewBasis{}, err
	}
	normalizeRecursiveBasis(&result)
	result.ID, err = Digest("canonical-implements-recursive-basis:v2", result)
	if err != nil {
		return RecursiveReviewBasis{}, err
	}
	sealed, err := json.Marshal(result)
	if err != nil || len(sealed) > MaxDerivedBasisBytes {
		return RecursiveReviewBasis{}, fmt.Errorf("%w: sealed recursive basis byte bound", ErrInvalid)
	}
	return result, nil
}

// ValidateRecursiveReviewBasis requires both valid evidence and its canonical
// ordering, version and content address; it does not normalize a stored receipt.
func ValidateRecursiveReviewBasis(cut AdmittedCut, expected RecursiveReviewBasis) error {
	want, err := NewRecursiveReviewBasis(cut, expected)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, expected) {
		return fmt.Errorf("%w: noncanonical recursive basis", ErrInvalid)
	}
	return nil
}

func normalizeRecursiveBasis(b *RecursiveReviewBasis) {
	a := &b.Ancestors
	slices.SortFunc(a.Nodes, func(x, y evidencegraph.CanonicalNode) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(a.Edges, func(x, y evidencegraph.CanonicalEdge) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(a.Payloads, func(x, y evidencegraph.EvidencePayload) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(a.Provenance, func(x, y evidencegraph.ProvenanceRecord) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(a.Temporal, func(x, y evidencegraph.TemporalRecord) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(a.Integrity, func(x, y evidencegraph.IntegrityRecord) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(a.Derivations, func(x, y evidencegraph.DerivationRecord) int { return cmp.Compare(x.ID, y.ID) })
	for i := range a.Derivations {
		d := &a.Derivations[i]
		slices.Sort(d.Parents)
		for j := range a.Provenance {
			if a.Provenance[j].ID == d.ProvenanceRef {
				slices.Sort(a.Provenance[j].OriginRefs)
			}
		}
	}
	slices.SortFunc(b.SourceLeaves, func(x, y ReviewEvidenceSnapshot) int {
		return cmp.Compare(x.SpecificationNodeID, y.SpecificationNodeID)
	})
	slices.SortFunc(b.Rules, func(x, y RecursiveDerivationRule) int { return cmp.Compare(x.NodeID, y.NodeID) })
}

// BuildRecursiveReviewDisplay exposes all derivations and source leaves without
// truncation. If the complete package cannot fit, no review subject is returned.
func BuildRecursiveReviewDisplay(cut AdmittedCut, report CandidateReport, candidateID string,
	expected RecursiveReviewBasis, mapping ReviewMapping,
) (DerivedReviewDisplay, DerivedReviewSubject, error) {
	fail := func(err error) (DerivedReviewDisplay, DerivedReviewSubject, error) {
		return DerivedReviewDisplay{}, DerivedReviewSubject{}, err
	}
	if err := ValidateRecursiveReviewBasis(cut, expected); err != nil {
		return fail(err)
	}
	if len(report.Candidates) > MaxCandidatesPerReport || len(report.Gaps) > MaxCandidateGapRecords || len(report.Truncations) > MaxCutNodes {
		return fail(ErrInvalid)
	}
	if err := ValidateCandidateReport(cut, report); err != nil {
		return fail(err)
	}
	index := slices.IndexFunc(report.Candidates, func(c Candidate) bool { return c.ID == candidateID })
	if index < 0 || report.Candidates[index].Specification.NodeID != expected.RootNodeID {
		return fail(ErrEndpointMissing)
	}
	candidate := report.Candidates[index]
	normalized, err := NormalizeReviewMapping(candidate, mapping)
	if err != nil {
		return fail(err)
	}
	review := RecursiveReviewPackage{ContractVersion: RecursiveDisplayVersion, ReportID: report.ID,
		Candidate: candidate, BasisID: expected.ID, Ancestors: expected.Ancestors, Rules: expected.Rules, Mapping: normalized,
		SourceDisplays: make([]DerivedSourceDisplay, 0, len(expected.SourceLeaves)),
		Limitations: []string{
			"Complete means the bounded root-reachable native AND ancestor closure, not all possible evidence or semantic completeness.",
			"Every derivation requires all its listed parents. Shared ancestors and claims from one source are not independent corroboration.",
			"Every displayed rule is an explicit reviewed assertion, not a recovered meaning of producer, method or trace metadata; structural validity does not establish semantic truth.",
			"Exact source reviews and this display bind bytes but do not authenticate a reviewer, prove human approval or prove actual rendering.",
			"Candidate ranking is not a relation witness. This pure receipt authorizes no write by itself and proves no source freshness, code execution or extraction qualification.",
			"Only the reviewed root-to-implementation relation is proposed; ancestors do not inherit implements, truth, support, status, supersession or currentness effects.",
		}}
	for _, leaf := range expected.SourceLeaves {
		display, subject, err := evidenceingestion.BuildSourceClaimReviewDisplayArtifact(leaf.ReviewSnapshot)
		if err != nil {
			return fail(err)
		}
		review.SourceDisplays = append(review.SourceDisplays, DerivedSourceDisplay{NodeID: leaf.SpecificationNodeID, Display: display, Subject: subject})
	}
	review.ID, err = Digest("canonical-implements-recursive-package:v2", review)
	if err != nil {
		return fail(err)
	}
	payload, err := json.Marshal(review)
	if err != nil || len(payload) > MaxReviewDisplayBytes {
		return fail(fmt.Errorf("%w: complete recursive display byte bound", ErrInvalid))
	}
	display := DerivedReviewDisplay{ContractVersion: RecursiveDisplayVersion,
		MediaType: "application/vnd.ahe.implements-recursive-review.v2+json", PayloadUTF8: string(payload)}
	display.ID, err = Digest("canonical-implements-recursive-display:v2", display)
	if err != nil {
		return fail(err)
	}
	return display, DerivedReviewSubject{AdmittedCutID: cut.ID, ReportID: report.ID, CandidateID: candidateID,
		BasisID: expected.ID, ReviewPackageID: review.ID, DisplayID: display.ID}, nil
}

// NewRecursiveReviewReceipt binds the exact complete display and every layer's
// assertion to one reviewer decision, while retaining the single directed edge ID.
func NewRecursiveReviewReceipt(cut AdmittedCut, report CandidateReport,
	expected RecursiveReviewBasis, input DerivedReviewInput,
) (DerivedReviewReceipt, error) {
	display, subject, err := BuildRecursiveReviewDisplay(cut, report, input.CandidateID, expected, input.Mapping)
	if err != nil {
		return DerivedReviewReceipt{}, err
	}
	if input.Display != display || input.Subject != subject {
		return DerivedReviewReceipt{}, fmt.Errorf("%w: exact recursive display or subject changed", ErrInvalid)
	}
	candidate := report.Candidates[slices.IndexFunc(report.Candidates, func(c Candidate) bool { return c.ID == input.CandidateID })]
	validated, err := NewReceipt(cut, Input{AdmittedCutID: cut.ID,
		SpecificationNodeID: expected.RootNodeID, ImplementationNodeID: candidate.Implementation.NodeID,
		ProposalSentence: input.Mapping.ProposalSentence, Witnesses: input.Mapping.Witnesses, Coverage: input.Mapping.Coverage,
		Limitations: input.Mapping.Limitations, ReviewerID: input.ReviewerID, DecisionReason: input.DecisionReason})
	if err != nil {
		return DerivedReviewReceipt{}, err
	}
	receipt := DerivedReviewReceipt{ContractVersion: RecursiveReceiptVersion,
		Authority: Authority, Direction: Direction, SemanticEffect: Effect, PolicyEffect: PolicyEffect,
		Mapping: ReviewMapping{ProposalSentence: validated.ProposalSentence, Witnesses: validated.Witnesses,
			Coverage: validated.Coverage, Limitations: validated.Limitations},
		Display: display, Subject: subject, ReviewerID: validated.ReviewerID, DecisionReason: validated.DecisionReason}
	receipt.ID, err = Digest("canonical-implements-recursive-receipt:v2", receipt)
	if err != nil {
		return DerivedReviewReceipt{}, err
	}
	receipt.ProjectedEdge = validated.ProjectedEdge
	receipt.ProjectedEdge.ProvenanceReceiptID = receipt.ID
	return receipt, nil
}

// ValidateRecursiveReviewReceipt rejects changed versions, evidence, rules,
// display, reviewer metadata or projected edge without rewriting older receipts.
func ValidateRecursiveReviewReceipt(cut AdmittedCut, report CandidateReport,
	expected RecursiveReviewBasis, receipt DerivedReviewReceipt,
) error {
	want, err := NewRecursiveReviewReceipt(cut, report, expected, DerivedReviewInput{CandidateID: receipt.Subject.CandidateID,
		Mapping: receipt.Mapping, Display: receipt.Display, Subject: receipt.Subject, ReviewerID: receipt.ReviewerID, DecisionReason: receipt.DecisionReason})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, receipt) {
		return fmt.Errorf("%w: noncanonical recursive receipt", ErrInvalid)
	}
	return nil
}

// ValidateRecursiveReviewReplay requires an identical recursive receipt, not a
// new interpretation of the same directed pair under changed evidence or review.
func ValidateRecursiveReviewReplay(cut AdmittedCut, report CandidateReport,
	expected RecursiveReviewBasis, stored DerivedReviewReceipt, requested DerivedReviewInput,
) error {
	if err := ValidateRecursiveReviewReceipt(cut, report, expected, stored); err != nil {
		return err
	}
	want, err := NewRecursiveReviewReceipt(cut, report, expected, requested)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, stored) {
		return ErrReplayConflict
	}
	return nil
}
