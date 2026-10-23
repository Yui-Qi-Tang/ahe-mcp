package evidencesupersession

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

const (
	// JointCutContractV1 identifies a complete cut of both native authorities.
	JointCutContractV1 = "core-joint-lineage-cut/v1"
	// JointWitnessContractV1 binds both authorities and the current object manifest.
	JointWitnessContractV1 = "core-joint-lineage-witness/v1"
)

// JointCutInput must come from one authoritative native snapshot. Empty catalogs
// are meaningful; the native loader, not this pure function, proves completeness.
type JointCutInput struct {
	ContractVersion string                      `json:"contract_version"`
	StoreID         string                      `json:"store_id"`
	LineageKey      string                      `json:"lineage_key"`
	Basis           Basis                       `json:"lineage_basis"`
	Head            ClosureHead                 `json:"supersession_head"`
	Events          []ClosureEventRecord        `json:"supersession_events"`
	Registrations   []RegistrationRecord        `json:"registrations"`
	Members         []JointMemberRecord         `json:"members"`
	Edges           []ClosureEdgeRecord         `json:"supersedes_edges"`
	ObjectClaims    []ObjectClaimClassification `json:"object_claims"`
}

// JointWitness is an unauthenticated deterministic binding, not a use permit.
// All manifests refer to the same current cut; creation evidence lives in R.
type JointWitness struct {
	ContractVersion          string      `json:"contract_version"`
	ID                       string      `json:"witness_id"`
	StoreID                  string      `json:"store_id"`
	LineageKey               string      `json:"lineage_key"`
	Basis                    Basis       `json:"lineage_basis"`
	AlgorithmVersion         string      `json:"algorithm_version"`
	Semantics                string      `json:"semantics_version"`
	CoveragePolicy           string      `json:"coverage_policy_version"`
	Head                     ClosureHead `json:"supersession_head"`
	HistoryHash              string      `json:"supersession_history_hash"`
	RegistrationManifestHash string      `json:"registration_manifest_hash"`
	MembershipManifestHash   string      `json:"membership_manifest_hash"`
	SupersedesManifestHash   string      `json:"supersedes_manifest_hash"`
	ObjectClaimManifestHash  string      `json:"object_claim_manifest_hash"`
	FrontierNodeIDs          []string    `json:"frontier_node_ids"`
}

// JointCurrentnessProjection separates complete closure from currentness. It
// carries no witness when the current object manifest remains unclassified.
type JointCurrentnessProjection struct {
	ContractVersion  string            `json:"contract_version"`
	StoreID          string            `json:"store_id"`
	LineageKey       string            `json:"lineage_key"`
	Head             ClosureHead       `json:"supersession_head"`
	CutHash          string            `json:"cut_hash"`
	ClosureAvailable bool              `json:"closure_available"`
	FrontierNodeIDs  []string          `json:"frontier_node_ids"`
	Nodes            []NodeCurrentness `json:"nodes"`
	Witness          *JointWitness     `json:"witness,omitempty"`
}

// JointAuthorityInput is the complete store-wide authority from one native cut.
// Its completeness and provenance must be supplied by the native loader.
type JointAuthorityInput struct {
	StoreID       string
	Head          ClosureHead
	Events        []ClosureEventRecord
	Registrations []RegistrationRecord
}

// JointLineageInput is one lineage's materialization from that same native cut.
type JointLineageInput struct {
	LineageKey   string
	Basis        Basis
	Members      []JointMemberRecord
	Edges        []ClosureEdgeRecord
	ObjectClaims []ObjectClaimClassification
}

// JointProjector owns a structurally verified, replayed authority frame. It is
// immutable after construction and retains no caller-owned slices or pointers.
// Native callers must keep it inside its owning transaction; this is neither a
// process cache nor proof that arbitrary caller-supplied records are authoritative.
type JointProjector struct {
	storeID          string
	head             ClosureHead
	historyHash      string
	registrationHash string
	replay           jointReplay
	legacyMembers    map[string]ClosureMemberRecord
}

// PrepareJointAuthority verifies and replays the full prefix/catalog once.
func PrepareJointAuthority(input JointAuthorityInput) (*JointProjector, error) {
	cut, err := canonicalJointAuthority(input)
	if err != nil {
		return nil, err
	}
	replay, err := replayJointMembership(cut)
	if err != nil {
		return nil, err
	}
	historyHash, err := closureHistoryHash(cut.Head, cut.Events)
	if err != nil {
		return nil, err
	}
	registrationHash, err := hashJSON(cut.Registrations)
	if err != nil {
		return nil, err
	}
	legacy := make(map[string]ClosureMemberRecord, len(replay.members))
	for id, m := range replay.members {
		legacy[id] = m.legacy()
	}
	return &JointProjector{storeID: cut.StoreID, head: cut.Head, historyHash: historyHash, registrationHash: registrationHash, replay: replay, legacyMembers: legacy}, nil
}

