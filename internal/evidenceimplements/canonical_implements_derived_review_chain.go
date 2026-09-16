package evidenceimplements

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

const (
	// DerivedBasisVersion preserves the retained review contract value.
	DerivedBasisVersion = "lab-canonical-implements-derived-review-basis/v1"
	// DerivedDisplayVersion preserves the retained review contract value.
	DerivedDisplayVersion = "lab-canonical-implements-derived-review-display/v1"
	// DerivedReceiptVersion preserves the retained review contract value.
	DerivedReceiptVersion = "lab-canonical-implements-derived-review-receipt/v1"
	// MaxDerivedParents preserves the retained review contract value.
	MaxDerivedParents = 8
	// MaxDerivedBasisBytes preserves the retained review contract value.
	MaxDerivedBasisBytes = 8 << 20
)

// ErrDerivedDepthUnsupported identifies a rejected pure review contract.
var ErrDerivedDepthUnsupported = errors.New("derived implements review supports exactly one derivation level with source_claim parents")

// This controller-pinned basis is independent of a submitted display. Native
// records describe a complete depth-one AND cut, not admission or currentness.
// Source leaves retain their original review sessions and exact source bytes.
// DerivedReviewBasis is a versioned pure review value; constructing it does not grant canonical write authority.
type DerivedReviewBasis struct {
	ContractVersion string                          `json:"contract_version"`
	ID              string                          `json:"id"`
	AdmittedCutID   string                          `json:"admitted_cut_id"`
	RootNodeID      string                          `json:"root_node_id"`
	Ancestors       evidencegraph.CanonicalArtifact `json:"ancestors"`
	SourceLeaves    []ReviewEvidenceSnapshot        `json:"source_leaves"`
	RuleStatement   string                          `json:"rule_statement"`
}

// DerivedReviewDisplay is a versioned pure review value; constructing it does not grant canonical write authority.
type DerivedReviewDisplay struct {
	ContractVersion string `json:"contract_version"`
	ID              string `json:"id"`
	MediaType       string `json:"media_type"`
	PayloadUTF8     string `json:"payload_utf8"`
}

// DerivedReviewSubject is a versioned pure review value; constructing it does not grant canonical write authority.
type DerivedReviewSubject struct {
	AdmittedCutID   string `json:"admitted_cut_id"`
	ReportID        string `json:"report_id"`
	CandidateID     string `json:"candidate_id"`
	BasisID         string `json:"basis_id"`
	ReviewPackageID string `json:"review_package_id"`
	DisplayID       string `json:"display_id"`
}

// DerivedReviewInput is a versioned pure review value; constructing it does not grant canonical write authority.
type DerivedReviewInput struct {
	CandidateID    string
	Mapping        ReviewMapping
	Display        DerivedReviewDisplay
	Subject        DerivedReviewSubject
	ReviewerID     string
	DecisionReason string
}

// Deliberately not an embedded v1/v2 receipt: this review artifact alone grants
// no canonical write authority. A runtime admission envelope must independently
// bind database-owned origins and the exact approved review.
// DerivedReviewReceipt is a versioned pure review value; constructing it does not grant canonical write authority.
type DerivedReviewReceipt struct {
	ContractVersion string               `json:"contract_version"`
	ID              string               `json:"id"`
	Authority       string               `json:"authority"`
	Direction       string               `json:"direction"`
	SemanticEffect  string               `json:"semantic_effect"`
	PolicyEffect    string               `json:"policy_effect"`
	Mapping         ReviewMapping        `json:"mapping"`
	Display         DerivedReviewDisplay `json:"display"`
	Subject         DerivedReviewSubject `json:"subject"`
	ReviewerID      string               `json:"reviewer_id"`
	DecisionReason  string               `json:"decision_reason"`
	ProjectedEdge   ProjectedEdge        `json:"projected_edge"`
}

// DerivedSourceDisplay is a versioned pure review value; constructing it does not grant canonical write authority.
type DerivedSourceDisplay struct {
	NodeID  string                                             `json:"node_id"`
	Display evidenceingestion.SourceClaimReviewDisplayArtifact `json:"display"`
	Subject evidenceingestion.ExactDisplayedReviewSubject      `json:"subject"`
}

