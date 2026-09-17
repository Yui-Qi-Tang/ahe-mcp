package evidenceimplements

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

const (
	// ContractVersion preserves the retained review contract value.
	ContractVersion = "lab-canonical-implements-review-receipt/v1"
	// Direction preserves the retained review contract value.
	Direction = "specification_claim_to_implementation_claim"
	// Authority preserves the retained review contract value.
	Authority = "review_asserted_lab_receipt"
	// Effect preserves the retained review contract value.
	Effect = "direct_structural_navigation_only"
	// PolicyEffect preserves the retained review contract value.
	PolicyEffect = "no_truth_support_status_supersession_or_currentness_effect"

	// SpecificationRole preserves the retained review contract value.

	SpecificationRole = "specification"
	// ImplementationRole preserves the retained review contract value.
	ImplementationRole = "implementation"
	// SpecificationClass preserves the retained review contract value.
	SpecificationClass = "specification_material"
	// CodeSourceType preserves the retained review contract value.
	CodeSourceType = "code_repository"
	// RevisionKind preserves the retained review contract value.
	RevisionKind = "git_commit"

	// MaxCutNodes preserves the retained review contract value.

	MaxCutNodes = 256
	// MaxWitnesses preserves the retained review contract value.
	MaxWitnesses = 8
	// MaxLimitations preserves the retained review contract value.
	MaxLimitations = 32
	// MaxNodeIDBytes preserves the retained review contract value.
	MaxNodeIDBytes = 512
	// MaxCutIDBytes preserves the retained review contract value.
	MaxCutIDBytes = 512
	// MaxTitleBytes preserves the retained review contract value.
	MaxTitleBytes = 1000
	// MaxLocationBytes preserves the retained review contract value.
	MaxLocationBytes = 2000
	// MaxRevisionBytes preserves the retained review contract value.
	MaxRevisionBytes = 1000
	// MaxExcerptBytes preserves the retained review contract value.
	MaxExcerptBytes = 16 * 1024
	// MaxPathBytes preserves the retained review contract value.
	MaxPathBytes = 2000
	// MaxSpanBytes preserves the retained review contract value.
	MaxSpanBytes = 500
	// MaxSymbolBytes preserves the retained review contract value.
	MaxSymbolBytes = 1000
	// MaxProposalBytes preserves the retained review contract value.
	MaxProposalBytes = 4000
	// MaxCoverageBytes preserves the retained review contract value.
	MaxCoverageBytes = 4000
	// MaxLimitationBytes preserves the retained review contract value.
	MaxLimitationBytes = 2000
	// MaxReviewerBytes preserves the retained review contract value.
	MaxReviewerBytes = 200
	// MaxDecisionReasonBytes preserves the retained review contract value.
	MaxDecisionReasonBytes = 2000
)

var (
	// ErrInvalid identifies a rejected pure review contract.
	ErrInvalid = errors.New("invalid canonical implements receipt")
	// ErrEndpointMissing identifies a rejected pure review contract.
	ErrEndpointMissing = errors.New("canonical implements endpoint is absent from admitted cut")
	// ErrWitnessRequired identifies a rejected pure review contract.
	ErrWitnessRequired = errors.New("canonical implements witness is required")
	// ErrDuplicateWitness identifies a rejected pure review contract.
	ErrDuplicateWitness = errors.New("duplicate canonical implements witness")
	// ErrReplayConflict identifies a rejected pure review contract.
	ErrReplayConflict = errors.New("canonical implements directed pair replay conflict")
)

// SpecificationInput is a versioned pure review value; constructing it does not grant canonical write authority.
type SpecificationInput struct {
	NodeID         string
	NodeKind       evidencegraph.CanonicalNodeKind
	DerivationID   string
	SourceTitle    string
	SourceLocation string
	SourceRevision string
	ClaimText      string
	ClaimHash      string
}

// Specification is a versioned pure review value; constructing it does not grant canonical write authority.
type Specification struct {
	NodeID         string                          `json:"node_id"`
	NodeKind       evidencegraph.CanonicalNodeKind `json:"node_kind"`
	Role           string                          `json:"role"`
	SourceClass    string                          `json:"source_class"`
	DerivationID   string                          `json:"derivation_id"`
	SourceTitle    string                          `json:"source_title"`
	SourceLocation string                          `json:"source_location"`
	SourceRevision string                          `json:"source_revision"`
	ClaimText      string                          `json:"claim_text"`
	ClaimHash      string                          `json:"claim_hash"`
}

