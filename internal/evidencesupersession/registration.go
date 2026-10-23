package evidencesupersession

import (
	"fmt"
)

const (
	// InitialRegistrationContractV1 identifies an independent first-membership decision.
	InitialRegistrationContractV1 = "core-initial-lineage-registration/v1"
	// MaxRegistrationEvents bounds the complete, store-wide registration catalog.
	MaxRegistrationEvents = 10_000
	// MaxRegistrationCatalogBytes bounds the canonical JSON array of registration records.
	MaxRegistrationCatalogBytes = 16 << 20
	registrationIDPrefix        = "initial-lineage-registration:v1:"
)

// RegistrationAnchor places a registration after an exact v2 prefix. A zero
// revision has a nil event ID, serialized as JSON null rather than an empty ID.
type RegistrationAnchor struct {
	ChainKey    string  `json:"chain_key"`
	Revision    int64   `json:"revision"`
	HeadEventID *string `json:"head_event_id"`
}

func (a RegistrationAnchor) validate() error {
	if a.ChainKey != ChainKey || (a.Revision == 0) != (a.HeadEventID == nil) {
		return fmt.Errorf("%w: invalid registration anchor", ErrInvalidPayload)
	}
	id := ""
	if a.HeadEventID != nil {
		id = *a.HeadEventID
	}
	return validateExpectedHead(a.Revision, id)
}

// RegistrationReviewBinding binds immutable native evidence and the exact
// creation-time review. Hashes refer to saved native materials, not caller
// assertions of completeness. The native loader must authenticate their joins;
// domain hashing alone does not authenticate a source, receipt or reviewer.
type RegistrationReviewBinding struct {
	StoreID                    string             `json:"store_id"`
	ClaimID                    string             `json:"claim_id"`
	OriginalAdmissionID        string             `json:"original_admission_id"`
	OriginalOccurrenceID       string             `json:"original_occurrence_id"`
	SnapshotID                 string             `json:"snapshot_id"`
	ViewID                     string             `json:"view_id"`
	GroundingHash              string             `json:"grounding_hash"`
	ObservationReceiptID       string             `json:"observation_receipt_id"`
	ObservationHash            string             `json:"observation_hash"`
	Basis                      Basis              `json:"lineage_basis"`
	LineageKey                 string             `json:"lineage_key"`
	Anchor                     RegistrationAnchor `json:"supersession_anchor"`
	CreationObjectManifestHash string             `json:"creation_object_manifest_hash"`
	GovernanceDigest           string             `json:"governance_digest"`
}

// SubjectID binds all immutable evidence and creation-time governance inputs.
func (b RegistrationReviewBinding) SubjectID() (string, error) {
	if err := b.validate(); err != nil {
		return "", err
	}
	payload := struct {
		Contract string                    `json:"contract_version"`
		Binding  RegistrationReviewBinding `json:"binding"`
	}{InitialRegistrationContractV1, b}
	h, err := hashJSON(payload)
	if err != nil {
		return "", err
	}
	return "initial-lineage-review:v1:" + h, nil
}

func (b RegistrationReviewBinding) validate() error {
	for _, f := range []struct{ name, value string }{
		{"store_id", b.StoreID}, {"snapshot_id", b.SnapshotID}, {"view_id", b.ViewID},
		{"observation_receipt_id", b.ObservationReceiptID},
	} {
		if err := validateCanonicalString(f.name, f.value, 200); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}
	for _, f := range []struct{ name, value, prefix string }{
		{"claim_id", b.ClaimID, "canon-node:"},
		{"original_admission_id", b.OriginalAdmissionID, "adm:"},
		{"original_occurrence_id", b.OriginalOccurrenceID, "occ:"},
	} {
		if err := validateRecordID(f.name, f.value, f.prefix); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}
	key, err := b.Basis.Key()
	if err != nil {
		return err
	}
	if b.LineageKey != key {
		return fmt.Errorf("%w: registration lineage/basis mismatch", ErrInvalidPayload)
	}
	if err := b.Anchor.validate(); err != nil {
		return err
	}
	if !validHash(b.GroundingHash) || !validHash(b.ObservationHash) || !validHash(b.GovernanceDigest) {
		return fmt.Errorf("%w: invalid registration evidence hash", ErrInvalidPayload)
	}
	// The creation manifest has exactly one unclassified claim. Later reads use
	// their current manifest; historical validity never re-applies this condition.
	want, err := closureObjectManifestHash(b.Basis, []ObjectClaimClassification{{NodeID: b.ClaimID}})
	if err != nil {
		return err
	}
	if b.CreationObjectManifestHash != want {
		return fmt.Errorf("%w: registration creation manifest is not singleton/unclassified", ErrInvalidPayload)
	}
	return nil
}

