//go:build integration

package evidenceingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestIntegrationEvidenceBoundaryStaleReviewCase runs sequential lifecycle transitions against
// isolated PostgreSQL schemas. All decisions are synthetic approval stubs, not
// human approvals. It makes no claim about simultaneous transaction races.
func TestIntegrationEvidenceBoundaryStaleReviewCase(t *testing.T) {
	if os.Getenv("DATABASE_DSN") == "" {
		t.Fatal("an explicitly selected non-production DATABASE_DSN is required; this experiment must not silently skip")
	}
	var conditions []map[string]any
	for _, test := range []struct {
		id           string
		intervention string
		wantStatus   string
		wantError    ErrorKind
		wantReplay   bool
	}{
		{"pending_unchanged", "none", admissionOutcomePending, "", false},
		{"rejected_after_review", "reject_by_other", admissionOutcomeRejected, ErrorAdmissionStateConflict, false},
		{"audit_only_after_review", "audit_only_by_other", admissionOutcomeAuditOnly, ErrorAdmissionStateConflict, false},
		{"admitted_by_other_after_review", "admit_by_other", admissionOutcomeAdmitted, ErrorAdmissionReplayConflict, false},
		{"exact_replay", "admit_same_request", admissionOutcomeAdmitted, "", true},
	} {
		t.Run(test.id, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			// Identical synthetic content and identifiers in independent schemas
			// keep condition labels out of the evidence supplied to the model.
			fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "stale-review-case", "revision-1")
			original, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
			if err != nil {
				t.Fatal(err)
			}
			if len(original.SourceRefs) != 1 || original.StatementText != original.SourceRefs[0].QuotedText {
				t.Fatal("experiment requires one exact source quote equal to the proposal claim")
			}
			originalBytes := staleReviewCaseSourceBytes(t, ctx, pool, original)
			display, subject, err := LoadSourceClaimReviewDisplayArtifact(ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
			if err != nil {
				t.Fatal(err)
			}
			input := ReviewedSourceClaimAdmissionInput{
				ExtractionAttemptID: fixture.ExtractionAttemptID,
				ExpectedSubject:     subject,
				DecisionBy:          "synthetic-reviewer-A",
				DecisionReason:      "APPROVAL STUB: synthetic stale-review experiment; not human approval",
			}
			atReview := staleReviewCaseState(t, ctx, pool, fixture.ProposalOccurrenceID)
			if atReview.Counts != (staleReviewCaseCounts{}) || atReview.ProposalStatus != admissionOutcomePending || atReview.CanonicalRef != "" {
				t.Fatalf("t0 must be pending without authority records: %+v", atReview)
			}

			other := input
			other.DecisionBy = "synthetic-reviewer-B"
			var firstAdmission AdmissionResult
			switch test.intervention {
			case "none":
			case "reject_by_other", "audit_only_by_other":
				disposition, err := RecordReviewedSourceClaimDisposition(ctx, pool, ReviewedSourceClaimDispositionInput{
					ExtractionAttemptID: other.ExtractionAttemptID, ExpectedSubject: other.ExpectedSubject,
					Outcome: test.wantStatus, DecisionBy: other.DecisionBy,
					DecisionReason: "APPROVAL STUB: synthetic terminal disposition; not human approval",
				})
				if err != nil || disposition.Replayed || disposition.AdmissionOutcome != test.wantStatus {
					t.Fatalf("t1 reviewed disposition = %+v, error = %v", disposition, err)
				}
			case "admit_by_other", "admit_same_request":
				interveningInput := other
				if test.intervention == "admit_same_request" {
					interveningInput = input
				}
				firstAdmission, err = AdmitReviewedSourceClaim(ctx, pool, interveningInput)
				if err != nil || firstAdmission.Replayed || firstAdmission.CanonicalRef == "" {
					t.Fatalf("t1 reviewed admission = %+v, error = %v", firstAdmission, err)
				}
				assertPersistedSourceClaimReviewBinding(t, ctx, pool, firstAdmission, interveningInput)
			default:
				t.Fatalf("unknown intervention %q", test.intervention)
			}

			before := staleReviewCaseState(t, ctx, pool, fixture.ProposalOccurrenceID)
			if before.ProposalStatus != test.wantStatus {
				t.Fatalf("t1 status = %s, want %s", before.ProposalStatus, test.wantStatus)
			}
			switch test.intervention {
			case "reject_by_other", "audit_only_by_other":
				if before.Counts != (staleReviewCaseCounts{Decisions: 1, DispositionBindings: 1}) || before.CanonicalRef != "" || before.DecisionBy != other.DecisionBy {
					t.Fatalf("t1 disposition changed unexpected authority: %+v", before)
				}
			case "admit_by_other":
				staleReviewCaseAssertAdmitted(t, before, other, firstAdmission)
			case "admit_same_request":
				staleReviewCaseAssertAdmitted(t, before, input, firstAdmission)
			}
			current, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
			if err != nil {
				t.Fatal(err)
			}
			staleReviewCaseAssertSourceUnchanged(t, original, current)
			if !bytes.Equal(originalBytes, staleReviewCaseSourceBytes(t, ctx, pool, current)) {
				t.Fatal("persisted source bytes changed between t0 and t1")
			}
			storedBinding := staleReviewCaseStoredBinding(t, ctx, pool, before, subject, display)
			freshDisplay, freshSubject, refreshErr := LoadSourceClaimReviewDisplayArtifact(ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
			refreshResult := "available"
			if before.ProposalStatus == admissionOutcomePending {
				if refreshErr != nil || freshDisplay != display || freshSubject != subject {
					t.Fatalf("unchanged pending review refresh differs: %v", refreshErr)
				}
			} else {
				assertKind(t, refreshErr, ErrorReviewContractConflict)
				refreshResult = string(ErrorReviewContractConflict)
			}

			result, submitErr := AdmitReviewedSourceClaim(ctx, pool, input)
			requestOutcome := "new_admission"
			if test.wantError != "" {
				assertKind(t, submitErr, test.wantError)
				if !reflect.DeepEqual(result, AdmissionResult{}) {
					t.Fatal("failed admission returned nonempty mutation result")
				}
				requestOutcome = string(test.wantError)
			} else {
				if submitErr != nil || result.Replayed != test.wantReplay || result.AdmissionOutcome != admissionOutcomeAdmitted || result.CanonicalRef == "" {
					t.Fatalf("t2 admission = %+v, error = %v", result, submitErr)
				}
				assertPersistedSourceClaimReviewBinding(t, ctx, pool, result, input)
				if test.wantReplay {
					requestOutcome = "exact_replay"
					withoutReplay := result
					withoutReplay.Replayed = false
					if !reflect.DeepEqual(withoutReplay, firstAdmission) {
						t.Fatal("exact replay changed the original admission result")
					}
				}
			}
			after := staleReviewCaseState(t, ctx, pool, fixture.ProposalOccurrenceID)
			if test.wantError != "" || test.wantReplay {
				if after != before {
					t.Fatalf("t2 changed authority or lifecycle state: before=%+v after=%+v", before, after)
				}
			} else {
				staleReviewCaseAssertAdmitted(t, after, input, result)
			}
			final, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
			if err != nil {
				t.Fatal(err)
			}
			staleReviewCaseAssertSourceUnchanged(t, original, final)
			if !bytes.Equal(originalBytes, staleReviewCaseSourceBytes(t, ctx, pool, final)) {
				t.Fatal("persisted source bytes changed between t0 and t2")
			}
			if after.CanonicalRef != "" {
				canonical, err := GetCanonicalEvidenceByID(ctx, pool, after.CanonicalRef)
				if err != nil || canonical.Payload.Claim != original.StatementText || canonical.OriginProposalOccurrenceID != fixture.ProposalOccurrenceID {
					t.Fatalf("canonical claim readback differs: %v", err)
				}
			}
			conditions = append(conditions, map[string]any{
				"id": test.id,
				"facts": map[string]any{
					"sequence": []string{"t0: reviewer A captures pending native review", "t1: " + test.intervention, "t2: reviewer A submits the unchanged t0 admission input"},
					"source": map[string]any{
						"claim": original.StatementText, "exact_quote": original.SourceRefs[0].QuotedText,
						"source_snapshot_id": original.SourceSnapshotID, "raw_content_hash": original.RawContentHash,
						"source_revision": original.SourceVersion, "source_refs": original.SourceRefs,
						"source_coverage": original.OriginMetadata["coverage"],
					},
					"reviewed_at_t0":                         map[string]any{"proposal_status": atReview.ProposalStatus, "reviewer": input.DecisionBy, "expected_subject": subject, "display": display},
					"current_at_t1":                          before,
					"stored_terminal_review_binding":         storedBinding,
					"submission_at_t2":                       input,
					"submitted_subject_matches_t0":           input.ExpectedSubject == subject,
					"source_refs_unchanged_at_t1":            reflect.DeepEqual(original.SourceRefs, current.SourceRefs),
					"persisted_source_bytes_unchanged_at_t1": true,
					"claim_unchanged_at_t1":                  original.StatementText == current.StatementText,
				},
				"observation": map[string]any{
					"function": "AdmitReviewedSourceClaim", "request_outcome": requestOutcome, "error_kind": test.wantError,
					"replayed": result.Replayed, "before": before, "after": after,
					"canonical_ref": after.CanonicalRef, "source_quote_still_matches": true,
					"persisted_source_bytes_unchanged_at_t2": true,
					"fresh_display_at_t1":                    refreshResult,
				},
			})
		})
	}
	if t.Failed() || len(conditions) != 5 {
		t.Fatal("all five lifecycle conditions must pass before exporting the experiment")
	}
	data, err := json.Marshal(map[string]any{
		"version": "stale-review-case/v1", "synthetic": true,
		"decision_scope": "APPROVAL STUB: isolated non-production integration test, not human approval",
		"conditions":     conditions,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("STALE_REVIEW_CASE_V1=" + string(data))
}

type staleReviewCaseCounts struct {
	Nodes               int `json:"canonical_nodes"`
	Edges               int `json:"canonical_edges"`
	Decisions           int `json:"admission_decisions"`
	OrdinaryManifests   int `json:"ordinary_manifests"`
	ReviewBindings      int `json:"admission_review_bindings"`
	DispositionBindings int `json:"disposition_review_bindings"`
	NodeBindings        int `json:"ordinary_node_bindings"`
	EdgeBindings        int `json:"ordinary_edge_bindings"`
}

type staleReviewCaseAuthorityState struct {
	Counts            staleReviewCaseCounts `json:"counts"`
	ProposalStatus    string                `json:"proposal_status"`
	CanonicalRef      string                `json:"canonical_ref"`
	DecisionID        string                `json:"decision_id"`
	DecisionBy        string                `json:"decision_by"`
	DecisionReason    string                `json:"decision_reason"`
	AuthorityRowsHash string                `json:"authority_rows_sha256"`
}

func staleReviewCaseState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, occurrenceID string) staleReviewCaseAuthorityState {
	t.Helper()
	counts := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	state := staleReviewCaseAuthorityState{Counts: staleReviewCaseCounts{
		Nodes: counts.Nodes, Edges: counts.Edges, Decisions: counts.Decisions,
		OrdinaryManifests: counts.OrdinaryManifests, ReviewBindings: counts.ReviewBindings,
	}}
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM source_claim_disposition_review_bindings),
		(SELECT count(*) FROM canonical_ordinary_admission_node_bindings),
		(SELECT count(*) FROM canonical_ordinary_admission_edge_bindings),
		p.admission_outcome, coalesce(p.canonical_ref, ''), coalesce(d.admission_decision_id, ''),
		coalesce(d.decision_by, ''), coalesce(d.decision_reason, '')
		FROM proposal_occurrences p LEFT JOIN admission_decisions d USING (proposal_occurrence_id)
		WHERE p.proposal_occurrence_id = $1`, occurrenceID).Scan(
		&state.Counts.DispositionBindings, &state.Counts.NodeBindings, &state.Counts.EdgeBindings,
		&state.ProposalStatus, &state.CanonicalRef, &state.DecisionID, &state.DecisionBy, &state.DecisionReason,
	); err != nil {
		t.Fatal(err)
	}
	var rows strings.Builder
	for _, table := range []string{
		"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions",
		"canonical_ordinary_admission_manifests", "canonical_ordinary_admission_node_bindings",
		"canonical_ordinary_admission_edge_bindings", "canonical_source_claim_review_bindings",
		"source_claim_disposition_review_bindings", "proposal_occurrences",
	} {
		var contents string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(row_data ORDER BY row_data::text), '[]'::jsonb)::text
			FROM (SELECT to_jsonb(r) AS row_data FROM `+table+` r) snapshot`).Scan(&contents); err != nil {
			t.Fatal(err)
		}
		rows.WriteString(table)
		rows.WriteByte('\n')
		rows.WriteString(contents)
		rows.WriteByte('\n')
	}
	state.AuthorityRowsHash = hashHex([]byte(rows.String()))
	return state
}

func staleReviewCaseStoredBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, state staleReviewCaseAuthorityState, subject ExactDisplayedReviewSubject, display SourceClaimReviewDisplayArtifact) map[string]any {
	t.Helper()
	if state.ProposalStatus == admissionOutcomePending {
		return map[string]any{"exists": false}
	}
	table := "source_claim_disposition_review_bindings"
	if state.ProposalStatus == admissionOutcomeAdmitted {
		table = "canonical_source_claim_review_bindings"
	}
	var stored ExactDisplayedReviewSubject
	var payload string
	if err := pool.QueryRow(ctx, `SELECT submission_receipt_id, proposal_manifest_id,
		proposal_occurrence_id, proposal_basis_id, review_package_id,
		review_display_artifact_id, review_display_payload_utf8 FROM `+table+`
		WHERE admission_decision_id = $1`, state.DecisionID).Scan(
		&stored.ReviewSubject.SubmissionReceiptID, &stored.ReviewSubject.ProposalManifestID,
		&stored.ReviewSubject.ProposalOccurrenceID, &stored.ReviewSubject.ProposalBasisID,
		&stored.ReviewSubject.ReviewPackageID, &stored.ReviewDisplayArtifactID, &payload,
	); err != nil {
		t.Fatal(err)
	}
	if stored != subject || payload != display.PayloadUTF8 {
		t.Fatal("intervening decision did not preserve the original exact native review subject and display")
	}
	return map[string]any{"exists": true, "subject": stored, "subject_matches_t0": stored == subject, "display_bytes_match_t0": payload == display.PayloadUTF8}
}