// ImplementationInput is a versioned pure review value; constructing it does not grant canonical write authority.
type ImplementationInput struct {
	NodeID         string
	NodeKind       evidencegraph.CanonicalNodeKind
	RepositoryID   string
	SourceTitle    string
	SourceLocation string
	Revision       string
	Path           string
	Span           string
	SymbolKind     string
	QualifiedName  string
	ExactExcerpt   string
	ExcerptHash    string
}

// Implementation is a versioned pure review value; constructing it does not grant canonical write authority.
type Implementation struct {
	NodeID         string                          `json:"node_id"`
	NodeKind       evidencegraph.CanonicalNodeKind `json:"node_kind"`
	Role           string                          `json:"role"`
	SourceType     string                          `json:"source_type"`
	RepositoryID   string                          `json:"repository_id"`
	SourceTitle    string                          `json:"source_title"`
	SourceLocation string                          `json:"source_location"`
	RevisionKind   string                          `json:"revision_kind"`
	Revision       string                          `json:"revision"`
	Path           string                          `json:"path"`
	Span           string                          `json:"span"`
	SymbolKind     string                          `json:"symbol_kind"`
	QualifiedName  string                          `json:"qualified_name"`
	ExactExcerpt   string                          `json:"exact_excerpt"`
	ExcerptHash    string                          `json:"excerpt_hash"`
}

// AdmittedCut is the pure review's closed world. Receipt input
// names node IDs only; it cannot assert admitted=true or replace node
// kind/source material. The cut does not authenticate admission; runtime
// promotion would need a PostgreSQL-owned loader.
// AdmittedCut is a versioned pure review value; constructing it does not grant canonical write authority.
type AdmittedCut struct {
	ID              string           `json:"id"`
	Specifications  []Specification  `json:"specifications"`
	Implementations []Implementation `json:"implementations"`
}

// WitnessKind is a versioned pure review value; constructing it does not grant canonical write authority.
type WitnessKind string

const (
	// ExplicitSourceMapping preserves the retained review contract value.
	ExplicitSourceMapping WitnessKind = "source_explicit_implementation_mapping"
	// LanguageContract preserves the retained review contract value.
	LanguageContract WitnessKind = "deterministic_language_contract"
	// ReviewedBehavior preserves the retained review contract value.
	ReviewedBehavior WitnessKind = "reviewed_behavior_mapping"
)

// Witness is a versioned pure review value; constructing it does not grant canonical write authority.
type Witness struct {
	Kind            WitnessKind `json:"kind"`
	EndpointNodeIDs []string    `json:"endpoint_node_ids"`
	SourceTitle     string      `json:"source_title"`
	SourceLocation  string      `json:"source_location"`
	ExactExcerpt    string      `json:"exact_excerpt"`
	ExcerptHash     string      `json:"excerpt_hash"`
}

// Input is a versioned pure review value; constructing it does not grant canonical write authority.
type Input struct {
	AdmittedCutID        string
	SpecificationNodeID  string
	ImplementationNodeID string
	ProposalSentence     string
	Witnesses            []Witness
	Coverage             string
	Limitations          []string
	ReviewerID           string
	DecisionReason       string
}

// ProjectedEdge is a versioned pure review value; constructing it does not grant canonical write authority.
type ProjectedEdge struct {
	ID                  string                              `json:"id"`
	From                string                              `json:"from"`
	To                  string                              `json:"to"`
	Relation            evidencegraph.CanonicalEdgeRelation `json:"relation"`
	ProvenanceReceiptID string                              `json:"provenance_receipt_id"`
}

