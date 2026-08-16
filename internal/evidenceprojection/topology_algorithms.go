package evidenceprojection

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	graph "github.com/Yui-Qi-Tang/graph"
)

// ErrDerivedCycle reports a circular derived_from dependency.
var ErrDerivedCycle = errors.New("derived_from topology contains a cycle")

// CycleWitness identifies one deterministic relation-scoped structural cycle.
// NodeIDs repeats the start node at the end.
type CycleWitness struct {
	Relation evidencegraph.CanonicalEdgeRelation `json:"relation"`
	NodeIDs  []string                            `json:"node_ids"`
	EdgeIDs  []string                            `json:"edge_ids"`
}

// TopologyDiagnostics reports relation-scoped structural defects without
// interpreting their evidence meaning.
type TopologyDiagnostics struct {
	DerivedFromCycle *CycleWitness `json:"derived_from_cycle,omitempty"`
	SupersedesCycle  *CycleWitness `json:"supersedes_cycle,omitempty"`
}

// PathQuery selects one point-to-point witness inside an immutable topology.
// Relations must be explicit; an empty relation scope is rejected.
type PathQuery struct {
	FromNodeID string
	ToNodeID   string
	Relations  []evidencegraph.CanonicalEdgeRelation
}

// PathWitness is a deterministic first-shortest path within one prepared
// topology. Found=false means only that the path was absent from that topology.
type PathWitness struct {
	FromNodeID string                                `json:"from_node_id"`
	ToNodeID   string                                `json:"to_node_id"`
	Found      bool                                  `json:"found_in_view"`
	NodeIDs    []string                              `json:"node_ids,omitempty"`
	EdgeIDs    []string                              `json:"edge_ids,omitempty"`
	Relations  []evidencegraph.CanonicalEdgeRelation `json:"relations,omitempty"`
}

// ConflictCluster groups nodes connected by explicit contradicts edges. EdgeIDs
// contains only original canonical edges; cluster membership does not imply a
// contradiction between every pair of nodes.
type ConflictCluster struct {
	NodeIDs []string `json:"node_ids"`
	EdgeIDs []string `json:"edge_ids"`
}

// PreparedTopology owns one immutable graph Snapshot and cached structural
// diagnostics. It creates no evidence authority and exposes no graph package
// types.
type PreparedTopology struct {
	snapshotID       string
	topology         *canonicalTopology
	diagnostics      TopologyDiagnostics
	conflictOnce     sync.Once
	conflictClusters []ConflictCluster
}

// PrepareTopology validates a canonical artifact and prepares its immutable
// topology for repeated external read operations.
func PrepareTopology(artifact evidencegraph.CanonicalArtifact) (*PreparedTopology, error) {
	if err := artifact.Validate(); err != nil {
		return nil, fmt.Errorf("validating canonical graph: %w", err)
	}
	return prepareTopologyValidated(artifact)
}

func prepareTopologyValidated(artifact evidencegraph.CanonicalArtifact) (*PreparedTopology, error) {
	topology, err := newCanonicalTopology(artifact)
	if err != nil {
		return nil, err
	}
	prepared := &PreparedTopology{
		snapshotID: artifact.SnapshotID,
		topology:   topology,
	}
	prepared.diagnostics = analyzeTopology(topology)
	return prepared, nil
}

// SnapshotID returns the identity of the artifact used to prepare the topology.
func (p *PreparedTopology) SnapshotID() string {
	if p == nil {
		return ""
	}
	return p.snapshotID
}

// Diagnostics returns a defensive copy of the cached cycle diagnostics.
func (p *PreparedTopology) Diagnostics() TopologyDiagnostics {
	if p == nil {
		return TopologyDiagnostics{}
	}
	return cloneTopologyDiagnostics(p.diagnostics)
}

// ConflictClusters returns defensive copies of contradiction components.
func (p *PreparedTopology) ConflictClusters() []ConflictCluster {
	if p == nil {
		return nil
	}
	p.conflictOnce.Do(func() {
		p.conflictClusters = buildConflictClusters(p.topology)
	})
	clusters := make([]ConflictCluster, len(p.conflictClusters))
	for i, cluster := range p.conflictClusters {
		clusters[i] = ConflictCluster{
			NodeIDs: append([]string(nil), cluster.NodeIDs...),
			EdgeIDs: append([]string(nil), cluster.EdgeIDs...),
		}
	}
	return clusters
}

