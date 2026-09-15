//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
)

// The saved two-candidate extraction below is synthetic, not a model-quality
// or authenticated-human witness. Every native submission contains exactly
// one candidate; the Detective index is not a native multi-proposal manifest.
func TestIntegrationDetectiveBatchRecoveryProtectedLauncher(t *testing.T) {
	detectiveRoot := detectivePendingSourceRoot(t)
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Second)
	defer cancel()
	directory := provisioningProtectedDirectory(t, databaseURL)
	binaries := buildProvisioningCommands(t, ctx, directory)
	detective := buildDetectivePendingCommand(t, ctx, detectiveRoot, directory)
	fixture := newProvisioningLauncherFixture(t, ctx, databaseURL, binaries["ahe-runtime-admin"])
	intake := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileIntake)
	query := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileQuery)
	reviewer := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileSourceClaimReviewer)
	queryFailure, queryMarker := detectiveCheckpointProbe(t, directory, "batch-first-query-failure")
	intakeProbe, intakeMarker := detectiveCheckpointProbe(t, directory, "batch-no-repeat-intake")
	forbidden := [][]byte{[]byte(databaseURL), []byte(detectiveCheckpointAPISecret), []byte(intake), []byte(query), []byte(reviewer), []byte(queryFailure), []byte(intakeProbe)}
	for _, identity := range fixture.identities {
		forbidden = append(forbidden, []byte(identity.dsn))
	}

	sourcePath, rowBatchPath := detectiveBatchSavedFixture(t, directory)
	indexPath := filepath.Join(directory, "candidate-batch.json")
	prepared := runDetectiveBatchCommand(t, ctx, detective, true, forbidden,
		"batch", "prepare", "-input", sourcePath, "-row-batch", rowBatchPath,
		"-source-id", "mock:detective-batch", "-index", indexPath, "-status-clause", "LAB PROVEN")
	if prepared.State != "prepared" || prepared.AllCandidatesChecked || prepared.AuthorityEffect != "none" || prepared.Summary.Total != 2 {
		t.Fatal("batch preparation did not retain two local candidates without authority")
	}
	indexBefore := readDetectiveCheckpoint(t, indexPath)
	checkpointBefore := make([][]byte, 2)
	for i, member := range prepared.Members {
		checkpointBefore[i] = readDetectiveCheckpoint(t, member.CheckpointPath)
		if member.Ordinal != i+1 || member.CandidateDigest == "" || member.CheckpointDigest == "" || !member.CheckpointAvailable || member.Locator != nil {
			t.Fatal("prepared index omitted an ordered exact candidate checkpoint")
		}
	}
	assertDetectiveBatchCounts(t, ctx, fixture, 0, false)

	partial := runDetectiveBatchCommand(t, ctx, detective, false, forbidden,
		"batch", "resume", "-index", indexPath, "-ingest-command", intake, "-query-command", queryFailure)
	if partial.State != "incomplete" || partial.AllCandidatesChecked || partial.Summary.Failed != 1 || partial.Summary.NotAttempted != 1 ||
		partial.Summary.SubmissionAttempts != 1 || partial.Members[0].FailureStage != "query_unverified" || partial.Members[0].Locator == nil ||
		partial.Members[1].State != "not_attempted" || partial.Members[1].Locator != nil {
		t.Fatal("failed first Query lost its native locator or remaining candidate")
	}
	if _, err := os.Stat(queryMarker); err != nil {
		t.Fatal("batch did not reach independent Query after its first native submission")
	}
	locatorBefore := readDetectiveCheckpoint(t, partial.Members[0].LocatorPath)
	assertDetectiveBatchCounts(t, ctx, fixture, 1, false)
	// Recover solely from frozen local material: neither input is consulted again.
	for _, path := range []string{sourcePath, rowBatchPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal("cannot remove the synthetic initial batch input")
		}
	}
	recovered := runDetectiveBatchCommand(t, ctx, detective, true, forbidden,
		"batch", "resume", "-index", indexPath, "-ingest-command", intake, "-query-command", query)
	if recovered.State != "all_candidates_observed" || !recovered.AllCandidatesChecked || recovered.Summary.Pending != 2 || recovered.Summary.SubmissionAttempts != 1 ||
		recovered.Members[0].SubmissionAttempted || !recovered.Members[1].SubmissionAttempted || !reflect.DeepEqual(recovered.Members[0].Locator, partial.Members[0].Locator) {
		t.Fatal("batch recovery did not independently verify the first candidate and submit the second")
	}
	if reflect.DeepEqual(recovered.Members[0].Locator, recovered.Members[1].Locator) {
		t.Fatal("two candidates were collapsed to only one native attempt or occurrence")
	}
	assertDetectiveCheckpointUnchanged(t, indexPath, indexBefore)
	assertDetectiveCheckpointUnchanged(t, partial.Members[0].LocatorPath, locatorBefore)
	receipts := make([][]byte, 2)
	for i, member := range recovered.Members {
		assertDetectiveCheckpointUnchanged(t, member.CheckpointPath, checkpointBefore[i])
		receipts[i] = readDetectiveCheckpoint(t, member.ReceiptPath)
		var receipt detectiveCheckpointResumeReceipt
		if json.Unmarshal(receipts[i], &receipt) != nil || receipt.SchemaVersion != "detective-pending-resume/v1" ||
			receipt.CheckpointDigest != member.CheckpointDigest || !receipt.ReadbackVerified || receipt.State != "pending_verified" ||
			receipt.Handoff.ProposalCount != 1 || receipt.Handoff.ProposalOccurrenceID != member.Locator.ProposalOccurrenceID {
			t.Fatal("batch member did not publish a complete existing-v1 pending review receipt")
		}
	}
	assertDetectiveBatchCounts(t, ctx, fixture, 2, false)
	replayed := runDetectiveBatchCommand(t, ctx, detective, true, forbidden,
		"batch", "resume", "-index", indexPath, "-ingest-command", intakeProbe, "-query-command", query)
	if replayed.Summary.Pending != 2 || replayed.Summary.SubmissionAttempts != 0 {
		t.Fatal("known candidate locators triggered native resubmission")
	}
	assertDetectiveHumanReviewNoLaunch(t, intakeMarker)
	assertDetectiveBatchCounts(t, ctx, fixture, 2, false)

	// The existing explicit single-candidate human workflow consumes an index
	// member unchanged; the batch command never makes this decision itself.
	first := recovered.Members[0]
	reviewPath, decisionPath := filepath.Join(directory, "batch-first-review.json"), filepath.Join(directory, "batch-first-decision.json")
	runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden,
		"review", "prepare", "-checkpoint", first.CheckpointPath, "-receipt", first.ReceiptPath,
		"-review-command", reviewer, "-query-command", query, "-out", reviewPath)
	var bundle detectiveHumanReviewBundle
	if json.Unmarshal(readDetectiveCheckpoint(t, reviewPath), &bundle) != nil || bundle.Review.Display.ID == "" ||
		bundle.Review.Subject.ReviewSubject.ProposalOccurrenceID != first.Locator.ProposalOccurrenceID || bundle.Review.ProposalManifest.ProposalCount != 1 {
		t.Fatal("existing review did not preserve the selected single-candidate identity")
	}
	runDetectiveHumanDecisionCommand(t, ctx, detective, reviewPath, decisionPath,
		"admit\n合成多候選恢復測試，不是真人核准。\n"+bundle.Review.Display.ID+"\n", forbidden)
	runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden,
		"review", "apply", "-decision", decisionPath, "-review-command", reviewer,
		"-query-command", query, "-confirm-display", bundle.Review.Display.ID)
	terminal := runDetectiveBatchCommand(t, ctx, detective, true, forbidden,
		"batch", "resume", "-index", indexPath, "-ingest-command", intakeProbe, "-query-command", query)
	if !terminal.AllCandidatesChecked || terminal.Summary.Admitted != 1 || terminal.Summary.Pending != 1 || terminal.Summary.SubmissionAttempts != 0 ||
		terminal.Members[0].State != "admitted_verified" || terminal.Members[1].State != "pending_verified" {
		t.Fatal("mixed terminal/pending recovery was falsely reported as all pending or resubmitted")
	}
	assertDetectiveHumanReviewNoLaunch(t, intakeMarker)
	for i, member := range terminal.Members {
		assertDetectiveCheckpointUnchanged(t, member.ReceiptPath, receipts[i])
		assertDetectiveCheckpointUnchanged(t, member.CheckpointPath, checkpointBefore[i])
		assertDetectiveHumanReviewPrivateFile(t, member.LocatorPath, forbidden)
	}
	assertDetectiveCheckpointUnchanged(t, indexPath, indexBefore)
	assertDetectiveBatchCounts(t, ctx, fixture, 2, true)
}