// Receipt is a versioned pure review value; constructing it does not grant canonical write authority.
type Receipt struct {
	ContractVersion  string         `json:"contract_version"`
	ID               string         `json:"id"`
	Authority        string         `json:"authority"`
	Direction        string         `json:"direction"`
	SemanticEffect   string         `json:"semantic_effect"`
	PolicyEffect     string         `json:"policy_effect"`
	AdmittedCutID    string         `json:"admitted_cut_id"`
	ProposalSentence string         `json:"proposal_sentence"`
	Specification    Specification  `json:"specification"`
	Implementation   Implementation `json:"implementation"`
	Witnesses        []Witness      `json:"witnesses"`
	Coverage         string         `json:"coverage"`
	Limitations      []string       `json:"limitations"`
	ReviewerID       string         `json:"reviewer_id"`
	DecisionReason   string         `json:"decision_reason"`
	ProjectedEdge    ProjectedEdge  `json:"projected_edge"`
}

// NewAdmittedCut applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func NewAdmittedCut(
	specifications []SpecificationInput,
	implementations []ImplementationInput,
) (AdmittedCut, error) {
	if len(specifications) == 0 || len(implementations) == 0 {
		return AdmittedCut{}, fmt.Errorf(
			"%w: admitted cut requires both endpoint partitions",
			ErrInvalid,
		)
	}
	if len(specifications) > MaxCutNodes || len(implementations) > MaxCutNodes {
		return AdmittedCut{}, fmt.Errorf(
			"%w: admitted cut endpoint count exceeds %d per partition",
			ErrInvalid,
			MaxCutNodes,
		)
	}

	normalizedSpecifications := make([]Specification, len(specifications))
	nodeIDs := make(map[string]string, len(specifications)+len(implementations))
	for index, input := range specifications {
		node, err := normalizeCanonicalImplementsSpecification(input)
		if err != nil {
			return AdmittedCut{}, fmt.Errorf("%w: specification %d: %v", ErrInvalid, index, err)
		}
		if role, exists := nodeIDs[node.NodeID]; exists {
			return AdmittedCut{}, fmt.Errorf("%w: node %q already belongs to %s partition", ErrInvalid, node.NodeID, role)
		}
		nodeIDs[node.NodeID] = SpecificationRole
		normalizedSpecifications[index] = node
	}
	slices.SortFunc(normalizedSpecifications, func(left, right Specification) int {
		return strings.Compare(left.NodeID, right.NodeID)
	})

	normalizedImplementations := make([]Implementation, len(implementations))
	for index, input := range implementations {
		node, err := normalizeCanonicalImplementsImplementation(input)
		if err != nil {
			return AdmittedCut{}, fmt.Errorf("%w: implementation %d: %v", ErrInvalid, index, err)
		}
		if role, exists := nodeIDs[node.NodeID]; exists {
			return AdmittedCut{}, fmt.Errorf("%w: node %q already belongs to %s partition", ErrInvalid, node.NodeID, role)
		}
		nodeIDs[node.NodeID] = ImplementationRole
		normalizedImplementations[index] = node
	}
	slices.SortFunc(normalizedImplementations, func(left, right Implementation) int {
		return strings.Compare(left.NodeID, right.NodeID)
	})

	cut := AdmittedCut{
		Specifications:  normalizedSpecifications,
		Implementations: normalizedImplementations,
	}
	cutID, err := Digest("canonical-implements-cut:v1", cut)
	if err != nil {
		return AdmittedCut{}, err
	}
	cut.ID = cutID
	return cut, nil
}

