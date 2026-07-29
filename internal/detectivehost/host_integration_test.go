//go:build integration

package detectivehost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/detective"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationHostRejectsUnmigratedSchema(t *testing.T) {
	databaseURL, _ := hostIntegrationDatabase(t)
	config := Config{
		SchemaVersion:         ConfigSchemaVersion,
		HostID:                "schema-gate",
		RegistrationRequestID: "register-schema-gate",
		WorkspaceID:           "workspace:schema-gate",
		WorkspaceRoot:         t.TempDir(),
		Sources: []detective.WorkspaceSourceRegistrationInput{{
			CapabilityName:    detective.SourceCapabilityLocalPRDText,
			CapabilityVersion: detective.SourceCapabilityLocalPRDTextVersion,
			SourceID:          "requirements",
			RelativePath:      "requirements.md",
		}},
		SourceInterval: time.Second,
		MaxSteps:       1,
	}

	err := Run(t.Context(), databaseURL, config, Options{})
	if !errors.Is(err, migrations.ErrSchemaNotCurrent) {
		t.Fatalf("Run() error = %v, want ErrSchemaNotCurrent", err)
	}
}

func TestIntegrationHostPollsExactMCPReadSourceWithoutCreatingProposals(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	databaseURL, pool := hostIntegrationDatabase(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	command, err := mcpstdio.MacOSLoopbackOnlyCommand(mcpstdio.CommandConfig{
		Path:        executable,
		Args:        []string{"-test.run=TestHostMCPReadHelperProcess"},
		Directory:   t.TempDir(),
		Environment: []string{"AHE_HOST_MCP_READ_HELPER=1"},
	})
	if err != nil {
		t.Fatalf("MacOSLoopbackOnlyCommand() error = %v", err)
	}
	schemaHash, err := mcpstdio.ToolInputSchemaHash(hostMCPReadInputSchema())
	if err != nil {
		t.Fatalf("ToolInputSchemaHash() error = %v", err)
	}
	config := Config{
		SchemaVersion:         ConfigSchemaVersionV3,
		HostID:                "mcp-read-integration",
		RegistrationRequestID: "register-mcp-read-integration",
		WorkspaceID:           "workspace:mcp-read-integration",
		WorkspaceRoot:         t.TempDir(),
		SourceInterval:        time.Second,
		MaxSteps:              1,
		MCPReadSources: []MCPReadSourceConfig{{
			SourceID:                    "fixture:AHE-50",
			ConnectorID:                 "host-loopback-read",
			Provider:                    "fixture",
			LogicalCapability:           "read_document",
			AdapterName:                 "host-loopback-fixture",
			AdapterVersion:              "v1",
			ProviderToolName:            "read_document",
			ProviderToolInputSchemaHash: schemaHash,
			Arguments:                   json.RawMessage(`{"object_id":"AHE-50"}`),
			Command:                     command,
		}},
	}

	runHostUntilMCPReadCycleCount(t, databaseURL, pool, config, true, 1)
	assertHostTableCount(t, pool, "detective_mcp_read_source_bindings", 1)
	assertHostTableCount(t, pool, "detective_mcp_read_collection_cycles", 1)
	assertHostTableCount(t, pool, "detective_periodic_source_cycles", 0)
	assertHostTableCount(t, pool, "detective_connector_inbox_deliveries", 1)
	assertHostTableCount(t, pool, "detective_connector_inbox_processing_work", 0)
	assertHostTableCount(t, pool, "source_snapshots", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 0)
	assertHostTableCount(t, pool, "canonical_graph_nodes", 0)

	runHostUntilMCPReadCycleCount(t, databaseURL, pool, config, false, 2)
	assertHostTableCount(t, pool, "detective_mcp_read_collection_cycles", 2)
	assertHostTableCount(t, pool, "detective_connector_inbox_deliveries", 1)
	assertHostTableCount(t, pool, "source_snapshots", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 0)
}

func TestIntegrationHostConvertsMCPReadSourceOnlyWithV4OptIn(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	databaseURL, pool := hostIntegrationDatabase(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	command, err := mcpstdio.MacOSLoopbackOnlyCommand(mcpstdio.CommandConfig{
		Path:        executable,
		Args:        []string{"-test.run=TestHostMCPReadHelperProcess"},
		Directory:   t.TempDir(),
		Environment: []string{"AHE_HOST_MCP_READ_HELPER=1"},
	})
	if err != nil {
		t.Fatalf("MacOSLoopbackOnlyCommand() error = %v", err)
	}
	schemaHash, err := mcpstdio.ToolInputSchemaHash(hostMCPReadInputSchema())
	if err != nil {
		t.Fatalf("ToolInputSchemaHash() error = %v", err)
	}
	config := Config{
		SchemaVersion:         ConfigSchemaVersionV4,
		HostID:                "mcp-conversion-integration",
		RegistrationRequestID: "register-mcp-conversion-integration",
		WorkspaceID:           "workspace:mcp-conversion-integration",
		WorkspaceRoot:         t.TempDir(),
		SourceInterval:        time.Second,
		MaxSteps:              1,
		MCPReadSources: []MCPReadSourceConfig{{
			SourceID:                    "fixture:AHE-51",
			ConnectorID:                 "host-loopback-conversion",
			Provider:                    "fixture",
			LogicalCapability:           "read_document",
			AdapterName:                 "host-loopback-fixture",
			AdapterVersion:              "v1",
			ProviderToolName:            "read_document",
			ProviderToolInputSchemaHash: schemaHash,
			Arguments:                   json.RawMessage(`{"object_id":"AHE-51"}`),
			Command:                     command,
			ProposalConversion:          true,
		}},
	}

	runHostUntilProposalCount(t, databaseURL, pool, config, true, 1)
	assertHostTableCount(t, pool, "detective_mcp_read_collection_cycles", 1)
	assertHostTableCount(t, pool, "source_snapshots", 1)
	assertHostTableCount(t, pool, "extraction_runs", 1)
	assertHostTableCount(t, pool, "extraction_attempts", 1)
	assertHostTableCount(t, pool, "proposal_batches", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 1)
	assertHostTableCount(t, pool, "canonical_graph_nodes", 0)
	assertHostTableCount(t, pool, "canonical_graph_edges", 0)
	var statement, outcome string
	if err := pool.QueryRow(context.Background(), `
		SELECT statement_text, admission_outcome
		FROM proposal_occurrences
	`).Scan(&statement, &outcome); err != nil {
		t.Fatalf("reading converted MCP proposal: %v", err)
	}
	if statement != "Refunds must be completed within 7 days." || outcome != "pending" {
		t.Fatalf("converted MCP proposal = %q/%q", statement, outcome)
	}

	runHostUntilMCPReadCycleCount(t, databaseURL, pool, config, false, 2)
	assertHostTableCount(t, pool, "detective_mcp_read_collection_cycles", 2)
	assertHostTableCount(t, pool, "source_snapshots", 1)
	assertHostTableCount(t, pool, "extraction_runs", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 1)
}

func TestIntegrationHostExtractsMCPReadSourceWithBoundedModelAndReplays(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	databaseURL, pool := hostIntegrationDatabase(t)
	var modelCalls atomic.Int64
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var request struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decoding model request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(request.Prompt, "Refunds must be completed within 7 days.") ||
			!strings.Contains(request.Prompt, "Do not answer questions") {
			t.Errorf("model prompt is not bounded: %s", request.Prompt)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": `{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"]}]}`,
		})
	}))
	defer modelServer.Close()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	command, err := mcpstdio.MacOSLoopbackOnlyCommand(mcpstdio.CommandConfig{
		Path:        executable,
		Args:        []string{"-test.run=TestHostMCPReadHelperProcess"},
		Directory:   t.TempDir(),
		Environment: []string{"AHE_HOST_MCP_READ_HELPER=1"},
	})
	if err != nil {
		t.Fatalf("MacOSLoopbackOnlyCommand() error = %v", err)
	}
	schemaHash, err := mcpstdio.ToolInputSchemaHash(hostMCPReadInputSchema())
	if err != nil {
		t.Fatalf("ToolInputSchemaHash() error = %v", err)
	}
	config := Config{
		SchemaVersion:         ConfigSchemaVersionV4,
		HostID:                "mcp-extraction-integration",
		RegistrationRequestID: "register-mcp-extraction-integration",
		WorkspaceID:           "workspace:mcp-extraction-integration",
		WorkspaceRoot:         t.TempDir(),
		SourceInterval:        time.Second,
		MaxSteps:              1,
		MCPReadSources: []MCPReadSourceConfig{{
			SourceID:                    "fixture:AHE-59",
			ConnectorID:                 "host-loopback-extraction",
			Provider:                    "fixture",
			LogicalCapability:           "read_document",
			AdapterName:                 "host-loopback-fixture",
			AdapterVersion:              "v1",
			ProviderToolName:            "read_document",
			ProviderToolInputSchemaHash: schemaHash,
			Arguments:                   json.RawMessage(`{"object_id":"AHE-59"}`),
			Command:                     command,
			ProposalExtraction: &MCPProposalExtractionConfig{
				Enabled:      true,
				BaseURL:      modelServer.URL,
				Model:        "fixture-model",
				Timeout:      5 * time.Second,
				NumPredict:   256,
				MaxProposals: 4,
			},
		}},
	}

	runHostUntilProposalCount(t, databaseURL, pool, config, true, 1)
	if got := modelCalls.Load(); got != 1 {
		t.Fatalf("model calls after first host = %d, want 1", got)
	}
	assertHostTableCount(t, pool, "extraction_attempts", 1)
	assertHostTableCount(t, pool, "proposal_batches", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 1)
	assertHostTableCount(t, pool, "canonical_graph_nodes", 0)

	runHostUntilMCPReadCycleCount(t, databaseURL, pool, config, false, 2)
	if got := modelCalls.Load(); got != 1 {
		t.Fatalf("model calls after restart replay = %d, want 1", got)
	}
	assertHostTableCount(t, pool, "detective_mcp_read_collection_cycles", 2)
	assertHostTableCount(t, pool, "source_snapshots", 1)
	assertHostTableCount(t, pool, "extraction_attempts", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 1)
}

func TestHostMCPReadHelperProcess(t *testing.T) {
	if os.Getenv("AHE_HOST_MCP_READ_HELPER") == "" {
		return
	}
	server, err := mcpstdio.NewServer(
		"host-loopback-read-adapter",
		"v1",
		hostMCPReadBackend{},
	)
	if err != nil {
		t.Fatalf("mcpstdio.NewServer() error = %v", err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		t.Fatalf("host MCP helper Serve() error = %v", err)
	}
}

type hostMCPReadBackend struct{}

func (hostMCPReadBackend) Tools() []mcpstdio.Tool {
	readOnly := true
	destructive := false
	return []mcpstdio.Tool{{
		Name:        "read_document",
		Description: "Returns one exact host integration document.",
		InputSchema: hostMCPReadInputSchema(),
		Annotations: mcpstdio.Annotations{
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
		},
	}}
}

func (hostMCPReadBackend) CallTool(
	_ context.Context,
	_ string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	var input struct {
		ObjectID string `json:"object_id"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, err
	}
	text := "Refunds must be completed within 7 days.\n"
	rawProvider := []byte(`{"key":"` + input.ObjectID + `","revision":"revision-50"}`)
	return json.Marshal(detective.MCPReadDocumentResult{
		Contract:       detective.MCPReadDocumentResultContract,
		ObjectID:       input.ObjectID,
		Revision:       "revision-50",
		SourceLocation: "https://fixture.invalid/browse/" + input.ObjectID,
		Coverage: detective.MCPReadCoverage{
			Complete:         true,
			CompletionReason: detective.MCPReadCompletionComplete,
		},
		Document: detective.MCPReadDocument{
			ID:          input.ObjectID + "-description",
			Title:       input.ObjectID,
			Text:        text,
			ContentHash: hostContentHash([]byte(text)),
		},
		ProposalCandidates: []detective.MCPReadProposalCandidate{{
			LocalID:      "document-line-1",
			SelectorKind: detective.MCPReadProposalSelectorLine,
			Selector:     "1",
		}},
		RawProviderResponseBase64: base64.StdEncoding.EncodeToString(rawProvider),
		RawProviderResponseHash:   hostContentHash(rawProvider),
		Limitations:               []string{},
	})
}

func hostMCPReadInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"object_id"},
		"properties": map[string]any{
			"object_id": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 500,
			},
		},
	}
}

func hostContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestIntegrationHostCollectsLocalTextAndRestartsWithoutDuplicateEvidence(t *testing.T) {
	databaseURL, pool := hostIntegrationDatabase(t)
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(workspaceRoot, "requirements.md"),
		[]byte("Refunds complete within seven days.\nOverseas orders require review.\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	config := Config{
		SchemaVersion:         ConfigSchemaVersion,
		HostID:                "local-integration",
		RegistrationRequestID: "register-local-integration",
		WorkspaceID:           "workspace:local-integration",
		WorkspaceRoot:         workspaceRoot,
		Sources: []detective.WorkspaceSourceRegistrationInput{{
			CapabilityName: detective.SourceCapabilityLocalPRDText, CapabilityVersion: detective.SourceCapabilityLocalPRDTextVersion,
			SourceID: "requirements", RelativePath: "requirements.md",
		}},
		SourceInterval: 20 * time.Millisecond,
		MaxSteps:       1,
	}

	runHostUntilProposalCount(t, databaseURL, pool, config, true, 2)
	assertHostTableCount(t, pool, "proposal_occurrences", 2)
	assertHostTableCount(t, pool, "canonical_graph_nodes", 0)
	assertHostTableCount(t, pool, "detective_periodic_source_cycles", 1)

	runHostUntilProposalCount(t, databaseURL, pool, config, false, 2)
	assertHostTableCount(t, pool, "proposal_occurrences", 2)
	assertHostTableCount(t, pool, "detective_periodic_source_cycles", 1)
}

