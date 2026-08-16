package evidencequerymcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestCanonicalReadViewToolsReuseOneMaterializedView(t *testing.T) {
	artifact := queryCanonicalReadArtifact(t)
	core := &fakeQueryCore{
		canonicalReadResult: evidenceingestion.CanonicalReadView{
			Artifact:    artifact,
			RootNodeIDs: []string{"canon-node:raw"},
			Relations: []evidencegraph.CanonicalEdgeRelation{
				evidencegraph.CanonicalContradicts,
				evidencegraph.CanonicalSupportsClaim,
			},
			MaxDepth: 1,
			MaxNodes: 8,
			MaxEdges: 16,
		},
	}
	server := newServer(core)

	openData, err := server.CallTool(context.Background(), ToolOpenCanonicalReadView, []byte(`{
		"root_node_ids":["canon-node:raw"],
		"relations":["supports_claim","contradicts"],
		"max_depth":1,
		"max_nodes":8,
		"max_edges":16
	}`))
	if err != nil {
		t.Fatalf("open canonical read view: %v", err)
	}
	var opened OpenCanonicalReadViewResponse
	if err := json.Unmarshal(openData, &opened); err != nil {
		t.Fatalf("decode open response: %v", err)
	}
	if opened.SchemaVersion != CanonicalReadViewQuerySchemaV1 ||
		opened.View.Handle == "" ||
		opened.View.SnapshotID != artifact.SnapshotID ||
		opened.View.NodeCount != 2 ||
		opened.View.EdgeCount != 2 ||
		opened.View.GlobalAbsenceInferenceAllowed ||
		opened.Artifact.SnapshotID != artifact.SnapshotID {
		t.Fatalf("open response = %+v", opened)
	}
	if core.canonicalReadCalls != 1 {
		t.Fatalf("canonical read calls = %d, want 1", core.canonicalReadCalls)
	}
	if !reflect.DeepEqual(core.canonicalReadInput, evidenceingestion.CanonicalReadInput{
		RootNodeIDs: []string{"canon-node:raw"},
		Relations: []evidencegraph.CanonicalEdgeRelation{
			evidencegraph.CanonicalSupportsClaim,
			evidencegraph.CanonicalContradicts,
		},
		MaxDepth: 1,
		MaxNodes: 8,
		MaxEdges: 16,
	}) {
		t.Fatalf("canonical read input = %+v", core.canonicalReadInput)
	}

	pathPayload, err := json.Marshal(FindCanonicalPathRequest{
		Handle:     opened.View.Handle,
		FromNodeID: "canon-node:raw",
		ToNodeID:   "canon-node:claim",
		Relations:  []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupportsClaim},
	})
	if err != nil {
		t.Fatal(err)
	}
	pathData, err := server.CallTool(context.Background(), ToolFindCanonicalPath, pathPayload)
	if err != nil {
		t.Fatalf("find canonical path: %v", err)
	}
	var path FindCanonicalPathResponse
	if err := json.Unmarshal(pathData, &path); err != nil {
		t.Fatalf("decode path response: %v", err)
	}
	if !path.Witness.Found ||
		!reflect.DeepEqual(path.Witness.NodeIDs, []string{"canon-node:raw", "canon-node:claim"}) ||
		!reflect.DeepEqual(path.Witness.EdgeIDs, []string{"canon-edge:support"}) ||
		!reflect.DeepEqual(path.RelationScope, []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupportsClaim}) {
		t.Fatalf("path response = %+v", path)
	}

	diagnosticsPayload, err := json.Marshal(GetCanonicalTopologyDiagnosticsRequest{Handle: opened.View.Handle})
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsData, err := server.CallTool(context.Background(), ToolGetCanonicalTopologyDiagnostics, diagnosticsPayload)
	if err != nil {
		t.Fatalf("get topology diagnostics: %v", err)
	}
	var diagnostics GetCanonicalTopologyDiagnosticsResponse
	if err := json.Unmarshal(diagnosticsData, &diagnostics); err != nil {
		t.Fatalf("decode diagnostics response: %v", err)
	}
	if diagnostics.Diagnostics.DerivedFromCycle != nil ||
		diagnostics.Diagnostics.SupersedesCycle != nil ||
		len(diagnostics.ConflictClusters) != 1 ||
		!reflect.DeepEqual(diagnostics.ConflictClusters[0].NodeIDs, []string{"canon-node:claim", "canon-node:raw"}) ||
		!reflect.DeepEqual(diagnostics.ConflictClusters[0].EdgeIDs, []string{"canon-edge:conflict"}) {
		t.Fatalf("diagnostics response = %+v", diagnostics)
	}
	if core.canonicalReadCalls != 1 {
		t.Fatalf("canonical read calls after cached reads = %d, want 1", core.canonicalReadCalls)
	}
}

