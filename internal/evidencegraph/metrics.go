package evidencegraph

import "strings"

// Metrics summarizes graph coverage and traceability.
type Metrics struct {
	NodeCount int `json:"node_count"`
	EdgeCount int `json:"edge_count"`

	SupportsCount   int `json:"supports_count"`
	ReferencesCount int `json:"references_count"`
	ImplementsCount int `json:"implements_count"`
	RequiresCount   int `json:"requires_count"`
	ConflictsCount  int `json:"conflicts_count"`
	SupersedesCount int `json:"supersedes_count"`

	RelationResolutionRate   float64 `json:"relation_resolution_rate"`
	ConflictDetectionRate    float64 `json:"conflict_detection_rate"`
	ReferenceTraceRate       float64 `json:"reference_trace_rate"`
	ImplementationTraceRate  float64 `json:"implementation_trace_rate"`
	SupersessionTraceRate    float64 `json:"supersession_trace_rate"`
	AllRelationTypesPresent  bool    `json:"all_relation_types_present"`
	ExitCriteriaMet          bool    `json:"exit_criteria_met"`
	ResolvedRelationEdges    int     `json:"resolved_relation_edges"`
	ReferenceTraceEdges      int     `json:"reference_trace_edges"`
	ImplementationTraceEdges int     `json:"implementation_trace_edges"`
	ConflictTraceEdges       int     `json:"conflict_trace_edges"`
	SupersessionTraceEdges   int     `json:"supersession_trace_edges"`
}

// Metrics returns graph metrics for the Phase 9 exit criteria.
func (g Graph) Metrics() Metrics {
	metrics := Metrics{
		NodeCount: len(g.Nodes),
		EdgeCount: len(g.Edges),
	}
	nodeIDs := map[string]bool{}
	for _, node := range g.Nodes {
		nodeIDs[node.ID] = true
	}
	for _, edge := range g.Edges {
		if nodeIDs[edge.From] && nodeIDs[edge.To] && ValidRelation(edge.Relation) {
			metrics.ResolvedRelationEdges++
		}
		switch edge.Relation {
		case Supports:
			metrics.SupportsCount++
		case References:
			metrics.ReferencesCount++
			if traceableReference(edge) {
				metrics.ReferenceTraceEdges++
			}
		case Implements:
			metrics.ImplementsCount++
			if traceableImplementation(edge) {
				metrics.ImplementationTraceEdges++
			}
		case Requires:
			metrics.RequiresCount++
		case ConflictsWith:
			metrics.ConflictsCount++
			if traceableConflict(edge) {
				metrics.ConflictTraceEdges++
			}
		case Supersedes:
			metrics.SupersedesCount++
			if traceableSupersession(edge) {
				metrics.SupersessionTraceEdges++
			}
		}
	}
	if metrics.EdgeCount > 0 {
		metrics.RelationResolutionRate = float64(metrics.ResolvedRelationEdges) / float64(metrics.EdgeCount)
	}
	if metrics.ReferencesCount > 0 {
		metrics.ReferenceTraceRate = float64(metrics.ReferenceTraceEdges) / float64(metrics.ReferencesCount)
	}
	if metrics.ImplementsCount > 0 {
		metrics.ImplementationTraceRate = float64(metrics.ImplementationTraceEdges) / float64(metrics.ImplementsCount)
	}
	if metrics.ConflictsCount > 0 {
		metrics.ConflictDetectionRate = float64(metrics.ConflictTraceEdges) / float64(metrics.ConflictsCount)
	}
	if metrics.SupersedesCount > 0 {
		metrics.SupersessionTraceRate = float64(metrics.SupersessionTraceEdges) / float64(metrics.SupersedesCount)
	}
	metrics.AllRelationTypesPresent = metrics.SupportsCount > 0 &&
		metrics.ReferencesCount > 0 &&
		metrics.ImplementsCount > 0 &&
		metrics.RequiresCount > 0 &&
		metrics.ConflictsCount > 0 &&
		metrics.SupersedesCount > 0
	metrics.ExitCriteriaMet = metrics.AllRelationTypesPresent &&
		metrics.EdgeCount >= 50 &&
		metrics.ReferenceTraceRate >= 0.95 &&
		metrics.ConflictDetectionRate >= 0.95 &&
		metrics.ImplementationTraceRate >= 0.90 &&
		metrics.SupersessionTraceRate >= 0.90
	return metrics
}

func traceableReference(edge EvidenceEdge) bool {
	return sourceContains(edge, "epl-005", "ref", "reference")
}

func traceableImplementation(edge EvidenceEdge) bool {
	return sourceContains(edge, "mix", "cg", "implementation", "implements")
}

func traceableConflict(edge EvidenceEdge) bool {
	return sourceContains(edge, "epl-004", "conflict")
}

func traceableSupersession(edge EvidenceEdge) bool {
	return sourceContains(edge, "epl-006", "supersedes", "stale", "current")
}

func sourceContains(edge EvidenceEdge, terms ...string) bool {
	source := strings.ToLower(edge.Source)
	if source == "" {
		return false
	}
	for _, term := range terms {
		if strings.Contains(source, strings.ToLower(term)) {
			return true
		}
	}
	return false
}
