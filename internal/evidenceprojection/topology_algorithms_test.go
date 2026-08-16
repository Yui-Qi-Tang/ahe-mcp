package evidenceprojection

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestPrepareTopologyReportsDerivedCycle(t *testing.T) {
	artifact := derivedCycleArtifact(t)
	prepared, err := PrepareTopology(artifact)
	if err != nil {
		t.Fatalf("PrepareTopology() error = %v", err)
	}
	diagnostics := prepared.Diagnostics()
	cycle := diagnostics.DerivedFromCycle
	if cycle == nil {
		t.Fatal("DerivedFromCycle = nil, want cycle witness")
	}
	if cycle.Relation != evidencegraph.CanonicalDerivedFrom || len(cycle.NodeIDs) != 3 || cycle.NodeIDs[0] != cycle.NodeIDs[len(cycle.NodeIDs)-1] {
		t.Fatalf("derived cycle = %+v, want closed two-edge witness", cycle)
	}
	wantEdges := []string{"edge:derived-derived2", "edge:derived2-derived"}
	gotEdges := append([]string(nil), cycle.EdgeIDs...)
	slices.Sort(gotEdges)
	if !reflect.DeepEqual(gotEdges, wantEdges) {
		t.Fatalf("cycle edge IDs = %v, want %v", gotEdges, wantEdges)
	}
}

func TestPrepareTopologyReportsSupersedesCycle(t *testing.T) {
	artifact := projectionTestArtifact(t)
	artifact.Edges = append(artifact.Edges,
		evidencegraph.CanonicalEdge{ID: "edge:a-b-supersedes", From: "source:a", To: "source:b", Relation: evidencegraph.CanonicalSupersedes, ProvenanceRef: "provenance:conflict"},
		evidencegraph.CanonicalEdge{ID: "edge:b-a-supersedes", From: "source:b", To: "source:a", Relation: evidencegraph.CanonicalSupersedes, ProvenanceRef: "provenance:conflict"},
	)
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareTopology(artifact)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := prepared.Diagnostics()
	if diagnostics.DerivedFromCycle != nil || diagnostics.SupersedesCycle == nil {
		t.Fatalf("diagnostics = %+v, want supersedes cycle only", diagnostics)
	}
}

