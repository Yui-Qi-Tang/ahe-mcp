//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

// Synthetic operator input witnesses transport and authority boundaries only.
// It does not establish that any real person inspected or approved a decision.
func TestIntegrationDetectiveReviewedDispositionProtectedLauncher(t *testing.T) {
	detectiveRoot := detectivePendingSourceRoot(t)
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Second)
	defer cancel()
	buildDirectory := provisioningProtectedDirectory(t, databaseURL)
	binaries := buildProvisioningCommands(t, ctx, buildDirectory)
	detective := buildDetectivePendingCommand(t, ctx, detectiveRoot, buildDirectory)
	for _, tc := range []struct{ decision, outcome string }{{"reject", "rejected"}, {"audit_only", "audit_only"}} {
		t.Run(tc.decision, func(t *testing.T) {
			directory := provisioningProtectedDirectory(t, databaseURL)
			fixture := newProvisioningLauncherFixture(t, ctx, databaseURL, binaries["ahe-runtime-admin"])
			intake := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileIntake)
			query := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileQuery)
			reviewer := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileSourceClaimReviewer)
			const row = "| Noncanonical review | **LAB PROVEN** | synthetic operator decision only |"
			const statement = "The synthetic source reports a bounded noncanonical review fixture."
			const reason = "合成拒絕或稽核理由，不是真人審查證明。"
			sourceID := "mock:detective-disposition-" + tc.decision
			source := "# Synthetic disposition\n\n## Status at a Glance\n\n| Capability | Status | Boundary |\n| --- | --- | --- |\n" + row + "\n"
			inputPath, checkpointPath := filepath.Join(directory, "disposition-STATUS.md"), filepath.Join(directory, "checkpoint.json")
			receiptPath, reviewPath := filepath.Join(directory, "receipt.json"), filepath.Join(directory, "review.json")
			decisionPath := filepath.Join(directory, "decision.json")
			writeProvisioningProtectedFile(t, inputPath, []byte(source))
			model, requests := newDetectivePendingModel(t, row, statement)
			forbidden := [][]byte{[]byte(detectiveCheckpointAPISecret), []byte(databaseURL), []byte(model.URL), []byte(intake), []byte(query), []byte(reviewer), []byte(reason)}
			for _, identity := range fixture.identities {
				forbidden = append(forbidden, []byte(identity.dsn))
			}
			runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden,
				"-input", inputPath, "-base-url", model.URL+"/v1", "-model", "mock-detective-model",
				"-row-chunks", "-row-line", "7", "-status-clause", "LAB PROVEN", "-ahe-checkpoint", checkpointPath,
				"-ahe-prepare-only", "-ahe-source-id", sourceID, "-timeout", "30s")
			checkpointBody := readDetectiveCheckpoint(t, checkpointPath)
			checkpoint := decodeDetectiveCheckpoint(t, checkpointBody, inputPath, sourceID, source, row, statement, "LAB PROVEN")
			model.Close()
			if requests.Load() != 1 {
				t.Fatal("synthetic source preparation did not use exactly one model call")
			}
			if err := os.Remove(inputPath); err != nil {
				t.Fatal("cannot remove synthetic original source")
			}
			receiptBody := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden,
				"-ahe-resume", checkpointPath, "-ahe-ingest-command", intake, "-ahe-query-command", query, "-timeout", "30s")
			var receipt detectiveCheckpointResumeReceipt
			if json.Unmarshal(receiptBody, &receipt) != nil || receipt.State != "pending_verified" || !receipt.ReadbackVerified || receipt.CheckpointDigest != checkpoint.Digest {
				t.Fatal("disposition fixture did not reach verified pending")
			}
			writeProvisioningProtectedFile(t, receiptPath, receiptBody)
			runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden,
				"review", "prepare", "-checkpoint", checkpointPath, "-receipt", receiptPath,
				"-review-command", reviewer, "-query-command", query, "-out", reviewPath)
			reviewBody := readDetectiveCheckpoint(t, reviewPath)
			var bundle detectiveHumanReviewBundle
			if json.Unmarshal(reviewBody, &bundle) != nil || bundle.SchemaVersion != "detective-source-review/v1" ||
				bundle.Review.Display.ID == "" || bundle.Review.Subject.ReviewSubject.ProposalOccurrenceID != receipt.Handoff.ProposalOccurrenceID {
				t.Fatal("saved disposition review lost exact display subject")
			}
			summaryBody := runDetectiveHumanDecisionCommand(t, ctx, detective, reviewPath, decisionPath,
				tc.decision+"\n"+reason+"\n"+bundle.Review.Display.ID+"\n", forbidden)
			var summary detectiveHumanDecisionSummary
			if json.Unmarshal(summaryBody, &summary) != nil || summary.Decision != tc.decision || summary.AuthorityEffect != "none" || summary.State != "decision_recorded_locally" {
				t.Fatal("disposition decision recording changed authority")
			}
			decisionBody := readDetectiveCheckpoint(t, decisionPath)
			assertDetectiveHumanReviewCounts(t, ctx, fixture, false)
			stdioAssertTableCount(t, ctx, fixture.pool, "source_claim_disposition_review_bindings", 0)

			writerProbe, writerMarker := detectiveCheckpointProbe(t, directory, "missing-confirm-writer")
			queryProbe, queryMarker := detectiveCheckpointProbe(t, directory, "missing-confirm-query")
			runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden,
				"review", "apply", "-decision", decisionPath, "-review-command", writerProbe, "-query-command", queryProbe)
			assertDetectiveHumanReviewNoLaunch(t, writerMarker, queryMarker)

			failedQuery, failedQueryMarker := detectiveCheckpointProbe(t, directory, "post-disposition-query-failure")
			args := []string{"review", "apply", "-decision", decisionPath, "-review-command", reviewer,
				"-query-command", failedQuery, "-confirm-display", bundle.Review.Display.ID}
			runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden, args...)
			if _, err := os.Stat(failedQueryMarker); err != nil {
				t.Fatal("post-disposition Query failure was not reached")
			}
			assertDetectiveDispositionCounts(t, ctx, fixture)
			var decisionID, outcome, decisionReason, decisionBy, payload, displayID, contract string
			if err := fixture.pool.QueryRow(ctx, `SELECT d.admission_decision_id,d.outcome,d.decision_reason,d.decision_by,
				b.review_display_payload_utf8,b.review_display_artifact_id,d.disposition_review_binding_contract_version
				FROM admission_decisions d JOIN source_claim_disposition_review_bindings b USING(admission_decision_id)
				WHERE d.proposal_occurrence_id=$1`, receipt.Handoff.ProposalOccurrenceID).Scan(&decisionID, &outcome, &decisionReason, &decisionBy, &payload, &displayID, &contract); err != nil {
				t.Fatal("cannot read exact persisted disposition")
			}
			if decisionID == "" || outcome != tc.outcome || decisionReason != reason || decisionBy != "mock:detective-checkpoint:"+string(dbrole.ProfileSourceClaimReviewer) ||
				payload != bundle.Review.Display.PayloadUTF8 || displayID != bundle.Review.Display.ID || contract != evidenceingestion.ReviewedSourceClaimDispositionV1 {
				t.Fatal("persisted disposition differs from exact review, reason or launcher principal")
			}
			forbidden = append(forbidden, []byte(decisionBy))
			args[7] = query
			output := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden, args...)
			var executed detectiveDispositionExecution
			if json.Unmarshal(output, &executed) != nil || executed.SchemaVersion != "detective-review-execution/v2" || executed.State != tc.outcome+"_verified" ||
				!executed.ReadbackVerified || executed.HumanReview != "operator_supplied_not_authenticated" || executed.DecisionDigest != summary.Digest ||
				executed.Disposition.AdmissionDecisionID != decisionID || executed.Disposition.AdmissionOutcome != tc.outcome ||
				executed.Disposition.ProposalOccurrenceID != receipt.Handoff.ProposalOccurrenceID || !executed.Disposition.Replayed {
				t.Fatal("exact noncanonical replay did not produce verified narrow execution")
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(output, &fields) != nil || len(fields) != 6 || fields["admission"] != nil {
				t.Fatal("noncanonical execution exposed admission authority")
			}
			var dispositionFields map[string]json.RawMessage
			if json.Unmarshal(fields["disposition"], &dispositionFields) != nil || len(dispositionFields) != 4 || dispositionFields["decision_by"] != nil || dispositionFields["decision_reason"] != nil {
				t.Fatal("noncanonical execution exposed reviewer metadata")
			}
			again := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden, args...)
			var replay detectiveDispositionExecution
			if json.Unmarshal(again, &replay) != nil || !reflect.DeepEqual(replay, executed) {
				t.Fatal("repeated verified disposition changed identities")
			}
			for _, changed := range []struct{ label, decision, reason string }{
				{"reason", tc.decision, "different synthetic decision reason"},
				{"decision", map[string]string{"reject": "audit_only", "audit_only": "reject"}[tc.decision], reason},
			} {
				path := filepath.Join(directory, "changed-"+changed.label+".json")
				runDetectiveHumanDecisionCommand(t, ctx, detective, reviewPath, path,
					changed.decision+"\n"+changed.reason+"\n"+bundle.Review.Display.ID+"\n", forbidden)
				runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden,
					"review", "apply", "-decision", path, "-review-command", reviewer,
					"-query-command", query, "-confirm-display", bundle.Review.Display.ID)
			}
			assertDetectiveDispositionCounts(t, ctx, fixture)
			if !bytes.Equal(readDetectiveCheckpoint(t, checkpointPath), checkpointBody) || !bytes.Equal(readDetectiveCheckpoint(t, receiptPath), receiptBody) ||
				!bytes.Equal(readDetectiveCheckpoint(t, reviewPath), reviewBody) || !bytes.Equal(readDetectiveCheckpoint(t, decisionPath), decisionBody) {
				t.Fatal("disposition execution rewrote saved review artifacts")
			}
			if requests.Load() != 1 {
				t.Fatal("review disposition called the closed synthetic model")
			}
		})
	}
}

type detectiveDispositionExecution struct {
	SchemaVersion  string `json:"schema_version"`
	State          string `json:"state"`
	DecisionDigest string `json:"decision_digest"`
	Disposition    struct {
		ProposalOccurrenceID string `json:"proposal_occurrence_id"`
		AdmissionDecisionID  string `json:"admission_decision_id"`
		AdmissionOutcome     string `json:"admission_outcome"`
		Replayed             bool   `json:"replayed"`
	} `json:"disposition"`
	ReadbackVerified bool   `json:"readback_verified"`
	HumanReview      string `json:"human_review"`
}

func assertDetectiveDispositionCounts(t *testing.T, ctx context.Context, fixture provisioningLauncherFixture) {
	t.Helper()
	for _, table := range []string{"source_snapshots", "extraction_views", "extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences",
		"admission_decisions", "source_claim_disposition_review_bindings"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 1)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "canonical_ordinary_admission_manifests",
		"canonical_ordinary_admission_node_bindings", "canonical_ordinary_admission_edge_bindings", "canonical_source_claim_review_bindings",
		"canonical_contradiction_proposals", "canonical_supersession_admission_events", "repository_generation_activation_requests", "repository_source_heads"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
}
