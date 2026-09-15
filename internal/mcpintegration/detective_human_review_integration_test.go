//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

// This actual-process witness supplies synthetic operator input, not authentic
// human approval. Only the explicitly selected disposable acceptance database
// may receive its governed admission and exact-replay probes.
func TestIntegrationDetectiveHumanReviewProtectedLauncher(t *testing.T) {
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

	const row = "| Human review workflow | **LAB PROVEN** | synthetic operator input only |"
	const statement = "The synthetic source reports a bounded human review workflow fixture."
	const sourceID = "mock:detective-human-review"
	const reason = "合成人工作業測試，不是真人核准或身分驗證。"
	source := "# Synthetic Detective human review\n\n## Status at a Glance\n\n| Capability | Status | Boundary |\n| --- | --- | --- |\n" + row + "\n"
	inputPath, checkpointPath := filepath.Join(directory, "review-STATUS.md"), filepath.Join(directory, "review-checkpoint.json")
	receiptPath, reviewPath := filepath.Join(directory, "review-resume.json"), filepath.Join(directory, "review-package.json")
	decisionPath := filepath.Join(directory, "review-decision.json")
	writeProvisioningProtectedFile(t, inputPath, []byte(source))
	model, requests := newDetectivePendingModel(t, row, statement)
	forbidden := [][]byte{[]byte(detectiveCheckpointAPISecret), []byte(databaseURL), []byte(model.URL), []byte(intake), []byte(query), []byte(reviewer)}
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
		t.Fatal("synthetic checkpoint preparation did not perform exactly one model request")
	}
	if err := os.Remove(inputPath); err != nil {
		t.Fatal("cannot remove the synthetic original source before review")
	}
	receiptBody := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden,
		"-ahe-resume", checkpointPath, "-ahe-ingest-command", intake, "-ahe-query-command", query, "-timeout", "30s")
	var receipt detectiveCheckpointResumeReceipt
	if json.Unmarshal(receiptBody, &receipt) != nil || receipt.State != "pending_verified" || !receipt.ReadbackVerified ||
		receipt.CheckpointDigest != checkpoint.Digest || receipt.Handoff.ProposalOccurrenceID == "" {
		t.Fatal("review fixture did not preserve a complete verified pending resume receipt")
	}
	writeProvisioningProtectedFile(t, receiptPath, receiptBody)
	assertDetectiveHumanReviewCounts(t, ctx, fixture, false)

	preparedText := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden,
		"review", "prepare", "-checkpoint", checkpointPath, "-receipt", receiptPath,
		"-review-command", reviewer, "-query-command", query, "-out", reviewPath)
	reviewBody := readDetectiveCheckpoint(t, reviewPath)
	var bundle detectiveHumanReviewBundle
	if json.Unmarshal(reviewBody, &bundle) != nil || bundle.SchemaVersion != "detective-source-review/v1" || bundle.Digest == "" ||
		!detectiveCheckpointJSONEqual(bundle.Checkpoint, checkpointBody) || !detectiveCheckpointJSONEqual(bundle.Receipt, receiptBody) ||
		bundle.Review.Display.ID == "" || bundle.Review.Display.PayloadUTF8 == "" ||
		bundle.Review.Subject.ReviewSubject.ProposalOccurrenceID != receipt.Handoff.ProposalOccurrenceID ||
		bundle.Review.Subject.ReviewDisplayArtifactID != bundle.Review.Display.ID ||
		bundle.Review.ProposalManifest.ProposalCount != 1 || len(bundle.Review.ProposalManifest.Entries) != 1 {
		t.Fatal("Detective did not save the complete exact review subject and display")
	}
	if !bytes.Contains(preparedText, []byte(bundle.Review.Display.ID)) ||
		(!bytes.Contains(preparedText, []byte(bundle.Review.Display.PayloadUTF8)) && !bytes.Contains(preparedText, []byte(strconv.Quote(bundle.Review.Display.PayloadUTF8)))) {
		t.Fatal("review preparation did not show the complete exact display and confirmation identity")
	}
	if !strings.Contains(bundle.Review.Display.PayloadUTF8, statement) || !strings.Contains(bundle.Review.Display.PayloadUTF8, row) {
		t.Fatal("prepared review display lost the source-grounded statement or exact quotation")
	}
	assertDetectiveHumanReviewPrivateFile(t, reviewPath, forbidden)
	retainDetectiveHumanReviewFixture(t, reviewBody)
	assertDetectiveHumanReviewCounts(t, ctx, fixture, false)

	decisionBody := runDetectiveHumanDecisionCommand(t, ctx, detective, reviewPath, decisionPath,
		"admit\n"+reason+"\n"+bundle.Review.Display.ID+"\n", forbidden)
	var summary detectiveHumanDecisionSummary
	if json.Unmarshal(decisionBody, &summary) != nil || summary.SchemaVersion != "detective-review-decision-recorded/v1" ||
		summary.State != "decision_recorded_locally" || summary.Decision != "admit" || summary.Digest == "" ||
		summary.ReviewDisplayID != bundle.Review.Display.ID || summary.AuthorityEffect != "none" {
		t.Fatal("operator input did not produce the bounded local-decision summary")
	}
	savedDecisionBody := readDetectiveCheckpoint(t, decisionPath)
	var decision detectiveHumanReviewDecision
	if json.Unmarshal(savedDecisionBody, &decision) != nil || decision.SchemaVersion != "detective-review-decision/v1" || decision.Digest != summary.Digest ||
		decision.Decision != "admit" || decision.Reason != reason || decision.ConfirmedDisplayID != bundle.Review.Display.ID ||
		!detectiveCheckpointJSONEqual(decision.ReviewBundle, reviewBody) {
		t.Fatal("saved decision is not bound to the complete displayed review package")
	}
	assertDetectiveHumanReviewPrivateFile(t, decisionPath, forbidden)
	assertDetectiveHumanReviewCounts(t, ctx, fixture, false)

	t.Run("confirmation_required_before_writer", func(t *testing.T) {
		for _, confirmation := range []string{"", "review-display:v1:sha256:" + strings.Repeat("0", 64)} {
			name := "missing-confirmation"
			if confirmation != "" {
				name = "wrong-confirmation"
			}
			writerProbe, writerMarker := detectiveCheckpointProbe(t, directory, name+"-reviewer")
			queryProbe, queryMarker := detectiveCheckpointProbe(t, directory, name+"-query")
			args := []string{"review", "apply", "-decision", decisionPath, "-review-command", writerProbe, "-query-command", queryProbe}
			if confirmation != "" {
				args = append(args, "-confirm-display", confirmation)
			}
			runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden, args...)
			assertDetectiveHumanReviewNoLaunch(t, writerMarker, queryMarker)
		}
		assertDetectiveHumanReviewCounts(t, ctx, fixture, false)
	})

	t.Run("decisions_are_local_until_apply_and_pending_cannot_apply", func(t *testing.T) {
		for _, outcome := range []string{"audit_only", "reject", "pending"} {
			t.Run(outcome, func(t *testing.T) {
				path := filepath.Join(directory, "decision-"+outcome+".json")
				body := runDetectiveHumanDecisionCommand(t, ctx, detective, reviewPath, path,
					outcome+"\n合成本機決定，不變更 AHE。\n"+bundle.Review.Display.ID+"\n", forbidden)
				var local detectiveHumanDecisionSummary
				if json.Unmarshal(body, &local) != nil || local.Decision != outcome || local.AuthorityEffect != "none" || local.State != "decision_recorded_locally" {
					t.Fatal("nonadmit input did not remain an explicit local decision")
				}
				assertDetectiveHumanReviewPrivateFile(t, path, forbidden)
				assertDetectiveHumanReviewCounts(t, ctx, fixture, false)
				if outcome != "pending" {
					return
				}
				writerProbe, writerMarker := detectiveCheckpointProbe(t, directory, outcome+"-reviewer")
				queryProbe, queryMarker := detectiveCheckpointProbe(t, directory, outcome+"-query")
				runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden,
					"review", "apply", "-decision", path, "-review-command", writerProbe,
					"-query-command", queryProbe, "-confirm-display", bundle.Review.Display.ID)
				assertDetectiveHumanReviewNoLaunch(t, writerMarker, queryMarker)
				assertDetectiveHumanReviewCounts(t, ctx, fixture, false)
			})
		}
	})

	t.Run("tampered_decisions_rejected_before_writer", func(t *testing.T) {
		for _, field := range []string{"subject", "reason"} {
			var changed map[string]any
			if json.Unmarshal(savedDecisionBody, &changed) != nil {
				t.Fatal("cannot decode synthetic decision tampering fixture")
			}
			if field == "reason" {
				changed["reason"] = "synthetic changed reason"
			} else {
				review := changed["review_bundle"].(map[string]any)["review"].(map[string]any)
				review["subject"].(map[string]any)["review_display_artifact_id"] = "review-display:v1:sha256:" + strings.Repeat("f", 64)
			}
			body, err := json.Marshal(changed)
			if err != nil {
				t.Fatal("cannot encode synthetic decision tampering fixture")
			}
			path := filepath.Join(directory, "tampered-"+field+".json")
			writeProvisioningProtectedFile(t, path, body)
			writerProbe, writerMarker := detectiveCheckpointProbe(t, directory, field+"-reviewer")
			queryProbe, queryMarker := detectiveCheckpointProbe(t, directory, field+"-query")
			runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden,
				"review", "apply", "-decision", path, "-review-command", writerProbe,
				"-query-command", queryProbe, "-confirm-display", bundle.Review.Display.ID)
			assertDetectiveHumanReviewNoLaunch(t, writerMarker, queryMarker)
		}
		assertDetectiveHumanReviewCounts(t, ctx, fixture, false)
	})

	queryFailure, queryFailureMarker := detectiveCheckpointProbe(t, directory, "review-post-admission-query-failure")
	applyArgs := []string{"review", "apply", "-decision", decisionPath, "-review-command", reviewer,
		"-query-command", queryFailure, "-confirm-display", bundle.Review.Display.ID}
	runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden, applyArgs...)
	if _, err := os.Stat(queryFailureMarker); err != nil {
		t.Fatal("post-admission Query failure was not reached")
	}
	assertDetectiveHumanReviewCounts(t, ctx, fixture, true)
	var canonicalRef, decisionID, rawID, edgeID, disposition string
	if err := fixture.pool.QueryRow(ctx, `SELECT canonical_ref, admission_outcome FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, receipt.Handoff.ProposalOccurrenceID).Scan(&canonicalRef, &disposition); err != nil || disposition != "admitted" || canonicalRef == "" {
		t.Fatal("Query failure incorrectly implied rollback of governed admission")
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT admission_decision_id FROM admission_decisions`).Scan(&decisionID); err != nil {
		t.Fatal("cannot read the isolated synthetic admission decision")
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT canonical_node_id FROM canonical_graph_nodes WHERE node_kind='raw_evidence'`).Scan(&rawID); err != nil {
		t.Fatal("cannot read the isolated synthetic raw evidence identity")
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT canonical_edge_id FROM canonical_graph_edges`).Scan(&edgeID); err != nil {
		t.Fatal("cannot read the isolated synthetic canonical edge identity")
	}
	applyArgs[7] = query
	output := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden, applyArgs...)
	var applied detectiveHumanReviewExecution
	if json.Unmarshal(output, &applied) != nil || applied.SchemaVersion != "detective-review-execution/v1" || applied.State != "admitted_verified" ||
		!applied.ReadbackVerified || applied.HumanReview != "operator_supplied_not_authenticated" || applied.DecisionDigest != decision.Digest ||
		!applied.Admission.Replayed || applied.Admission.AdmissionOutcome != "admitted" ||
		applied.Admission.ProposalOccurrenceID != receipt.Handoff.ProposalOccurrenceID || applied.Admission.CanonicalRef != canonicalRef ||
		applied.Admission.AdmissionDecisionID != decisionID || !reflect.DeepEqual(applied.Admission.RawEvidenceNodeIDs, []string{rawID}) ||
		!reflect.DeepEqual(applied.Admission.CanonicalEdgeIDs, []string{edgeID}) {
		t.Fatal("exact decision replay changed identities or failed to verify admitted state")
	}
	again := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden, applyArgs...)
	var replay detectiveHumanReviewExecution
	if json.Unmarshal(again, &replay) != nil || !reflect.DeepEqual(applied, replay) {
		t.Fatal("further exact decision replay changed its verified execution receipt")
	}
	assertDetectiveHumanReviewCounts(t, ctx, fixture, true)

	t.Run("changed_reason_conflicts_with_admitted_replay", func(t *testing.T) {
		path := filepath.Join(directory, "different-valid-decision.json")
		runDetectiveHumanDecisionCommand(t, ctx, detective, reviewPath, path,
			"admit\nsynthetic different reason for replay conflict\n"+bundle.Review.Display.ID+"\n", forbidden)
		runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden,
			"review", "apply", "-decision", path, "-review-command", reviewer,
			"-query-command", query, "-confirm-display", bundle.Review.Display.ID)
		assertDetectiveHumanReviewCounts(t, ctx, fixture, true)
	})

	process := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], detectiveCheckpointConfigPath(directory, dbrole.ProfileQuery), "ahe-query-mcp")
	record := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, process, "get_evidence_record", map[string]any{"proposal_occurrence_id": receipt.Handoff.ProposalOccurrenceID})
	if record.AdmissionOutcome != "admitted" || record.CanonicalRef == nil || *record.CanonicalRef != canonicalRef || record.StatementText != statement ||
		record.Source.SourceID != sourceID || record.Source.RawContentHash != stdioContentHash([]byte(source)) ||
		len(record.SourceRefs) != 1 || record.SourceRefs[0].QuotedText != row || record.SourceRefs[0].QuotedTextHash != stdioContentHash([]byte(row)) {
		t.Fatal("independent Query lost admitted identity or exact source provenance")
	}
	process.finish(t)
	var payload, displayID, principal, savedReason string
	if err := fixture.pool.QueryRow(ctx, `SELECT b.review_display_payload_utf8, b.review_display_artifact_id, d.decision_by, d.decision_reason FROM canonical_source_claim_review_bindings b JOIN admission_decisions d USING (admission_decision_id) WHERE b.admission_decision_id=$1`, decisionID).Scan(&payload, &displayID, &principal, &savedReason); err != nil {
		t.Fatal("cannot read the isolated exact review binding")
	}
	if payload != bundle.Review.Display.PayloadUTF8 || displayID != bundle.Review.Display.ID || savedReason != reason ||
		principal != "mock:detective-checkpoint:"+string(dbrole.ProfileSourceClaimReviewer) {
		t.Fatal("admission changed the display, reason, or protected reviewer principal")
	}
	for path, want := range map[string][]byte{checkpointPath: checkpointBody, receiptPath: receiptBody, reviewPath: reviewBody, decisionPath: savedDecisionBody} {
		assertDetectiveCheckpointUnchanged(t, path, want)
	}
	if requests.Load() != 1 {
		t.Fatal("review workflow called the unavailable model")
	}
	if _, err := os.Stat(inputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("review workflow recreated or consulted the removed original source")
	}
}

