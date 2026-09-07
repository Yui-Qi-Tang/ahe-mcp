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
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

const detectiveCheckpointAPISecret = "synthetic-checkpoint-api-secret"

// This witness proves that a saved one-candidate checkpoint is sufficient to
// resume after the original source and model are unavailable. It deliberately
// injects a Query failure after the intake write: failure is not rollback, and
// replay must converge on the same pending authority before reporting success.
func TestIntegrationDetectiveCheckpointResumeProtectedLauncher(t *testing.T) {
	detectiveRoot := detectivePendingSourceRoot(t)
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Second)
	defer cancel()
	directory := provisioningProtectedDirectory(t, databaseURL)
	binaries := buildProvisioningCommands(t, ctx, directory)
	detective := buildDetectivePendingCommand(t, ctx, detectiveRoot, directory)
	fixture := newProvisioningLauncherFixture(t, ctx, databaseURL, binaries["ahe-runtime-admin"])
	intakeWrapper := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileIntake)
	queryWrapper := detectiveCheckpointLauncher(t, directory, binaries, fixture, dbrole.ProfileQuery)

	const row = "| Pending recovery | **LAB PROVEN** | synthetic checkpoint only |"
	const statement = "The synthetic checkpoint preserves one pending recovery candidate."
	const sourceID = "mock:detective-checkpoint-resume"
	const statusClause = "LAB PROVEN"
	sourceText := "# Synthetic Detective checkpoint\n\n## Status at a Glance\n\n| Capability | Status | Boundary |\n| --- | --- | --- |\n" + row + "\n"
	inputPath := filepath.Join(directory, "checkpoint-STATUS.md")
	checkpointPath := filepath.Join(directory, "pending-checkpoint.json")
	writeProvisioningProtectedFile(t, inputPath, []byte(sourceText))
	model, modelRequests := newDetectivePendingModel(t, row, statement)

	forbidden := [][]byte{
		[]byte(detectiveCheckpointAPISecret), []byte(model.URL), []byte(intakeWrapper),
		[]byte(queryWrapper), []byte(databaseURL),
		[]byte(fixture.identities[dbrole.ProfileIntake].dsn),
		[]byte(fixture.identities[dbrole.ProfileQuery].dsn),
	}
	prepareArgs := []string{
		"-input", inputPath, "-base-url", model.URL + "/v1", "-model", "mock-detective-model",
		"-row-chunks", "-row-line", "7", "-status-clause", statusClause,
		"-ahe-checkpoint", checkpointPath, "-ahe-prepare-only", "-ahe-source-id", sourceID,
		"-timeout", "30s",
	}
	preparedBody := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden, prepareArgs...)
	var prepared detectiveCheckpointPreparedReceipt
	if json.Unmarshal(preparedBody, &prepared) != nil || prepared.SchemaVersion != "detective-pending-prepared/v1" ||
		prepared.State != "prepared" || prepared.CheckpointDigest == "" {
		t.Fatal("Detective did not return the exact checkpoint-prepared marker")
	}
	if modelRequests.Load() != 1 {
		t.Fatal("checkpoint preparation did not make exactly one model request")
	}

	checkpointBody := readDetectiveCheckpoint(t, checkpointPath)
	checkpoint := decodeDetectiveCheckpoint(t, checkpointBody, inputPath, sourceID, sourceText, row, statement, statusClause)
	if checkpoint.Digest != prepared.CheckpointDigest {
		t.Fatal("prepared marker does not identify the immutable checkpoint")
	}
	for _, secret := range forbidden {
		if len(secret) != 0 && detectiveCheckpointContains(checkpointBody, secret) {
			t.Fatal("checkpoint retained an API credential, DSN, model URL, or launcher path")
		}
	}
	for _, table := range []string{"source_snapshots", "extraction_runs", "extraction_attempts", "proposal_occurrences", "canonical_graph_nodes", "admission_decisions"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}

	t.Run("existing_checkpoint_rejected_before_model", func(t *testing.T) {
		runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden, prepareArgs...)
		if modelRequests.Load() != 1 {
			t.Fatal("existing checkpoint path was not rejected before model extraction")
		}
		assertDetectiveCheckpointUnchanged(t, checkpointPath, checkpointBody)
	})

	t.Run("invalid_checkpoints_start_no_mcp", func(t *testing.T) {
		trimmed := bytes.TrimSpace(checkpointBody)
		unknown := append([]byte(nil), trimmed[:len(trimmed)-1]...)
		unknown = append(unknown, []byte(`,"unknown_checkpoint_field":true}`)...)
		for _, test := range []struct {
			name string
			body []byte
		}{
			{name: "unknown", body: unknown},
			{name: "malformed", body: []byte(`{"schema_version":`)},
		} {
			t.Run(test.name, func(t *testing.T) {
				path := filepath.Join(directory, "invalid-"+test.name+".json")
				writeProvisioningProtectedFile(t, path, test.body)
				intakeProbe, intakeMarker := detectiveCheckpointProbe(t, directory, test.name+"-intake")
				queryProbe, queryMarker := detectiveCheckpointProbe(t, directory, test.name+"-query")
				runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden,
					"-ahe-resume", path, "-ahe-ingest-command", intakeProbe,
					"-ahe-query-command", queryProbe, "-timeout", "30s")
				for _, marker := range []string{intakeMarker, queryMarker} {
					if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("invalid checkpoint started an MCP launcher")
					}
				}
			})
		}
		for _, table := range []string{"source_snapshots", "extraction_attempts", "proposal_occurrences"} {
			stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
		}
	})

	model.Close()
	if err := os.Remove(inputPath); err != nil {
		t.Fatal("cannot remove original synthetic source before resume")
	}
	queryFailureMarker := filepath.Join(directory, "query-failure-invoked")
	queryFailure := filepath.Join(directory, "query-failure-launcher")
	writeDetectiveCheckpointExecutable(t, queryFailure,
		"#!/bin/sh\nprintf invoked > "+detectivePendingShellQuote(queryFailureMarker)+"\nexit 23\n")
	resumeArgs := []string{
		"-ahe-resume", checkpointPath, "-ahe-ingest-command", intakeWrapper,
		"-ahe-query-command", queryFailure, "-timeout", "30s",
	}
	runDetectiveCheckpointCommand(t, ctx, detective, false, forbidden, resumeArgs...)
	if _, err := os.Stat(queryFailureMarker); err != nil {
		t.Fatal("injected Query failure was not reached after pending submission")
	}
	assertDetectiveCheckpointUnchanged(t, checkpointPath, checkpointBody)
	for _, table := range []string{"source_snapshots", "extraction_views", "extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 1)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_ordinary_admission_manifests", "canonical_source_claim_review_bindings", "canonical_contradiction_proposals", "canonical_supersession_admission_events"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
	persisted := detectiveCheckpointPersistedIDs{}
	if err := fixture.pool.QueryRow(ctx, `SELECT source_snapshot_id FROM source_snapshots`).Scan(&persisted.SourceSnapshotID); err != nil {
		t.Fatal("cannot read the single persisted source identity")
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT extraction_view_id FROM extraction_views`).Scan(&persisted.ExtractionViewID); err != nil {
		t.Fatal("cannot read the single persisted extraction view identity")
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT extraction_attempt_id FROM extraction_attempts`).Scan(&persisted.ExtractionAttemptID); err != nil {
		t.Fatal("cannot read the single persisted extraction attempt identity")
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT proposal_occurrence_id FROM proposal_occurrences`).Scan(&persisted.ProposalOccurrenceID); err != nil {
		t.Fatal("cannot read the single persisted proposal identity")
	}

	resumeArgs[5] = queryWrapper
	resumeBody := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden, resumeArgs...)
	resume := decodeDetectiveCheckpointResume(t, resumeBody, checkpoint, persisted)
	assertDetectiveCheckpointUnchanged(t, checkpointPath, checkpointBody)
	if !detectiveCheckpointJSONEqual(checkpoint.Batch, resume.Batch) {
		t.Fatal("resume output changed the saved validated candidate batch")
	}

	query := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], detectiveCheckpointConfigPath(directory, dbrole.ProfileQuery), "ahe-query-mcp")
	readback := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": resume.Handoff.ProposalOccurrenceID})
	if readback.RecordRef.Kind != "proposal" || readback.RecordRef.ID != resume.Handoff.ProposalOccurrenceID ||
		readback.AdmissionOutcome != "pending" || readback.CanonicalRef != nil || readback.Canonical != nil ||
		readback.StatementText != statement || readback.Source.SourceSnapshotID != resume.Handoff.SourceSnapshotID ||
		readback.ExtractionViewID != resume.Handoff.ExtractionViewID || readback.Source.SourceID != sourceID ||
		readback.Source.SourceVersion != stdioContentHash([]byte(sourceText)) || readback.Source.RawContentHash != stdioContentHash([]byte(sourceText)) ||
		len(readback.SourceRefs) != 1 || readback.SourceRefs[0].QuotedText != row || readback.SourceRefs[0].QuotedTextHash != stdioContentHash([]byte(row)) {
		t.Fatal("independent Query did not preserve the exact checkpointed pending record")
	}
	query.finish(t)

	secondBody := runDetectiveCheckpointCommand(t, ctx, detective, true, forbidden, resumeArgs...)
	second := decodeDetectiveCheckpointResume(t, secondBody, checkpoint, persisted)
	if !reflect.DeepEqual(second, resume) {
		t.Fatal("further checkpoint resume changed its verified pending result")
	}
	assertDetectiveCheckpointUnchanged(t, checkpointPath, checkpointBody)
	if modelRequests.Load() != 1 {
		t.Fatal("checkpoint resume consulted the unavailable model")
	}
	if _, err := os.Stat(inputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checkpoint resume recreated or required the removed original source")
	}
	for _, table := range []string{"source_snapshots", "extraction_views", "extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 1)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
}

type detectiveCheckpointPreparedReceipt struct {
	SchemaVersion    string `json:"schema_version"`
	State            string `json:"state"`
	CheckpointDigest string `json:"checkpoint_digest"`
}

type detectiveCheckpointFile struct {
	SchemaVersion          string          `json:"schema_version"`
	RequestIdentityVersion string          `json:"request_identity_version"`
	SourceID               string          `json:"source_id"`
	RawText                string          `json:"raw_text"`
	Batch                  json.RawMessage `json:"batch"`
	StatusClause           string          `json:"status_clause"`
	Digest                 string          `json:"digest"`
}

type detectiveCheckpointResumeReceipt struct {
	SchemaVersion    string          `json:"schema_version"`
	CheckpointDigest string          `json:"checkpoint_digest"`
	State            string          `json:"state"`
	Batch            json.RawMessage `json:"batch"`
	Handoff          struct {
		SchemaVersion string `json:"schema_version"`
		evidenceingestionmcp.SubmitExtractorOutputResponse
	} `json:"handoff"`
	ReadbackVerified bool `json:"readback_verified"`
}

type detectiveCheckpointPersistedIDs struct {
	SourceSnapshotID     string
	ExtractionViewID     string
	ExtractionAttemptID  string
	ProposalOccurrenceID string
}

func detectiveCheckpointLauncher(t *testing.T, directory string, binaries map[string]string, fixture provisioningLauncherFixture, profile dbrole.Profile) string {
	t.Helper()
	identity := fixture.identities[profile]
	command := "ahe-ingest-mcp"
	if profile == dbrole.ProfileQuery {
		command = "ahe-query-mcp"
	}
	credentialPath := filepath.Join(directory, "checkpoint-"+string(profile)+".dsn")
	writeProvisioningProtectedFile(t, credentialPath, []byte(identity.dsn))
	configPath := detectiveCheckpointConfigPath(directory, profile)
	writeProvisioningConfig(t, configPath, provisioningLauncherConfig{
		SchemaVersion: "ahe-mcp-launcher/v1", BinaryPath: binaries[command], DatabaseDNSFile: credentialPath,
		Database: fixture.database, SessionUser: identity.login, Schema: fixture.schema,
		Role: identity.group, Profile: string(profile), PrincipalID: "mock:detective-checkpoint:" + string(profile),
	})
	wrapper := filepath.Join(directory, "checkpoint-"+string(profile)+"-launcher")
	writeDetectiveCheckpointExecutable(t, wrapper,
		"#!/bin/sh\nexec "+detectivePendingShellQuote(binaries["ahe-mcp-launch"])+" --config "+detectivePendingShellQuote(configPath)+"\n")
	return wrapper
}

func detectiveCheckpointConfigPath(directory string, profile dbrole.Profile) string {
	return filepath.Join(directory, "checkpoint-"+string(profile)+".json")
}

func writeDetectiveCheckpointExecutable(t *testing.T, path, body string) {
	t.Helper()
	writeProvisioningProtectedFile(t, path, []byte(body))
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal("cannot protect checkpoint launcher fixture")
	}
}

func detectiveCheckpointProbe(t *testing.T, directory, name string) (string, string) {
	t.Helper()
	path := filepath.Join(directory, name+"-probe")
	marker := filepath.Join(directory, name+"-started")
	writeDetectiveCheckpointExecutable(t, path,
		"#!/bin/sh\nprintf started > "+detectivePendingShellQuote(marker)+"\nexit 91\n")
	return path, marker
}

func detectiveCheckpointEnvironment() []string {
	environment := provisioningPollutedEnvironment()
	return append(environment,
		"OPENAI_API_KEY="+detectiveCheckpointAPISecret,
		"DETECTIVE_MODEL=must-not-run",
		"DETECTIVE_BASE_URL=http://127.0.0.1:1/v1",
		"DETECTIVE_AHE_INGEST_COMMAND=/invalid/inherited-launcher",
	)
}

func runDetectiveCheckpointCommand(t *testing.T, ctx context.Context, binary string, wantSuccess bool, forbidden [][]byte, args ...string) []byte {
	t.Helper()
	childCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, binary, args...)
	cmd.Env = detectiveCheckpointEnvironment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if childCtx.Err() != nil || bytes.Contains(stderr.Bytes(), []byte("WARNING: DATA RACE")) {
		t.Fatal("Detective checkpoint command timed out or reported a data race")
	}
	for _, value := range forbidden {
		if len(value) != 0 && (detectiveCheckpointContains(stdout.Bytes(), value) || detectiveCheckpointContains(stderr.Bytes(), value)) {
			t.Fatal("Detective checkpoint command exposed a credential, model URL, or launcher path")
		}
	}
	if wantSuccess {
		if err != nil || stdout.Len() == 0 {
			t.Fatal("Detective checkpoint command did not return its success receipt")
		}
	} else if err == nil || stdout.Len() != 0 {
		t.Fatal("Detective checkpoint command did not fail without a success receipt")
	}
	return append([]byte(nil), stdout.Bytes()...)
}

// Inspect decoded string values as well as raw diagnostics: JSON escaping must
// not hide a synthetic DSN or credential from the test's leak assertions.
func detectiveCheckpointContains(body, forbidden []byte) bool {
	if bytes.Contains(body, forbidden) {
		return true
	}
	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		return false
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch value := value.(type) {
		case string:
			return strings.Contains(value, string(forbidden))
		case []any:
			for _, item := range value {
				if contains(item) {
					return true
				}
			}
		case map[string]any:
			for key, item := range value {
				if contains(key) || contains(item) {
					return true
				}
			}
		}
		return false
	}
	return contains(decoded)
}

func TestDetectiveCheckpointLeakAssertionDecodesEscapedStrings(t *testing.T) {
	const forbidden = "synthetic?socket=private&port=1"
	body, err := json.Marshal(map[string]any{"nested": []string{forbidden}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(forbidden)) || !detectiveCheckpointContains(body, []byte(forbidden)) {
		t.Fatal("synthetic credential escaping defeated the decoded-string assertion")
	}
	if detectiveCheckpointContains([]byte(`{"safe":"not a credential"}`), []byte(forbidden)) {
		t.Fatal("unrelated JSON string triggered the credential assertion")
	}
}

func readDetectiveCheckpoint(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("Detective did not publish a private regular checkpoint")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read the prepared checkpoint")
	}
	return body
}

func decodeDetectiveCheckpoint(t *testing.T, body []byte, inputPath, sourceID, sourceText, row, statement, statusClause string) detectiveCheckpointFile {
	t.Helper()
	var fields map[string]json.RawMessage
	var checkpoint detectiveCheckpointFile
	if json.Unmarshal(body, &fields) != nil || json.Unmarshal(body, &checkpoint) != nil {
		t.Fatal("prepared checkpoint is not valid JSON")
	}
	for _, name := range []string{"schema_version", "request_identity_version", "source_id", "raw_text", "batch", "status_clause", "digest"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("prepared checkpoint omitted a required closed-schema field")
		}
	}
	if len(fields) != 7 || checkpoint.SchemaVersion != "detective-pending-checkpoint/v1" || checkpoint.RequestIdentityVersion != "detective-v1" ||
		checkpoint.SourceID != sourceID || checkpoint.RawText != sourceText || checkpoint.StatusClause != statusClause || checkpoint.Digest == "" {
		t.Fatal("prepared checkpoint changed its versioned exact inputs")
	}
	var batch struct {
		SchemaVersion string `json:"schema_version"`
		Source        struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"source"`
		Extractor struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Model   string `json:"model"`
		} `json:"extractor"`
		Rows []struct {
			Status string `json:"status"`
			Result *struct {
				Outcome string `json:"outcome"`
				Records []struct {
					Statement string `json:"statement"`
					Citation  struct {
						ExactQuote string `json:"exact_quote"`
					} `json:"citation"`
				} `json:"records"`
			} `json:"result"`
		} `json:"rows"`
	}
	if json.Unmarshal(checkpoint.Batch, &batch) != nil || batch.SchemaVersion != "lab-status-row-batch/v0" ||
		batch.Source.Path != inputPath || batch.Source.SHA256 != strings.TrimPrefix(stdioContentHash([]byte(sourceText)), "sha256:") ||
		batch.Extractor.Name != "lab-status-extractor" || batch.Extractor.Version != "0.1.0" || batch.Extractor.Model != "mock-detective-model" ||
		len(batch.Rows) != 1 || batch.Rows[0].Status != "validated" || batch.Rows[0].Result == nil || batch.Rows[0].Result.Outcome != "extracted" ||
		len(batch.Rows[0].Result.Records) != 1 || batch.Rows[0].Result.Records[0].Statement != statement || batch.Rows[0].Result.Records[0].Citation.ExactQuote != row {
		t.Fatal("prepared checkpoint lost the one exact validated candidate")
	}
	return checkpoint
}

func assertDetectiveCheckpointUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	if got := readDetectiveCheckpoint(t, path); !bytes.Equal(got, want) {
		t.Fatal("checkpoint was replaced or mutated during rejection or resume")
	}
}

func decodeDetectiveCheckpointResume(t *testing.T, body []byte, checkpoint detectiveCheckpointFile, persisted detectiveCheckpointPersistedIDs) detectiveCheckpointResumeReceipt {
	t.Helper()
	var result detectiveCheckpointResumeReceipt
	if json.Unmarshal(body, &result) != nil || result.SchemaVersion != "detective-pending-resume/v1" ||
		result.CheckpointDigest != checkpoint.Digest || result.State != "pending_verified" || !result.ReadbackVerified ||
		result.Handoff.SchemaVersion != "ahe-mcp-pending-handoff/v0" || result.Handoff.Status != "pending" ||
		!result.Handoff.Replayed || result.Handoff.ProposalCount != 1 ||
		result.Handoff.SourceSnapshotID != persisted.SourceSnapshotID || result.Handoff.ExtractionViewID != persisted.ExtractionViewID ||
		result.Handoff.ExtractionAttemptID != persisted.ExtractionAttemptID || result.Handoff.ProposalOccurrenceID != persisted.ProposalOccurrenceID {
		t.Fatal("checkpoint resume did not return the exact replayed pending/readback receipt")
	}
	return result
}

func detectiveCheckpointJSONEqual(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}
