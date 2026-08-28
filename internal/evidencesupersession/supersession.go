// Package evidencesupersession defines deterministic identity and currentness
// contracts for governed canonical supersession. It contains no persistence or
// generic graph mutation API.
package evidencesupersession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// LineageContractVersionV1 identifies the six-field lineage key contract.
	LineageContractVersionV1 = "supersession-lineage/v1"
	// RequestContractVersionV2 identifies the reviewed replacement request hash contract.
	RequestContractVersionV2 = "supersession-admission-request/v2"
	// DecisionContractVersionV2 identifies the reviewer decision hash contract.
	DecisionContractVersionV2 = "supersession-admission-decision/v2"
	// AdmissionEventContractVersionV2 identifies the decision-bound event ID contract.
	AdmissionEventContractVersionV2 = "supersession-admission-event/v2"

	// ChainKey identifies the one ordered canonical supersession admission chain.
	ChainKey = "canonical-supersession/v1"
	// AtomicReplacementEventKind is the only transition defined by this package.
	AtomicReplacementEventKind = "atomic_replacement"
	// MaxReplacementTargets bounds one atomic replacement request.
	MaxReplacementTargets = 64

	lineageIDPrefix        = "lineage:v1:"
	admissionEventIDPrefix = "admission-event:v2:"
	admissionOutcome       = "admitted"

	maxSourceSystemBytes    = 64
	maxSourceNamespaceBytes = 200
	maxObjectTypeBytes      = 100
	maxObjectIDBytes        = 500
	maxSlotKindBytes        = 100
	maxSlotIDBytes          = 500
	maxDecisionByBytes      = 200
	maxDecisionReasonBytes  = 2000
)

var (
	// ErrInvalidBasis means a lineage basis is incomplete or non-canonical.
	ErrInvalidBasis = errors.New("invalid supersession lineage basis")
	// ErrInvalidTargets means a replacement target set is empty, malformed, or too large.
	ErrInvalidTargets = errors.New("invalid supersession replacement targets")
	// ErrInvalidPayload means a request, decision, or event payload violates its contract.
	ErrInvalidPayload = errors.New("invalid supersession payload")
)

// Basis contains the immutable source-object and source-slot fields that
// participate in lineage identity. Provider revision, content, model,
// extractor, and session metadata are intentionally excluded.
type Basis struct {
	SourceSystem    string `json:"source_system"`
	SourceNamespace string `json:"source_namespace"`
	ObjectType      string `json:"object_type"`
	ObjectID        string `json:"object_id"`
	SlotKind        string `json:"slot_kind"`
	SlotID          string `json:"slot_id"`
}

// Normalize returns a copy with surrounding Unicode whitespace removed from
// every field.
func (b Basis) Normalize() Basis {
	b.SourceSystem = strings.TrimSpace(b.SourceSystem)
	b.SourceNamespace = strings.TrimSpace(b.SourceNamespace)
	b.ObjectType = strings.TrimSpace(b.ObjectType)
	b.ObjectID = strings.TrimSpace(b.ObjectID)
	b.SlotKind = strings.TrimSpace(b.SlotKind)
	b.SlotID = strings.TrimSpace(b.SlotID)
	return b
}

// Validate requires an already-normalized, bounded six-field basis.
func (b Basis) Validate() error {
	fields := []struct {
		name     string
		value    string
		maxBytes int
	}{
		{name: "source_system", value: b.SourceSystem, maxBytes: maxSourceSystemBytes},
		{name: "source_namespace", value: b.SourceNamespace, maxBytes: maxSourceNamespaceBytes},
		{name: "object_type", value: b.ObjectType, maxBytes: maxObjectTypeBytes},
		{name: "object_id", value: b.ObjectID, maxBytes: maxObjectIDBytes},
		{name: "slot_kind", value: b.SlotKind, maxBytes: maxSlotKindBytes},
		{name: "slot_id", value: b.SlotID, maxBytes: maxSlotIDBytes},
	}
	for _, field := range fields {
		if err := validateCanonicalString(field.name, field.value, field.maxBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidBasis, err)
		}
	}
	if !isSourceSystemToken(b.SourceSystem) {
		return fmt.Errorf("%w: source_system must use lower-case letters, digits, dot, underscore, or hyphen", ErrInvalidBasis)
	}
	return nil
}