// DerivedReviewPackage is a versioned pure review value; constructing it does not grant canonical write authority.
type DerivedReviewPackage struct {
	ContractVersion string                          `json:"contract_version"`
	ID              string                          `json:"id"`
	ReportID        string                          `json:"report_id"`
	Candidate       Candidate                       `json:"candidate"`
	BasisID         string                          `json:"basis_id"`
	Ancestors       evidencegraph.CanonicalArtifact `json:"ancestors"`
	RuleStatement   string                          `json:"rule_statement"`
	SourceDisplays  []DerivedSourceDisplay          `json:"source_displays"`
	Mapping         ReviewMapping                   `json:"mapping"`
	Limitations     []string                        `json:"limitations"`
}

// NewDerivedReviewBasis applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func NewDerivedReviewBasis(cut AdmittedCut, input DerivedReviewBasis) (DerivedReviewBasis, error) {
	if err := ValidateDerivedBounds(input); err != nil {
		return DerivedReviewBasis{}, err
	}
	if len(cut.Specifications) > MaxCutNodes || len(cut.Implementations) > MaxCutNodes {
		return DerivedReviewBasis{}, ErrInvalid
	}
	if err := ValidateAdmittedCut(cut); err != nil {
		return DerivedReviewBasis{}, err
	}
	root, found := FindSpecification(cut.Specifications, input.RootNodeID)
	if !found || input.AdmittedCutID != cut.ID || root.NodeKind != evidencegraph.CanonicalDerivedClaim {
		return DerivedReviewBasis{}, ErrEndpointMissing
	}
	a := input.Ancestors
	for _, node := range a.Nodes {
		if node.ID != root.NodeID && node.Kind == evidencegraph.CanonicalDerivedClaim {
			return DerivedReviewBasis{}, ErrDerivedDepthUnsupported
		}
	}
	if err := a.Validate(); err != nil {
		return DerivedReviewBasis{}, fmt.Errorf("%w: native ancestor cut: %v", ErrInvalid, err)
	}
	d := a.Derivations[0]
	if d.NodeID != root.NodeID || d.ID != root.DerivationID || len(d.Parents) != len(input.SourceLeaves) ||
		len(a.Nodes) != len(d.Parents)+1 || len(a.Edges) != len(d.Parents) {
		return DerivedReviewBasis{}, fmt.Errorf("%w: incomplete AND parent cut", ErrInvalid)
	}
	parents := make(map[string]bool, len(d.Parents))
	for _, id := range d.Parents {
		if id == root.NodeID {
			return DerivedReviewBasis{}, ErrDerivedDepthUnsupported
		}
		parents[id] = true // Native validation already rejected duplicates.
	}
	usedPayload, usedProvenance := make(map[string]bool, len(a.Payloads)), make(map[string]bool, len(a.Provenance))
	usedTemporal, usedIntegrity := make(map[string]bool, len(a.Temporal)), make(map[string]bool, len(a.Integrity))
	for _, node := range a.Nodes {
		if node.ID == root.NodeID {
			if node.Kind != evidencegraph.CanonicalDerivedClaim || node.ProvenanceRef != d.ProvenanceRef {
				return DerivedReviewBasis{}, ErrInvalid
			}
		} else if !parents[node.ID] || node.Kind != evidencegraph.CanonicalSourceClaim {
			return DerivedReviewBasis{}, ErrDerivedDepthUnsupported
		}
		spec, exists := FindSpecification(cut.Specifications, node.ID)
		p := a.Payloads[slices.IndexFunc(a.Payloads, func(p evidencegraph.EvidencePayload) bool { return p.ID == node.PayloadRef })]
		if !exists || spec.NodeKind != node.Kind || spec.ClaimText != p.Claim || spec.ClaimHash != ExcerptHash(p.Claim) ||
			(node.ID == root.NodeID && (spec.SourceTitle != p.Title || spec.SourceLocation != p.Source)) {
			return DerivedReviewBasis{}, fmt.Errorf("%w: native node differs from admitted specification", ErrInvalid)
		}
		usedPayload[node.PayloadRef], usedProvenance[node.ProvenanceRef], usedTemporal[node.TemporalRef], usedIntegrity[node.IntegrityRef] = true, true, true, true
	}
	// Native source payload labels are not original external-document metadata.
	// Source title/location/revision are checked against each exact leaf below.
	prov := a.Provenance[slices.IndexFunc(a.Provenance, func(p evidencegraph.ProvenanceRecord) bool { return p.ID == d.ProvenanceRef })]
	if !slices.Equal(prov.OriginRefs, d.Parents) || prov.Method != d.Method || prov.Producer != d.Producer || prov.TraceRef != d.TraceRef {
		return DerivedReviewBasis{}, fmt.Errorf("%w: derivation provenance differs from the complete parent set", ErrInvalid)
	}
	edgeParents := make(map[string]bool, len(d.Parents))
	for _, edge := range a.Edges {
		if !parents[edge.From] || edge.To != root.NodeID || edge.Relation != evidencegraph.CanonicalDerivedFrom || edgeParents[edge.From] {
			return DerivedReviewBasis{}, fmt.Errorf("%w: parent-to-child edge mismatch", ErrInvalid)
		}
		p := a.Provenance[slices.IndexFunc(a.Provenance, func(p evidencegraph.ProvenanceRecord) bool { return p.ID == edge.ProvenanceRef })]
		if !slices.Equal(p.OriginRefs, []string{edge.From, root.NodeID}) || p.Method != d.Method || p.Producer != d.Producer || p.TraceRef != d.TraceRef {
			return DerivedReviewBasis{}, fmt.Errorf("%w: derived edge provenance mismatch", ErrInvalid)
		}
		edgeParents[edge.From], usedProvenance[edge.ProvenanceRef] = true, true
	}
	if len(usedPayload) != len(a.Payloads) || len(usedProvenance) != len(a.Provenance) || len(usedTemporal) != len(a.Temporal) || len(usedIntegrity) != len(a.Integrity) {
		return DerivedReviewBasis{}, fmt.Errorf("%w: unrelated ancestor sidecar padding", ErrInvalid)
	}
	seen := make(map[string]bool, len(d.Parents))
	for _, leaf := range input.SourceLeaves {
		if !parents[leaf.SpecificationNodeID] || seen[leaf.SpecificationNodeID] {
			return DerivedReviewBasis{}, fmt.Errorf("%w: source leaves are not the exact AND parent set", ErrInvalid)
		}
		if err := ValidateReviewEvidenceSnapshot(cut, leaf); err != nil {
			return DerivedReviewBasis{}, err
		}
		seen[leaf.SpecificationNodeID] = true
	}
	input.ContractVersion, input.ID = DerivedBasisVersion, ""
	data, err := json.Marshal(input)
	if err != nil || len(data) > MaxDerivedBasisBytes {
		return DerivedReviewBasis{}, fmt.Errorf("%w: derived basis byte bound", ErrInvalid)
	}
	// Typed round trip detaches every nested native record, manifest and source
	// ref after all collection/string bounds; it is not a public JSON decoder.
	var result DerivedReviewBasis
	if err := json.Unmarshal(data, &result); err != nil {
		return DerivedReviewBasis{}, err
	}
	result.ID, err = Digest("canonical-implements-derived-basis:v1", result)
	if err != nil {
		return DerivedReviewBasis{}, err
	}
	sealed, err := json.Marshal(result)
	if err != nil || len(sealed) > MaxDerivedBasisBytes {
		return DerivedReviewBasis{}, fmt.Errorf("%w: sealed derived basis byte bound", ErrInvalid)
	}
	return result, nil
}