// NewReceipt applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func NewReceipt(
	cut AdmittedCut,
	input Input,
) (Receipt, error) {
	if err := ValidateAdmittedCut(cut); err != nil {
		return Receipt{}, err
	}
	if input.AdmittedCutID != cut.ID {
		return Receipt{}, fmt.Errorf("%w: admitted cut identity is stale or mismatched", ErrInvalid)
	}
	specification, ok := FindSpecification(cut.Specifications, input.SpecificationNodeID)
	if !ok {
		return Receipt{}, fmt.Errorf("%w: specification %q", ErrEndpointMissing, input.SpecificationNodeID)
	}
	implementation, ok := findCanonicalImplementsImplementation(cut.Implementations, input.ImplementationNodeID)
	if !ok {
		return Receipt{}, fmt.Errorf("%w: implementation %q", ErrEndpointMissing, input.ImplementationNodeID)
	}
	if specification.NodeID == implementation.NodeID {
		return Receipt{}, fmt.Errorf("%w: self-edge", ErrInvalid)
	}

	proposalSentence, err := RequiredText("proposal_sentence", input.ProposalSentence, MaxProposalBytes)
	if err != nil {
		return Receipt{}, err
	}
	witnesses, err := normalizeCanonicalImplementsWitnesses(input.Witnesses, specification.NodeID, implementation.NodeID)
	if err != nil {
		return Receipt{}, err
	}
	coverage, err := RequiredText("coverage", input.Coverage, MaxCoverageBytes)
	if err != nil {
		return Receipt{}, err
	}
	if input.Limitations == nil {
		return Receipt{}, fmt.Errorf("%w: limitations must be explicitly provided", ErrInvalid)
	}
	limitations, err := normalizeCanonicalImplementsLimitations(input.Limitations)
	if err != nil {
		return Receipt{}, err
	}
	reviewerID, err := RequiredText("reviewer_id", input.ReviewerID, MaxReviewerBytes)
	if err != nil {
		return Receipt{}, err
	}
	decisionReason, err := RequiredText("decision_reason", input.DecisionReason, MaxDecisionReasonBytes)
	if err != nil {
		return Receipt{}, err
	}

	receipt := Receipt{
		ContractVersion:  ContractVersion,
		Authority:        Authority,
		Direction:        Direction,
		SemanticEffect:   Effect,
		PolicyEffect:     PolicyEffect,
		AdmittedCutID:    cut.ID,
		ProposalSentence: proposalSentence,
		Specification:    specification,
		Implementation:   implementation,
		Witnesses:        witnesses,
		Coverage:         coverage,
		Limitations:      limitations,
		ReviewerID:       reviewerID,
		DecisionReason:   decisionReason,
	}
	receiptID, err := canonicalImplementsReceiptID(receipt)
	if err != nil {
		return Receipt{}, err
	}
	receipt.ID = receiptID
	// Canonical graph edge identity is structural. The separately persisted
	// receipt binds review metadata without making the same directed pair acquire
	// a new graph identity when audit metadata changes.
	edgeID := evidencegraph.StableCanonicalID(
		"canon-edge",
		specification.NodeID,
		implementation.NodeID,
		string(evidencegraph.CanonicalImplements),
	)
	receipt.ProjectedEdge = ProjectedEdge{
		ID:                  edgeID,
		From:                specification.NodeID,
		To:                  implementation.NodeID,
		Relation:            evidencegraph.CanonicalImplements,
		ProvenanceReceiptID: receiptID,
	}
	return receipt, nil
}

// ValidateAdmittedCut applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateAdmittedCut(cut AdmittedCut) error {
	if !validCanonicalImplementsTypedID(cut.ID, "canonical-implements-cut:v1:sha256:", MaxCutIDBytes) {
		return fmt.Errorf("%w: admitted cut ID", ErrInvalid)
	}
	specifications := make([]SpecificationInput, len(cut.Specifications))
	for index, node := range cut.Specifications {
		specifications[index] = SpecificationInput{
			NodeID:         node.NodeID,
			NodeKind:       node.NodeKind,
			DerivationID:   node.DerivationID,
			SourceTitle:    node.SourceTitle,
			SourceLocation: node.SourceLocation,
			SourceRevision: node.SourceRevision,
			ClaimText:      node.ClaimText,
			ClaimHash:      node.ClaimHash,
		}
	}
	implementations := make([]ImplementationInput, len(cut.Implementations))
	for index, node := range cut.Implementations {
		implementations[index] = ImplementationInput{
			NodeID:         node.NodeID,
			NodeKind:       node.NodeKind,
			RepositoryID:   node.RepositoryID,
			SourceTitle:    node.SourceTitle,
			SourceLocation: node.SourceLocation,
			Revision:       node.Revision,
			Path:           node.Path,
			Span:           node.Span,
			SymbolKind:     node.SymbolKind,
			QualifiedName:  node.QualifiedName,
			ExactExcerpt:   node.ExactExcerpt,
			ExcerptHash:    node.ExcerptHash,
		}
	}
	want, err := NewAdmittedCut(specifications, implementations)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(cut, want) {
		return fmt.Errorf("%w: admitted cut is not canonical", ErrInvalid)
	}
	return nil
}