// ProjectJointCurrentness verifies a single complete joint cut. Batched native
// readers use PrepareJointAuthority and ProjectLineage with the same semantics.
func ProjectJointCurrentness(input JointCutInput) (JointCurrentnessProjection, error) {
	if input.ContractVersion != JointCutContractV1 {
		return JointCurrentnessProjection{}, fmt.Errorf("%w: unknown joint cut contract", ErrInvalidClosureCut)
	}
	authority, err := PrepareJointAuthority(JointAuthorityInput{StoreID: input.StoreID, Head: input.Head, Events: input.Events, Registrations: input.Registrations})
	if err != nil {
		return JointCurrentnessProjection{}, err
	}
	return authority.ProjectLineage(JointLineageInput{LineageKey: input.LineageKey, Basis: input.Basis, Members: input.Members, Edges: input.Edges, ObjectClaims: input.ObjectClaims})
}

// ProjectLineage checks the selected materialization and current object manifest
// against the prepared history before deriving its frontier and exact witness.
func (authority *JointProjector) ProjectLineage(input JointLineageInput) (JointCurrentnessProjection, error) {
	if authority == nil || authority.replay.members == nil {
		return JointCurrentnessProjection{}, fmt.Errorf("%w: unprepared joint authority", ErrInvalidClosureCut)
	}
	cut, err := canonicalJointLineage(input)
	if err != nil {
		return JointCurrentnessProjection{}, err
	}
	if err := validateJointMaterialization(cut, authority.replay); err != nil {
		return JointCurrentnessProjection{}, err
	}
	members := make([]ClosureMemberRecord, 0, len(cut.Members))
	for _, m := range cut.Members {
		members = append(members, m.legacy())
	}
	complete, err := validateObjectClaimCoverage(cut.LineageKey, members, authority.legacyMembers, cut.ObjectClaims)
	if err != nil {
		return JointCurrentnessProjection{}, err
	}
	if closureEdgesContainCycle(members, cut.Edges) {
		return JointCurrentnessProjection{}, fmt.Errorf("%w: joint supersedes cycle", ErrInvalidClosureCut)
	}
	projected := buildCurrentnessProjection(canonicalClosureCut{
		lineageKey: cut.LineageKey, basis: cut.Basis, head: authority.head, members: members, edges: cut.Edges,
	})
	// An absent lineage cannot obtain an empty-vacuous closure witness.
	complete = complete && len(members) > 0
	if complete {
		for i := range projected.Nodes {
			if projected.Nodes[i].Status == CurrentnessUnknown {
				projected.Nodes[i].Status = CurrentnessAmbiguous
				if len(projected.FrontierNodeIDs) == 1 {
					projected.Nodes[i].Status = CurrentnessCurrent
				}
			}
		}
	}
	objectHash, err := closureObjectManifestHash(cut.Basis, cut.ObjectClaims)
	if err != nil {
		return JointCurrentnessProjection{}, err
	}
	membershipHash, err := hashJSON(cut.Members)
	if err != nil {
		return JointCurrentnessProjection{}, err
	}
	edgesHash, err := hashJSON(cut.Edges)
	if err != nil {
		return JointCurrentnessProjection{}, err
	}
	witness := JointWitness{
		ContractVersion: JointWitnessContractV1, StoreID: authority.storeID, LineageKey: cut.LineageKey,
		Basis: cut.Basis, AlgorithmVersion: CurrentnessAlgorithmVersionV1,
		Semantics: CurrentnessSemanticsV1, CoveragePolicy: ObjectCoveragePolicyV1,
		Head: authority.head, HistoryHash: authority.historyHash, RegistrationManifestHash: authority.registrationHash,
		MembershipManifestHash: membershipHash, SupersedesManifestHash: edgesHash,
		ObjectClaimManifestHash: objectHash, FrontierNodeIDs: projected.FrontierNodeIDs,
	}
	// ID is blank in its own preimage. The cut includes all bound materials even
	// when incomplete and therefore has no witness.
	digest, err := hashJSON(witness)
	if err != nil {
		return JointCurrentnessProjection{}, err
	}
	result := JointCurrentnessProjection{
		ContractVersion: JointCutContractV1, StoreID: authority.storeID, LineageKey: cut.LineageKey,
		Head: authority.head, CutHash: "joint-lineage-cut:v1:" + digest, ClosureAvailable: complete,
		FrontierNodeIDs: projected.FrontierNodeIDs, Nodes: projected.Nodes,
	}
	if complete {
		witness.ID = "joint-lineage-witness:v1:" + digest
		result.Witness = &witness
	}
	return result, nil
}

