package evidencegraph

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedGraphMetrics(t *testing.T) {
	graph, err := Load(filepath.Join("testdata", "evidence_graph_seed.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	metrics := graph.Metrics()
	if metrics.NodeCount == 0 {
		t.Fatal("NodeCount = 0")
	}
	if metrics.EdgeCount < 50 {
		t.Fatalf("EdgeCount = %d, want at least 50", metrics.EdgeCount)
	}
	if !metrics.AllRelationTypesPresent {
		t.Fatal("AllRelationTypesPresent = false")
	}
	if metrics.ReferenceTraceRate < 0.95 {
		t.Fatalf("ReferenceTraceRate = %.4f, want >= 0.95", metrics.ReferenceTraceRate)
	}
	if metrics.ConflictDetectionRate < 0.95 {
		t.Fatalf("ConflictDetectionRate = %.4f, want >= 0.95", metrics.ConflictDetectionRate)
	}
	if metrics.ImplementationTraceRate < 0.90 {
		t.Fatalf("ImplementationTraceRate = %.4f, want >= 0.90", metrics.ImplementationTraceRate)
	}
	if metrics.SupersessionTraceRate < 0.90 {
		t.Fatalf("SupersessionTraceRate = %.4f, want >= 0.90", metrics.SupersessionTraceRate)
	}
	if !metrics.ExitCriteriaMet {
		t.Fatal("ExitCriteriaMet = false")
	}
}

func TestGraphQueries(t *testing.T) {
	graph, err := Load(filepath.Join("testdata", "evidence_graph_seed.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	outgoing := graph.Outgoing("annual-leave-reference")
	if len(outgoing) == 0 {
		t.Fatal("Outgoing(annual-leave-reference) returned no edges")
	}
	incoming := graph.Incoming("appendix-a-annual-leave")
	if len(incoming) == 0 {
		t.Fatal("Incoming(appendix-a-annual-leave) returned no edges")
	}
	related := graph.Related("annual-leave-reference", References)
	if len(related) != 1 || related[0].ID != "appendix-a-annual-leave" {
		t.Fatalf("Related() = %v, want appendix-a-annual-leave", related)
	}
}

func TestValidateRejectsUnknownRelation(t *testing.T) {
	graph := Graph{
		SchemaVersion: "test/v1",
		Nodes: []EvidenceNode{
			{ID: "doc:a", SourceType: "doc", Title: "A", Source: "test"},
			{ID: "doc:b", SourceType: "doc", Title: "B", Source: "test"},
		},
		Edges: []EvidenceEdge{
			{From: "doc:a", To: "doc:b", Relation: EvidenceRelation("similar_to"), Source: "test", Confidence: 1},
		},
	}
	if err := graph.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid relation error")
	}
}

func TestValidateRejectsMissingEndpoint(t *testing.T) {
	graph := Graph{
		SchemaVersion: "test/v1",
		Nodes: []EvidenceNode{
			{ID: "doc:a", SourceType: "doc", Title: "A", Source: "test"},
		},
		Edges: []EvidenceEdge{
			{From: "doc:a", To: "doc:missing", Relation: References, Source: "test", Confidence: 1},
		},
	}
	if err := graph.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing endpoint error")
	}
}

func TestStableNodeIDDeterministic(t *testing.T) {
	first := StableNodeID("Leave Policy", "section-2", "正式員工特休15天")
	second := StableNodeID("Leave Policy", "section-2", "正式員工特休15天")
	if first != second {
		t.Fatalf("StableNodeID() = %q then %q", first, second)
	}
	if first == StableNodeID("Leave Policy", "section-3", "正式員工特休15天") {
		t.Fatal("StableNodeID() did not change when locator changed")
	}
}

func TestValidateAcceptsSourceBackedNodeFields(t *testing.T) {
	graph := Graph{
		SchemaVersion: "test/v1",
		Nodes: []EvidenceNode{
			{
				ID:            StableNodeID("Leave Policy", "section-2", "正式員工特休15天"),
				SourceType:    "document",
				Title:         "Annual Leave",
				Source:        "Leave Policy",
				Span:          "正式員工特休15天。",
				SpanLocator:   "section-2",
				Claim:         "正式員工特休15天",
				Applicability: "正式員工",
				TargetAnchors: []TargetAnchor{{Kind: "policy", ID: "leave-policy"}},
			},
		},
	}
	if err := graph.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateAcceptsCrossDomainTargetAnchors(t *testing.T) {
	for _, anchor := range []TargetAnchor{
		{Kind: "section", ID: "employee-handbook/leave"},
		{Kind: "symbol", ID: "github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph.Graph"},
		{Kind: "resource", ID: "production/api-gateway"},
	} {
		graph := Graph{
			SchemaVersion: "test/v1",
			Nodes: []EvidenceNode{{
				ID:            "evidence:" + anchor.Kind,
				SourceType:    "source",
				Title:         anchor.Kind,
				Source:        "fixture",
				TargetAnchors: []TargetAnchor{anchor},
			}},
		}
		if err := graph.Validate(); err != nil {
			t.Errorf("Validate() anchor %v error = %v", anchor, err)
		}
	}
}

func TestValidateRejectsInvalidOrDuplicateTargetAnchor(t *testing.T) {
	base := Graph{
		SchemaVersion: "test/v1",
		Nodes: []EvidenceNode{{
			ID:         "doc:a",
			SourceType: "document",
			Title:      "A",
			Source:     "policy.md",
		}},
	}

	graph := base
	graph.Nodes[0].TargetAnchors = []TargetAnchor{{Kind: "section"}}
	if err := graph.Validate(); err == nil {
		t.Fatal("Validate() accepted target anchor without id")
	}

	graph = base
	graph.Nodes[0].TargetAnchors = []TargetAnchor{
		{Kind: "section", ID: "leave"},
		{Kind: "section", ID: "leave"},
	}
	if err := graph.Validate(); err == nil {
		t.Fatal("Validate() accepted duplicate target anchor")
	}
}

func TestArtifactExtensionStaysOutsidePolicyGraph(t *testing.T) {
	artifact := Artifact{
		Graph: Graph{
			SchemaVersion: "test/v1",
			Nodes: []EvidenceNode{{
				ID: "doc:a", SourceType: "document", Title: "A", Source: "policy.md",
				Span: "source span", SpanLocator: "policy.md:1", Claim: "source claim",
			}},
		},
		Extensions: []Extension{{
			Namespace: "code.example/navigation",
			Version:   "v1",
			Data:      json.RawMessage(`{"symbol":"FileSystemStorage"}`),
		}},
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeArtifact(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Extensions) != 1 {
		t.Fatalf("decoded extensions = %d, want 1", len(decoded.Extensions))
	}
	base, err := json.Marshal(decoded.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(base, []byte(`"extensions"`)) || bytes.Contains(base, []byte(`"symbol"`)) {
		t.Fatalf("policy-visible Graph leaked extension data: %s", base)
	}
}

func TestArtifactRejectsNodeMetadataOutsideExtensionEnvelope(t *testing.T) {
	payload := `{"schema_version":"test/v1","nodes":[{"id":"doc:a","source_type":"document","title":"A","source":"policy.md","metadata":{"selected_anchor_id":"candidate_01"}}],"edges":[]}`
	if _, err := DecodeArtifact(strings.NewReader(payload)); err == nil {
		t.Fatal("DecodeArtifact() accepted undeclared node metadata")
	}
}
