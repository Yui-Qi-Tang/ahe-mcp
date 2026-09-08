//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpquery"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

const detectiveLiveReason = "This scripted disposable-database decision accepts only that the frozen project document reports the quoted bounded capability. It does not establish deployment, semantic truth, or human approval."

type detectiveLiveRecord struct {
	Statement        string   `json:"statement"`
	RecordType       string   `json:"record_type"`
	Subject          string   `json:"subject"`
	EpistemicClass   string   `json:"epistemic_class"`
	Status           string   `json:"status"`
	Scope            string   `json:"scope"`
	SelectionState   string   `json:"selection_state"`
	BlockedBy        []string `json:"blocked_by"`
	DoesNotEstablish []string `json:"does_not_establish"`
	Qualifiers       []string `json:"qualifiers"`
}

// This test-side projection deliberately does not import Detective internals.
// It independently checks the versioned native bytes against actual Query;
// full saved-batch and source validation remains the Detective CLI's job.
func detectiveLiveStatement(version string, record detectiveLiveRecord) (string, error) {
	statement := record.Statement
	switch version {
	case "0.1.0", "0.1.1":
		// Historical title-only candidates retain their original projection.
	case "0.1.2":
		if statement == "" || statement != strings.TrimSpace(statement) || strings.EqualFold(statement, strings.TrimSpace(record.Subject)) {
			return "", errors.New("current live candidate requires a complete unmodified statement form")
		}
		type projectionContext struct {
			RecordType       string   `json:"record_type"`
			Subject          string   `json:"subject"`
			EpistemicClass   string   `json:"epistemic_class"`
			Status           string   `json:"status"`
			Scope            string   `json:"scope"`
			SelectionState   string   `json:"selection_state"`
			BlockedBy        []string `json:"blocked_by"`
			DoesNotEstablish []string `json:"does_not_establish"`
			Qualifiers       []string `json:"qualifiers"`
		}
		projection := projectionContext{
			RecordType: record.RecordType, Subject: record.Subject, EpistemicClass: record.EpistemicClass,
			Status: record.Status, Scope: record.Scope, SelectionState: record.SelectionState,
			BlockedBy: record.BlockedBy, DoesNotEstablish: record.DoesNotEstablish, Qualifiers: record.Qualifiers,
		}
		body, err := json.Marshal(projection)
		if err != nil {
			return "", errors.New("cannot encode live candidate context")
		}
		var roundtrip projectionContext
		if json.Unmarshal(body, &roundtrip) != nil || !reflect.DeepEqual(projection, roundtrip) {
			return "", errors.New("live candidate context would change during JSON projection")
		}
		statement += "\n\nDetective extraction context (model-classified candidate, not independent verification):\n" + string(body)
	default:
		return "", errors.New("unsupported live candidate projection version")
	}
	if strings.TrimSpace(statement) == "" || len(statement) > 2000 || !utf8.ValidString(statement) || strings.ContainsRune(statement, 0) {
		return "", errors.New("complete live candidate projection must fit 2000 UTF-8 bytes without NUL")
	}
	return statement, nil
}

