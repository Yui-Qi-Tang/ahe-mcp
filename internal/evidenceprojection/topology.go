package evidenceprojection

import (
	"cmp"
	"fmt"
	"iter"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	graph "github.com/Yui-Qi-Tang/graph"
)

type canonicalTopology = graph.Snapshot[string, string, evidencegraph.CanonicalNode, evidencegraph.CanonicalEdge]

type canonicalTopologySource struct {
	nodes []graph.Node[string, evidencegraph.CanonicalNode]
	edges []graph.Edge[string, string, evidencegraph.CanonicalEdge]
}

func (s canonicalTopologySource) Nodes() iter.Seq[graph.Node[string, evidencegraph.CanonicalNode]] {
	return slices.Values(s.nodes)
}

func (s canonicalTopologySource) Edges() iter.Seq[graph.Edge[string, string, evidencegraph.CanonicalEdge]] {
	return slices.Values(s.edges)
}

func newCanonicalTopology(artifact evidencegraph.CanonicalArtifact) (*canonicalTopology, error) {
	source := canonicalTopologySource{
		nodes: make([]graph.Node[string, evidencegraph.CanonicalNode], 0, len(artifact.Nodes)),
		edges: make([]graph.Edge[string, string, evidencegraph.CanonicalEdge], 0, len(artifact.Edges)),
	}
	for _, node := range artifact.Nodes {
		source.nodes = append(source.nodes, graph.Node[string, evidencegraph.CanonicalNode]{
			ID:   node.ID,
			Data: node,
		})
	}
	for _, edge := range artifact.Edges {
		source.edges = append(source.edges, graph.Edge[string, string, evidencegraph.CanonicalEdge]{
			ID:   edge.ID,
			From: edge.From,
			To:   edge.To,
			Data: edge,
		})
	}
	slices.SortFunc(source.nodes, func(a, b graph.Node[string, evidencegraph.CanonicalNode]) int {
		return cmp.Compare(a.ID, b.ID)
	})
	slices.SortFunc(source.edges, func(a, b graph.Edge[string, string, evidencegraph.CanonicalEdge]) int {
		return cmp.Compare(a.ID, b.ID)
	})

	topology, err := graph.Build(source)
	if err != nil {
		return nil, fmt.Errorf("building canonical topology: %w", err)
	}
	return topology, nil
}