func TestPreparedTopologyFindPathUsesExplicitRelationScope(t *testing.T) {
	prepared, err := PrepareTopology(projectionTestArtifact(t))
	if err != nil {
		t.Fatal(err)
	}

	derived, err := prepared.FindPath(PathQuery{
		FromNodeID: "source:a",
		ToNodeID:   "derived",
		Relations:  []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !derived.Found || !reflect.DeepEqual(derived.NodeIDs, []string{"source:a", "derived"}) || !reflect.DeepEqual(derived.EdgeIDs, []string{"edge:a-derived"}) {
		t.Fatalf("derived path = %+v", derived)
	}

	filtered, err := prepared.FindPath(PathQuery{
		FromNodeID: "source:a",
		ToNodeID:   "source:c",
		Relations:  []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Found {
		t.Fatalf("derived-only path unexpectedly found: %+v", filtered)
	}

	conflict, err := prepared.FindPath(PathQuery{
		FromNodeID: "source:a",
		ToNodeID:   "source:c",
		Relations:  []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalContradicts},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !conflict.Found || !reflect.DeepEqual(conflict.EdgeIDs, []string{"edge:a-c-conflict"}) {
		t.Fatalf("conflict path = %+v", conflict)
	}

	if _, err := prepared.FindPath(PathQuery{FromNodeID: "source:a", ToNodeID: "derived"}); err == nil {
		t.Fatal("FindPath() accepted an empty relation scope")
	}
}

func TestPreparedTopologyConflictClustersMirrorCanonicalEdges(t *testing.T) {
	artifact := projectionTestArtifact(t)
	artifact.Edges = append(artifact.Edges,
		evidencegraph.CanonicalEdge{ID: "edge:c-b-conflict", From: "source:c", To: "source:b", Relation: evidencegraph.CanonicalContradicts, ProvenanceRef: "provenance:conflict"},
		evidencegraph.CanonicalEdge{ID: "edge:stale-self-conflict", From: "source:stale", To: "source:stale", Relation: evidencegraph.CanonicalContradicts, ProvenanceRef: "provenance:conflict"},
	)
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareTopology(artifact)
	if err != nil {
		t.Fatal(err)
	}

	clusters := prepared.ConflictClusters()
	want := []ConflictCluster{
		{NodeIDs: []string{"source:a", "source:b", "source:c"}, EdgeIDs: []string{"edge:a-c-conflict", "edge:c-b-conflict"}},
		{NodeIDs: []string{"source:stale"}, EdgeIDs: []string{"edge:stale-self-conflict"}},
	}
	if !reflect.DeepEqual(clusters, want) {
		t.Fatalf("ConflictClusters() = %+v, want %+v", clusters, want)
	}

	clusters[0].NodeIDs[0] = "mutated"
	if got := prepared.ConflictClusters(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cached clusters were mutated through caller slice: %+v", got)
	}
}

func derivedCycleArtifact(t *testing.T) evidencegraph.CanonicalArtifact {
	t.Helper()
	artifact := projectionTestArtifact(t)
	payload := evidencegraph.EvidencePayload{
		ID: "payload:derived2", SourceType: "derived", Title: "Derived 2", Source: "fixture", Claim: "cycle fixture",
	}
	digest, err := evidencegraph.PayloadDigest(payload)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Payloads = append(artifact.Payloads, payload)
	artifact.Integrity = append(artifact.Integrity, evidencegraph.IntegrityRecord{
		ID: "integrity:payload:derived2", Algorithm: "sha256", Digest: digest,
	})
	artifact.Nodes = append(artifact.Nodes, evidencegraph.CanonicalNode{
		ID: "derived2", Kind: evidencegraph.CanonicalDerivedClaim,
		PayloadRef: "payload:derived2", ProvenanceRef: "provenance:derived",
		TemporalRef: "temporal:derived", IntegrityRef: "integrity:payload:derived2",
	})
	artifact.Edges = append(artifact.Edges,
		evidencegraph.CanonicalEdge{ID: "edge:derived-derived2", From: "derived", To: "derived2", Relation: evidencegraph.CanonicalDerivedFrom, ProvenanceRef: "provenance:derived"},
		evidencegraph.CanonicalEdge{ID: "edge:derived2-derived", From: "derived2", To: "derived", Relation: evidencegraph.CanonicalDerivedFrom, ProvenanceRef: "provenance:derived"},
	)
	artifact.Derivations[0].Parents = append(artifact.Derivations[0].Parents, "derived2")
	artifact.Derivations = append(artifact.Derivations, evidencegraph.DerivationRecord{
		ID: "derivation:derived2", NodeID: "derived2", Parents: []string{"derived"},
		Method: "cycle fixture", Producer: "test", TraceRef: "derived2", ProvenanceRef: "provenance:derived",
	})
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func projectionTestArtifact(t *testing.T) evidencegraph.CanonicalArtifact {
	t.Helper()
	payloads := []evidencegraph.EvidencePayload{
		{ID: "payload:a", SourceType: "doc", Title: "A", Source: "doc-a", Span: "a"},
		{ID: "payload:b", SourceType: "doc", Title: "B", Source: "doc-a", Span: "b"},
		{ID: "payload:c", SourceType: "doc", Title: "C", Source: "doc-c", Span: "c"},
		{ID: "payload:stale", SourceType: "doc", Title: "Stale", Source: "doc-stale", Span: "stale"},
		{ID: "payload:derived", SourceType: "derived", Title: "Derived", Source: "fixture", Claim: "a and c"},
	}
	var integrity []evidencegraph.IntegrityRecord
	for _, payload := range payloads {
		digest, err := evidencegraph.PayloadDigest(payload)
		if err != nil {
			t.Fatal(err)
		}
		integrity = append(integrity, evidencegraph.IntegrityRecord{
			ID: "integrity:" + payload.ID, Algorithm: "sha256", Digest: digest,
		})
	}
	artifact := evidencegraph.CanonicalArtifact{
		SchemaVersion: evidencegraph.CanonicalSchemaVersion,
		SnapshotID:    "projection-fixture",
		Nodes: []evidencegraph.CanonicalNode{
			{ID: "source:a", Kind: evidencegraph.CanonicalRawEvidence, PayloadRef: "payload:a", ProvenanceRef: "provenance:a", TemporalRef: "temporal:a", IntegrityRef: "integrity:payload:a"},
			{ID: "source:b", Kind: evidencegraph.CanonicalRawEvidence, PayloadRef: "payload:b", ProvenanceRef: "provenance:b", TemporalRef: "temporal:b", IntegrityRef: "integrity:payload:b"},
			{ID: "source:c", Kind: evidencegraph.CanonicalRawEvidence, PayloadRef: "payload:c", ProvenanceRef: "provenance:c", TemporalRef: "temporal:c", IntegrityRef: "integrity:payload:c"},
			{ID: "source:stale", Kind: evidencegraph.CanonicalRawEvidence, PayloadRef: "payload:stale", ProvenanceRef: "provenance:stale", TemporalRef: "temporal:stale", IntegrityRef: "integrity:payload:stale"},
			{ID: "derived", Kind: evidencegraph.CanonicalDerivedClaim, PayloadRef: "payload:derived", ProvenanceRef: "provenance:derived", TemporalRef: "temporal:derived", IntegrityRef: "integrity:payload:derived"},
		},
		Edges: []evidencegraph.CanonicalEdge{
			{ID: "edge:a-derived", From: "source:a", To: "derived", Relation: evidencegraph.CanonicalDerivedFrom, ProvenanceRef: "provenance:derived"},
			{ID: "edge:c-derived", From: "source:c", To: "derived", Relation: evidencegraph.CanonicalDerivedFrom, ProvenanceRef: "provenance:derived"},
			{ID: "edge:a-c-conflict", From: "source:a", To: "source:c", Relation: evidencegraph.CanonicalContradicts, ProvenanceRef: "provenance:conflict"},
		},
		Payloads: payloads,
		Provenance: []evidencegraph.ProvenanceRecord{
			{ID: "provenance:a", OriginRefs: []string{"doc-a"}, OriginGroupID: "origin:a", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "a"},
			{ID: "provenance:b", OriginRefs: []string{"doc-a"}, OriginGroupID: "origin:a", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "b"},
			{ID: "provenance:c", OriginRefs: []string{"doc-c"}, OriginGroupID: "origin:c", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "c"},
			{ID: "provenance:stale", OriginRefs: []string{"doc-stale"}, OriginGroupID: "origin:stale", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "stale"},
			{ID: "provenance:derived", OriginRefs: []string{"doc-a", "doc-c"}, OriginGroupID: "origin:derived", Producer: "test", Method: "conjunction", MethodVersion: "v1", TraceRef: "derived"},
			{ID: "provenance:conflict", OriginRefs: []string{"fixture"}, OriginGroupID: "origin:conflict", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "conflict"},
		},
		Temporal: []evidencegraph.TemporalRecord{
			{ID: "temporal:a", Status: evidencegraph.TemporalCurrent},
			{ID: "temporal:b", Status: evidencegraph.TemporalCurrent},
			{ID: "temporal:c", Status: evidencegraph.TemporalCurrent},
			{ID: "temporal:stale", Status: evidencegraph.TemporalStale},
			{ID: "temporal:derived", Status: evidencegraph.TemporalCurrent},
		},
		Integrity: integrity,
		Derivations: []evidencegraph.DerivationRecord{
			{ID: "derivation:derived", NodeID: "derived", Parents: []string{"source:a", "source:c"}, Method: "conjunction", Producer: "test", TraceRef: "derived", ProvenanceRef: "provenance:derived"},
		},
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	return artifact
}
