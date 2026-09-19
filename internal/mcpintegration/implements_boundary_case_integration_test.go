//go:build integration

package mcpintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceimplements"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcprelations"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestIntegrationEvidenceBoundaryImplementsCase exercises the actual restricted
// backend and public query API, without an MCP transport process. Every approval
// is a synthetic APPROVAL STUB in an explicitly selected disposable database.
func TestIntegrationEvidenceBoundaryImplementsCase(t *testing.T) {
	if os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN") == "" {
		t.Fatal("an explicitly selected non-production AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is required; this experiment must not silently skip")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git is required for the synthetic repository fixture")
	}
	// The existing fixture commits only a temporary synthetic repository. Fixed
	// dates make its code identity independent of the experiment condition.
	t.Setenv("GIT_AUTHOR_DATE", "2026-09-19T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-09-19T00:00:00Z")
	var conditions []map[string]any
	for _, test := range []struct {
		id       string
		action   string
		decision string
		mismatch bool
		outcome  string
	}{
		{"endpoints_only", "none", "", false, "no_write"},
		{"review_only", "read_review", "", false, "review_only"},
		{"missing_approval", "submit", "", false, "approval_required"},
		{"mismatched_review", "submit", "approved", true, "review_conflict"},
		{"approved_relation", "submit", "approved", false, "relation_admitted"},
	} {
		t.Run(test.id, func(t *testing.T) {
			ctx, fixture := relationFixture(t)
			input, native := runtimeRecursiveFixture(t, ctx, fixture.pool, "independent-relation")
			baseline := implementsCaseState(t, ctx, fixture.pool)
			if baseline.Counts.ImplementsReceipts != 0 {
				t.Fatal("fixture setup unexpectedly admitted an implements relation")
			}
			backend, err := mcprelations.NewBackend(relationRuntimePool(t, ctx, fixture), runtimeauth.Principal{ID: "synthetic:relation-reviewer"})
			if err != nil {
				t.Fatal(err)
			}
			query := implementsCaseQueryServer(t, ctx, fixture)
			specID, codeID := input.Review.SpecificationNodeID, input.Review.ImplementationNodeID
			spec, err := query.GetEvidenceRecord(ctx, evidencequerymcp.GetEvidenceRecordRequest{CanonicalID: specID})
			if err != nil {
				t.Fatal(err)
			}
			code, err := query.GetEvidenceRecord(ctx, evidencequerymcp.GetEvidenceRecordRequest{CanonicalID: codeID})
			if err != nil {
				t.Fatal(err)
			}
			if spec.AdmissionOutcome != "admitted" || code.AdmissionOutcome != "admitted" || spec.Canonical == nil || code.Canonical == nil ||
				spec.Canonical.NodeKind != string(evidencegraph.CanonicalDerivedClaim) || spec.Canonical.Payload.SourceType != "derived" ||
				code.Canonical.NodeKind != string(evidencegraph.CanonicalSourceClaim) || code.Canonical.Payload.SourceType != evidenceimplements.CodeSourceType || code.CodeFact == nil {
				t.Fatal("experiment requires an admitted derived specification and repository-backed code endpoint")
			}
			if err := evidenceimplements.ValidateRecursiveReviewBasis(native.Cut, native.Basis); err != nil {
				t.Fatalf("complete native AND ancestry validation failed: %v", err)
			}
			pairBefore := implementsCasePair(t, ctx, fixture.pool, query, specID, codeID)
			if pairBefore.PairCount != 0 || pairBefore.TotalOutgoing != 0 {
				t.Fatal("fixture setup unexpectedly created a direct implements edge")
			}
			var review mcprelations.ImplementsReviewResponse
			clientHasReview := test.action == "submit"
			if clientHasReview {
				review = implementsCaseLoadReview(t, ctx, backend, input.Review)
				if review.Subject != native.Subject || review.Display != native.Display {
					t.Fatal("backend review differs from native complete AND review")
				}
			}
			before := implementsCaseState(t, ctx, fixture.pool)
			if before != baseline {
				t.Fatal("endpoint reads or relation review preparation wrote authority")
			}
			request := mcprelations.ImplementsAdmissionRequest{
				RequestID: "synthetic-independent-relation", Review: input.Review,
				ExpectedSubject: review.Subject, Decision: test.decision,
				DecisionReason: "APPROVAL STUB: synthetic independent relation decision; not human approval or execution proof",
			}
			if test.mismatch {
				request.ExpectedSubject.DisplayID = implementsCaseDifferentID(request.ExpectedSubject.DisplayID)
			}
			facts := map[string]any{
				"endpoints": []map[string]any{
					{"node_id": specID, "endpoint_kind": "derived_spec", "payload_source_type": spec.Canonical.Payload.SourceType, "canonical_node_kind": spec.Canonical.NodeKind, "admission_outcome": spec.AdmissionOutcome, "claim": spec.StatementText},
					{"node_id": codeID, "endpoint_kind": "repository_code", "payload_source_type": code.Canonical.Payload.SourceType, "canonical_node_kind": code.Canonical.NodeKind, "admission_outcome": code.AdmissionOutcome, "qualified_name": code.CodeFact.QualifiedName},
				},
				"and_ancestry": map[string]any{"complete_native_validation_passed": true, "root_node_id": native.Basis.RootNodeID,
					"derived_node_count": len(native.Basis.Ancestors.Derivations), "source_leaf_count": len(native.Basis.SourceLeaves), "derivations": native.Basis.Ancestors.Derivations},
				"relation_lookup_before":               pairBefore,
				"independent_relation_receipts_before": before.Counts.ImplementsReceipts,
				"proposed_action":                      test.action, "request_decision": test.decision,
				"request_decision_reason":                       request.DecisionReason,
				"client_obtained_relation_review_before_action": clientHasReview,
				"captured_subject":                              review.Subject, "submitted_expected_subject": request.ExpectedSubject,
				"submitted_subject_matches_captured_review": clientHasReview && request.ExpectedSubject == review.Subject,
				"execution_tests_supplied":                  false,
			}

			var result evidenceimplements.DerivedAdmissionResult
			var callErr error
			var rawResult json.RawMessage
			switch test.action {
			case "none":
			case "read_review":
				review = implementsCaseLoadReview(t, ctx, backend, input.Review)
				if review.Subject != native.Subject || review.Display != native.Display {
					t.Fatal("returned review differs from the native complete display")
				}
			case "submit":
				args, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				rawResult, callErr = backend.CallTool(ctx, mcprelations.ToolAdmitReviewedImplements, args)
				if callErr == nil {
					if err := json.Unmarshal(rawResult, &result); err != nil {
						t.Fatal(err)
					}
				}
			default:
				t.Fatal("unknown experiment action")
			}
			switch test.outcome {
			case "approval_required":
				if callErr == nil || callErr.Error() != "explicit decision=approved is required for this exact relation; node approval is insufficient" || rawResult != nil {
					t.Fatalf("missing approval did not fail at the explicit approval gate: %v", callErr)
				}
			case "review_conflict":
				if !errors.Is(callErr, evidenceimplements.ErrReplayConflict) || rawResult != nil {
					t.Fatalf("mismatched review did not reject its exact subject: %v", callErr)
				}
			case "relation_admitted":
				if callErr != nil || result.Replayed || result.Edge.From != specID || result.Edge.To != codeID ||
					result.Edge.Relation != evidencegraph.CanonicalImplements || result.Receipt.Subject != review.Subject ||
					result.Receipt.Display != review.Display || result.Receipt.ReviewerID != "synthetic:relation-reviewer" {
					t.Fatalf("approved exact relation did not produce the expected independent receipt: %v", callErr)
				}
			}
			after := implementsCaseState(t, ctx, fixture.pool)
			wantCounts := before.Counts
			if test.outcome == "relation_admitted" {
				wantCounts.Edges++
				wantCounts.ImplementsReceipts++
				if before.UnchangedAuthorityHash != after.UnchangedAuthorityHash {
					t.Fatal("relation admission changed nodes, ancestry, original decisions, or existing edges")
				}
			} else if after != before {
				t.Fatal("no-op, read-only review, or rejected relation submission changed authority rows")
			}
			if after.Counts != wantCounts {
				t.Fatalf("unexpected relation authority counts: got %+v, want %+v", after.Counts, wantCounts)
			}
			pairAfter := implementsCasePair(t, ctx, fixture.pool, query, specID, codeID)
			reverse := implementsCasePair(t, ctx, fixture.pool, query, codeID, specID)
			wantPairs := 0
			if test.outcome == "relation_admitted" {
				wantPairs = 1
			}
			if pairAfter.PairCount != wantPairs || reverse.PairCount != 0 || reverse.TotalOutgoing != 0 {
				t.Fatal("public relation query did not preserve the directed root-to-code boundary")
			}
			ancestorQueries := []implementsCasePairLookup{}
			for _, node := range native.Basis.Ancestors.Nodes {
				if node.ID == specID {
					continue
				}
				lookup := implementsCasePair(t, ctx, fixture.pool, query, node.ID, codeID)
				if lookup.PairCount != 0 || lookup.TotalOutgoing != 0 {
					t.Fatal("relation admission manufactured an ancestor-to-code relation")
				}
				ancestorQueries = append(ancestorQueries, lookup)
			}
			provenance := map[string]any{"receipt_exists": false}
			var nativeProvenance any
			if test.outcome == "relation_admitted" {
				read, err := query.GetRelationProvenance(ctx, evidencequerymcp.GetRelationProvenanceRequest{CanonicalEdgeID: result.Edge.ID})
				if err != nil || read.ImplementsAdmission == nil {
					t.Fatalf("public query could not read the independent receipt: %v", err)
				}
				authority := read.ImplementsAdmission
				var receipt evidenceimplements.DerivedReviewReceipt
				if err := json.Unmarshal([]byte(authority.ReceiptPayloadUTF8), &receipt); err != nil {
					t.Fatal(err)
				}
				if authority.ContractVersion != evidenceimplements.RecursiveAdmissionContract || authority.DisplayPayloadUTF8 != review.Display.PayloadUTF8 ||
					authority.DisplayID != review.Display.ID || authority.ReceiptID != result.Receipt.ID || !reflect.DeepEqual(receipt, result.Receipt) {
					t.Fatal("public provenance lost the exact independent review binding")
				}
				provenance = map[string]any{"receipt_exists": true, "contract_version": authority.ContractVersion, "receipt_id": authority.ReceiptID,
					"display_id": authority.DisplayID, "display_bytes_match_review": true, "receipt_matches_result": true, "reviewer_id": authority.ReviewerID}
				nativeProvenance = authority
				// Exact replay is a deterministic persistence check outside the model
				// cells. It must preserve the first edge, receipt, and every row.
				args, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				replayBody, err := backend.CallTool(ctx, mcprelations.ToolAdmitReviewedImplements, args)
				if err != nil {
					t.Fatal(err)
				}
				var replay evidenceimplements.DerivedAdmissionResult
				if err := json.Unmarshal(replayBody, &replay); err != nil || !replay.Replayed {
					t.Fatalf("exact relation replay failed: %v", err)
				}
				replay.Replayed = false
				if !reflect.DeepEqual(result, replay) || implementsCaseState(t, ctx, fixture.pool) != after {
					t.Fatal("exact relation replay changed the original receipt or authority")
				}
			}
			errorText := ""
			if callErr != nil {
				errorText = callErr.Error()
			}
			conditions = append(conditions, map[string]any{
				"id": test.id, "facts": facts,
				"observation": map[string]any{
					"operation_outcome": test.outcome, "error": errorText, "before": before, "after": after,
					"exact_pair_after": pairAfter, "reverse_pair_after": reverse, "ancestor_pairs_after": ancestorQueries,
					"independent_relation_provenance": provenance, "runtime_correctness_proven": false,
				},
				"raw_native": map[string]any{"review": native, "returned_review": review, "admission_result": result, "public_provenance": nativeProvenance},
			})
		})
	}
	if t.Failed() || len(conditions) != 5 {
		t.Fatal("all five conditions must pass before exporting the experiment")
	}
	data, err := json.Marshal(map[string]any{"version": "implements-boundary-case/v1", "synthetic": true,
		"decision_scope": "APPROVAL STUB: isolated non-production experiment; not human approval or execution proof", "conditions": conditions})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/Users/", "/private/", "/var/folders/", "postgres://", "postgresql://"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("experiment export contains an unexpected machine path or connection string")
		}
	}
	t.Log("IMPLEMENTS_BOUNDARY_CASE_V1=" + string(data))
}