// ValidateDerivedReviewBasis applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateDerivedReviewBasis(cut AdmittedCut, expected DerivedReviewBasis) error {
	want, err := NewDerivedReviewBasis(cut, expected)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, expected) {
		return fmt.Errorf("%w: noncanonical derived basis", ErrInvalid)
	}
	return nil
}

// BuildDerivedReviewDisplay applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func BuildDerivedReviewDisplay(cut AdmittedCut, report CandidateReport, candidateID string,
	expected DerivedReviewBasis, mapping ReviewMapping,
) (DerivedReviewDisplay, DerivedReviewSubject, error) {
	fail := func(err error) (DerivedReviewDisplay, DerivedReviewSubject, error) {
		return DerivedReviewDisplay{}, DerivedReviewSubject{}, err
	}
	if err := ValidateDerivedReviewBasis(cut, expected); err != nil {
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
	review := DerivedReviewPackage{ContractVersion: DerivedDisplayVersion, ReportID: report.ID,
		Candidate: candidate, BasisID: expected.ID, Ancestors: expected.Ancestors, RuleStatement: expected.RuleStatement, Mapping: normalized,
		SourceDisplays: make([]DerivedSourceDisplay, 0, len(expected.SourceLeaves)),
		Limitations: []string{
			"Complete means only this independently pinned depth-one native AND parent cut; deeper derivations are unsupported.",
			"All source leaves retain their exact original source review basis. Multiple claims from one source are not independent corroboration.",
			"The rule, derived statement and implements mapping remain semantic assertions; structural validation cannot detect every semantic overclaim.",
			"Candidate ranking is not a relation witness. Exact bytes do not authenticate a producer, reviewer, human approval or actual rendering.",
			"This pure Lab artifact proves no current PostgreSQL admission, extraction qualification, source freshness or code execution; no runtime writer or policy effect.",
		}}
	for _, leaf := range expected.SourceLeaves {
		display, subject, err := evidenceingestion.BuildSourceClaimReviewDisplayArtifact(leaf.ReviewSnapshot)
		if err != nil {
			return fail(err)
		}
		review.SourceDisplays = append(review.SourceDisplays, DerivedSourceDisplay{NodeID: leaf.SpecificationNodeID, Display: display, Subject: subject})
	}
	review.ID, err = Digest("canonical-implements-derived-package:v1", review)
	if err != nil {
		return fail(err)
	}
	payload, err := json.Marshal(review)
	if err != nil || len(payload) > MaxReviewDisplayBytes {
		return fail(fmt.Errorf("%w: derived display byte bound", ErrInvalid))
	}
	display := DerivedReviewDisplay{ContractVersion: DerivedDisplayVersion,
		MediaType: "application/vnd.ahe.lab-implements-derived-review.v1+json", PayloadUTF8: string(payload)}
	display.ID, err = Digest("canonical-implements-derived-display:v1", display)
	if err != nil {
		return fail(err)
	}
	return display, DerivedReviewSubject{AdmittedCutID: cut.ID, ReportID: report.ID, CandidateID: candidateID,
		BasisID: expected.ID, ReviewPackageID: review.ID, DisplayID: display.ID}, nil
}

// NewDerivedReviewReceipt applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func NewDerivedReviewReceipt(cut AdmittedCut, report CandidateReport,
	expected DerivedReviewBasis, input DerivedReviewInput,
) (DerivedReviewReceipt, error) {
	display, subject, err := BuildDerivedReviewDisplay(cut, report, input.CandidateID, expected, input.Mapping)
	if err != nil {
		return DerivedReviewReceipt{}, err
	}
	if input.Display != display || input.Subject != subject {
		return DerivedReviewReceipt{}, fmt.Errorf("%w: exact derived display or subject changed", ErrInvalid)
	}
	candidate := report.Candidates[slices.IndexFunc(report.Candidates, func(c Candidate) bool { return c.ID == input.CandidateID })]
	validated, err := NewReceipt(cut, Input{AdmittedCutID: cut.ID,
		SpecificationNodeID: expected.RootNodeID, ImplementationNodeID: candidate.Implementation.NodeID,
		ProposalSentence: input.Mapping.ProposalSentence, Witnesses: input.Mapping.Witnesses, Coverage: input.Mapping.Coverage,
		Limitations: input.Mapping.Limitations, ReviewerID: input.ReviewerID, DecisionReason: input.DecisionReason})
	if err != nil {
		return DerivedReviewReceipt{}, err
	}
	receipt := DerivedReviewReceipt{ContractVersion: DerivedReceiptVersion,
		Authority: Authority, Direction: Direction, SemanticEffect: Effect, PolicyEffect: PolicyEffect,
		Mapping: ReviewMapping{ProposalSentence: validated.ProposalSentence, Witnesses: validated.Witnesses, Coverage: validated.Coverage, Limitations: validated.Limitations},
		Display: display, Subject: subject, ReviewerID: validated.ReviewerID, DecisionReason: validated.DecisionReason}
	receipt.ID, err = Digest("canonical-implements-derived-receipt:v1", receipt)
	if err != nil {
		return DerivedReviewReceipt{}, err
	}
	receipt.ProjectedEdge = validated.ProjectedEdge
	receipt.ProjectedEdge.ProvenanceReceiptID = receipt.ID
	return receipt, nil
}