// ValidateReceipt applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateReceipt(
	cut AdmittedCut,
	receipt Receipt,
) error {
	input := Input{
		AdmittedCutID:        receipt.AdmittedCutID,
		SpecificationNodeID:  receipt.Specification.NodeID,
		ImplementationNodeID: receipt.Implementation.NodeID,
		ProposalSentence:     receipt.ProposalSentence,
		Witnesses:            CloneWitnesses(receipt.Witnesses),
		Coverage:             receipt.Coverage,
		Limitations:          append([]string{}, receipt.Limitations...),
		ReviewerID:           receipt.ReviewerID,
		DecisionReason:       receipt.DecisionReason,
	}
	want, err := NewReceipt(cut, input)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(receipt, want) {
		return fmt.Errorf("%w: receipt is not canonical", ErrInvalid)
	}
	return nil
}

// ValidateReplay applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateReplay(
	cut AdmittedCut,
	stored Receipt,
	requested Input,
) error {
	if err := ValidateReceipt(cut, stored); err != nil {
		return err
	}
	want, err := NewReceipt(cut, requested)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(stored, want) {
		return ErrReplayConflict
	}
	return nil
}

func normalizeCanonicalImplementsSpecification(
	input SpecificationInput,
) (Specification, error) {
	if !validCanonicalImplementsNodeID(input.NodeID) {
		return Specification{}, errors.New("node_id must be a canonical node ID")
	}
	switch input.NodeKind {
	case evidencegraph.CanonicalSourceClaim:
		if input.DerivationID != "" {
			return Specification{}, errors.New("source_claim specification must not have derivation_id")
		}
	case evidencegraph.CanonicalDerivedClaim:
		if _, err := RequiredText("derivation_id", input.DerivationID, MaxNodeIDBytes); err != nil {
			return Specification{}, err
		}
	default:
		return Specification{}, errors.New("specification must be source_claim or derived_claim")
	}
	title, err := RequiredText("source_title", input.SourceTitle, MaxTitleBytes)
	if err != nil {
		return Specification{}, err
	}
	location, err := RequiredText("source_location", input.SourceLocation, MaxLocationBytes)
	if err != nil {
		return Specification{}, err
	}
	revision, err := RequiredText("source_revision", input.SourceRevision, MaxRevisionBytes)
	if err != nil {
		return Specification{}, err
	}
	if err := validateCanonicalImplementsContent(
		"claim_text",
		"claim_hash",
		input.ClaimText,
		input.ClaimHash,
	); err != nil {
		return Specification{}, err
	}
	return Specification{
		NodeID:         input.NodeID,
		NodeKind:       input.NodeKind,
		Role:           SpecificationRole,
		SourceClass:    SpecificationClass,
		DerivationID:   input.DerivationID,
		SourceTitle:    title,
		SourceLocation: location,
		SourceRevision: revision,
		ClaimText:      input.ClaimText,
		ClaimHash:      input.ClaimHash,
	}, nil
}

func normalizeCanonicalImplementsImplementation(
	input ImplementationInput,
) (Implementation, error) {
	if !validCanonicalImplementsNodeID(input.NodeID) {
		return Implementation{}, errors.New("node_id must be a canonical node ID")
	}
	if input.NodeKind != evidencegraph.CanonicalSourceClaim {
		return Implementation{}, errors.New("implementation must be source_claim")
	}
	fields := []struct {
		name     string
		value    string
		maxBytes int
	}{
		{name: "repository_id", value: input.RepositoryID, maxBytes: MaxLocationBytes},
		{name: "source_title", value: input.SourceTitle, maxBytes: MaxTitleBytes},
		{name: "source_location", value: input.SourceLocation, maxBytes: MaxLocationBytes},
		{name: "revision", value: input.Revision, maxBytes: MaxRevisionBytes},
		{name: "path", value: input.Path, maxBytes: MaxPathBytes},
		{name: "span", value: input.Span, maxBytes: MaxSpanBytes},
		{name: "symbol_kind", value: input.SymbolKind, maxBytes: MaxSymbolBytes},
		{name: "qualified_name", value: input.QualifiedName, maxBytes: MaxSymbolBytes},
	}
	normalized := make(map[string]string, len(fields))
	for _, field := range fields {
		value, err := RequiredText(field.name, field.value, field.maxBytes)
		if err != nil {
			return Implementation{}, err
		}
		normalized[field.name] = value
	}
	if !validCanonicalImplementsGitCommit(normalized["revision"]) {
		return Implementation{}, errors.New("revision must be a canonical 40- or 64-hex Git commit ID")
	}
	if err := ValidateExcerpt(input.ExactExcerpt, input.ExcerptHash); err != nil {
		return Implementation{}, err
	}
	return Implementation{
		NodeID:         input.NodeID,
		NodeKind:       input.NodeKind,
		Role:           ImplementationRole,
		SourceType:     CodeSourceType,
		RepositoryID:   normalized["repository_id"],
		SourceTitle:    normalized["source_title"],
		SourceLocation: normalized["source_location"],
		RevisionKind:   RevisionKind,
		Revision:       normalized["revision"],
		Path:           normalized["path"],
		Span:           normalized["span"],
		SymbolKind:     normalized["symbol_kind"],
		QualifiedName:  normalized["qualified_name"],
		ExactExcerpt:   input.ExactExcerpt,
		ExcerptHash:    input.ExcerptHash,
	}, nil
}

