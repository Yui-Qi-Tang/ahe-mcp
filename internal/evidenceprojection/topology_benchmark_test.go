package evidenceprojection

import (
	"cmp"
	"fmt"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

type legacyAdjacency struct {
	outgoing map[string][]evidencegraph.CanonicalEdge
	incoming map[string][]evidencegraph.CanonicalEdge
}

var (
	benchmarkTopology  *canonicalTopology
	benchmarkAdjacency legacyAdjacency
	benchmarkEdgeBytes int
)

func BenchmarkTopologyBuild(b *testing.B) {
	for _, size := range []struct {
		nodes int
		edges int
	}{
		{nodes: 2, edges: 1},
		{nodes: 23, edges: 6},
		{nodes: 100, edges: 400},
		{nodes: 1_000, edges: 4_000},
	} {
		artifact := topologyBenchmarkArtifact(size.nodes, size.edges)
		name := fmt.Sprintf("nodes=%d/edges=%d", size.nodes, size.edges)
		b.Run("snapshot/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				topology, err := newCanonicalTopology(artifact)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkTopology = topology
			}
		})
		b.Run("legacy-adjacency/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				benchmarkAdjacency = buildLegacyAdjacency(artifact.Edges)
			}
		})
	}
}

func BenchmarkTopologyOutgoing(b *testing.B) {
	artifact := topologyBenchmarkArtifact(1_000, 4_000)
	topology, err := newCanonicalTopology(artifact)
	if err != nil {
		b.Fatal(err)
	}
	adjacency := buildLegacyAdjacency(artifact.Edges)
	nodeID := "node:000500"

	b.Run("snapshot", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			bytes := 0
			for edge := range topology.Outgoing(nodeID) {
				bytes += len(edge.ID)
			}
			benchmarkEdgeBytes = bytes
		}
	})
	b.Run("legacy-adjacency", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			bytes := 0
			for _, edge := range adjacency.outgoing[nodeID] {
				bytes += len(edge.ID)
			}
			benchmarkEdgeBytes = bytes
		}
	})
}

func buildLegacyAdjacency(edges []evidencegraph.CanonicalEdge) legacyAdjacency {
	adjacency := legacyAdjacency{
		outgoing: make(map[string][]evidencegraph.CanonicalEdge),
		incoming: make(map[string][]evidencegraph.CanonicalEdge),
	}
	for _, edge := range edges {
		adjacency.outgoing[edge.From] = append(adjacency.outgoing[edge.From], edge)
		adjacency.incoming[edge.To] = append(adjacency.incoming[edge.To], edge)
	}
	for id := range adjacency.outgoing {
		slices.SortFunc(adjacency.outgoing[id], func(a, b evidencegraph.CanonicalEdge) int {
			return cmp.Compare(a.ID, b.ID)
		})
	}
	for id := range adjacency.incoming {
		slices.SortFunc(adjacency.incoming[id], func(a, b evidencegraph.CanonicalEdge) int {
			return cmp.Compare(a.ID, b.ID)
		})
	}
	return adjacency
}

func topologyBenchmarkArtifact(nodes, edges int) evidencegraph.CanonicalArtifact {
	artifact := evidencegraph.CanonicalArtifact{
		Nodes: make([]evidencegraph.CanonicalNode, 0, nodes),
		Edges: make([]evidencegraph.CanonicalEdge, 0, edges),
	}
	for i := range nodes {
		id := fmt.Sprintf("node:%06d", i)
		artifact.Nodes = append(artifact.Nodes, evidencegraph.CanonicalNode{ID: id})
	}
	for i := range edges {
		artifact.Edges = append(artifact.Edges, evidencegraph.CanonicalEdge{
			ID:   fmt.Sprintf("edge:%06d", edges-i),
			From: fmt.Sprintf("node:%06d", i%nodes),
			To:   fmt.Sprintf("node:%06d", (i*17+1)%nodes),
		})
	}
	return artifact
}
