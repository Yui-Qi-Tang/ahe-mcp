package evidencegraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

const CanonicalSchemaVersion = "canonical-evidence-graph/v1"

// CanonicalNodeKind separates directly observed evidence from transformed or
// task-candidate claims. The legacy Graph remains the runtime policy surface.
type CanonicalNodeKind string

const (
	CanonicalRawEvidence  CanonicalNodeKind = "raw_evidence"
	CanonicalSourceClaim  CanonicalNodeKind = "source_claim"
	CanonicalDerivedClaim CanonicalNodeKind = "derived_claim"
	CanonicalCandidate    CanonicalNodeKind = "candidate"
)

// CanonicalEdgeRelation contains only source-backed or explicitly derived graph
// relations. Task requirements do not belong in this relation set.
type CanonicalEdgeRelation string

const (
	CanonicalSupportsClaim CanonicalEdgeRelation = "supports_claim"
	CanonicalContradicts   CanonicalEdgeRelation = "contradicts"
	CanonicalReferences    CanonicalEdgeRelation = "references"
	CanonicalSupersedes    CanonicalEdgeRelation = "supersedes"
	CanonicalImplements    CanonicalEdgeRelation = "implements"
	CanonicalDerivedFrom   CanonicalEdgeRelation = "derived_from"
)

type TemporalStatus string

const (
	TemporalUnknown    TemporalStatus = "unknown"
	TemporalCurrent    TemporalStatus = "current"
	TemporalStale      TemporalStatus = "stale"
	TemporalSuperseded TemporalStatus = "superseded"
)

// CanonicalArtifact is the additive vNext research representation. It is not
// consumed by Evidence Policy or the runtime EvidenceGraph interface.
type CanonicalArtifact struct {
	SchemaVersion string             `json:"schema_version"`
	SnapshotID    string             `json:"snapshot_id"`
	Nodes         []CanonicalNode    `json:"nodes"`
	Edges         []CanonicalEdge    `json:"edges"`
	Payloads      []EvidencePayload  `json:"payloads"`
	Provenance    []ProvenanceRecord `json:"provenance"`
	Temporal      []TemporalRecord   `json:"temporal"`
	Integrity     []IntegrityRecord  `json:"integrity"`
	Derivations   []DerivationRecord `json:"derivations,omitempty"`
}

type CanonicalNode struct {
	ID            string            `json:"id"`
	Kind          CanonicalNodeKind `json:"kind"`
	PayloadRef    string            `json:"payload_ref"`
	ProvenanceRef string            `json:"provenance_ref"`
	TemporalRef   string            `json:"temporal_ref"`
	IntegrityRef  string            `json:"integrity_ref"`
}

type CanonicalEdge struct {
	ID            string                `json:"id"`
	From          string                `json:"from"`
	To            string                `json:"to"`
	Relation      CanonicalEdgeRelation `json:"relation"`
	ProvenanceRef string                `json:"provenance_ref"`
}

type EvidencePayload struct {
	ID            string         `json:"id"`
	SourceType    string         `json:"source_type"`
	Title         string         `json:"title"`
	Source        string         `json:"source"`
	Span          string         `json:"span,omitempty"`
	SpanLocator   string         `json:"span_locator,omitempty"`
	Claim         string         `json:"claim,omitempty"`
	Applicability string         `json:"applicability,omitempty"`
	TargetAnchors []TargetAnchor `json:"target_anchors,omitempty"`
}

// ProvenanceRecord groups evidence that ultimately comes from the same
// origin. Multiple slices of one document therefore do not count as
// independent sources.
type ProvenanceRecord struct {
	ID            string   `json:"id"`
	OriginRefs    []string `json:"origin_refs"`
	OriginGroupID string   `json:"origin_group_id"`
	Producer      string   `json:"producer"`
	Method        string   `json:"method"`
	MethodVersion string   `json:"method_version"`
	TraceRef      string   `json:"trace_ref"`
	ReviewRef     string   `json:"review_ref,omitempty"`
}

type TemporalRecord struct {
	ID         string         `json:"id"`
	Status     TemporalStatus `json:"status"`
	ObservedAt string         `json:"observed_at,omitempty"`
	ValidFrom  string         `json:"valid_from,omitempty"`
	ValidTo    string         `json:"valid_to,omitempty"`
}