func implementsCaseLoadReview(t *testing.T, ctx context.Context, backend *mcprelations.Backend, request evidenceimplements.RecursiveReviewRequest) mcprelations.ImplementsReviewResponse {
	t.Helper()
	args, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := backend.CallTool(ctx, mcprelations.ToolGetImplementsReview, args)
	if err != nil {
		t.Fatal(err)
	}
	var review mcprelations.ImplementsReviewResponse
	if err := json.Unmarshal(body, &review); err != nil {
		t.Fatal(err)
	}
	return review
}

func implementsCaseQueryServer(t *testing.T, ctx context.Context, fixture authorityProcessFixture) *evidencequerymcp.Server {
	t.Helper()
	config, err := pgxpool.ParseConfig(fixture.query.dsn)
	if err != nil {
		t.Fatal("invalid isolated query connection")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: fixture.query.group, Schema: fixture.schema, Profile: dbrole.ProfileQuery})
	if err != nil {
		t.Fatal("cannot open restricted query pool")
	}
	t.Cleanup(pool.Close)
	query, err := evidencequerymcp.NewServer(pool)
	if err != nil {
		t.Fatal(err)
	}
	return query
}

type implementsCaseCounts struct {
	Nodes              int `json:"canonical_nodes"`
	Edges              int `json:"canonical_edges"`
	NodeDecisions      int `json:"node_admission_decisions"`
	ImplementsReceipts int `json:"implements_admission_receipts"`
	ReferencesReceipts int `json:"references_admission_receipts"`
}

