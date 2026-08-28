package evidencesupersession

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
)

const (
	// ClosureCutContractVersionV1 identifies the runtime v2 admission-backed
	// lineage cut used by the currentness projection.
	ClosureCutContractVersionV1 = "supersession-closure-cut/v1"
	// ClosureWitnessContractVersionV1 identifies an immutable, snapshot-bound
	// binding for complete admitted-object coverage of the selected lineage.
	ClosureWitnessContractVersionV1 = "supersession-closure-witness/v1"
	// CurrentnessAlgorithmVersionV1 identifies the incoming-edge/frontier
	// projection implemented by this package.
	CurrentnessAlgorithmVersionV1 = "supersession-currentness/v1"
	// CurrentnessSemanticsV1 limits currentness to one read-consistent admitted
	// graph snapshot. It does not claim provider freshness or global truth.
	CurrentnessSemanticsV1 = "current_in_admitted_graph_snapshot/v1"
	// ObjectCoveragePolicyV1 requires every admitted external source_claim for
	// the source object to have a reviewed lineage assignment. This conservative
	// guard prevents migration-40 lazy membership from being mistaken for
	// complete lineage coverage.
	ObjectCoveragePolicyV1 = "classified_admitted_source_object_claims/v1"

	// MaxClosureEvents bounds an exact global admission-chain read. Exceeding a
	// bound is an error; a closure cut is never silently truncated.
	MaxClosureEvents = 10_000
	// MaxClosureMembers bounds one lineage membership manifest.
	MaxClosureMembers = 10_000
	// MaxClosureEdges bounds one lineage supersedes-edge manifest.
	MaxClosureEdges = 100_000
	// MaxClosureObjectClaims bounds the conservative source-object manifest.
	MaxClosureObjectClaims = 50_000
)

var (
	// ErrInvalidClosureCut means the supplied runtime records do not form one
	// exact, replayable, acyclic supersession cut.
	ErrInvalidClosureCut = errors.New("invalid supersession closure cut")
	// ErrClosureLimitExceeded means an authoritative read exceeded a hard
	// closure bound and therefore cannot produce a complete projection.
	ErrClosureLimitExceeded = errors.New("supersession closure limit exceeded")
)

// CurrentnessStatus is a derived supersession status. It is deliberately
// separate from evidencegraph.TemporalStatus and is never written to a node.
type CurrentnessStatus string

const (
	CurrentnessUnknown    CurrentnessStatus = "unknown"
	CurrentnessCurrent    CurrentnessStatus = "current"
	CurrentnessSuperseded CurrentnessStatus = "superseded"
	CurrentnessAmbiguous  CurrentnessStatus = "ambiguous"
)

// ClosureHead identifies the exact runtime v2 supersession admission head
// observed by the read transaction.
type ClosureHead struct {
	ChainKey    string `json:"chain_key"`
	Revision    int64  `json:"revision"`
	HeadEventID string `json:"head_event_id"`
}

// ClosureEventRecord binds one persisted event row, its decision audit fields,
// and its canonical event payload. Records for every global revision are
// required so a filtered or gapped database view fails closed.
type ClosureEventRecord struct {
	ID                  string                  `json:"event_id"`
	ContractVersion     string                  `json:"contract_version"`
	Revision            int64                   `json:"revision"`
	PreviousRevision    int64                   `json:"previous_revision"`
	PreviousEventID     string                  `json:"previous_event_id,omitempty"`
	Kind                string                  `json:"event_kind"`
	LineageKey          string                  `json:"lineage_key"`
	ReplacementNodeID   string                  `json:"replacement_node_id"`
	ProposalOccurrence  string                  `json:"proposal_occurrence_id"`
	AdmissionDecisionID string                  `json:"admission_decision_id"`
	AdmissionOutcome    string                  `json:"admission_outcome"`
	DecisionBy          string                  `json:"decision_by"`
	DecisionReason      string                  `json:"decision_reason"`
	RequestPayloadHash  string                  `json:"request_payload_hash"`
	DecisionPayloadHash string                  `json:"decision_payload_hash"`
	EventPayloadHash    string                  `json:"event_payload_hash"`
	Payload             AdmissionEventPayloadV2 `json:"event_payload"`
}

// ClosureMemberRecord is one persisted member of the selected lineage.
type ClosureMemberRecord struct {
	NodeID                string `json:"node_id"`
	LineageKey            string `json:"lineage_key"`
	FirstAdmissionEventID string `json:"first_admission_event_id"`
	WasBootstrapped       bool   `json:"was_bootstrapped"`
}