// InitialRegistrationPayload is the append-only decision preimage. It does not
// advance a v2 head and does not replace or re-admit the original claim.
type InitialRegistrationPayload struct {
	ContractVersion string                    `json:"contract_version"`
	Binding         RegistrationReviewBinding `json:"binding"`
	ReviewSubjectID string                    `json:"review_subject_id"`
	RequestID       string                    `json:"request_id"`
	Reviewer        string                    `json:"reviewer"`
	Reason          string                    `json:"reason"`
}

// Validate checks canonical structure and exact review binding. Historical
// validation deliberately does not inspect current withdrawal or current heads.
func (p InitialRegistrationPayload) Validate() error {
	if p.ContractVersion != InitialRegistrationContractV1 {
		return fmt.Errorf("%w: unknown registration contract", ErrInvalidPayload)
	}
	subject, err := p.Binding.SubjectID()
	if err != nil {
		return err
	}
	if p.ReviewSubjectID != subject {
		return fmt.Errorf("%w: registration review subject mismatch", ErrInvalidPayload)
	}
	for _, f := range []struct {
		name, value string
		limit       int
	}{
		{"request_id", p.RequestID, 200}, {"reviewer", p.Reviewer, maxDecisionByBytes},
		{"reason", p.Reason, maxDecisionReasonBytes},
	} {
		if err := validateCanonicalString(f.name, f.value, f.limit); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}
	return nil
}

// ID returns the complete versioned decision identity.
func (p InitialRegistrationPayload) ID() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	h, err := hashJSON(p)
	if err != nil {
		return "", err
	}
	return registrationIDPrefix + h, nil
}

// RegistrationRecord is one native authority with its complete hash preimage.
type RegistrationRecord struct {
	ID      string                     `json:"event_id"`
	Payload InitialRegistrationPayload `json:"payload"`
}

func (r RegistrationRecord) validate() error {
	id, err := r.Payload.ID()
	if err != nil {
		return err
	}
	if r.ID != id {
		return fmt.Errorf("%w: registration event identity mismatch", ErrInvalidPayload)
	}
	return nil
}

// ValidateRegistrationCatalogCapacity checks post-append usage. Bytes count the
// canonical JSON array including brackets and commas. Native apply must compute
// this under the shared lock, before any insert, using unfiltered stored rows.
func ValidateRegistrationCatalogCapacity(count, size int64) error {
	if count < 0 || size < 2 {
		return fmt.Errorf("%w: invalid registration catalog usage", ErrInvalidPayload)
	}
	if count > MaxRegistrationEvents || size > MaxRegistrationCatalogBytes {
		return fmt.Errorf("%w: registration catalog capacity exceeded", ErrClosureLimitExceeded)
	}
	return nil
}

// MembershipOrigin identifies the sole authority that first enrolled a claim.
type MembershipOrigin struct {
	Kind    MembershipOriginKind `json:"kind"`
	EventID string               `json:"event_id"`
}

// MembershipOriginKind distinguishes enrollment authority from claim admission.
type MembershipOriginKind string

const (
	MembershipRegistration MembershipOriginKind = "registration"
	MembershipReplacement  MembershipOriginKind = "replacement"
	MembershipBootstrap    MembershipOriginKind = "bootstrap"
)

// JointMemberRecord is the materialized membership with its typed first origin.
type JointMemberRecord struct {
	NodeID     string           `json:"node_id"`
	LineageKey string           `json:"lineage_key"`
	Origin     MembershipOrigin `json:"origin"`
}

func (m JointMemberRecord) legacy() ClosureMemberRecord {
	return ClosureMemberRecord{NodeID: m.NodeID, LineageKey: m.LineageKey,
		FirstAdmissionEventID: m.Origin.EventID, WasBootstrapped: m.Origin.Kind == MembershipBootstrap}
}

// RegistrationCreationManifestHash binds the singleton unclassified creation
// manifest. Computing it does not prove that a database manifest is complete.
func RegistrationCreationManifestHash(basis Basis, claimID string) (string, error) {
	if err := basis.Validate(); err != nil {
		return "", err
	}
	if err := validateRecordID("claim_id", claimID, "canon-node:"); err != nil {
		return "", err
	}
	return closureObjectManifestHash(basis, []ObjectClaimClassification{{NodeID: claimID}})
}