// TestIntegrationDetectiveLiveModelExperiment consumes a separately frozen real
// extraction once. It is opt-in research, not a deterministic quality gate or
// human approval. Stopping on invalid/adverse advice is this experiment's policy,
// not a new production review-apply prerequisite. Only the runner-selected
// disposable database and operator-controlled loopback model endpoint are used.
func TestIntegrationDetectiveLiveModelExperiment(t *testing.T) {
	mode := os.Getenv("AHE_DETECTIVE_LIVE_MODE")
	if mode == "" {
		t.Skip("AHE_DETECTIVE_LIVE_MODE is not set")
	}
	if mode != "handoff" && mode != "synthetic" {
		t.Fatal("live experiment mode must explicitly be handoff or synthetic")
	}
	selected := map[string]string{}
	for _, key := range []string{"SOURCE", "ROW_BATCH", "OUTPUT", "TAP", "MODEL", "BASE_URL"} {
		selected[key] = os.Getenv("AHE_DETECTIVE_LIVE_" + key)
		if selected[key] == "" {
			t.Skip("required live experiment input is not set: " + key)
		}
	}
	if selected["MODEL"] != "gemma4:e4b-it-qat" || !detectiveLiveEndpoint(selected["BASE_URL"]) {
		t.Fatal("live experiment requires its exact selected model and explicit IPv4-loopback HTTP endpoint")
	}
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	detectiveRoot := detectivePendingSourceRoot(t)
	output := selected["OUTPUT"]
	resolved, err := filepath.EvalSymlinks(output)
	info, statErr := os.Stat(output)
	if err != nil || statErr != nil || !filepath.IsAbs(output) || filepath.Clean(output) != output || resolved != output || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("live experiment output must be a preexisting clean private 0700 directory")
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 {
		t.Fatal("live experiment output must be empty; outputs are never replaced")
	}
	summary := map[string]any{
		"schema_version": "detective-live-model-experiment/v1", "decision_mode": mode,
		"human_approval": false, "semantic_acceptance": false, "completion_status": "incomplete",
		"reached_stage": "input_validation", "model_assessment": "not_called", "model_assessment_attempts": 0,
		"automatic_model_retries": 0, "source_id_is_lab_namespace": true,
		"source_coverage":    "exact selected frozen file only; upstream excerpt provenance is separate",
		"artifact_lifecycle": "historical after fixture cleanup; not a live approval package",
		"database_lifecycle": "disposable fixture; schema/role cleanup is required, cluster cleanup belongs to the runner",
		"mcp_trace":          "metadata_only", "lab_policy": "stop before writer on invalid advice or any concern",
	}
	allowed := map[string]bool{}
	for _, name := range []string{"batch.json", "batch.json.recovery", "batch.json.candidate-001.checkpoint.json", "batch.json.candidate-001.locator.json", "batch.json.candidate-001.receipt.json", "review.json", "decision.json", "assessment.json"} {
		allowed[name], allowed[name+".lock"] = true, true
	}
	forbidden := [][]byte{[]byte(databaseURL), []byte(detectiveCheckpointAPISecret)}
	// Registered before fixture cleanup, therefore this executes after its
	// cleanup callbacks. A cleanup failure cannot retain a completed summary.
	t.Cleanup(func() {
		if t.Failed() {
			summary["completion_status"] = "failed_contract_or_cleanup"
		}
		retained := []string{}
		files, err := os.ReadDir(output)
		if err != nil {
			t.Error("cannot inspect the private live experiment output")
			return
		}
		for _, entry := range files {
			if !allowed[entry.Name()] || !entry.Type().IsRegular() {
				t.Error("live experiment produced an artifact outside its explicit allowlist")
				return
			}
			body := readDetectiveCheckpoint(t, filepath.Join(output, entry.Name()))
			detectiveLiveNoSecrets(t, body, forbidden)
			retained = append(retained, entry.Name())
		}
		summary["retained_files"] = retained
		detectiveLiveJSON(t, output, "summary.json", summary, allowed, forbidden)
	})

	source := detectiveLiveInput(t, selected["SOURCE"], 4<<20)
	batchBody := detectiveLiveInput(t, selected["ROW_BATCH"], 4<<20)
	sourceHash := strings.TrimPrefix(stdioContentHash(source), "sha256:")
	summary["source_sha256"], summary["row_batch_sha256"] = sourceHash, strings.TrimPrefix(stdioContentHash(batchBody), "sha256:")
	var batch struct {
		SchemaVersion string `json:"schema_version"`
		Source        struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"source"`
		Extractor struct{ Name, Version, Model string } `json:"extractor"`
		Rows      []struct {
			Status string `json:"status"`
			Result *struct {
				Outcome string                `json:"outcome"`
				Records []detectiveLiveRecord `json:"records"`
			} `json:"result"`
		} `json:"rows"`
	}
	if json.Unmarshal(batchBody, &batch) != nil || batch.SchemaVersion != "lab-status-row-batch/v0" || batch.Source.Path != selected["SOURCE"] || batch.Source.SHA256 != sourceHash || batch.Extractor.Name != "lab-status-extractor" || batch.Extractor.Model != selected["MODEL"] {
		t.Fatal("saved live extraction does not match the exact selected source/model identity")
	}
	if len(batch.Rows) != 1 || batch.Rows[0].Status != "validated" || batch.Rows[0].Result == nil || batch.Rows[0].Result.Outcome != "extracted" || len(batch.Rows[0].Result.Records) != 1 {
		summary["completion_status"], summary["blocked_reason"] = "blocked_scope", "exactly one validated row and one candidate required; no candidate was selected or repaired"
		return
	}
	record := batch.Rows[0].Result.Records[0]
	nativeStatement, err := detectiveLiveStatement(batch.Extractor.Version, record)
	if err != nil {
		t.Fatal("saved live extraction has no valid exact native statement projection")
	}
	summary["extractor_version"] = batch.Extractor.Version
	summary["raw_model_statement"], summary["native_statement"] = record.Statement, nativeStatement
	sourceID := "lab:detective-live-model:" + sourceHash
	summary["source_id"], summary["candidate_count"] = sourceID, 1
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	directory := provisioningProtectedDirectory(t, databaseURL)
	binaries := buildProvisioningCommands(t, ctx, directory)
	detective := os.Getenv("AHE_DETECTIVE_LIVE_BINARY")
	if detective == "" {
		detective = buildDetectivePendingCommand(t, ctx, detectiveRoot, directory)
	}
	summary["detective_binary_sha256"] = strings.TrimPrefix(stdioContentHash(detectiveLiveInput(t, detective, 128<<20)), "sha256:")
	fixture := newProvisioningLauncherFixture(t, ctx, databaseURL, binaries["ahe-runtime-admin"])
	for _, identity := range fixture.identities {
		forbidden = append(forbidden, []byte(identity.dsn))
	}
	launchers := map[string]string{}
	for role, profile := range map[string]dbrole.Profile{"intake": dbrole.ProfileIntake, "query": dbrole.ProfileQuery, "reviewer": dbrole.ProfileSourceClaimReviewer} {
		inner := detectiveCheckpointLauncher(t, directory, binaries, fixture, profile)
		wrapper := filepath.Join(directory, "live-"+role+"-tap")
		trace := "mcp-" + role + ".jsonl"
		allowed[trace] = true
		// The wrapper intentionally accepts no caller-selected configuration.
		// It pins the exact already-provisioned inner launcher and drops argv.
		// System Ruby consults uname while loading its standard library. Pin a
		// minimal lookup path even for the deliberately polluted query helper;
		// the inner protected launcher still rejects inherited DB coordinates.
		writeDetectiveCheckpointExecutable(t, wrapper, "#!/bin/sh\nPATH=/usr/bin:/bin\nexport PATH\nexec /usr/bin/ruby "+detectivePendingShellQuote(selected["TAP"])+" "+role+" "+detectivePendingShellQuote(filepath.Join(output, trace))+" "+detectivePendingShellQuote(inner)+"\n")
		launchers[role] = wrapper
		forbidden = append(forbidden, []byte(wrapper), []byte(inner))
	}
	run := func(stage, input string, args ...string) ([]byte, bool) {
		summary["reached_stage"] = stage
		return detectiveLiveCommand(t, ctx, detective, output, stage, input, args, allowed, forbidden)
	}
	mustRun := func(stage, input string, args ...string) []byte {
		body, ok := run(stage, input, args...)
		if !ok {
			t.Fatal("live experiment CLI transport or contract failed at " + stage)
		}
		return body
	}
	index := filepath.Join(output, "batch.json")
	mustRun("batch-prepare", "", "batch", "prepare", "-input", selected["SOURCE"], "-row-batch", selected["ROW_BATCH"], "-source-id", sourceID, "-index", index)
	assertDetectiveBatchCounts(t, ctx, fixture, 0, false)
	resumed := mustRun("batch-resume", "", "batch", "resume", "-index", index, "-ingest-command", launchers["intake"], "-query-command", launchers["query"])
	var receipt detectiveBatchReceipt
	if json.Unmarshal(resumed, &receipt) != nil || !receipt.AllCandidatesChecked || receipt.Summary.Pending != 1 || receipt.Summary.Total != 1 || len(receipt.Members) != 1 || receipt.Members[0].Locator == nil {
		t.Fatal("live experiment did not verify the complete single-candidate pending handoff")
	}
	member := receipt.Members[0]
	summary["native_locator"] = member.Locator
	summary["authority_effects"] = map[string]any{"source_intake": true, "pending_proposals": 1, "canonical_mutation": false, "review_decision": false}
	assertDetectiveBatchCounts(t, ctx, fixture, 1, false)
	mustRun("batch-inspect", "", "batch", "inspect", "-index", index, "-query-command", launchers["query"])
	reviewPath, decisionPath, assessmentPath := filepath.Join(output, "review.json"), filepath.Join(output, "decision.json"), filepath.Join(output, "assessment.json")
	mustRun("review-prepare", "", "review", "prepare", "-checkpoint", member.CheckpointPath, "-receipt", member.ReceiptPath, "-review-command", launchers["reviewer"], "-query-command", launchers["query"], "-out", reviewPath)
	var review detectiveHumanReviewBundle
	if json.Unmarshal(readDetectiveCheckpoint(t, reviewPath), &review) != nil || review.Review.Subject.ReviewSubject.ProposalOccurrenceID != member.Locator.ProposalOccurrenceID || review.Review.Display.ID == "" || review.Review.ProposalManifest.ProposalCount != 1 {
		t.Fatal("live review package differs from the sole frozen proposal")
	}
	summary["review_display_id"] = review.Review.Display.ID
	summary["review_subject"] = review.Review.Subject
	detectiveLiveReadback(t, ctx, launchers["query"], detectiveCheckpointConfigPath(directory, dbrole.ProfileQuery), output, "pending", member.Locator.ProposalOccurrenceID, "", nativeStatement, sourceID, allowed, forbidden)
	assertDetectiveBatchCounts(t, ctx, fixture, 1, false)
	if mode == "handoff" {
		summary["reached_stage"], summary["completion_status"] = "review_prepared", "handoff_prepared"
		return
	}
	mustRun("review-decide", "admit\n"+detectiveLiveReason+"\n"+review.Review.Display.ID+"\n", "review", "decide", "-review", reviewPath, "-out", decisionPath)
	decisionBefore := readDetectiveCheckpoint(t, decisionPath)
	summary["expected_assessment_input_sha256"] = map[string]string{
		"source": strings.TrimPrefix(stdioContentHash(source), "sha256:"), "claim": strings.TrimPrefix(stdioContentHash([]byte(nativeStatement)), "sha256:"),
		"reason": strings.TrimPrefix(stdioContentHash([]byte(detectiveLiveReason)), "sha256:"), "review": strings.TrimPrefix(stdioContentHash([]byte(review.Review.Display.PayloadUTF8)), "sha256:"),
	}
	summary["model_assessment_attempts"] = 1
	_, validAdvice := run("review-assess", "", "review", "assess", "-decision", decisionPath, "-base-url", selected["BASE_URL"], "-model", selected["MODEL"], "-out", assessmentPath, "-timeout", "120s")
	assertDetectiveCheckpointUnchanged(t, decisionPath, decisionBefore)
	assertDetectiveBatchCounts(t, ctx, fixture, 1, false)
	if !validAdvice {
		summary["completion_status"], summary["model_assessment"] = "blocked_advice", "invalid_or_unavailable; no model retry or writer"
		return
	}
	var advice struct {
		SchemaVersion   string            `json:"schema_version"`
		Model           string            `json:"model"`
		ModelDigest     string            `json:"model_digest"`
		Verdict         string            `json:"verdict"`
		Concerns        []json.RawMessage `json:"concerns"`
		AuthorityEffect string            `json:"authority_effect"`
		Validation      string            `json:"validation"`
	}
	if json.Unmarshal(readDetectiveCheckpoint(t, assessmentPath), &advice) != nil || advice.SchemaVersion != "detective-reason-assessment/v1" || advice.Model != selected["MODEL"] || advice.AuthorityEffect != "none" || advice.Validation != "closed_schema_and_exact_quotes_only" {
		t.Fatal("successful advice command did not retain its expected non-authoritative contract")
	}
	mustRun("review-questions", "", "review", "questions", "-assessment", assessmentPath)
	summary["model_assessment"] = map[string]any{"verdict": advice.Verdict, "concerns": len(advice.Concerns), "model_digest": advice.ModelDigest, "validation": advice.Validation, "semantic_acceptance": false}
	if advice.Verdict != "no_specific_concern" || len(advice.Concerns) != 0 {
		summary["completion_status"], summary["blocked_reason"] = "blocked_advice", "experiment policy requires no specific concern; production apply has no model gate"
		return
	}
	applyArgs := []string{"review", "apply", "-decision", decisionPath, "-review-command", launchers["reviewer"], "-query-command", launchers["query"], "-confirm-display", review.Review.Display.ID}
	appliedBody := mustRun("review-apply", "", applyArgs...)
	var applied detectiveHumanReviewExecution
	if json.Unmarshal(appliedBody, &applied) != nil || applied.State != "admitted_verified" || !applied.ReadbackVerified || applied.Admission.ProposalOccurrenceID != member.Locator.ProposalOccurrenceID || applied.Admission.CanonicalRef == "" || applied.Admission.Replayed {
		t.Fatal("synthetic admission did not verify the exact new candidate")
	}
	replayBody := mustRun("review-replay", "", applyArgs...)
	var replay detectiveHumanReviewExecution
	if json.Unmarshal(replayBody, &replay) != nil || !replay.Admission.Replayed {
		t.Fatal("synthetic admission exact replay was not identified")
	}
	replay.Admission.Replayed = false
	if !reflect.DeepEqual(applied, replay) {
		t.Fatal("exact admission replay changed authority identities")
	}
	assertDetectiveCheckpointUnchanged(t, decisionPath, decisionBefore)
	detectiveLiveReadback(t, ctx, launchers["query"], detectiveCheckpointConfigPath(directory, dbrole.ProfileQuery), output, "admitted", member.Locator.ProposalOccurrenceID, applied.Admission.CanonicalRef, nativeStatement, sourceID, allowed, forbidden)
	assertDetectiveBatchCounts(t, ctx, fixture, 1, true)
	summary["authority_effects"] = map[string]any{"source_intake": true, "pending_proposals": 0, "admitted_proposals": 1, "canonical_mutation": true, "review_decision": "scripted_disposable_only", "admission": applied.Admission}
	summary["reached_stage"], summary["completion_status"] = "admitted_replayed_and_readback_verified", "synthetic_roundtrip_completed"
}

func detectiveLiveEndpoint(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && u.Host == "127.0.0.1:"+strconv.Itoa(port)
}

func detectiveLiveInput(t *testing.T, path string, limit int64) []byte {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || resolved != path || !info.Mode().IsRegular() || info.Size() > limit {
		t.Fatal("live experiment input must be an explicit bounded regular file without symlinks")
	}
	body, err := os.ReadFile(path)
	if err != nil || int64(len(body)) > limit {
		t.Fatal("cannot read the selected bounded live experiment input")
	}
	return body
}

func detectiveLiveNoSecrets(t *testing.T, body []byte, forbidden [][]byte) {
	t.Helper()
	for _, value := range forbidden {
		if len(value) != 0 && detectiveCheckpointContains(body, value) {
			t.Fatal("live experiment attempted to retain private runtime credential or launcher material")
		}
	}
}

func detectiveLiveSave(t *testing.T, directory, name string, body []byte, allowed map[string]bool, forbidden [][]byte) {
	t.Helper()
	if filepath.Base(name) != name {
		t.Fatal("live experiment artifact name escaped its private directory")
	}
	detectiveLiveNoSecrets(t, body, forbidden)
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot create a new private live experiment artifact")
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		t.Fatal("cannot retain the complete live experiment artifact")
	}
	if err := file.Close(); err != nil {
		t.Fatal("cannot finish the private live experiment artifact")
	}
	allowed[name] = true
}

func detectiveLiveJSON(t *testing.T, directory, name string, value any, allowed map[string]bool, forbidden [][]byte) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal("cannot encode the live experiment observation")
	}
	detectiveLiveSave(t, directory, name, append(body, '\n'), allowed, forbidden)
}

func detectiveLiveCommand(t *testing.T, ctx context.Context, binary, output, stage, input string, args []string, allowed map[string]bool, forbidden [][]byte) ([]byte, bool) {
	t.Helper()
	// Unlike the existing 60-second synthetic helper, this allows one selected
	// real-model assessment its explicit 120-second CLI deadline plus shutdown.
	childCtx, cancel := context.WithTimeout(ctx, 135*time.Second)
	defer cancel()
	command := exec.CommandContext(childCtx, binary, args...)
	command.Env, command.Stdin = detectiveCheckpointEnvironment(), strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr, command.WaitDelay = &stdout, &stderr, 2*time.Second
	err := command.Run()
	detectiveLiveSave(t, output, stage+".stdout", stdout.Bytes(), allowed, forbidden)
	detectiveLiveSave(t, output, stage+".stderr", stderr.Bytes(), allowed, forbidden)
	if bytes.Contains(stderr.Bytes(), []byte("WARNING: DATA RACE")) {
		t.Fatal("live experiment child reported a data race")
	}
	return append([]byte(nil), stdout.Bytes()...), err == nil && childCtx.Err() == nil
}

func detectiveLiveReadback(t *testing.T, ctx context.Context, command, configPath, output, state, occurrenceID, canonicalID, statement, sourceID string, allowed map[string]bool, forbidden [][]byte) {
	t.Helper()
	// The tapped wrapper pins its own protected configuration; the shared
	// launcher-process helper's argv cannot select another inner launcher.
	query := startProvisionedLauncher(t, ctx, command, configPath, "ahe-query-mcp")
	listed := query.request(t, "tools/list", map[string]any{})
	var inventory struct {
		Tools []mcpstdio.Tool `json:"tools"`
	}
	if listed.Error != nil || json.Unmarshal(listed.Result, &inventory) != nil || len(inventory.Tools) != 13 {
		t.Fatal("independent live Query did not expose its complete inventory")
	}
	want := mcpquery.NewBackend(&evidencequerymcp.Server{}).Tools()
	if len(want) != len(inventory.Tools) {
		t.Fatal("independent live Query inventory differs from the selected native contract")
	}
	for i, tool := range inventory.Tools {
		gotBody, err := json.Marshal(tool)
		if err != nil {
			t.Fatal("cannot encode observed live Query schema")
		}
		wantBody, err := json.Marshal(want[i])
		if err != nil || !detectiveCheckpointJSONEqual(gotBody, wantBody) {
			t.Fatal("independent live Query schema differs from the selected native contract")
		}
	}
	detectiveLiveJSON(t, output, "query-"+state+"-inventory.json", inventory, allowed, forbidden)
	proposal := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]string{"proposal_occurrence_id": occurrenceID})
	if proposal.RecordRef.Kind != "proposal" || proposal.RecordRef.ID != occurrenceID || proposal.AdmissionOutcome != state || proposal.StatementText != statement || proposal.Source.SourceID != sourceID ||
		(state == "pending" && proposal.CanonicalRef != nil) || (state == "admitted" && (proposal.CanonicalRef == nil || *proposal.CanonicalRef != canonicalID)) {
		t.Fatal("independent live Query differs from the exact source-bound candidate lifecycle")
	}
	detectiveLiveJSON(t, output, "query-"+state+"-proposal.json", proposal, allowed, forbidden)
	if state == "admitted" {
		canonical := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]string{"canonical_id": canonicalID})
		if canonical.RecordRef.Kind != "canonical_evidence" || canonical.RecordRef.ID != canonicalID || canonical.StatementText != statement || canonical.Source.SourceID != sourceID || canonical.AdmissionOutcome != "admitted" {
			t.Fatal("independent live Query did not return the selected admitted canonical claim")
		}
		detectiveLiveJSON(t, output, "query-admitted-canonical.json", canonical, allowed, forbidden)
	}
	query.finish(t)
}