type IntegrityRecord struct {
	ID        string `json:"id"`
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

// DerivationRecord records AND-dependencies. Every listed parent is required;
// this is deliberately stronger than a loose collection of binary edges.
type DerivationRecord struct {
	ID            string   `json:"id"`
	NodeID        string   `json:"node_id"`
	Parents       []string `json:"parents"`
	Method        string   `json:"method"`
	Producer      string   `json:"producer"`
	TraceRef      string   `json:"trace_ref"`
	ProvenanceRef string   `json:"provenance_ref"`
}

func CanonicalNodeKinds() []CanonicalNodeKind {
	return []CanonicalNodeKind{
		CanonicalRawEvidence,
		CanonicalSourceClaim,
		CanonicalDerivedClaim,
		CanonicalCandidate,
	}
}

func CanonicalRelations() []CanonicalEdgeRelation {
	return []CanonicalEdgeRelation{
		CanonicalSupportsClaim,
		CanonicalContradicts,
		CanonicalReferences,
		CanonicalSupersedes,
		CanonicalImplements,
		CanonicalDerivedFrom,
	}
}

func TemporalStatuses() []TemporalStatus {
	return []TemporalStatus{
		TemporalUnknown,
		TemporalCurrent,
		TemporalStale,
		TemporalSuperseded,
	}
}

// StableCanonicalID returns a deterministic typed identifier.
func StableCanonicalID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(strings.TrimSpace(part)))
		_, _ = h.Write([]byte{0})
	}
	return strings.TrimSpace(prefix) + ":" + hex.EncodeToString(h.Sum(nil))[:16]
}

// PayloadDigest returns the canonical SHA-256 digest used by integrity records.
func PayloadDigest(payload EvidencePayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (a CanonicalArtifact) Validate() error {
	if a.SchemaVersion != CanonicalSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CanonicalSchemaVersion)
	}
	if strings.TrimSpace(a.SnapshotID) == "" {
		return fmt.Errorf("snapshot_id is required")
	}

	payloads, err := indexPayloads(a.Payloads)
	if err != nil {
		return err
	}
	provenance, err := indexProvenance(a.Provenance)
	if err != nil {
		return err
	}
	temporal, err := indexTemporal(a.Temporal)
	if err != nil {
		return err
	}
	integrity, err := indexIntegrity(a.Integrity)
	if err != nil {
		return err
	}

	nodes := make(map[string]CanonicalNode, len(a.Nodes))
	for i, node := range a.Nodes {
		if err := node.validate(payloads, provenance, temporal, integrity); err != nil {
			return fmt.Errorf("node %d: %w", i, err)
		}
		if _, exists := nodes[node.ID]; exists {
			return fmt.Errorf("duplicate node %q", node.ID)
		}
		nodes[node.ID] = node
	}

	edges := make(map[string]CanonicalEdge, len(a.Edges))
	derivedEdges := make(map[string]bool)
	for i, edge := range a.Edges {
		if err := edge.validate(nodes, provenance); err != nil {
			return fmt.Errorf("edge %d: %w", i, err)
		}
		if _, exists := edges[edge.ID]; exists {
			return fmt.Errorf("duplicate edge %q", edge.ID)
		}
		edges[edge.ID] = edge
		if edge.Relation == CanonicalDerivedFrom {
			derivedEdges[edge.From+"\x00"+edge.To] = true
		}
	}

	derivations := make(map[string]DerivationRecord, len(a.Derivations))
	derivedNodes := make(map[string]bool)
	derivationByNode := make(map[string]DerivationRecord, len(a.Derivations))
	for i, derivation := range a.Derivations {
		if err := derivation.validate(nodes, provenance, derivedEdges); err != nil {
			return fmt.Errorf("derivation %d: %w", i, err)
		}
		if _, exists := derivations[derivation.ID]; exists {
			return fmt.Errorf("duplicate derivation %q", derivation.ID)
		}
		if derivedNodes[derivation.NodeID] {
			return fmt.Errorf("node %q has multiple derivation records", derivation.NodeID)
		}
		derivations[derivation.ID] = derivation
		derivedNodes[derivation.NodeID] = true
		derivationByNode[derivation.NodeID] = derivation
	}
	for _, node := range a.Nodes {
		if node.Kind == CanonicalDerivedClaim || node.Kind == CanonicalCandidate {
			if !derivedNodes[node.ID] {
				return fmt.Errorf("node %q requires a derivation record", node.ID)
			}
		}
	}
	for _, edge := range a.Edges {
		if edge.Relation != CanonicalDerivedFrom {
			continue
		}
		derivation, ok := derivationByNode[edge.To]
		if !ok || !slices.Contains(derivation.Parents, edge.From) {
			return fmt.Errorf("derived_from edge %q is not declared by its target derivation", edge.ID)
		}
	}
	return nil
}

