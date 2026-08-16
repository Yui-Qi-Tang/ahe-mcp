package evidenceprojection

import (
	"errors"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	graph "github.com/Yui-Qi-Tang/graph"
)

func TestNewCanonicalTopologySortsAndCopiesTopology(t *testing.T) {
	artifact := evidencegraph.CanonicalArtifact{
		Nodes: []evidencegraph.CanonicalNode{
			{ID: "node:b", Kind: evidencegraph.CanonicalSourceClaim},
			{ID: "node:a", Kind: evidencegraph.CanonicalRawEvidence},
		},
		Edges: []evidencegraph.CanonicalEdge{
			{ID: "edge:z", From: "node:b", To: "node:a", Relation: evidencegraph.CanonicalReferences},
			{ID: "edge:a", From: "node:a", To: "node:b", Relation: evidencegraph.CanonicalSupportsClaim},
		},
	}

	topology, err := newCanonicalTopology(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := slices.Collect(topology.NodeIDs()), []string{"node:a", "node:b"}; !slices.Equal(got, want) {
		t.Fatalf("NodeIDs() = %v, want %v", got, want)
	}
	edges := slices.Collect(topology.Edges())
	if got, want := []string{edges[0].ID, edges[1].ID}, []string{"edge:a", "edge:z"}; !slices.Equal(got, want) {
		t.Fatalf("Edges() IDs = %v, want %v", got, want)
	}
	outgoing := slices.Collect(topology.Outgoing("node:a"))
	if len(outgoing) != 1 || outgoing[0].Data != artifact.Edges[1] {
		t.Fatalf("Outgoing(node:a) = %+v, want edge:a with canonical data", outgoing)
	}
	if got := slices.Collect(topology.Incoming("node:missing")); len(got) != 0 {
		t.Fatalf("Incoming(node:missing) = %+v, want empty", got)
	}

	artifact.Nodes[0].ID = "node:changed"
	artifact.Edges[0].From = "node:changed"
	artifact.Edges[0].Relation = evidencegraph.CanonicalContradicts
	node, ok := topology.Node("node:b")
	if !ok || node.Data.ID != "node:b" {
		t.Fatalf("Node(node:b) = %+v, %t after source mutation", node, ok)
	}
	edge, ok := topology.Edge("edge:z")
	if !ok || edge.From != "node:b" || edge.Data.Relation != evidencegraph.CanonicalReferences {
		t.Fatalf("Edge(edge:z) = %+v, %t after source mutation", edge, ok)
	}
}

func TestNewCanonicalTopologyAcceptsEmptyArtifact(t *testing.T) {
	topology, err := newCanonicalTopology(evidencegraph.CanonicalArtifact{})
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Collect(topology.NodeIDs()); len(got) != 0 {
		t.Fatalf("NodeIDs() = %v, want empty", got)
	}
	if got := slices.Collect(topology.Edges()); len(got) != 0 {
		t.Fatalf("Edges() = %v, want empty", got)
	}
}

func TestNewCanonicalTopologyReportsInvalidTopology(t *testing.T) {
	artifact := evidencegraph.CanonicalArtifact{
		Nodes: []evidencegraph.CanonicalNode{{ID: "node:a"}},
		Edges: []evidencegraph.CanonicalEdge{{ID: "edge:a", From: "node:a", To: "node:missing"}},
	}
	_, err := newCanonicalTopology(artifact)
	if !errors.Is(err, graph.ErrMissingEndpoint) {
		t.Fatalf("newCanonicalTopology() error = %v, want ErrMissingEndpoint", err)
	}
}