// ValidateJointCurrentness rejects a modified or stale projection.
func ValidateJointCurrentness(input JointCutInput, result JointCurrentnessProjection) error {
	want, err := ProjectJointCurrentness(input)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, result) {
		return fmt.Errorf("%w: joint projection mismatch", ErrInvalidClosureCut)
	}
	return nil
}

func canonicalJointAuthority(input JointAuthorityInput) (JointAuthorityInput, error) {
	if err := validateCanonicalString("store_id", input.StoreID, 200); err != nil {
		return JointAuthorityInput{}, fmt.Errorf("%w: %v", ErrInvalidClosureCut, err)
	}
	if len(input.Events) > MaxClosureEvents || len(input.Registrations) > MaxRegistrationEvents {
		return JointAuthorityInput{}, fmt.Errorf("%w: joint cut count limit", ErrClosureLimitExceeded)
	}
	if input.Head.ChainKey != ChainKey || input.Head.Revision != int64(len(input.Events)) || validateExpectedHead(input.Head.Revision, input.Head.HeadEventID) != nil {
		return JointAuthorityInput{}, fmt.Errorf("%w: joint head/history mismatch", ErrInvalidClosureCut)
	}
	cut := input
	cut.Events = cloneClosureEvents(input.Events)
	slices.SortFunc(cut.Events, func(a, b ClosureEventRecord) int {
		if a.Revision < b.Revision {
			return -1
		}
		if a.Revision > b.Revision {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(cut.Events) > 0 {
		if err := validateClosureHistory(cut.Head, cut.Events); err != nil {
			return JointAuthorityInput{}, err
		}
	}
	cut.Registrations = append([]RegistrationRecord{}, input.Registrations...)
	slices.SortFunc(cut.Registrations, func(a, b RegistrationRecord) int { return strings.Compare(a.ID, b.ID) })
	requests := make(map[string]bool, len(cut.Registrations))
	catalogBytes := int64(2)
	for i, r := range cut.Registrations {
		if err := r.validate(); err != nil {
			return JointAuthorityInput{}, fmt.Errorf("%w: registration: %v", ErrInvalidClosureCut, err)
		}
		if i > 0 && cut.Registrations[i-1].ID == r.ID {
			return JointAuthorityInput{}, fmt.Errorf("%w: duplicate registration", ErrInvalidClosureCut)
		}
		b := r.Payload.Binding
		if b.StoreID != cut.StoreID || requests[r.Payload.RequestID] {
			return JointAuthorityInput{}, fmt.Errorf("%w: registration store/request mismatch", ErrInvalidClosureCut)
		}
		requests[r.Payload.RequestID] = true
		if b.Anchor.Revision > cut.Head.Revision {
			return JointAuthorityInput{}, fmt.Errorf("%w: registration anchor is beyond head", ErrInvalidClosureCut)
		}
		if b.Anchor.Revision > 0 && *b.Anchor.HeadEventID != cut.Events[b.Anchor.Revision-1].ID {
			return JointAuthorityInput{}, fmt.Errorf("%w: registration anchor does not match prefix", ErrInvalidClosureCut)
		}
		data, err := json.Marshal(r)
		if err != nil {
			return JointAuthorityInput{}, err
		}
		catalogBytes += int64(len(data))
		if i > 0 {
			catalogBytes++
		}
		if err := ValidateRegistrationCatalogCapacity(int64(i+1), catalogBytes); err != nil {
			return JointAuthorityInput{}, err
		}
	}
	return cut, nil
}

func canonicalJointLineage(input JointLineageInput) (JointLineageInput, error) {
	if len(input.Members) > MaxClosureMembers || len(input.Edges) > MaxClosureEdges || len(input.ObjectClaims) > MaxClosureObjectClaims {
		return JointLineageInput{}, fmt.Errorf("%w: joint cut count limit", ErrClosureLimitExceeded)
	}
	key, err := input.Basis.Key()
	if err != nil || key != input.LineageKey {
		return JointLineageInput{}, fmt.Errorf("%w: joint lineage basis mismatch", ErrInvalidClosureCut)
	}
	cut := input
	cut.Members = append([]JointMemberRecord{}, input.Members...)
	slices.SortFunc(cut.Members, func(a, b JointMemberRecord) int { return strings.Compare(a.NodeID, b.NodeID) })
	cut.Edges = append([]ClosureEdgeRecord{}, input.Edges...)
	slices.SortFunc(cut.Edges, func(a, b ClosureEdgeRecord) int { return strings.Compare(a.ID, b.ID) })
	cut.ObjectClaims = append([]ObjectClaimClassification{}, input.ObjectClaims...)
	slices.SortFunc(cut.ObjectClaims, func(a, b ObjectClaimClassification) int { return strings.Compare(a.NodeID, b.NodeID) })
	return cut, nil
}
