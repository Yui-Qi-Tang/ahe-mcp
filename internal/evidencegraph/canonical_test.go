package evidencegraph

import (
	"strings"
	"testing"
)

func TestCanonicalArtifactValidate(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	if err := artifact.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestCanonicalArtifactRejectsIntegrityMismatch(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	artifact.Payloads[0].Title = "changed without a new digest"
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("Validate() error = %v, want integrity mismatch", err)
	}
}

func TestCanonicalArtifactRejectsMissingDerivation(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	artifact.Derivations = nil
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "requires a derivation record") {
		t.Fatalf("Validate() error = %v, want missing derivation", err)
	}
}

func TestCanonicalArtifactRejectsDanglingEdge(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	artifact.Edges[0].To = "missing"
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "is not defined") {
		t.Fatalf("Validate() error = %v, want dangling edge", err)
	}
}

func TestCanonicalArtifactRejectsDuplicateNodeID(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	artifact.Nodes = append(artifact.Nodes, artifact.Nodes[0])
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate node") {
		t.Fatalf("Validate() error = %v, want duplicate node", err)
	}
}

func TestCanonicalArtifactRejectsUnknownRelation(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	artifact.Edges[0].Relation = "semantic_similarity"
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "invalid relation") {
		t.Fatalf("Validate() error = %v, want invalid relation", err)
	}
}

func TestCanonicalArtifactRejectsMissingProvenance(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	artifact.Nodes[0].ProvenanceRef = "missing"
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "provenance_ref") {
		t.Fatalf("Validate() error = %v, want missing provenance", err)
	}
}

func TestCanonicalArtifactRejectsUndeclaredDerivedEdge(t *testing.T) {
	artifact := canonicalTestArtifact(t)
	artifact.Edges = append(artifact.Edges, CanonicalEdge{
		ID: "edge:a-b", From: "a", To: "b", Relation: CanonicalDerivedFrom, ProvenanceRef: "provenance:a",
	})
	if err := artifact.Validate(); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("Validate() error = %v, want undeclared derived edge", err)
	}
}

func canonicalTestArtifact(t *testing.T) CanonicalArtifact {
	t.Helper()
	payloads := []EvidencePayload{
		{ID: "payload:a", SourceType: "doc", Title: "A", Source: "fixture-a", Span: "alpha"},
		{ID: "payload:b", SourceType: "doc", Title: "B", Source: "fixture-b", Span: "beta"},
		{ID: "payload:c", SourceType: "derived", Title: "C", Source: "fixture", Claim: "alpha and beta"},
	}
	var integrity []IntegrityRecord
	for _, payload := range payloads {
		digest, err := PayloadDigest(payload)
		if err != nil {
			t.Fatal(err)
		}
		integrity = append(integrity, IntegrityRecord{ID: "integrity:" + payload.ID, Algorithm: "sha256", Digest: digest})
	}
	return CanonicalArtifact{
		SchemaVersion: CanonicalSchemaVersion,
		SnapshotID:    "fixture",
		Nodes: []CanonicalNode{
			{ID: "a", Kind: CanonicalRawEvidence, PayloadRef: "payload:a", ProvenanceRef: "provenance:a", TemporalRef: "temporal:a", IntegrityRef: "integrity:payload:a"},
			{ID: "b", Kind: CanonicalRawEvidence, PayloadRef: "payload:b", ProvenanceRef: "provenance:b", TemporalRef: "temporal:b", IntegrityRef: "integrity:payload:b"},
			{ID: "c", Kind: CanonicalDerivedClaim, PayloadRef: "payload:c", ProvenanceRef: "provenance:c", TemporalRef: "temporal:c", IntegrityRef: "integrity:payload:c"},
		},
		Edges: []CanonicalEdge{
			{ID: "edge:a-c", From: "a", To: "c", Relation: CanonicalDerivedFrom, ProvenanceRef: "provenance:c"},
			{ID: "edge:b-c", From: "b", To: "c", Relation: CanonicalDerivedFrom, ProvenanceRef: "provenance:c"},
		},
		Payloads: payloads,
		Provenance: []ProvenanceRecord{
			{ID: "provenance:a", OriginRefs: []string{"fixture-a"}, OriginGroupID: "origin:a", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "trace:a"},
			{ID: "provenance:b", OriginRefs: []string{"fixture-b"}, OriginGroupID: "origin:b", Producer: "test", Method: "observed", MethodVersion: "v1", TraceRef: "trace:b"},
			{ID: "provenance:c", OriginRefs: []string{"fixture-a", "fixture-b"}, OriginGroupID: "derived:c", Producer: "test", Method: "conjunction", MethodVersion: "v1", TraceRef: "trace:c"},
		},
		Temporal: []TemporalRecord{
			{ID: "temporal:a", Status: TemporalCurrent},
			{ID: "temporal:b", Status: TemporalCurrent},
			{ID: "temporal:c", Status: TemporalCurrent},
		},
		Integrity: integrity,
		Derivations: []DerivationRecord{
			{ID: "derivation:c", NodeID: "c", Parents: []string{"a", "b"}, Method: "conjunction", Producer: "test", TraceRef: "trace:c", ProvenanceRef: "provenance:c"},
		},
	}
}