func normalizeCanonicalImplementsWitnesses(
	values []Witness,
	specificationNodeID string,
	implementationNodeID string,
) ([]Witness, error) {
	if len(values) == 0 {
		return nil, ErrWitnessRequired
	}
	if len(values) > MaxWitnesses {
		return nil, fmt.Errorf("%w: witness count exceeds %d", ErrInvalid, MaxWitnesses)
	}
	wantEndpointIDs := []string{specificationNodeID, implementationNodeID}
	slices.Sort(wantEndpointIDs)
	normalized := make([]Witness, len(values))
	type witnessSource struct {
		location    string
		excerptHash string
	}
	seen := make(map[witnessSource]struct{}, len(values))
	for index, value := range values {
		if !allowlistedCanonicalImplementsWitnessKind(value.Kind) {
			return nil, fmt.Errorf("%w: witness kind %q is not allowlisted", ErrInvalid, value.Kind)
		}
		endpointIDs := append([]string(nil), value.EndpointNodeIDs...)
		slices.Sort(endpointIDs)
		if !slices.Equal(endpointIDs, wantEndpointIDs) {
			return nil, fmt.Errorf("%w: witness must bind both endpoint node IDs exactly", ErrInvalid)
		}
		title, err := RequiredText("witness source_title", value.SourceTitle, MaxTitleBytes)
		if err != nil {
			return nil, err
		}
		location, err := RequiredText("witness source_location", value.SourceLocation, MaxLocationBytes)
		if err != nil {
			return nil, err
		}
		if err := ValidateExcerpt(value.ExactExcerpt, value.ExcerptHash); err != nil {
			return nil, err
		}
		key := witnessSource{location: location, excerptHash: value.ExcerptHash}
		if _, exists := seen[key]; exists {
			return nil, ErrDuplicateWitness
		}
		seen[key] = struct{}{}
		normalized[index] = Witness{
			Kind:            value.Kind,
			EndpointNodeIDs: endpointIDs,
			SourceTitle:     title,
			SourceLocation:  location,
			ExactExcerpt:    value.ExactExcerpt,
			ExcerptHash:     value.ExcerptHash,
		}
	}
	slices.SortFunc(normalized, func(left, right Witness) int {
		if order := strings.Compare(string(left.Kind), string(right.Kind)); order != 0 {
			return order
		}
		if order := strings.Compare(left.SourceLocation, right.SourceLocation); order != 0 {
			return order
		}
		if order := strings.Compare(left.ExcerptHash, right.ExcerptHash); order != 0 {
			return order
		}
		return strings.Compare(left.SourceTitle, right.SourceTitle)
	})
	return normalized, nil
}

func normalizeCanonicalImplementsLimitations(values []string) ([]string, error) {
	if len(values) > MaxLimitations {
		return nil, fmt.Errorf("%w: limitation count exceeds %d", ErrInvalid, MaxLimitations)
	}
	normalized := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		limitation, err := RequiredText("limitation", value, MaxLimitationBytes)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[limitation]; exists {
			return nil, fmt.Errorf("%w: duplicate limitation %q", ErrInvalid, limitation)
		}
		seen[limitation] = struct{}{}
		normalized[index] = limitation
	}
	slices.Sort(normalized)
	return normalized, nil
}

