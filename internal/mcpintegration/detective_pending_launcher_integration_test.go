//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

// This witness builds Detective from the explicitly selected shared checkout instead of
// importing its internal packages or replacing its CLI with a test helper.
// Only synthetic local text and an in-process HTTP model fixture are used.
// The shared fixture provisions a reviewer identity, but this test never starts
// that profile, calls review/admission, or claims a provider intake contract.
func TestIntegrationDetectivePendingProtectedLauncherRoundTrip(t *testing.T) {
	detectiveRoot := detectivePendingSourceRoot(t)
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	defer cancel()
	directory := provisioningProtectedDirectory(t, databaseURL)
	binaries := buildProvisioningCommands(t, ctx, directory)
	detective := buildDetectivePendingCommand(t, ctx, detectiveRoot, directory)
	fixture := newProvisioningLauncherFixture(t, ctx, databaseURL, binaries["ahe-runtime-admin"])
	paths := make(map[dbrole.Profile]string)
	for _, profile := range []dbrole.Profile{dbrole.ProfileIntake, dbrole.ProfileQuery} {
		identity := fixture.identities[profile]
		command := "ahe-ingest-mcp"
		if profile == dbrole.ProfileQuery {
			command = "ahe-query-mcp"
		}
		credentialPath := filepath.Join(directory, "detective-"+string(profile)+".dsn")
		writeProvisioningProtectedFile(t, credentialPath, []byte(identity.dsn))
		configPath := filepath.Join(directory, "detective-"+string(profile)+".json")
		writeProvisioningConfig(t, configPath, provisioningLauncherConfig{
			SchemaVersion: "ahe-mcp-launcher/v1", BinaryPath: binaries[command], DatabaseDNSFile: credentialPath,
			Database: fixture.database, SessionUser: identity.login, Schema: fixture.schema,
			Role: identity.group, Profile: string(profile), PrincipalID: "mock:detective:" + string(profile),
		})
		paths[profile] = configPath
	}
	// The consumer receives one executable path, never a shell command or DSN.
	wrapper := filepath.Join(directory, "detective-intake-launcher")
	script := "#!/bin/sh\nexec " + detectivePendingShellQuote(binaries["ahe-mcp-launch"]) + " --config " + detectivePendingShellQuote(paths[dbrole.ProfileIntake]) + "\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal("cannot create private credential-free intake wrapper")
	}

	const row = "| Pending intake | **LAB PROVEN** | synthetic fixture only |"
	const sourceText = "# Synthetic Detective fixture\n\n## Status at a Glance\n\n| Capability | Status | Boundary |\n| --- | --- | --- |\n" + row + "\n"
	const statement = "The synthetic source reports pending intake as lab proven."
	const sourceID = "mock:detective-pending-launcher"
	inputPath := filepath.Join(directory, "STATUS.md")
	writeProvisioningProtectedFile(t, inputPath, []byte(sourceText))
	model, modelRequests := newDetectivePendingModel(t, row, statement)

	intake := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], paths[dbrole.ProfileIntake], "ahe-ingest-mcp")
	query := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], paths[dbrole.ProfileQuery], "ahe-query-mcp")
	t.Run("bounded_tool_inventories", func(t *testing.T) {
		intake.assertTools(t, []string{"submit_manual_evidence", "submit_text_source", "submit_external_source", "submit_extractor_output", "get_extractor_input"})
		query.assertTools(t, []string{"get_evidence_record", "list_evidence_records", "search_evidence_records", "get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance", "get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path", "get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal", "get_canonical_supersession_head", "get_canonical_supersession_currentness"})
	})
	first := runDetectivePendingCommand(t, ctx, detective, wrapper, inputPath, model.URL, sourceID)
	handoff := first.Handoff
	if handoff.SchemaVersion != "ahe-mcp-pending-handoff/v0" || handoff.Status != "pending" || handoff.Replayed || handoff.ProposalCount != 1 ||
		handoff.SourceSnapshotID == "" || handoff.ExtractionViewID == "" || handoff.ExtractionAttemptID == "" || handoff.ProposalOccurrenceID == "" {
		t.Fatal("Detective did not return one new pending proposal with complete source coordinates")
	}
	if first.Batch.Source.SHA256 != strings.TrimPrefix(stdioContentHash([]byte(sourceText)), "sha256:") || len(first.Batch.Rows) != 1 ||
		first.Batch.Rows[0].Status != "validated" || first.Batch.Rows[0].Result.Outcome != "extracted" || len(first.Batch.Rows[0].Result.Records) != 1 ||
		first.Batch.Rows[0].Result.Records[0].Statement != statement || first.Batch.Rows[0].Result.Records[0].Citation.ExactQuote != row {
		t.Fatal("Detective CLI lost its validated row, exact local source or controller-hydrated citation")
	}
	t.Run("exact_source_and_span_readback", func(t *testing.T) {
		input := authorityProcessTool[evidenceingestionmcp.GetExtractorInputResponse](t, intake, "get_extractor_input", map[string]any{"extraction_view_id": handoff.ExtractionViewID})
		if input.SourceSnapshotID != handoff.SourceSnapshotID || input.ExtractionViewID != handoff.ExtractionViewID || input.RenderedText != sourceText ||
			input.RawContentHash != stdioContentHash([]byte(sourceText)) || input.RenderedContentHash != input.RawContentHash {
			t.Fatal("Detective intake did not persist exact synthetic document bytes")
		}
		var selected int
		for _, span := range input.Spans {
			if span.DisplayLine != 7 {
				continue
			}
			selected++
			if span.Text != row || span.QuotedTextHash != stdioContentHash([]byte(row)) || span.StartByte != strings.Index(sourceText, row) || span.EndByte != span.StartByte+len(row) {
				t.Fatal("Detective row citation does not match the persisted native span")
			}
		}
		if selected != 1 {
			t.Fatal("Detective row did not resolve to exactly one native span")
		}
	})
	readback := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": handoff.ProposalOccurrenceID})
	if readback.RecordRef.Kind != "proposal" || readback.RecordRef.ID != handoff.ProposalOccurrenceID || readback.AdmissionOutcome != "pending" || readback.CanonicalRef != nil || readback.Canonical != nil ||
		readback.StatementText != statement || readback.Source.SourceSnapshotID != handoff.SourceSnapshotID || readback.ExtractionViewID != handoff.ExtractionViewID ||
		readback.Source.SourceID != sourceID || readback.Source.SourceVersion != stdioContentHash([]byte(sourceText)) || readback.Source.RawContentHash != stdioContentHash([]byte(sourceText)) ||
		readback.Source.ExternalSource != nil || readback.Source.MCPRead != nil || len(readback.SourceRefs) != 1 || readback.SourceRefs[0].QuotedText != row || readback.SourceRefs[0].QuotedTextHash != stdioContentHash([]byte(row)) {
		t.Fatal("Query did not preserve pending-only lifecycle, manual snapshot identity and exact source quote")
	}
	t.Run("exact_retry_preserves_pending_identity", func(t *testing.T) {
		replay := runDetectivePendingCommand(t, ctx, detective, wrapper, inputPath, model.URL, sourceID)
		if !replay.Handoff.Replayed {
			t.Fatal("exact Detective retry was not marked replayed")
		}
		replay.Handoff.Replayed = false
		if !reflect.DeepEqual(replay, first) {
			t.Fatal("exact Detective retry changed its validated row or persisted proposal identity")
		}
	})
	if modelRequests.Load() != 2 {
		t.Fatal("Detective must make exactly one synthetic model request per selected-row invocation")
	}
	t.Run("authority_boundary_rejects_writers", func(t *testing.T) {
		args := map[string]any{"proposal_occurrence_id": handoff.ProposalOccurrenceID, "decision_by": "forged-reviewer", "decision_reason": "synthetic denial probe, not approval", "outcome": "rejected", "request_id": "forbidden-detective-writer"}
		for _, tool := range []string{"get_source_claim_review", "admit_reviewed_source_claim", "admit_pending_proposal", "record_pending_proposal_disposition", "admit_pending_supersession", "submit_canonical_contradiction_proposal", "activate_repository_source_generation"} {
			intake.assertDenied(t, tool, args)
			query.assertDenied(t, tool, args)
		}
		for _, tool := range []string{"submit_text_source", "submit_external_source", "submit_extractor_output", "submit_manual_evidence"} {
			query.assertDenied(t, tool, args)
		}
	})
	t.Run("no_canonical_mutation", func(t *testing.T) {
		for _, table := range []string{"source_snapshots", "extraction_views", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
			stdioAssertTableCount(t, ctx, fixture.pool, table, 1)
		}
		for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_ordinary_admission_manifests", "canonical_source_claim_review_bindings", "canonical_contradiction_proposals", "canonical_supersession_admission_events", "repository_generation_activation_requests", "repository_source_heads"} {
			stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
		}
		after := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": handoff.ProposalOccurrenceID})
		if !reflect.DeepEqual(after, readback) {
			t.Fatal("replay or denied writers changed the pending Query record")
		}
		listed := authorityProcessTool[evidencequerymcp.ListEvidenceRecordsResponse](t, query, "list_evidence_records", map[string]any{"source_snapshot_id": handoff.SourceSnapshotID, "admission_outcome": "pending", "limit": 5})
		if listed.Count != 1 || len(listed.Records) != 1 || listed.Records[0].RecordRef.ID != handoff.ProposalOccurrenceID {
			t.Fatal("bounded Query did not return exactly the one pending Detective proposal")
		}
	})
	// EOF and a successful Wait are required; failure-only cleanup is not proof
	// that race-enabled children exited normally after their final response.
	query.finish(t)
	intake.finish(t)
}

func detectivePendingSourceRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("AHE_DETECTIVE_SOURCE_ROOT")
	if root == "" {
		t.Skip("AHE_DETECTIVE_SOURCE_ROOT is not set")
	}
	root, err := detectiveSharedSourceRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func buildDetectivePendingCommand(t *testing.T, ctx context.Context, root, directory string) string {
	t.Helper()
	binary := filepath.Join(directory, "detective")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./apps/detective/cmd/detective")
	build.Dir = root
	// As with the native command fixture, GOFLAGS=-race covers this real child.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build selected Detective CLI: %v\n%s", err, output)
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal("cannot protect compiled Detective binary")
	}
	return binary
}

func detectivePendingShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type detectivePendingReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Handoff       struct {
		SchemaVersion string `json:"schema_version"`
		evidenceingestionmcp.SubmitExtractorOutputResponse
	} `json:"handoff"`
	Batch struct {
		Source struct {
			SHA256 string `json:"sha256"`
		} `json:"source"`
		Rows []struct {
			Status string `json:"status"`
			Result struct {
				Outcome string `json:"outcome"`
				Records []struct {
					Statement string `json:"statement"`
					Citation  struct {
						ExactQuote string `json:"exact_quote"`
					} `json:"citation"`
				} `json:"records"`
			} `json:"result"`
		} `json:"rows"`
	} `json:"batch"`
}