// Key returns the v1 lineage ID for an already-normalized valid basis.
func (b Basis) Key() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	payload := struct {
		ContractVersion string `json:"contract_version"`
		Basis           Basis  `json:"basis"`
	}{
		ContractVersion: LineageContractVersionV1,
		Basis:           b,
	}
	digest, err := hashJSON(payload)
	if err != nil {
		return "", fmt.Errorf("hashing lineage basis: %w", err)
	}
	return lineageIDPrefix + digest, nil
}

// NormalizeTargets copies, trims, validates, and sorts an exact replacement
// target set. Duplicate IDs after normalization are rejected.
func NormalizeTargets(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: at least one target is required", ErrInvalidTargets)
	}
	if len(values) > MaxReplacementTargets {
		return nil, fmt.Errorf("%w: target count %d exceeds %d", ErrInvalidTargets, len(values), MaxReplacementTargets)
	}
	targets := make([]string, len(values))
	for index, value := range values {
		targets[index] = strings.TrimSpace(value)
		if err := validateRecordID("target_node_id", targets[index], "canon-node:"); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidTargets, err)
		}
	}
	slices.Sort(targets)
	for index := 1; index < len(targets); index++ {
		if targets[index-1] == targets[index] {
			return nil, fmt.Errorf("%w: duplicate target %q", ErrInvalidTargets, targets[index])
		}
	}
	return targets, nil
}

// RequestPayloadV2 is the exact reviewed command identity before any graph
// rows are written.
type RequestPayloadV2 struct {
	ContractVersion    string   `json:"contract_version"`
	ProposalOccurrence string   `json:"proposal_occurrence_id"`
	ReplacementNodeID  string   `json:"replacement_node_id"`
	LineageBasis       Basis    `json:"lineage_basis"`
	LineageKey         string   `json:"lineage_key"`
	TargetNodeIDs      []string `json:"target_node_ids"`
	ExpectedRevision   int64    `json:"expected_revision"`
	ExpectedHeadEvent  string   `json:"expected_head_event_id,omitempty"`
}

