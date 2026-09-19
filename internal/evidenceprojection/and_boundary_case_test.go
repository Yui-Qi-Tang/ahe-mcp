package evidenceprojection

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	graph "github.com/Yui-Qi-Tang/graph"
)

const andCaseRequirement = "Webhook readiness requires BOTH signature-verification evidence AND anti-replay evidence."

// TestEvidenceBoundaryANDCase compares ordinary reachability with the declared
// AND contract. All records are authored fixtures, not database admissions.
func TestEvidenceBoundaryANDCase(t *testing.T) {
	conditions := []map[string]any{}
	for _, test := range []struct {
		name         string
		includeNode  bool
		includeEdge  bool
		declareBoth  bool
		errorWitness string
	}{
		{"complete", true, true, true, ""},
		{"missing_edge", true, false, true, `parent "anti_replay" is missing derived_from edge to "ready"`},
		{"missing_parent", false, false, true, `parent node "anti_replay" is not defined`},
		{"underdeclared", false, false, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := andCaseArtifact(t, test.includeNode, test.includeEdge, test.declareBoth)
			// This explicitly bypasses semantic preparation only for the
			// structural baseline. It is not a public AHE read-view operation.
			topology, err := newCanonicalTopology(artifact)
			if err != nil {
				t.Fatal(err)
			}
			path, found := graph.FindPath[string, string, evidencegraph.CanonicalEdge](topology, "signature", "ready")
			if !found || !reflect.DeepEqual(path.Nodes, []string{"signature", "ready"}) || len(path.Edges) != 1 {
				t.Fatalf("controlled signature path changed: %+v, found=%t", path, found)
			}
			validation := "accepted"
			message := ""
			prepared, err := PrepareTopology(artifact)
			if test.errorWitness == "" {
				if err != nil {
					t.Fatalf("valid declared contract rejected: %v", err)
				}
				witness, err := prepared.FindPath(PathQuery{FromNodeID: "signature", ToNodeID: "ready",
					Relations: []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom}})
				if err != nil || !witness.Found || !reflect.DeepEqual(witness.NodeIDs, path.Nodes) {
					t.Fatalf("public path query disagrees: %+v, error=%v", witness, err)
				}
			} else {
				if err == nil || prepared != nil || !strings.Contains(err.Error(), test.errorWitness) {
					t.Fatalf("PrepareTopology() = %v, want %q and no prepared view", err, test.errorWitness)
				}
				validation, message = "rejected", err.Error()
			}
			facts := map[string]any{
				"source_requirement": andCaseRequirement,
				"node_meanings": map[string]string{"signature": "signature-verification evidence",
					"anti_replay": "anti-replay evidence", "ready": "derived webhook readiness claim"},
				"nodes": artifact.Nodes, "edges": artifact.Edges, "derivation": artifact.Derivations[0],
				"record_origin": "synthetic in-memory fixture; no execution or database admission",
			}
			conditions = append(conditions, map[string]any{
				"id": test.name, "facts": facts, "artifact": artifact,
				"structural_path": map[string]any{"found": found, "nodes": path.Nodes,
					"edge_ids": []string{path.Edges[0].ID}, "relation": "derived_from",
					"algorithm": "github.com/Yui-Qi-Tang/graph.FindPath"},
				"ahe_domain_result": map[string]any{"function": "evidenceprojection.PrepareTopology",
					"validation": validation, "error": message, "database_or_admission_writer_used": false},
			})
			t.Logf("structural path found=%t; AHE declared-AND validation=%s; %s", found, validation, message)
		})
	}
	data, err := json.Marshal(map[string]any{"schema": "and-boundary-case/v1", "conditions": conditions})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("AND_CASE_V1=" + string(data))
}

func andCaseArtifact(t *testing.T, includeNode, includeEdge, declareBoth bool) evidencegraph.CanonicalArtifact {
	t.Helper()
	artifact := evidencegraph.CanonicalArtifact{
		SchemaVersion: evidencegraph.CanonicalSchemaVersion, SnapshotID: "synthetic-and-case-v1",
		Provenance: []evidencegraph.ProvenanceRecord{{ID: "provenance:fixture", OriginRefs: []string{"synthetic-spec"},
			OriginGroupID: "origin:fixture", Producer: "test", Method: "authored-fixture", MethodVersion: "v1", TraceRef: "fixture:v1"}},
		Temporal: []evidencegraph.TemporalRecord{{ID: "temporal:fixture", Status: evidencegraph.TemporalCurrent}},
		Edges: []evidencegraph.CanonicalEdge{{ID: "edge:signature-ready", From: "signature", To: "ready",
			Relation: evidencegraph.CanonicalDerivedFrom, ProvenanceRef: "provenance:fixture"}},
		Derivations: []evidencegraph.DerivationRecord{{ID: "derivation:ready", NodeID: "ready", Parents: []string{"signature"},
			Method: "conjunction", Producer: "test", TraceRef: "fixture:v1", ProvenanceRef: "provenance:fixture"}},
	}
	if includeEdge {
		artifact.Edges = append(artifact.Edges, evidencegraph.CanonicalEdge{ID: "edge:anti-replay-ready", From: "anti_replay", To: "ready",
			Relation: evidencegraph.CanonicalDerivedFrom, ProvenanceRef: "provenance:fixture"})
	}
	if declareBoth {
		artifact.Derivations[0].Parents = append(artifact.Derivations[0].Parents, "anti_replay")
	}
	for _, item := range []struct {
		id    string
		claim string
		kind  evidencegraph.CanonicalNodeKind
	}{
		{"signature", "Signature-verification evidence is present in the fixture.", evidencegraph.CanonicalSourceClaim},
		{"anti_replay", "Anti-replay evidence is present in the fixture.", evidencegraph.CanonicalSourceClaim},
		{"ready", andCaseRequirement, evidencegraph.CanonicalDerivedClaim},
	} {
		if item.id == "anti_replay" && !includeNode {
			continue
		}
		payload := evidencegraph.EvidencePayload{ID: "payload:" + item.id, SourceType: "synthetic", Title: item.id,
			Source: "fixture:and-spec", Claim: item.claim}
		digest, err := evidencegraph.PayloadDigest(payload)
		if err != nil {
			t.Fatal(err)
		}
		artifact.Payloads = append(artifact.Payloads, payload)
		artifact.Integrity = append(artifact.Integrity, evidencegraph.IntegrityRecord{
			ID: "integrity:" + item.id, Algorithm: "sha256", Digest: digest})
		artifact.Nodes = append(artifact.Nodes, evidencegraph.CanonicalNode{ID: item.id, Kind: item.kind,
			PayloadRef: payload.ID, ProvenanceRef: "provenance:fixture", TemporalRef: "temporal:fixture", IntegrityRef: "integrity:" + item.id})
	}
	return artifact
}