// FindPath returns the deterministic first-shortest path within the explicit
// relation scope.
func (p *PreparedTopology) FindPath(query PathQuery) (PathWitness, error) {
	if p == nil || p.topology == nil {
		return PathWitness{}, errors.New("prepared topology is required")
	}
	from := strings.TrimSpace(query.FromNodeID)
	to := strings.TrimSpace(query.ToNodeID)
	if from == "" || to == "" {
		return PathWitness{}, errors.New("from_node_id and to_node_id are required")
	}
	if !p.topology.HasNode(from) {
		return PathWitness{}, fmt.Errorf("from node %q is not in the prepared topology", from)
	}
	if !p.topology.HasNode(to) {
		return PathWitness{}, fmt.Errorf("to node %q is not in the prepared topology", to)
	}
	relations, err := normalizePathRelations(query.Relations)
	if err != nil {
		return PathWitness{}, err
	}
	witness := PathWitness{FromNodeID: from, ToNodeID: to}
	path, found := graph.FindPath[string, string, evidencegraph.CanonicalEdge](
		canonicalRelationView{topology: p.topology, relations: relationSet(relations)},
		from,
		to,
	)
	if !found {
		return witness, nil
	}
	witness.Found = true
	witness.NodeIDs = append([]string(nil), path.Nodes...)
	witness.EdgeIDs = make([]string, 0, len(path.Edges))
	witness.Relations = make([]evidencegraph.CanonicalEdgeRelation, 0, len(path.Edges))
	for _, edge := range path.Edges {
		witness.EdgeIDs = append(witness.EdgeIDs, edge.ID)
		witness.Relations = append(witness.Relations, edge.Data.Relation)
	}
	return witness, nil
}

type canonicalRelationView struct {
	topology  *canonicalTopology
	relations map[evidencegraph.CanonicalEdgeRelation]struct{}
}

func (v canonicalRelationView) NodeIDs() iter.Seq[string] {
	return v.topology.NodeIDs()
}

func (v canonicalRelationView) Outgoing(id string) iter.Seq[graph.Edge[string, string, evidencegraph.CanonicalEdge]] {
	return func(yield func(graph.Edge[string, string, evidencegraph.CanonicalEdge]) bool) {
		for edge := range v.topology.Outgoing(id) {
			if _, allowed := v.relations[edge.Data.Relation]; !allowed {
				continue
			}
			if !yield(edge) {
				return
			}
		}
	}
}

type conflictEdgeID struct {
	canonicalID string
	reversed    bool
}

type canonicalConflictView struct {
	topology *canonicalTopology
}

func (v canonicalConflictView) NodeIDs() iter.Seq[string] {
	return v.topology.NodeIDs()
}

func (v canonicalConflictView) Outgoing(id string) iter.Seq[graph.Edge[string, conflictEdgeID, evidencegraph.CanonicalEdge]] {
	return func(yield func(graph.Edge[string, conflictEdgeID, evidencegraph.CanonicalEdge]) bool) {
		for edge := range v.topology.Outgoing(id) {
			if edge.Data.Relation != evidencegraph.CanonicalContradicts {
				continue
			}
			projected := graph.Edge[string, conflictEdgeID, evidencegraph.CanonicalEdge]{
				ID:   conflictEdgeID{canonicalID: edge.ID},
				From: edge.From,
				To:   edge.To,
				Data: edge.Data,
			}
			if !yield(projected) {
				return
			}
		}
		for edge := range v.topology.Incoming(id) {
			if edge.Data.Relation != evidencegraph.CanonicalContradicts || edge.From == edge.To {
				continue
			}
			projected := graph.Edge[string, conflictEdgeID, evidencegraph.CanonicalEdge]{
				ID:   conflictEdgeID{canonicalID: edge.ID, reversed: true},
				From: id,
				To:   edge.From,
				Data: edge.Data,
			}
			if !yield(projected) {
				return
			}
		}
	}
}

func analyzeTopology(topology *canonicalTopology) TopologyDiagnostics {
	return TopologyDiagnostics{
		DerivedFromCycle: findRelationCycle(topology, evidencegraph.CanonicalDerivedFrom),
		SupersedesCycle:  findRelationCycle(topology, evidencegraph.CanonicalSupersedes),
	}
}