func TestIntegrationPlannerHostConsumesOpaqueChoiceAndRestartsWithoutModel(t *testing.T) {
	databaseURL, pool := hostIntegrationDatabase(t)
	workspaceRoot := hostPlannerGitWorkspace(t)
	config := Config{
		SchemaVersion:         ConfigSchemaVersionV2,
		HostID:                "planner-integration",
		RegistrationRequestID: "register-planner-integration",
		WorkspaceID:           "workspace:planner-integration",
		WorkspaceRoot:         workspaceRoot,
		Sources: []detective.WorkspaceSourceRegistrationInput{
			{
				CapabilityName: detective.SourceCapabilityGitGoRepository, CapabilityVersion: detective.SourceCapabilityGitGoRepositoryVersion,
				SourceID: "repository", RelativePath: ".",
			},
			{
				CapabilityName: detective.SourceCapabilityLocalPRDText, CapabilityVersion: detective.SourceCapabilityLocalPRDTextVersion,
				SourceID: "requirements", RelativePath: "requirements.md",
			},
		},
		SourceInterval: time.Hour,
		MaxSteps:       3,
		RepositoryMaintenance: RepositoryMaintenanceConfig{
			Enabled: true, Interval: time.Hour, BatchLimit: 10,
		},
		Planner: PlannerConfig{Enabled: true, Model: "test-planner"},
	}
	var plannerInvocations atomic.Int32
	runner := func(_ context.Context, input detective.PlannerInput) ([]byte, error) {
		plannerInvocations.Add(1)
		for _, source := range input.Sources {
			if source.CapabilityName == detective.SourceCapabilityLocalPRDText {
				return []byte(`{"schema_version":"detective-planner-decision-v1","classification":"query_source","source_choice":"` + source.ChoiceKey + `"}`), nil
			}
		}
		return nil, errors.New("planner input has no local-text source")
	}

	runPlannerHostUntilConsumption(t, databaseURL, pool, config, true, runner, true)
	if plannerInvocations.Load() != 1 {
		t.Fatalf("first host planner invocations = %d, want 1", plannerInvocations.Load())
	}
	assertHostTableCount(t, pool, "detective_planner_recommendation_consumptions", 1)
	assertHostTableCount(t, pool, "detective_periodic_source_cycles", 2)
	assertHostTableCount(t, pool, "detective_periodic_source_cursors", 2)
	assertHostTableCount(t, pool, "source_snapshots", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 1)

	runPlannerHostUntilConsumption(t, databaseURL, pool, config, false, runner, false)
	if plannerInvocations.Load() != 1 {
		t.Fatalf("restarted host planner invocations = %d, want 1", plannerInvocations.Load())
	}
	assertHostTableCount(t, pool, "detective_planner_recommendation_consumptions", 1)
	assertHostTableCount(t, pool, "detective_periodic_source_cycles", 2)
	assertHostTableCount(t, pool, "source_snapshots", 1)
	assertHostTableCount(t, pool, "proposal_occurrences", 1)
}