// ClosureEdgeRecord binds one materialized supersedes edge to the event and
// audit fields that authorized it.
type ClosureEdgeRecord struct {
	ID                         string `json:"edge_id"`
	LineageKey                 string `json:"lineage_key"`
	EventID                    string `json:"event_id"`
	FromNodeID                 string `json:"from_node_id"`
	ToNodeID                   string `json:"to_node_id"`
	Relation                   string `json:"relation"`
	OriginProposalOccurrenceID string `json:"origin_proposal_occurrence_id"`
	TraceRef                   string `json:"trace_ref"`
	ReviewRef                  string `json:"review_ref"`
	Method                     string `json:"method"`
	MethodVersion              string `json:"method_version"`
}

// ObjectClaimClassification records the lineage assignment visible for one
// admitted external source_claim in the selected source object. An empty
// LineageKey is an explicit unclassified claim and prevents closure.
type ObjectClaimClassification struct {
	NodeID     string `json:"node_id"`
	LineageKey string `json:"lineage_key,omitempty"`
}

// ClosureCutInput is populated only by an unfiltered authoritative loader. It
// intentionally has no caller-provided completeness, hash, or winner field.
type ClosureCutInput struct {
	LineageKey   string                      `json:"lineage_key"`
	Basis        Basis                       `json:"basis"`
	Head         ClosureHead                 `json:"head"`
	Events       []ClosureEventRecord        `json:"events"`
	Members      []ClosureMemberRecord       `json:"members"`
	Edges        []ClosureEdgeRecord         `json:"edges"`
	ObjectClaims []ObjectClaimClassification `json:"object_claims"`
}

// NodeCurrentness contains one node's derived status and the exact incoming
// supersedes edges that positively prove supersession.
type NodeCurrentness struct {
	NodeID          string            `json:"node_id"`
	Status          CurrentnessStatus `json:"status"`
	IncomingEdgeIDs []string          `json:"incoming_edge_ids"`
}

// ClosureWitness is a deterministic, snapshot-bound closure binding. It is not
// persisted, authenticated, or valid for a later database snapshot without a
// fresh authoritative read.
type ClosureWitness struct {
	ContractVersion         string      `json:"contract_version"`
	ID                      string      `json:"closure_witness_id"`
	AlgorithmVersion        string      `json:"algorithm_version"`
	Semantics               string      `json:"semantics"`
	CoveragePolicy          string      `json:"coverage_policy"`
	LineageKey              string      `json:"lineage_key"`
	Basis                   Basis       `json:"basis"`
	Head                    ClosureHead `json:"head"`
	HistoryHash             string      `json:"history_hash"`
	CutHash                 string      `json:"cut_hash"`
	ObjectClaimManifestHash string      `json:"object_claim_manifest_hash"`
	NodeIDs                 []string    `json:"node_ids"`
	EdgeIDs                 []string    `json:"edge_ids"`
	FrontierNodeIDs         []string    `json:"frontier_node_ids"`
}

// CurrentnessProjection is the deterministic read result for one lineage.
// ClosureAvailable is false when the graph is valid but source-object claims
// remain unclassified; in that case frontiers stay unknown.
type CurrentnessProjection struct {
	ContractVersion         string            `json:"contract_version"`
	AlgorithmVersion        string            `json:"algorithm_version"`
	Semantics               string            `json:"semantics"`
	CoveragePolicy          string            `json:"coverage_policy"`
	LineageKey              string            `json:"lineage_key"`
	Head                    ClosureHead       `json:"head"`
	HistoryHash             string            `json:"history_hash"`
	CutHash                 string            `json:"cut_hash"`
	ObjectClaimManifestHash string            `json:"object_claim_manifest_hash"`
	ClosureAvailable        bool              `json:"closure_available"`
	FrontierNodeIDs         []string          `json:"frontier_node_ids"`
	Nodes                   []NodeCurrentness `json:"nodes"`
	Witness                 *ClosureWitness   `json:"witness,omitempty"`
}