func findRelationCycle(topology *canonicalTopology, relation evidencegraph.CanonicalEdgeRelation) *CycleWitness {
	cycle, found := graph.FindCycle[string, string, evidencegraph.CanonicalEdge](canonicalRelationView{
		topology:  topology,
		relations: relationSet([]evidencegraph.CanonicalEdgeRelation{relation}),
	})
	if !found {
		return nil
	}
	witness := &CycleWitness{
		Relation: relation,
		NodeIDs:  append([]string(nil), cycle.Nodes...),
		EdgeIDs:  make([]string, 0, len(cycle.Edges)),
	}
	for _, edge := range cycle.Edges {
		witness.EdgeIDs = append(witness.EdgeIDs, edge.ID)
	}
	return witness
}

func buildConflictClusters(topology *canonicalTopology) []ConflictCluster {
	components := graph.StronglyConnectedComponents[string, conflictEdgeID, evidencegraph.CanonicalEdge](canonicalConflictView{topology: topology})
	componentByNode := make(map[string]int)
	for i, component := range components {
		for _, id := range component {
			componentByNode[id] = i
		}
		slices.Sort(component)
	}
	edgeIDs := make([][]string, len(components))
	for edge := range topology.Edges() {
		if edge.Data.Relation != evidencegraph.CanonicalContradicts {
			continue
		}
		fromComponent, fromIncluded := componentByNode[edge.From]
		toComponent, toIncluded := componentByNode[edge.To]
		if !fromIncluded || !toIncluded || fromComponent != toComponent {
			continue
		}
		edgeIDs[fromComponent] = append(edgeIDs[fromComponent], edge.ID)
	}
	clusters := make([]ConflictCluster, 0)
	for i, component := range components {
		if len(edgeIDs[i]) == 0 {
			continue
		}
		slices.Sort(edgeIDs[i])
		clusters = append(clusters, ConflictCluster{
			NodeIDs: append([]string(nil), component...),
			EdgeIDs: edgeIDs[i],
		})
	}
	slices.SortFunc(clusters, func(a, b ConflictCluster) int {
		return strings.Compare(strings.Join(a.NodeIDs, "\x00"), strings.Join(b.NodeIDs, "\x00"))
	})
	return clusters
}

func normalizePathRelations(relations []evidencegraph.CanonicalEdgeRelation) ([]evidencegraph.CanonicalEdgeRelation, error) {
	if len(relations) == 0 {
		return nil, errors.New("at least one path relation is required")
	}
	allowed := relationSet(evidencegraph.CanonicalRelations())
	seen := make(map[evidencegraph.CanonicalEdgeRelation]struct{}, len(relations))
	normalized := make([]evidencegraph.CanonicalEdgeRelation, 0, len(relations))
	for _, relation := range relations {
		if _, ok := allowed[relation]; !ok {
			return nil, fmt.Errorf("path relation %q is not supported", relation)
		}
		if _, duplicate := seen[relation]; duplicate {
			continue
		}
		seen[relation] = struct{}{}
		normalized = append(normalized, relation)
	}
	slices.Sort(normalized)
	return normalized, nil
}

func relationSet(relations []evidencegraph.CanonicalEdgeRelation) map[evidencegraph.CanonicalEdgeRelation]struct{} {
	set := make(map[evidencegraph.CanonicalEdgeRelation]struct{}, len(relations))
	for _, relation := range relations {
		set[relation] = struct{}{}
	}
	return set
}

func cloneTopologyDiagnostics(diagnostics TopologyDiagnostics) TopologyDiagnostics {
	return TopologyDiagnostics{
		DerivedFromCycle: cloneCycleWitness(diagnostics.DerivedFromCycle),
		SupersedesCycle:  cloneCycleWitness(diagnostics.SupersedesCycle),
	}
}

func cloneCycleWitness(witness *CycleWitness) *CycleWitness {
	if witness == nil {
		return nil
	}
	return &CycleWitness{
		Relation: witness.Relation,
		NodeIDs:  append([]string(nil), witness.NodeIDs...),
		EdgeIDs:  append([]string(nil), witness.EdgeIDs...),
	}
}