type detectiveHumanReviewBundle struct {
	SchemaVersion string                                            `json:"schema_version"`
	Checkpoint    json.RawMessage                                   `json:"checkpoint"`
	Receipt       json.RawMessage                                   `json:"receipt"`
	Review        evidenceingestionmcp.GetSourceClaimReviewResponse `json:"review"`
	Digest        string                                            `json:"digest"`
}

type detectiveHumanReviewDecision struct {
	SchemaVersion      string          `json:"schema_version"`
	ReviewBundle       json.RawMessage `json:"review_bundle"`
	Decision           string          `json:"decision"`
	Reason             string          `json:"reason"`
	ConfirmedDisplayID string          `json:"confirmed_display_id"`
	Digest             string          `json:"digest"`
}

type detectiveHumanDecisionSummary struct {
	SchemaVersion   string `json:"schema_version"`
	State           string `json:"state"`
	Decision        string `json:"decision"`
	Digest          string `json:"digest"`
	ReviewDisplayID string `json:"review_display_id"`
	AuthorityEffect string `json:"authority_effect"`
}

type detectiveHumanReviewExecution struct {
	SchemaVersion    string                                                `json:"schema_version"`
	State            string                                                `json:"state"`
	DecisionDigest   string                                                `json:"decision_digest"`
	Admission        evidenceingestionmcp.AdmitReviewedSourceClaimResponse `json:"admission"`
	ReadbackVerified bool                                                  `json:"readback_verified"`
	HumanReview      string                                                `json:"human_review"`
}