func runDetectivePendingCommand(t *testing.T, ctx context.Context, binary, wrapper, input, modelURL, sourceID string) detectivePendingReceipt {
	t.Helper()
	childCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, binary, "-input", input, "-base-url", modelURL+"/v1", "-model", "mock-detective-model", "-row-chunks", "-row-line", "7", "-ahe-submit-pending", "-ahe-ingest-command", wrapper, "-ahe-source-id", sourceID, "-timeout", "30s")
	// No real model credential, proxy, or ambient database credential reaches
	// Detective. The protected launcher must also reject these hostile controls.
	cmd.Env = provisioningPollutedEnvironment()
	var stdout bytes.Buffer
	raceOutput := &authorityRaceOutput{}
	cmd.Stdout, cmd.Stderr = &stdout, raceOutput
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	var result detectivePendingReceipt
	if childCtx.Err() != nil || err != nil || raceOutput.detected || json.Unmarshal(stdout.Bytes(), &result) != nil || result.SchemaVersion != "lab-status-ahe-pending-handoff/v0" {
		t.Fatal("compiled Detective did not complete its pending-only launcher handoff normally (child output suppressed)")
	}
	return result
}

func newDetectivePendingModel(t *testing.T, row, statement string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	candidates := map[string]any{
		"outcome": "extracted", "abstentions": []any{}, "limitations": []any{}, "abstention_reason": "",
		"records": []any{map[string]any{
			"record_type": "capability_state", "subject": "pending_intake", "statement": statement,
			"epistemic_class": "claim", "status": "lab_proven", "scope": "lab_contract", "selection_state": "unspecified",
			"citation": map[string]int{"start_line": 7, "end_line": 7}, "blocked_by": []any{}, "does_not_establish": []any{}, "qualifiers": []any{},
		}},
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal("cannot encode synthetic Detective candidate fixture")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"data":[{"id":"mock-detective-model"}]}`); err != nil {
			t.Error("cannot return synthetic model inventory")
		}
	})
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var request struct {
			Model string `json:"model"`
			Think *bool  `json:"think"`
		}
		if err != nil || json.Unmarshal(body, &request) != nil || request.Model != "mock-detective-model" || request.Think == nil || *request.Think || !bytes.Contains(body, []byte(row)) {
			t.Error("Detective did not send its selected synthetic row with thinking disabled")
			http.Error(w, "invalid synthetic model request", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{"id": "mock-detective-response", "model": "mock-detective-model", "output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": string(encoded)}}}}}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error("cannot return synthetic Detective candidate response")
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &requests
}