// ProjectCurrentness validates the complete runtime v2 history and selected
// lineage materialization, then derives currentness. A valid but incomplete
// object classification returns a projection without closure rather than an
// error. Integrity, history, topology, or manifest mismatches fail closed.
func ProjectCurrentness(input ClosureCutInput) (CurrentnessProjection, error) {
	cut, err := validateAndCanonicalizeClosureCut(input)
	if err != nil {
		return CurrentnessProjection{}, err
	}
	projection := buildCurrentnessProjection(cut)
	if !cut.coverageComplete {
		return projection, nil
	}

	for index := range projection.Nodes {
		if projection.Nodes[index].Status != CurrentnessUnknown {
			continue
		}
		if len(projection.FrontierNodeIDs) == 1 {
			projection.Nodes[index].Status = CurrentnessCurrent
		} else {
			projection.Nodes[index].Status = CurrentnessAmbiguous
		}
	}
	witness, err := newClosureWitness(cut, projection.FrontierNodeIDs)
	if err != nil {
		return CurrentnessProjection{}, err
	}
	projection.ClosureAvailable = true
	projection.Witness = witness
	return projection, nil
}

// ValidateCurrentnessProjection recomputes the complete projection and rejects
// any stale or modified result.
func ValidateCurrentnessProjection(input ClosureCutInput, got CurrentnessProjection) error {
	want, err := ProjectCurrentness(input)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%w: currentness projection does not match its cut", ErrInvalidClosureCut)
	}
	return nil
}

type canonicalClosureCut struct {
	lineageKey              string
	basis                   Basis
	head                    ClosureHead
	events                  []ClosureEventRecord
	members                 []ClosureMemberRecord
	edges                   []ClosureEdgeRecord
	objectClaims            []ObjectClaimClassification
	historyHash             string
	cutHash                 string
	objectClaimManifestHash string
	coverageComplete        bool
}

func buildCurrentnessProjection(cut canonicalClosureCut) CurrentnessProjection {
	incoming := make(map[string][]string, len(cut.members))
	for _, member := range cut.members {
		incoming[member.NodeID] = []string{}
	}
	for _, edge := range cut.edges {
		incoming[edge.ToNodeID] = append(incoming[edge.ToNodeID], edge.ID)
	}
	frontiers := make([]string, 0, len(cut.members))
	nodes := make([]NodeCurrentness, 0, len(cut.members))
	for _, member := range cut.members {
		witnesses := append([]string(nil), incoming[member.NodeID]...)
		slices.Sort(witnesses)
		status := CurrentnessUnknown
		if len(witnesses) > 0 {
			status = CurrentnessSuperseded
		} else {
			frontiers = append(frontiers, member.NodeID)
		}
		nodes = append(nodes, NodeCurrentness{
			NodeID:          member.NodeID,
			Status:          status,
			IncomingEdgeIDs: witnesses,
		})
	}
	return CurrentnessProjection{
		ContractVersion:         ClosureCutContractVersionV1,
		AlgorithmVersion:        CurrentnessAlgorithmVersionV1,
		Semantics:               CurrentnessSemanticsV1,
		CoveragePolicy:          ObjectCoveragePolicyV1,
		LineageKey:              cut.lineageKey,
		Head:                    cut.head,
		HistoryHash:             cut.historyHash,
		CutHash:                 cut.cutHash,
		ObjectClaimManifestHash: cut.objectClaimManifestHash,
		FrontierNodeIDs:         frontiers,
		Nodes:                   nodes,
	}
}

func newClosureWitness(cut canonicalClosureCut, frontiers []string) (*ClosureWitness, error) {
	nodeIDs := make([]string, 0, len(cut.members))
	for _, member := range cut.members {
		nodeIDs = append(nodeIDs, member.NodeID)
	}
	edgeIDs := make([]string, 0, len(cut.edges))
	for _, edge := range cut.edges {
		edgeIDs = append(edgeIDs, edge.ID)
	}
	witness := &ClosureWitness{
		ContractVersion:         ClosureWitnessContractVersionV1,
		AlgorithmVersion:        CurrentnessAlgorithmVersionV1,
		Semantics:               CurrentnessSemanticsV1,
		CoveragePolicy:          ObjectCoveragePolicyV1,
		LineageKey:              cut.lineageKey,
		Basis:                   cut.basis,
		Head:                    cut.head,
		HistoryHash:             cut.historyHash,
		CutHash:                 cut.cutHash,
		ObjectClaimManifestHash: cut.objectClaimManifestHash,
		NodeIDs:                 nodeIDs,
		EdgeIDs:                 edgeIDs,
		FrontierNodeIDs:         append([]string(nil), frontiers...),
	}
	payload := *witness
	payload.ID = ""
	digest, err := hashJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("hashing supersession closure witness: %w", err)
	}
	witness.ID = "closure-witness:v1:" + digest
	return witness, nil
}