// NewRequestPayloadV2 normalizes and binds one replacement request to the
// exact pre-event chain head.
func NewRequestPayloadV2(
	proposalOccurrence string,
	replacementNodeID string,
	basis Basis,
	targetNodeIDs []string,
	expectedRevision int64,
	expectedHeadEvent string,
) (RequestPayloadV2, error) {
	proposalOccurrence = strings.TrimSpace(proposalOccurrence)
	replacementNodeID = strings.TrimSpace(replacementNodeID)
	expectedHeadEvent = strings.TrimSpace(expectedHeadEvent)
	basis = basis.Normalize()
	if err := validateRecordID("proposal_occurrence_id", proposalOccurrence, "occ:"); err != nil {
		return RequestPayloadV2{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateRecordID("replacement_node_id", replacementNodeID, "canon-node:"); err != nil {
		return RequestPayloadV2{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	lineageKey, err := basis.Key()
	if err != nil {
		return RequestPayloadV2{}, err
	}
	targets, err := NormalizeTargets(targetNodeIDs)
	if err != nil {
		return RequestPayloadV2{}, err
	}
	if slices.Contains(targets, replacementNodeID) {
		return RequestPayloadV2{}, fmt.Errorf("%w: replacement node cannot target itself", ErrInvalidPayload)
	}
	if err := validateExpectedHead(expectedRevision, expectedHeadEvent); err != nil {
		return RequestPayloadV2{}, err
	}
	return RequestPayloadV2{
		ContractVersion:    RequestContractVersionV2,
		ProposalOccurrence: proposalOccurrence,
		ReplacementNodeID:  replacementNodeID,
		LineageBasis:       basis,
		LineageKey:         lineageKey,
		TargetNodeIDs:      targets,
		ExpectedRevision:   expectedRevision,
		ExpectedHeadEvent:  expectedHeadEvent,
	}, nil
}

// Validate verifies that RequestPayloadV2 is already in its canonical form.
func (p RequestPayloadV2) Validate() error {
	want, err := NewRequestPayloadV2(
		p.ProposalOccurrence,
		p.ReplacementNodeID,
		p.LineageBasis,
		p.TargetNodeIDs,
		p.ExpectedRevision,
		p.ExpectedHeadEvent,
	)
	if err != nil {
		return err
	}
	if p.ContractVersion != RequestContractVersionV2 ||
		p.ProposalOccurrence != want.ProposalOccurrence ||
		p.ReplacementNodeID != want.ReplacementNodeID ||
		p.LineageBasis != want.LineageBasis ||
		p.LineageKey != want.LineageKey ||
		!slices.Equal(p.TargetNodeIDs, want.TargetNodeIDs) ||
		p.ExpectedRevision != want.ExpectedRevision ||
		p.ExpectedHeadEvent != want.ExpectedHeadEvent {
		return fmt.Errorf("%w: request payload is not canonical", ErrInvalidPayload)
	}
	return nil
}

// Hash returns the complete SHA-256 request payload identity.
func (p RequestPayloadV2) Hash() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return hashJSON(p)
}

// DecisionPayloadV2 binds the exact reviewer decision to the admitted
// proposal and replacement node.
type DecisionPayloadV2 struct {
	ContractVersion    string `json:"contract_version"`
	ProposalOccurrence string `json:"proposal_occurrence_id"`
	Outcome            string `json:"outcome"`
	CanonicalRef       string `json:"canonical_ref"`
	DecisionBy         string `json:"decision_by"`
	DecisionReason     string `json:"decision_reason"`
}

// NewDecisionPayloadV2 normalizes one admitted supersession decision.
func NewDecisionPayloadV2(
	proposalOccurrence string,
	replacementNodeID string,
	decisionBy string,
	decisionReason string,
) (DecisionPayloadV2, error) {
	proposalOccurrence = strings.TrimSpace(proposalOccurrence)
	replacementNodeID = strings.TrimSpace(replacementNodeID)
	decisionBy = strings.TrimSpace(decisionBy)
	decisionReason = strings.TrimSpace(decisionReason)
	if err := validateRecordID("proposal_occurrence_id", proposalOccurrence, "occ:"); err != nil {
		return DecisionPayloadV2{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateRecordID("canonical_ref", replacementNodeID, "canon-node:"); err != nil {
		return DecisionPayloadV2{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateCanonicalString("decision_by", decisionBy, maxDecisionByBytes); err != nil {
		return DecisionPayloadV2{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateCanonicalString("decision_reason", decisionReason, maxDecisionReasonBytes); err != nil {
		return DecisionPayloadV2{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return DecisionPayloadV2{
		ContractVersion:    DecisionContractVersionV2,
		ProposalOccurrence: proposalOccurrence,
		Outcome:            admissionOutcome,
		CanonicalRef:       replacementNodeID,
		DecisionBy:         decisionBy,
		DecisionReason:     decisionReason,
	}, nil
}

// Validate verifies that DecisionPayloadV2 is already in its canonical form.
func (p DecisionPayloadV2) Validate() error {
	want, err := NewDecisionPayloadV2(
		p.ProposalOccurrence,
		p.CanonicalRef,
		p.DecisionBy,
		p.DecisionReason,
	)
	if err != nil {
		return err
	}
	if p.ContractVersion != DecisionContractVersionV2 || p.Outcome != admissionOutcome || p != want {
		return fmt.Errorf("%w: decision payload is not canonical", ErrInvalidPayload)
	}
	return nil
}

// Hash returns the complete SHA-256 decision payload identity.
func (p DecisionPayloadV2) Hash() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return hashJSON(p)
}

// AdmissionEventPayloadV2 is the immutable identity preimage for one
// decision-bound atomic replacement event.
type AdmissionEventPayloadV2 struct {
	ContractVersion           string   `json:"contract_version"`
	Kind                      string   `json:"kind"`
	ProposalOccurrence        string   `json:"proposal_occurrence_id"`
	ReplacementNodeID         string   `json:"replacement_node_id"`
	TargetNodeIDs             []string `json:"target_node_ids"`
	BootstrappedTargetNodeIDs []string `json:"bootstrapped_target_node_ids"`
	LineageBasis              Basis    `json:"lineage_basis"`
	LineageKey                string   `json:"lineage_key"`
	PreviousRevision          int64    `json:"previous_revision"`
	PreviousEventID           string   `json:"previous_event_id,omitempty"`
	Revision                  int64    `json:"revision"`
	AdmissionDecisionID       string   `json:"admission_decision_id"`
	RequestPayloadHash        string   `json:"request_payload_hash"`
	DecisionPayloadHash       string   `json:"decision_payload_hash"`
}

// NewAtomicReplacementEventV2 builds the canonical event from the exact
// request and decision payloads plus the memberships created by the same
// transaction.
func NewAtomicReplacementEventV2(
	request RequestPayloadV2,
	decision DecisionPayloadV2,
	admissionDecisionID string,
	bootstrappedTargetNodeIDs []string,
) (AdmissionEventPayloadV2, error) {
	if err := request.Validate(); err != nil {
		return AdmissionEventPayloadV2{}, err
	}
	if err := decision.Validate(); err != nil {
		return AdmissionEventPayloadV2{}, err
	}
	admissionDecisionID = strings.TrimSpace(admissionDecisionID)
	if err := validateRecordID("admission_decision_id", admissionDecisionID, "adm:"); err != nil {
		return AdmissionEventPayloadV2{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if decision.ProposalOccurrence != request.ProposalOccurrence || decision.CanonicalRef != request.ReplacementNodeID {
		return AdmissionEventPayloadV2{}, fmt.Errorf("%w: decision does not match replacement request", ErrInvalidPayload)
	}
	bootstrapped, err := normalizeBootstrappedTargets(bootstrappedTargetNodeIDs)
	if err != nil {
		return AdmissionEventPayloadV2{}, err
	}
	allowed := make(map[string]struct{}, len(request.TargetNodeIDs))
	for _, target := range request.TargetNodeIDs {
		allowed[target] = struct{}{}
	}
	for _, nodeID := range bootstrapped {
		if _, ok := allowed[nodeID]; !ok {
			return AdmissionEventPayloadV2{}, fmt.Errorf("%w: bootstrapped node %q is not an event endpoint", ErrInvalidPayload, nodeID)
		}
	}
	if request.ExpectedRevision == math.MaxInt64 {
		return AdmissionEventPayloadV2{}, fmt.Errorf("%w: resulting revision overflows int64", ErrInvalidPayload)
	}
	requestHash, err := request.Hash()
	if err != nil {
		return AdmissionEventPayloadV2{}, err
	}
	decisionHash, err := decision.Hash()
	if err != nil {
		return AdmissionEventPayloadV2{}, err
	}
	return AdmissionEventPayloadV2{
		ContractVersion:           AdmissionEventContractVersionV2,
		Kind:                      AtomicReplacementEventKind,
		ProposalOccurrence:        request.ProposalOccurrence,
		ReplacementNodeID:         request.ReplacementNodeID,
		TargetNodeIDs:             append([]string(nil), request.TargetNodeIDs...),
		BootstrappedTargetNodeIDs: bootstrapped,
		LineageBasis:              request.LineageBasis,
		LineageKey:                request.LineageKey,
		PreviousRevision:          request.ExpectedRevision,
		PreviousEventID:           request.ExpectedHeadEvent,
		Revision:                  request.ExpectedRevision + 1,
		AdmissionDecisionID:       admissionDecisionID,
		RequestPayloadHash:        requestHash,
		DecisionPayloadHash:       decisionHash,
	}, nil
}

// Validate verifies that AdmissionEventPayloadV2 is already canonical and
// internally consistent. It cannot authenticate the reviewer or source.
func (p AdmissionEventPayloadV2) Validate() error {
	if p.ContractVersion != AdmissionEventContractVersionV2 || p.Kind != AtomicReplacementEventKind {
		return fmt.Errorf("%w: event contract or kind is invalid", ErrInvalidPayload)
	}
	if err := validateRecordID("proposal_occurrence_id", p.ProposalOccurrence, "occ:"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateRecordID("replacement_node_id", p.ReplacementNodeID, "canon-node:"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := p.LineageBasis.Validate(); err != nil {
		return err
	}
	lineageKey, err := p.LineageBasis.Key()
	if err != nil {
		return err
	}
	if p.LineageKey != lineageKey {
		return fmt.Errorf("%w: event lineage key does not match its basis", ErrInvalidPayload)
	}
	targets, err := NormalizeTargets(p.TargetNodeIDs)
	if err != nil {
		return err
	}
	if !slices.Equal(p.TargetNodeIDs, targets) || slices.Contains(targets, p.ReplacementNodeID) {
		return fmt.Errorf("%w: event targets are not canonical", ErrInvalidPayload)
	}
	bootstrapped, err := normalizeBootstrappedTargets(p.BootstrappedTargetNodeIDs)
	if err != nil {
		return err
	}
	if !slices.Equal(p.BootstrappedTargetNodeIDs, bootstrapped) {
		return fmt.Errorf("%w: event bootstrapped targets are not canonical", ErrInvalidPayload)
	}
	allowed := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		allowed[target] = struct{}{}
	}
	for _, nodeID := range bootstrapped {
		if _, ok := allowed[nodeID]; !ok {
			return fmt.Errorf("%w: bootstrapped node %q is not an event endpoint", ErrInvalidPayload, nodeID)
		}
	}
	if err := validateExpectedHead(p.PreviousRevision, p.PreviousEventID); err != nil {
		return err
	}
	if p.PreviousRevision == math.MaxInt64 || p.Revision != p.PreviousRevision+1 {
		return fmt.Errorf("%w: event revision does not immediately follow its previous revision", ErrInvalidPayload)
	}
	if err := validateRecordID("admission_decision_id", p.AdmissionDecisionID, "adm:"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if !validHash(p.RequestPayloadHash) || !validHash(p.DecisionPayloadHash) {
		return fmt.Errorf("%w: request and decision hashes must be complete SHA-256 values", ErrInvalidPayload)
	}
	request, err := NewRequestPayloadV2(
		p.ProposalOccurrence,
		p.ReplacementNodeID,
		p.LineageBasis,
		p.TargetNodeIDs,
		p.PreviousRevision,
		p.PreviousEventID,
	)
	if err != nil {
		return err
	}
	requestHash, err := request.Hash()
	if err != nil {
		return err
	}
	if requestHash != p.RequestPayloadHash {
		return fmt.Errorf("%w: request payload hash does not match event fields", ErrInvalidPayload)
	}
	return nil
}

// ID returns the v2 event ID. The complete canonical event payload is the
// hash preimage; changing any event field changes the ID.
func (p AdmissionEventPayloadV2) ID() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	digest, err := hashJSON(p)
	if err != nil {
		return "", fmt.Errorf("hashing admission event: %w", err)
	}
	return admissionEventIDPrefix + digest, nil
}

func normalizeBootstrappedTargets(values []string) ([]string, error) {
	if len(values) > MaxReplacementTargets {
		return nil, fmt.Errorf("%w: bootstrapped target count exceeds %d", ErrInvalidPayload, MaxReplacementTargets)
	}
	nodes := make([]string, len(values))
	for index, value := range values {
		nodes[index] = strings.TrimSpace(value)
		if err := validateRecordID("bootstrapped_node_id", nodes[index], "canon-node:"); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}
	slices.Sort(nodes)
	for index := 1; index < len(nodes); index++ {
		if nodes[index-1] == nodes[index] {
			return nil, fmt.Errorf("%w: duplicate bootstrapped node %q", ErrInvalidPayload, nodes[index])
		}
	}
	return nodes, nil
}

func validateExpectedHead(revision int64, eventID string) error {
	if revision < 0 {
		return fmt.Errorf("%w: expected revision cannot be negative", ErrInvalidPayload)
	}
	if revision == 0 {
		if eventID != "" {
			return fmt.Errorf("%w: revision zero requires an empty head event", ErrInvalidPayload)
		}
		return nil
	}
	if !validAdmissionEventID(eventID) {
		return fmt.Errorf("%w: positive revision requires a v2 admission event head", ErrInvalidPayload)
	}
	return nil
}

func validateCanonicalString(name, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have surrounding whitespace", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	return nil
}

func validateRecordID(name, value, prefix string) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return fmt.Errorf("%s must be a canonical %s ID", name, strings.TrimSuffix(prefix, ":"))
	}
	return nil
}

func isSourceSystemToken(value string) bool {
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return value != ""
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validHash(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && value == strings.ToLower(value)
}

func validAdmissionEventID(value string) bool {
	if !strings.HasPrefix(value, admissionEventIDPrefix) {
		return false
	}
	return validHash(strings.TrimPrefix(value, admissionEventIDPrefix))
}