func (n CanonicalNode) validate(
	payloads map[string]EvidencePayload,
	provenance map[string]ProvenanceRecord,
	temporal map[string]TemporalRecord,
	integrity map[string]IntegrityRecord,
) error {
	if strings.TrimSpace(n.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if !slices.Contains(CanonicalNodeKinds(), n.Kind) {
		return fmt.Errorf("node %q has invalid kind %q", n.ID, n.Kind)
	}
	payload, ok := payloads[n.PayloadRef]
	if !ok {
		return fmt.Errorf("node %q payload_ref %q is not defined", n.ID, n.PayloadRef)
	}
	if _, ok := provenance[n.ProvenanceRef]; !ok {
		return fmt.Errorf("node %q provenance_ref %q is not defined", n.ID, n.ProvenanceRef)
	}
	if _, ok := temporal[n.TemporalRef]; !ok {
		return fmt.Errorf("node %q temporal_ref %q is not defined", n.ID, n.TemporalRef)
	}
	record, ok := integrity[n.IntegrityRef]
	if !ok {
		return fmt.Errorf("node %q integrity_ref %q is not defined", n.ID, n.IntegrityRef)
	}
	digest, err := PayloadDigest(payload)
	if err != nil {
		return fmt.Errorf("node %q computing payload digest: %w", n.ID, err)
	}
	if record.Digest != digest {
		return fmt.Errorf("node %q payload integrity mismatch", n.ID)
	}
	return nil
}

func (e CanonicalEdge) validate(nodes map[string]CanonicalNode, provenance map[string]ProvenanceRecord) error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if _, ok := nodes[e.From]; !ok {
		return fmt.Errorf("from node %q is not defined", e.From)
	}
	if _, ok := nodes[e.To]; !ok {
		return fmt.Errorf("to node %q is not defined", e.To)
	}
	if !slices.Contains(CanonicalRelations(), e.Relation) {
		return fmt.Errorf("invalid relation %q", e.Relation)
	}
	if _, ok := provenance[e.ProvenanceRef]; !ok {
		return fmt.Errorf("provenance_ref %q is not defined", e.ProvenanceRef)
	}
	return nil
}

func (d DerivationRecord) validate(
	nodes map[string]CanonicalNode,
	provenance map[string]ProvenanceRecord,
	derivedEdges map[string]bool,
) error {
	if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.NodeID) == "" {
		return fmt.Errorf("id and node_id are required")
	}
	node, ok := nodes[d.NodeID]
	if !ok {
		return fmt.Errorf("node %q is not defined", d.NodeID)
	}
	if node.Kind != CanonicalDerivedClaim && node.Kind != CanonicalCandidate {
		return fmt.Errorf("node %q kind %q cannot have a derivation", d.NodeID, node.Kind)
	}
	if len(d.Parents) == 0 {
		return fmt.Errorf("derivation %q requires at least one parent", d.ID)
	}
	seen := map[string]bool{}
	for _, parent := range d.Parents {
		if _, ok := nodes[parent]; !ok {
			return fmt.Errorf("parent node %q is not defined", parent)
		}
		if seen[parent] {
			return fmt.Errorf("duplicate parent %q", parent)
		}
		seen[parent] = true
		if !derivedEdges[parent+"\x00"+d.NodeID] {
			return fmt.Errorf("parent %q is missing derived_from edge to %q", parent, d.NodeID)
		}
	}
	if strings.TrimSpace(d.Method) == "" || strings.TrimSpace(d.Producer) == "" || strings.TrimSpace(d.TraceRef) == "" {
		return fmt.Errorf("derivation %q requires method, producer, and trace_ref", d.ID)
	}
	if _, ok := provenance[d.ProvenanceRef]; !ok {
		return fmt.Errorf("provenance_ref %q is not defined", d.ProvenanceRef)
	}
	return nil
}

