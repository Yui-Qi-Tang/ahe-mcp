// Package evidencegraph contains a small policy-visible edge-list graph and a
// separate envelope for policy-invisible extensions.
package evidencegraph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// EvidenceRelation is a policy-defined relation between evidence nodes.
type EvidenceRelation string

const (
	// Supports means evidence A strengthens evidence B.
	Supports EvidenceRelation = "supports"
	// References means evidence A points to evidence B.
	References EvidenceRelation = "references"
	// Implements means evidence B realizes evidence A.
	Implements EvidenceRelation = "implements"
	// Requires means evidence A is incomplete without evidence B.
	Requires EvidenceRelation = "requires"
	// ConflictsWith means evidence A and evidence B contain incompatible claims.
	ConflictsWith EvidenceRelation = "conflicts_with"
	// Supersedes means evidence A replaces evidence B.
	Supersedes EvidenceRelation = "supersedes"
)

// EvidenceNode is a graph vertex backed by an existing evidence identifier.
type EvidenceNode struct {
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

// TargetAnchor identifies a stable object that source-backed evidence points
// to. Kind is intentionally open so documents, code, infrastructure, and other
// domains can share the base shape without sharing domain-specific semantics.
type TargetAnchor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// EvidenceEdge is a directed, policy-defined relation between two evidence nodes.
type EvidenceEdge struct {
	From       string           `json:"from"`
	To         string           `json:"to"`
	Relation   EvidenceRelation `json:"relation"`
	Source     string           `json:"source"`
	Confidence float64          `json:"confidence"`
}

// EvidenceGraph is the minimal Phase 10 graph query surface.
type EvidenceGraph interface {
	Outgoing(id string) []EvidenceEdge
	Incoming(id string) []EvidenceEdge
	Related(id string, relation EvidenceRelation) []EvidenceNode
}

// Graph is an in-memory edge-list evidence graph.
type Graph struct {
	SchemaVersion string         `json:"schema_version"`
	Nodes         []EvidenceNode `json:"nodes"`
	Edges         []EvidenceEdge `json:"edges"`
}

// Load reads and validates an evidence graph JSON file.
func Load(path string) (Graph, error) {
	artifact, err := LoadArtifact(path)
	if err != nil {
		return Graph{}, err
	}
	return artifact.Graph, nil
}

// Validate checks that a graph is a bounded Phase 9 edge list.
func (g Graph) Validate() error {
	if strings.TrimSpace(g.SchemaVersion) == "" {
		return fmt.Errorf("schema_version is required")
	}
	nodes := map[string]bool{}
	for i, node := range g.Nodes {
		if strings.TrimSpace(node.ID) == "" {
			return fmt.Errorf("node %d id is required", i)
		}
		if nodes[node.ID] {
			return fmt.Errorf("duplicate node %q", node.ID)
		}
		nodes[node.ID] = true
		if strings.TrimSpace(node.SourceType) == "" {
			return fmt.Errorf("node %q source_type is required", node.ID)
		}
		if strings.TrimSpace(node.Title) == "" {
			return fmt.Errorf("node %q title is required", node.ID)
		}
		if strings.TrimSpace(node.Source) == "" {
			return fmt.Errorf("node %q source is required", node.ID)
		}
		anchors := map[string]bool{}
		for j, anchor := range node.TargetAnchors {
			if err := anchor.Validate(); err != nil {
				return fmt.Errorf("node %q target_anchor %d: %w", node.ID, j, err)
			}
			key := strings.TrimSpace(anchor.Kind) + "|" + strings.TrimSpace(anchor.ID)
			if anchors[key] {
				return fmt.Errorf("node %q duplicate target_anchor %q", node.ID, key)
			}
			anchors[key] = true
		}
	}
	edges := map[string]bool{}
	for i, edge := range g.Edges {
		if strings.TrimSpace(edge.From) == "" || strings.TrimSpace(edge.To) == "" {
			return fmt.Errorf("edge %d from and to are required", i)
		}
		if !nodes[edge.From] {
			return fmt.Errorf("edge %d from node %q is not defined", i, edge.From)
		}
		if !nodes[edge.To] {
			return fmt.Errorf("edge %d to node %q is not defined", i, edge.To)
		}
		if !ValidRelation(edge.Relation) {
			return fmt.Errorf("edge %d invalid relation %q", i, edge.Relation)
		}
		if strings.TrimSpace(edge.Source) == "" {
			return fmt.Errorf("edge %d source is required", i)
		}
		if edge.Confidence < 0 || edge.Confidence > 1 {
			return fmt.Errorf("edge %d confidence %.4f is outside [0,1]", i, edge.Confidence)
		}
		key := edgeKey(edge)
		if edges[key] {
			return fmt.Errorf("duplicate edge %q", key)
		}
		edges[key] = true
	}
	return nil
}

// Validate checks that a target anchor has a domain-neutral kind and identity.
func (a TargetAnchor) Validate() error {
	if strings.TrimSpace(a.Kind) == "" {
		return fmt.Errorf("kind is required")
	}
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("id is required")
	}
	return nil
}

// ValidRelation reports whether rel is allowed in Phase 9.
func ValidRelation(rel EvidenceRelation) bool {
	return slices.Contains(Relations(), rel)
}

// Relations returns the Phase 9 relation set.
func Relations() []EvidenceRelation {
	return []EvidenceRelation{
		Supports,
		References,
		Implements,
		Requires,
		ConflictsWith,
		Supersedes,
	}
}

// StableNodeID returns a deterministic ID for source-backed evidence nodes.
func StableNodeID(source, spanLocator, claim string) string {
	h := sha256.New()
	for _, part := range []string{source, spanLocator, claim} {
		_, _ = h.Write([]byte(strings.TrimSpace(part)))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return "eg:" + hex.EncodeToString(sum[:])[:16]
}

// Outgoing returns all edges that start from id.
func (g Graph) Outgoing(id string) []EvidenceEdge {
	var edges []EvidenceEdge
	for _, edge := range g.Edges {
		if edge.From == id {
			edges = append(edges, edge)
		}
	}
	return edges
}

// Incoming returns all edges that point to id.
func (g Graph) Incoming(id string) []EvidenceEdge {
	var edges []EvidenceEdge
	for _, edge := range g.Edges {
		if edge.To == id {
			edges = append(edges, edge)
		}
	}
	return edges
}

// Related returns outgoing relation targets for id.
func (g Graph) Related(id string, relation EvidenceRelation) []EvidenceNode {
	nodesByID := g.nodeIndex()
	var related []EvidenceNode
	for _, edge := range g.Edges {
		if edge.From != id || edge.Relation != relation {
			continue
		}
		node, ok := nodesByID[edge.To]
		if ok {
			related = append(related, node)
		}
	}
	return related
}

func (g Graph) nodeIndex() map[string]EvidenceNode {
	nodes := make(map[string]EvidenceNode, len(g.Nodes))
	for _, node := range g.Nodes {
		nodes[node.ID] = node
	}
	return nodes
}

func edgeKey(edge EvidenceEdge) string {
	return edge.From + "|" + string(edge.Relation) + "|" + edge.To
}