func TestCanonicalReadViewMissingHandleRequiresReopen(t *testing.T) {
	server := newServer(&fakeQueryCore{})
	_, err := server.FindCanonicalPath(FindCanonicalPathRequest{
		Handle:     "canonical-read-view:missing",
		FromNodeID: "canon-node:raw",
		ToNodeID:   "canon-node:claim",
		Relations:  []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupportsClaim},
	})
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolErrorReadViewMissing {
		t.Fatalf("FindCanonicalPath() error = %v", err)
	}
}

func TestCanonicalReadViewCacheIsBoundedLRU(t *testing.T) {
	cache := newCanonicalReadViewCache(2)
	cache.put(&cachedCanonicalReadView{handle: "a"})
	cache.put(&cachedCanonicalReadView{handle: "b"})
	if _, ok := cache.get("a"); !ok {
		t.Fatal("cache.get(a) = missing")
	}
	cache.put(&cachedCanonicalReadView{handle: "c"})
	if _, ok := cache.get("b"); ok {
		t.Fatal("cache.get(b) = present after LRU eviction")
	}
	if _, ok := cache.get("a"); !ok {
		t.Fatal("cache.get(a) = missing after refresh")
	}
	if _, ok := cache.get("c"); !ok {
		t.Fatal("cache.get(c) = missing")
	}
}

func TestCanonicalReadViewHandleIncludesScope(t *testing.T) {
	artifact := queryCanonicalReadArtifact(t)
	base := evidenceingestion.CanonicalReadView{
		Artifact:    artifact,
		RootNodeIDs: []string{"canon-node:raw"},
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupportsClaim},
		MaxDepth:    1,
		MaxNodes:    8,
		MaxEdges:    16,
	}
	first, err := canonicalReadViewHandle(base)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := canonicalReadViewHandle(base)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != first {
		t.Fatalf("replayed handle = %q, want %q", replayed, first)
	}
	base.MaxDepth = 2
	second, err := canonicalReadViewHandle(base)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("different scope produced the same handle")
	}
}

func queryCanonicalReadArtifact(t *testing.T) evidencegraph.CanonicalArtifact {
	t.Helper()
	payloads := []evidencegraph.EvidencePayload{
		{ID: "payload:raw", SourceType: "document", Title: "Raw", Source: "fixture", Span: "observed"},
		{ID: "payload:claim", SourceType: "document", Title: "Claim", Source: "fixture", Claim: "supported"},
	}
	integrity := make([]evidencegraph.IntegrityRecord, 0, len(payloads))
	for _, payload := range payloads {
		digest, err := evidencegraph.PayloadDigest(payload)
		if err != nil {
			t.Fatal(err)
		}
		integrity = append(integrity, evidencegraph.IntegrityRecord{
			ID:        "integrity:" + payload.ID,
			Algorithm: "sha256",
			Digest:    digest,
		})
	}
	artifact := evidencegraph.CanonicalArtifact{
		SchemaVersion: evidencegraph.CanonicalSchemaVersion,
		SnapshotID:    "postgres-read-view:fixture",
		Nodes: []evidencegraph.CanonicalNode{
			{ID: "canon-node:raw", Kind: evidencegraph.CanonicalRawEvidence, PayloadRef: "payload:raw", ProvenanceRef: "provenance:fixture", TemporalRef: "temporal:raw", IntegrityRef: "integrity:payload:raw"},
			{ID: "canon-node:claim", Kind: evidencegraph.CanonicalSourceClaim, PayloadRef: "payload:claim", ProvenanceRef: "provenance:fixture", TemporalRef: "temporal:claim", IntegrityRef: "integrity:payload:claim"},
		},
		Edges: []evidencegraph.CanonicalEdge{
			{ID: "canon-edge:support", From: "canon-node:raw", To: "canon-node:claim", Relation: evidencegraph.CanonicalSupportsClaim, ProvenanceRef: "provenance:fixture"},
			{ID: "canon-edge:conflict", From: "canon-node:claim", To: "canon-node:raw", Relation: evidencegraph.CanonicalContradicts, ProvenanceRef: "provenance:fixture"},
		},
		Payloads: payloads,
		Provenance: []evidencegraph.ProvenanceRecord{
			{ID: "provenance:fixture", OriginRefs: []string{"fixture"}, OriginGroupID: "origin:fixture", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "trace:fixture"},
		},
		Temporal: []evidencegraph.TemporalRecord{
			{ID: "temporal:raw", Status: evidencegraph.TemporalCurrent},
			{ID: "temporal:claim", Status: evidencegraph.TemporalCurrent},
		},
		Integrity: integrity,
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	return artifact
}