func indexPayloads(items []EvidencePayload) (map[string]EvidencePayload, error) {
	index := make(map[string]EvidencePayload, len(items))
	for i, item := range items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.SourceType) == "" || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Source) == "" {
			return nil, fmt.Errorf("payload %d requires id, source_type, title, and source", i)
		}
		if _, exists := index[item.ID]; exists {
			return nil, fmt.Errorf("duplicate payload %q", item.ID)
		}
		anchors := map[string]bool{}
		for j, anchor := range item.TargetAnchors {
			if err := anchor.Validate(); err != nil {
				return nil, fmt.Errorf("payload %q target_anchor %d: %w", item.ID, j, err)
			}
			key := anchor.Kind + "\x00" + anchor.ID
			if anchors[key] {
				return nil, fmt.Errorf("payload %q duplicate target_anchor %q", item.ID, key)
			}
			anchors[key] = true
		}
		index[item.ID] = item
	}
	return index, nil
}

func indexProvenance(items []ProvenanceRecord) (map[string]ProvenanceRecord, error) {
	index := make(map[string]ProvenanceRecord, len(items))
	for i, item := range items {
		if strings.TrimSpace(item.ID) == "" || len(item.OriginRefs) == 0 || strings.TrimSpace(item.OriginGroupID) == "" ||
			strings.TrimSpace(item.Producer) == "" || strings.TrimSpace(item.Method) == "" || strings.TrimSpace(item.MethodVersion) == "" || strings.TrimSpace(item.TraceRef) == "" {
			return nil, fmt.Errorf("provenance %d requires id, origin_refs, origin_group_id, producer, method, method_version, and trace_ref", i)
		}
		for _, origin := range item.OriginRefs {
			if strings.TrimSpace(origin) == "" {
				return nil, fmt.Errorf("provenance %q contains an empty origin_ref", item.ID)
			}
		}
		if _, exists := index[item.ID]; exists {
			return nil, fmt.Errorf("duplicate provenance %q", item.ID)
		}
		index[item.ID] = item
	}
	return index, nil
}

func indexTemporal(items []TemporalRecord) (map[string]TemporalRecord, error) {
	index := make(map[string]TemporalRecord, len(items))
	for i, item := range items {
		if strings.TrimSpace(item.ID) == "" || !slices.Contains(TemporalStatuses(), item.Status) {
			return nil, fmt.Errorf("temporal %d requires id and a valid status", i)
		}
		for name, value := range map[string]string{"observed_at": item.ObservedAt, "valid_from": item.ValidFrom, "valid_to": item.ValidTo} {
			if value == "" {
				continue
			}
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return nil, fmt.Errorf("temporal %q %s must use RFC3339: %w", item.ID, name, err)
			}
		}
		if _, exists := index[item.ID]; exists {
			return nil, fmt.Errorf("duplicate temporal %q", item.ID)
		}
		index[item.ID] = item
	}
	return index, nil
}

func indexIntegrity(items []IntegrityRecord) (map[string]IntegrityRecord, error) {
	index := make(map[string]IntegrityRecord, len(items))
	for i, item := range items {
		if strings.TrimSpace(item.ID) == "" || item.Algorithm != "sha256" || len(item.Digest) != 64 {
			return nil, fmt.Errorf("integrity %d requires id, sha256 algorithm, and a 64-character digest", i)
		}
		if _, err := hex.DecodeString(item.Digest); err != nil {
			return nil, fmt.Errorf("integrity %q digest is not hexadecimal", item.ID)
		}
		if _, exists := index[item.ID]; exists {
			return nil, fmt.Errorf("duplicate integrity %q", item.ID)
		}
		index[item.ID] = item
	}
	return index, nil
}

func LoadCanonicalArtifact(path string) (CanonicalArtifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return CanonicalArtifact{}, fmt.Errorf("opening canonical evidence graph: %w", err)
	}
	defer file.Close()
	artifact, err := DecodeCanonicalArtifact(file)
	if err != nil {
		return CanonicalArtifact{}, fmt.Errorf("parsing canonical evidence graph: %w", err)
	}
	return artifact, nil
}

func DecodeCanonicalArtifact(r io.Reader) (CanonicalArtifact, error) {
	var artifact CanonicalArtifact
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return CanonicalArtifact{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return CanonicalArtifact{}, err
		}
		return CanonicalArtifact{}, fmt.Errorf("trailing JSON values")
	}
	if err := artifact.Validate(); err != nil {
		return CanonicalArtifact{}, err
	}
	return artifact, nil
}