type implementsCaseAuthorityState struct {
	Counts                 implementsCaseCounts `json:"counts"`
	FullAuthorityHash      string               `json:"full_authority_rows_sha256"`
	UnchangedAuthorityHash string               `json:"non_implements_authority_rows_sha256"`
}

func implementsCaseState(t *testing.T, ctx context.Context, pool *pgxpool.Pool) implementsCaseAuthorityState {
	t.Helper()
	counts := relationCounts(t, ctx, pool)
	var all, unchanged strings.Builder
	for _, table := range []string{
		"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_implements_admissions", "canonical_references_admissions",
		"canonical_derivations", "canonical_derivation_parents", "canonical_ordinary_admission_manifests",
		"canonical_ordinary_admission_node_bindings", "canonical_ordinary_admission_edge_bindings", "proposal_occurrences",
	} {
		var rows string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(row_data ORDER BY row_data::text), '[]'::jsonb)::text
			FROM (SELECT to_jsonb(r) row_data FROM `+table+` r) snapshot`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		all.WriteString(table + "\n" + rows + "\n")
		if table == "canonical_implements_admissions" {
			continue
		}
		if table == "canonical_graph_edges" {
			if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(row_data ORDER BY row_data::text), '[]'::jsonb)::text
				FROM (SELECT to_jsonb(r) row_data FROM canonical_graph_edges r WHERE relation <> 'implements') snapshot`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
		}
		unchanged.WriteString(table + "\n" + rows + "\n")
	}
	return implementsCaseAuthorityState{Counts: implementsCaseCounts{counts[0], counts[1], counts[2], counts[3], counts[4]},
		FullAuthorityHash: implementsCaseHash(all.String()), UnchangedAuthorityHash: implementsCaseHash(unchanged.String())}
}