func runDetectiveHumanDecisionCommand(t *testing.T, ctx context.Context, binary, reviewPath, decisionPath, input string, forbidden [][]byte) []byte {
	t.Helper()
	childCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	command := exec.CommandContext(childCtx, binary, "review", "decide", "-review", reviewPath, "-out", decisionPath)
	command.Env, command.Stdin = detectiveCheckpointEnvironment(), strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr, command.WaitDelay = &stdout, &stderr, 2*time.Second
	err := command.Run()
	if childCtx.Err() != nil || bytes.Contains(stderr.Bytes(), []byte("WARNING: DATA RACE")) {
		t.Fatal("Detective decision command timed out or reported a data race")
	}
	for _, value := range forbidden {
		if len(value) != 0 && (detectiveCheckpointContains(stdout.Bytes(), value) || detectiveCheckpointContains(stderr.Bytes(), value)) {
			t.Fatal("Detective decision command exposed private connection or launcher material")
		}
	}
	if err != nil || stdout.Len() == 0 {
		t.Fatal("synthetic operator input did not record a local decision (output suppressed)")
	}
	return append([]byte(nil), stdout.Bytes()...)
}

func assertDetectiveHumanReviewNoLaunch(t *testing.T, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("locally denied review operation started an MCP launcher")
		}
	}
}