func runHostUntilProposalCount(
	t *testing.T,
	databaseURL string,
	pool *pgxpool.Pool,
	config Config,
	applyMigrations bool,
	want int,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, databaseURL, config, Options{ApplyMigrations: applyMigrations})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var count int
		err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM proposal_occurrences`).Scan(&count)
		if err == nil && count == want {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run() returned before proposals were ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("proposal count did not reach %d", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Run() returned before cancellation: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func runHostUntilMCPReadCycleCount(
	t *testing.T,
	databaseURL string,
	pool *pgxpool.Pool,
	config Config,
	applyMigrations bool,
	want int,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, databaseURL, config, Options{ApplyMigrations: applyMigrations})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var count int
		err := pool.QueryRow(context.Background(), `
			SELECT COUNT(*)
			FROM detective_mcp_read_collection_cycles
			WHERE status = 'completed'
		`).Scan(&count)
		if err == nil && count == want {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("MCP host Run() returned before cycle completion: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("completed MCP read cycle count did not reach %d", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MCP host Run() after cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP host Run() did not stop after cancellation")
	}
}

func runPlannerHostUntilConsumption(
	t *testing.T,
	databaseURL string,
	pool *pgxpool.Pool,
	config Config,
	applyMigrations bool,
	runner detective.PlannerRunner,
	waitForConsumption bool,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, databaseURL, config, Options{
			ApplyMigrations: applyMigrations,
			PlannerRunner:   runner,
		})
	}()
	if waitForConsumption {
		deadline := time.Now().Add(10 * time.Second)
		for {
			var consumptions, completedCycles int
			err := pool.QueryRow(context.Background(), `
				SELECT
				  (SELECT COUNT(*) FROM detective_planner_recommendation_consumptions),
				  (SELECT COUNT(*) FROM detective_periodic_source_cycles WHERE status = 'completed')
			`).Scan(&consumptions, &completedCycles)
			if err == nil && consumptions == 1 && completedCycles == 2 {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("planner Run() returned before completion: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("planner host did not complete its initial source cycles")
			}
			time.Sleep(20 * time.Millisecond)
		}
	} else {
		time.Sleep(250 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("planner Run() returned before cancellation: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("planner Run() after cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("planner Run() did not stop after cancellation")
	}
}

func hostPlannerGitWorkspace(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", root},
		{"-C", root, "config", "user.name", "Detective Host Test"},
		{"-C", root, "config", "user.email", "detective@example.invalid"},
	} {
		if output, commandErr := exec.Command(gitPath, args...).CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}
	files := map[string]string{
		"go.mod":          "module example.com/detective-host\n\ngo 1.24\n",
		"main.go":         "package host\n\nfunc Evidence() string { return \"git\" }\n",
		"requirements.md": "Planner host requirement remains source backed.\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatalf("write planner host %s: %v", path, err)
		}
	}
	for _, args := range [][]string{
		{"-C", root, "add", "."},
		{"-C", root, "commit", "-q", "-m", "fixture"},
	} {
		if output, commandErr := exec.Command(gitPath, args...).CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}
	return root
}

func hostIntegrationDatabase(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	schema := "ahe_detective_host_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	schemaURL, err := hostSchemaDatabaseURL(databaseURL, schema)
	if err != nil {
		t.Fatalf("schema database URL: %v", err)
	}
	config, err := pgxpool.ParseConfig(schemaURL)
	if err != nil {
		t.Fatalf("parse schema database URL: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("new schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return schemaURL, pool
}

func hostSchemaDatabaseURL(databaseURL, schema string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", errUnsupportedDatabaseURL
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

var errUnsupportedDatabaseURL = errors.New("DATABASE_DNS must be a PostgreSQL URL for host integration tests")

func assertHostTableCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}