type detectiveBatchReceipt struct {
	SchemaVersion        string                        `json:"schema_version"`
	NativeSubmissionMode string                        `json:"native_submission_mode"`
	State                string                        `json:"state"`
	AllCandidatesChecked bool                          `json:"all_candidates_checked"`
	AuthorityEffect      string                        `json:"authority_effect"`
	Members              []detectiveBatchMemberReceipt `json:"members"`
	Summary              struct {
		Total              int `json:"total"`
		SubmissionAttempts int `json:"submission_attempts"`
		Pending            int `json:"pending"`
		Admitted           int `json:"admitted"`
		Failed             int `json:"failed"`
		NotAttempted       int `json:"not_attempted"`
	} `json:"summary"`
}

type detectiveBatchMemberReceipt struct {
	Ordinal             int    `json:"ordinal"`
	CandidateDigest     string `json:"candidate_digest"`
	CheckpointDigest    string `json:"checkpoint_digest"`
	CheckpointPath      string `json:"checkpoint_path"`
	LocatorPath         string `json:"locator_path"`
	ReceiptPath         string `json:"receipt_path"`
	CheckpointAvailable bool   `json:"checkpoint_available"`
	State               string `json:"state"`
	FailureStage        string `json:"failure_stage"`
	SubmissionAttempted bool   `json:"submission_attempted"`
	Locator             *struct {
		SourceSnapshotID     string `json:"source_snapshot_id"`
		ExtractionViewID     string `json:"extraction_view_id"`
		ExtractionAttemptID  string `json:"extraction_attempt_id"`
		ProposalOccurrenceID string `json:"proposal_occurrence_id"`
		SubmissionOutcome    string `json:"submission_outcome"`
		Replayed             bool   `json:"replayed"`
	} `json:"locator"`
}