type implementsCasePairLookup struct {
	FromID        string   `json:"from_node_id"`
	ToID          string   `json:"to_node_id"`
	Relation      string   `json:"relation"`
	Direction     string   `json:"direction"`
	PairCount     int      `json:"exact_pair_count"`
	TotalOutgoing int      `json:"total_outgoing_relation_count"`
	EdgeIDs       []string `json:"exact_pair_edge_ids"`
	Complete      bool     `json:"bounded_lookup_complete"`
}

func implementsCasePair(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query *evidencequerymcp.Server, from, to string) implementsCasePairLookup {
	t.Helper()
	read, err := query.ListEvidenceNeighbors(ctx, evidencequerymcp.ListEvidenceNeighborsRequest{CanonicalID: from, Direction: "outgoing", Relation: "implements", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	lookup := implementsCasePairLookup{FromID: from, ToID: to, Relation: "implements", Direction: "outgoing", EdgeIDs: []string{}, TotalOutgoing: read.Count}
	for _, neighbor := range read.Neighbors {
		edge := neighbor.Relation.CanonicalEdge
		if edge == nil || edge.From.ID != from || edge.Relation != "implements" {
			t.Fatal("public query returned an inconsistent directed relation")
		}
		if edge.To.ID == to {
			lookup.PairCount++
			lookup.EdgeIDs = append(lookup.EdgeIDs, neighbor.Relation.RelationRef.ID)
		}
	}
	var all, pair int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE to_node_id = $2)
		FROM canonical_graph_edges WHERE from_node_id = $1 AND relation = 'implements'`, from, to).Scan(&all, &pair); err != nil {
		t.Fatal(err)
	}
	if read.Count != len(read.Neighbors) || all != read.Count || pair != lookup.PairCount || read.Count >= read.Limit {
		t.Fatal("public query is incomplete or differs from the exact authoritative relation rows")
	}
	lookup.Complete = true
	return lookup
}

func implementsCaseDifferentID(id string) string {
	if strings.HasSuffix(id, "0") {
		return id[:len(id)-1] + "1"
	}
	return id[:len(id)-1] + "0"
}

func implementsCaseHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