func allowlistedCanonicalImplementsWitnessKind(kind WitnessKind) bool {
	switch kind {
	case ExplicitSourceMapping,
		LanguageContract,
		ReviewedBehavior:
		return true
	default:
		return false
	}
}

// FindSpecification retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func FindSpecification(
	values []Specification,
	nodeID string,
) (Specification, bool) {
	index, found := slices.BinarySearchFunc(values, nodeID, func(node Specification, target string) int {
		return strings.Compare(node.NodeID, target)
	})
	if !found {
		return Specification{}, false
	}
	return values[index], true
}

func findCanonicalImplementsImplementation(
	values []Implementation,
	nodeID string,
) (Implementation, bool) {
	index, found := slices.BinarySearchFunc(values, nodeID, func(node Implementation, target string) int {
		return strings.Compare(node.NodeID, target)
	})
	if !found {
		return Implementation{}, false
	}
	return values[index], true
}

// RequiredText retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func RequiredText(name, value string, maxBytes int) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: %s must be valid UTF-8", ErrInvalid, name)
	}
	if strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%w: %s must not contain NUL", ErrInvalid, name)
	}
	if value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	if strings.TrimSpace(value) != value {
		return "", fmt.Errorf("%w: %s must not have surrounding whitespace", ErrInvalid, name)
	}
	if len(value) > maxBytes {
		return "", fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalid, name, maxBytes)
	}
	return value, nil
}

// ValidateExcerpt retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func ValidateExcerpt(excerpt, excerptHash string) error {
	return validateCanonicalImplementsContent(
		"exact_excerpt",
		"excerpt_hash",
		excerpt,
		excerptHash,
	)
}

func validateCanonicalImplementsContent(textName, hashName, text, contentHash string) error {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: %s is required valid UTF-8", ErrInvalid, textName)
	}
	if strings.ContainsRune(text, '\x00') {
		return fmt.Errorf("%w: %s must not contain NUL", ErrInvalid, textName)
	}
	if len(text) > MaxExcerptBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalid, textName, MaxExcerptBytes)
	}
	if contentHash != ExcerptHash(text) {
		return fmt.Errorf("%w: %s does not match %s", ErrInvalid, hashName, textName)
	}
	return nil
}

// ExcerptHash retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func ExcerptHash(excerpt string) string {
	digest := sha256.Sum256([]byte(excerpt))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validCanonicalImplementsNodeID(value string) bool {
	return validCanonicalImplementsTypedID(value, "canon-node:", MaxNodeIDBytes)
}

func validCanonicalImplementsGitCommit(value string) bool {
	if (len(value) != 40 && len(value) != 64) || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validCanonicalImplementsTypedID(value, prefix string, maxBytes int) bool {
	return utf8.ValidString(value) &&
		strings.HasPrefix(value, prefix) &&
		len(value) > len(prefix) &&
		len(value) <= maxBytes &&
		!strings.ContainsRune(value, '\x00') &&
		strings.TrimSpace(value) == value
}

func canonicalImplementsReceiptID(receipt Receipt) (string, error) {
	receipt.ID = ""
	receipt.ProjectedEdge = ProjectedEdge{}
	return Digest("canonical-implements-receipt:v1", receipt)
}

// Digest retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func Digest(prefix string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encoding %s identity: %w", prefix, err)
	}
	digest := sha256.Sum256(encoded)
	return prefix + ":sha256:" + hex.EncodeToString(digest[:]), nil
}

// CloneWitnesses retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func CloneWitnesses(values []Witness) []Witness {
	cloned := make([]Witness, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].EndpointNodeIDs = append([]string(nil), value.EndpointNodeIDs...)
	}
	return cloned
}

// CloneReceipt retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func CloneReceipt(receipt Receipt) Receipt {
	receipt.Witnesses = CloneWitnesses(receipt.Witnesses)
	receipt.Limitations = append([]string{}, receipt.Limitations...)
	return receipt
}

// CloneAdmittedCut retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func CloneAdmittedCut(cut AdmittedCut) AdmittedCut {
	cut.Specifications = append([]Specification(nil), cut.Specifications...)
	cut.Implementations = append([]Implementation(nil), cut.Implementations...)
	return cut
}