func staleReviewCaseAssertSourceUnchanged(t *testing.T, original, current ProposalQueryResult) {
	t.Helper()
	if original.StatementText != current.StatementText || original.SourceSnapshotID != current.SourceSnapshotID ||
		original.RawContentHash != current.RawContentHash || original.SourceVersion != current.SourceVersion ||
		!reflect.DeepEqual(original.SourceRefs, current.SourceRefs) {
		t.Fatal("source citation or claim changed during lifecycle-only experiment")
	}
}

func staleReviewCaseSourceBytes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proposal ProposalQueryResult) []byte {
	t.Helper()
	var raw, rendered []byte
	var rawHash, renderedHash, rendererName, rendererVersion string
	if err := pool.QueryRow(ctx, `SELECT b.raw_content, s.raw_content_hash,
		v.rendered_content, v.rendered_content_hash, v.renderer_name, v.renderer_version
		FROM source_snapshots s JOIN source_blobs b USING (raw_content_hash)
		JOIN extraction_views v USING (source_snapshot_id)
		WHERE s.source_snapshot_id = $1 AND v.extraction_view_id = $2`,
		proposal.SourceSnapshotID, proposal.ExtractionViewID).Scan(
		&raw, &rawHash, &rendered, &renderedHash, &rendererName, &rendererVersion,
	); err != nil {
		t.Fatal(err)
	}
	if rawHash != proposal.RawContentHash || contentHash(raw) != rawHash ||
		renderedHash != proposal.RenderedContentHash || contentHash(rendered) != renderedHash {
		t.Fatal("persisted source or rendered bytes do not match their recorded hashes")
	}
	if rendererName != RendererExternalDocumentIdentity || rendererVersion != RendererExternalDocumentIdentityVersion ||
		proposal.RendererName != rendererName || proposal.RendererVersion != rendererVersion || !bytes.Equal(raw, rendered) {
		t.Fatal("fixture no longer uses the verified external-document identity renderer")
	}
	for _, ref := range proposal.SourceRefs {
		if ref.ExtractionViewID != proposal.ExtractionViewID || ref.StartByte < 0 || ref.EndByte < ref.StartByte || ref.EndByte > len(raw) {
			t.Fatal("source quote does not select a valid interval in the persisted source bytes")
		}
		quote := raw[ref.StartByte:ref.EndByte]
		if string(quote) != ref.QuotedText || contentHash(quote) != ref.QuotedTextHash {
			t.Fatal("persisted source byte slice differs from the exact source citation")
		}
	}
	return raw
}

func staleReviewCaseAssertAdmitted(t *testing.T, state staleReviewCaseAuthorityState, input ReviewedSourceClaimAdmissionInput, result AdmissionResult) {
	t.Helper()
	wantCounts := staleReviewCaseCounts{Nodes: 2, Edges: 1, Decisions: 1, OrdinaryManifests: 1, ReviewBindings: 1, NodeBindings: 2, EdgeBindings: 1}
	if state.Counts != wantCounts || state.ProposalStatus != admissionOutcomeAdmitted ||
		state.CanonicalRef != result.CanonicalRef || state.DecisionID != result.AdmissionDecisionID ||
		state.DecisionBy != input.DecisionBy || state.DecisionReason != input.DecisionReason {
		t.Fatalf("new admission did not persist exactly one bound source claim: %+v", state)
	}
}