func assertDetectiveHumanReviewPrivateFile(t *testing.T, path string, forbidden [][]byte) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("review artifact is not a private regular file")
	}
	body := readDetectiveCheckpoint(t, path)
	for _, value := range forbidden {
		if len(value) != 0 && detectiveCheckpointContains(body, value) {
			t.Fatal("review artifact retained credentials or runtime launcher material")
		}
	}
}

func assertDetectiveHumanReviewCounts(t *testing.T, ctx context.Context, fixture provisioningLauncherFixture, admitted bool) {
	t.Helper()
	for _, table := range []string{"source_snapshots", "extraction_views", "extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 1)
	}
	for table, count := range map[string]int{"canonical_graph_nodes": 2, "canonical_graph_edges": 1, "admission_decisions": 1,
		"canonical_ordinary_admission_manifests": 1, "canonical_ordinary_admission_node_bindings": 2,
		"canonical_ordinary_admission_edge_bindings": 1, "canonical_source_claim_review_bindings": 1} {
		if !admitted {
			count = 0
		}
		stdioAssertTableCount(t, ctx, fixture.pool, table, count)
	}
	for _, table := range []string{"canonical_contradiction_proposals", "canonical_supersession_admission_events", "repository_generation_activation_requests", "repository_source_heads"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
}

// retainDetectiveHumanReviewFixture is an explicit test-only opt-in for one
// synthetic fixture. It never defaults to a repository path or overwrites a
// preexisting file; the caller owns subsequent use and cleanup of this output.
func retainDetectiveHumanReviewFixture(t *testing.T, body []byte) {
	t.Helper()
	path := os.Getenv("DETECTIVE_REVIEW_FIXTURE_OUTPUT")
	if path == "" {
		return
	}
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		!strings.HasPrefix(path, "/private/tmp/") || strings.ContainsAny(path, "\x00\r\n") || resolved != parent {
		t.Fatal("synthetic review fixture output requires an explicit clean absolute file path in private temporary storage")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("synthetic review fixture output requires an existing private parent directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot exclusively create the selected synthetic review fixture output")
	}
	_, writeErr := file.Write(body)
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		t.Fatal("cannot completely retain the selected synthetic review fixture output")
	}
}