// ValidateDerivedReviewReceipt applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateDerivedReviewReceipt(cut AdmittedCut, report CandidateReport,
	expected DerivedReviewBasis, receipt DerivedReviewReceipt,
) error {
	want, err := NewDerivedReviewReceipt(cut, report, expected, DerivedReviewInput{CandidateID: receipt.Subject.CandidateID,
		Mapping: receipt.Mapping, Display: receipt.Display, Subject: receipt.Subject, ReviewerID: receipt.ReviewerID, DecisionReason: receipt.DecisionReason})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, receipt) {
		return ErrInvalid
	}
	return nil
}

// ValidateDerivedReviewReplay applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateDerivedReviewReplay(cut AdmittedCut, report CandidateReport,
	expected DerivedReviewBasis, stored DerivedReviewReceipt, requested DerivedReviewInput,
) error {
	if err := ValidateDerivedReviewReceipt(cut, report, expected, stored); err != nil {
		return err
	}
	want, err := NewDerivedReviewReceipt(cut, report, expected, requested)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, stored) {
		return ErrReplayConflict
	}
	return nil
}

// ValidateDerivedBounds retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func ValidateDerivedBounds(b DerivedReviewBasis) error {
	a := b.Ancestors
	if len(a.Derivations) > 1 {
		return ErrDerivedDepthUnsupported
	}
	if len(b.SourceLeaves) < 1 || len(b.SourceLeaves) > MaxDerivedParents || len(a.Derivations) != 1 ||
		len(a.Derivations[0].Parents) < 1 || len(a.Derivations[0].Parents) > MaxDerivedParents ||
		len(a.Nodes) > 9 || len(a.Edges) > 8 || len(a.Payloads) > 9 || len(a.Provenance) > 17 || len(a.Temporal) > 9 || len(a.Integrity) > 9 ||
		strings.TrimSpace(b.RuleStatement) == "" || strings.TrimSpace(b.RuleStatement) != b.RuleStatement {
		return ErrInvalid
	}
	metadata := make([]string, 0, 7+6*len(a.Nodes)+6*len(a.Edges)+24*len(a.Payloads)+15*len(a.Provenance)+5*len(a.Temporal)+3*len(a.Integrity)+14)
	metadata = append(metadata, b.ContractVersion, b.ID, b.AdmittedCutID, b.RootNodeID, b.RuleStatement, a.SchemaVersion, a.SnapshotID)
	for _, n := range a.Nodes {
		metadata = append(metadata, n.ID, string(n.Kind), n.PayloadRef, n.ProvenanceRef, n.TemporalRef, n.IntegrityRef)
	}
	for _, e := range a.Edges {
		metadata = append(metadata, e.ID, e.From, e.To, string(e.Relation), e.ProvenanceRef)
	}
	for _, p := range a.Payloads {
		if len(p.TargetAnchors) > MaxDerivedParents {
			return ErrInvalid
		}
		metadata = append(metadata, p.ID, p.SourceType, p.Title, p.Source, p.Span, p.SpanLocator, p.Claim, p.Applicability)
		for _, target := range p.TargetAnchors {
			metadata = append(metadata, target.Kind, target.ID)
		}
	}
	for _, p := range a.Provenance {
		if len(p.OriginRefs) > MaxDerivedParents {
			return ErrInvalid
		}
		metadata = append(metadata, p.ID, p.OriginGroupID, p.Producer, p.Method, p.MethodVersion, p.TraceRef, p.ReviewRef)
		metadata = append(metadata, p.OriginRefs...)
	}
	for _, v := range a.Temporal {
		metadata = append(metadata, v.ID, string(v.Status), v.ObservedAt, v.ValidFrom, v.ValidTo)
	}
	for _, v := range a.Integrity {
		metadata = append(metadata, v.ID, v.Algorithm, v.Digest)
	}
	d := a.Derivations[0]
	metadata = append(metadata, d.ID, d.NodeID, d.Method, d.Producer, d.TraceRef, d.ProvenanceRef)
	metadata = append(metadata, d.Parents...)
	for _, value := range metadata {
		if len(value) > MaxExcerptBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return ErrInvalid
		}
	}
	for _, leaf := range b.SourceLeaves {
		if len(leaf.ID) > MaxLocationBytes || len(leaf.ContractVersion) > MaxLocationBytes {
			return ErrInvalid
		}
		if err := ValidateReviewEvidenceBounds(ReviewEvidenceInput{AdmittedCutID: leaf.AdmittedCutID,
			SpecificationNodeID: leaf.SpecificationNodeID, ReviewSnapshot: leaf.ReviewSnapshot, RawText: leaf.RawText, RenderedText: leaf.RenderedText}); err != nil {
			return err
		}
	}
	// All constituent lengths are bounded above before encoding. Check the
	// aggregate serialized envelope before native validation or leaf rebuilding.
	data, err := json.Marshal(b)
	if err != nil || len(data) > MaxDerivedBasisBytes {
		return fmt.Errorf("%w: derived basis byte bound", ErrInvalid)
	}
	return nil
}