func runDetectiveBatchCommand(t *testing.T, ctx context.Context, binary string, success bool, forbidden [][]byte, args ...string) detectiveBatchReceipt {
	t.Helper()
	childCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	command := exec.CommandContext(childCtx, binary, args...)
	command.Env = detectiveCheckpointEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr, command.WaitDelay = &stdout, &stderr, 2*time.Second
	err := command.Run()
	if childCtx.Err() != nil || bytes.Contains(stderr.Bytes(), []byte("WARNING: DATA RACE")) || (err == nil) != success {
		t.Fatal("Detective batch process did not return its expected bounded outcome (output suppressed)")
	}
	for _, value := range forbidden {
		if len(value) != 0 && (detectiveCheckpointContains(stdout.Bytes(), value) || detectiveCheckpointContains(stderr.Bytes(), value)) {
			t.Fatal("Detective batch exposed private credential or launcher material")
		}
	}
	var result detectiveBatchReceipt
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.SchemaVersion != "detective-candidate-batch-result/v1" ||
		result.NativeSubmissionMode != "independent-single-candidate-native-attempts/v1" || len(result.Members) != 2 {
		t.Fatal("Detective batch omitted the complete ordered partial/success report")
	}
	return result
}

func detectiveBatchSavedFixture(t *testing.T, directory string) (string, string) {
	t.Helper()
	const row = "| Batch recovery | **LAB PROVEN** | two saved candidates; independent recovery only |"
	source := "# Synthetic batch fixture\n\n## Status at a Glance\n\n| Capability | Status | Boundary |\n| --- | --- | --- |\n" + row + "\n"
	sourcePath, batchPath := filepath.Join(directory, "batch-STATUS.md"), filepath.Join(directory, "saved-row-batch.json")
	section := map[string]any{"heading": "Status at a Glance", "start_line": 3, "end_line": 7}
	records := []any{}
	for i, statement := range []string{"The synthetic source reports two saved candidates.", "The synthetic source limits the fixture to independent recovery."} {
		records = append(records, map[string]any{
			"record_type": "capability_state", "subject": []string{"saved candidates", "independent recovery"}[i], "statement": statement,
			"epistemic_class": "claim", "status": "lab_proven", "scope": "lab_contract", "selection_state": "unspecified",
			"citation":   map[string]any{"start_line": 7, "end_line": 7, "exact_quote": row},
			"blocked_by": []string{}, "does_not_establish": []string{}, "qualifiers": []string{},
		})
	}
	batch := map[string]any{
		"schema_version": "lab-status-row-batch/v0",
		"source":         map[string]any{"path": sourcePath, "sha256": strings.TrimPrefix(stdioContentHash([]byte(source)), "sha256:"), "bytes": len(source), "lines": 7, "selected_sections": []any{section}},
		// Explicit historical saved-output compatibility, not a current prompt version assertion.
		"extractor": map[string]any{"name": "lab-status-extractor", "version": "0.1.0", "model": "synthetic-saved-output"},
		"section":   section,
		"rows": []any{map[string]any{
			"row": map[string]any{"start_line": 7, "end_line": 7, "text": row}, "status": "validated",
			"result": map[string]any{"outcome": "extracted", "records": records, "abstentions": []any{}, "limitations": []string{}, "abstention_reason": ""},
		}},
		"summary": map[string]any{"attempted": 1, "validated": 1, "failed": 0, "not_attempted": 0},
	}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal("cannot encode synthetic saved batch")
	}
	writeProvisioningProtectedFile(t, sourcePath, []byte(source))
	writeProvisioningProtectedFile(t, batchPath, body)
	return sourcePath, batchPath
}

func assertDetectiveBatchCounts(t *testing.T, ctx context.Context, fixture provisioningLauncherFixture, candidates int, admitted bool) {
	t.Helper()
	sources := 0
	if candidates > 0 {
		sources = 1
	}
	for _, table := range []string{"source_snapshots", "extraction_views"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, sources)
	}
	for _, table := range []string{"extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, candidates)
	}
	for table, count := range map[string]int{"canonical_graph_nodes": 2, "canonical_graph_edges": 1, "admission_decisions": 1} {
		if !admitted {
			count = 0
		}
		stdioAssertTableCount(t, ctx, fixture.pool, table, count)
	}
}
