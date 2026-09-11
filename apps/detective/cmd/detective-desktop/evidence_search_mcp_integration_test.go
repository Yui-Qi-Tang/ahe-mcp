package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
)

// TestNativeBridgeEvidenceSearchQueryMCPIntegration is an opt-in native bridge
// test, not a Wails UI-click test. The operator supplies a credential-free Query
// launcher for an already prepared disposable DB; this test never prepares or
// mutates that DB. No model, intake, review, or source-server settings are read.
//
// DETECTIVE_DESKTOP_QUERY_LAB must name a private, canonical absolute JSON file:
//
//	{"version":"detective-desktop-query-lab/v1","query_launcher":"/absolute/launcher",
//	 "workspace_root":"/absolute/new-workspace","output_directory":"/absolute/new-output",
//	 "cases":[{"name":"case-name","request":{"question":"operator question",
//	 "sourceID":"","admissionOutcome":"","lifecycleScope":"active","limit":20},
//	 "expected_response_file":"/absolute/private-native-response.json"}, ...]}
//
// Supply 2–12 cases and native CLI responses with expected matches/source_scopes.
// Input JSON files require 0600, fresh empty workspace/output directories 0700,
// and all paths must be free of symlinks. Responses stay in the private output
// directory, never in repository fixtures or test diagnostics.
func TestNativeBridgeEvidenceSearchQueryMCPIntegration(t *testing.T) {
	manifestPath := os.Getenv("DETECTIVE_DESKTOP_QUERY_LAB")
	if manifestPath == "" {
		t.Skip("set DETECTIVE_DESKTOP_QUERY_LAB to an explicit private disposable-DB manifest")
	}
	var manifest struct {
		Version         string `json:"version"`
		QueryLauncher   string `json:"query_launcher"`
		WorkspaceRoot   string `json:"workspace_root"`
		OutputDirectory string `json:"output_directory"`
		Cases           []struct {
			Name                 string                        `json:"name"`
			Request              desktop.EvidenceSearchRequest `json:"request"`
			ExpectedResponseFile string                        `json:"expected_response_file"`
		} `json:"cases"`
	}
	body := queryLabReadPrivate(t, manifestPath, 64<<10)
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("lab manifest: invalid or unsupported configuration")
	}
	if manifest.Version != "detective-desktop-query-lab/v1" || len(manifest.Cases) < 2 || len(manifest.Cases) > 12 {
		t.Fatal("lab manifest: missing opt-in marker or bounded case list")
	}
	launcher := queryLabPath(t, manifest.QueryLauncher)
	if !launcher.Mode().IsRegular() || launcher.Mode().Perm()&0o111 == 0 {
		t.Fatal("lab manifest: query launcher is not a regular executable")
	}
	queryLabEmptyDirectory(t, manifest.WorkspaceRoot)
	queryLabEmptyDirectory(t, manifest.OutputDirectory)
	if manifest.WorkspaceRoot == manifest.OutputDirectory {
		t.Fatal("lab manifest: workspace and output must be separate")
	}
	names := map[string]bool{}
	validName := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	expected := make([]map[string]any, len(manifest.Cases))
	for i, test := range manifest.Cases {
		if !validName.MatchString(test.Name) || names[test.Name] {
			t.Fatal("lab manifest: invalid or duplicate case name")
		}
		names[test.Name] = true
		expected[i] = queryLabResponse(t, queryLabReadPrivate(t, test.ExpectedResponseFile, 16<<20))
	}

	service, err := desktop.New(manifest.WorkspaceRoot)
	if err != nil {
		t.Fatal("lab setup: cannot open fresh desktop workspace")
	}
	t.Cleanup(service.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	app := &App{ctx: ctx, service: service}
	settings := app.Snapshot().Settings
	settings.Mode = "local"
	settings.QueryLauncher = manifest.QueryLauncher
	if _, err := app.SaveSettings(settings); err != nil {
		t.Fatal("lab setup: cannot save query-only settings")
	}
	before := app.Snapshot()
	settingsPath := filepath.Join(manifest.WorkspaceRoot, "settings.json")
	savedSettings := queryLabReadPrivate(t, settingsPath, 64<<10)
	for i, test := range manifest.Cases {
		if !t.Run(test.Name, func(t *testing.T) {
			// This calls App -> desktop.Service -> SearchPractical -> real MCP
			// process. SearchPractical validates mode, schema and request echoes.
			state, err := app.SearchEvidence(test.Request)
			if err != nil {
				t.Fatal("native query: failed; upstream details withheld")
			}
			queryLabCheckCompleted(t, state)
			if state.Search == nil || state.Search.Demo || state.Search.Request != test.Request {
				t.Fatal("native query: invalid completed search state")
			}
			if !reflect.DeepEqual(state.Search, app.Snapshot().Search) {
				t.Fatal("native query: result was not retained in the current session")
			}
			raw := []byte(state.Search.RawJSON)
			output := filepath.Join(manifest.OutputDirectory, test.Name+".json")
			file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal("native query: cannot create private raw-response artifact")
			}
			_, writeErr := file.Write(raw)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatal("native query: cannot finish private raw-response artifact")
			}
			if !bytes.Equal(raw, queryLabReadPrivate(t, output, 16<<20)) {
				t.Fatal("native query: raw-response artifact changed")
			}
			actual := queryLabResponse(t, raw)
			queryLabCheckEcho(t, actual, test.Request)
			for _, field := range []string{"matches", "source_scopes"} {
				if !reflect.DeepEqual(actual[field], expected[i][field]) {
					t.Fatalf("native query: %s differs from expected response", field)
				}
			}
			if state.Search.ReturnedMatches != len(actual["matches"].([]any)) {
				t.Fatal("native query: displayed match count differs from native response")
			}
			// Only search results and their operational events may change.
			state.Search = before.Search
			state.Events = before.Events
			state.Operation = before.Operation
			if !reflect.DeepEqual(state, before) {
				t.Fatal("native query: changed unrelated workspace, Brief, batch or conversation state")
			}
			queryLabCheckWorkspace(t, manifest.WorkspaceRoot, savedSettings)
			t.Logf("native bridge query passed; private raw response: %s", output)
		}) {
			return
		}
	}
}

func TestNativeBridgeEvidenceSearchQueryLabCompletionContract(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil || os.Chmod(directory, 0o700) != nil {
		t.Fatal("lab setup: cannot prepare private test directory")
	}
	service, err := desktop.New(directory)
	if err != nil {
		t.Fatal("lab setup: cannot open fresh desktop workspace")
	}
	t.Cleanup(service.Close)
	app := &App{ctx: t.Context(), service: service}
	queryLabEmptyDirectory(t, directory)
	settings := app.Snapshot().Settings
	settings.Mode = "local"
	settings.QueryLauncher = filepath.Join(directory, "unused-query-launcher")
	state, err := app.SaveSettings(settings)
	if err != nil {
		t.Fatal("lab setup: cannot save query-only settings")
	}
	queryLabCheckCompleted(t, state)
	savedSettings := queryLabReadPrivate(t, filepath.Join(directory, "settings.json"), 64<<10)
	queryLabCheckWorkspace(t, directory, savedSettings)
	// The fixed demo shares finishEvidenceSearch with real search, exercising
	// the completion contract without starting a process or connecting to a DB.
	state, err = app.LoadEvidenceSearchDemo("mixed")
	if err != nil || state.Search == nil || !state.Search.Demo {
		t.Fatal("lab contract: fixed offline search failed")
	}
	queryLabCheckCompleted(t, state)
	queryLabCheckWorkspace(t, directory, savedSettings)
}

func queryLabCheckCompleted(t *testing.T, state desktop.State) {
	t.Helper()
	if state.Busy || state.Error != "" || state.Operation != "" {
		t.Fatal("native query: invalid completed operation state")
	}
}

func queryLabCheckWorkspace(t *testing.T, directory string, savedSettings []byte) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" || !bytes.Equal(savedSettings, queryLabReadPrivate(t, filepath.Join(directory, "settings.json"), 64<<10)) {
		t.Fatal("native query: added or changed a workspace file")
	}
}

func queryLabPath(t *testing.T, path string) os.FileInfo {
	t.Helper()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		t.Fatal("lab path: requires a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		t.Fatal("lab path: missing path or symlink")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal("lab path: cannot inspect path")
	}
	return info
}

func queryLabEmptyDirectory(t *testing.T, path string) {
	t.Helper()
	info := queryLabPath(t, path)
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("lab directory: requires a private 0700 directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatal("lab directory: requires a fresh empty directory")
	}
}

func queryLabReadPrivate(t *testing.T, path string, limit int64) []byte {
	t.Helper()
	info := queryLabPath(t, path)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > limit {
		t.Fatal("lab input: requires a bounded private 0600 regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil || int64(len(body)) > limit {
		t.Fatal("lab input: cannot read bounded private file")
	}
	return body
}

func queryLabResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var response map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&response) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("native response: invalid JSON object")
	}
	for _, field := range []string{"matches", "source_scopes"} {
		if _, ok := response[field].([]any); !ok {
			t.Fatalf("native response: missing %s array", field)
		}
	}
	return response
}

func queryLabCheckEcho(t *testing.T, response map[string]any, request desktop.EvidenceSearchRequest) {
	t.Helper()
	execution, ok := response["query_execution"].(map[string]any)
	if !ok || response["schema_version"] != ahemcp.PracticalQuerySchema || response["query"] != request.Question || execution["original_query"] != request.Question || execution["query_mode"] != ahemcp.PracticalQueryMode {
		t.Fatal("native response: schema, mode or query mismatch")
	}
	filters, ok := execution["filters"].(map[string]any)
	if !ok || filters["lifecycle_scope"] != request.LifecycleScope {
		t.Fatal("native response: lifecycle filter mismatch")
	}
	for field, want := range map[string]string{"source_id": request.SourceID, "admission_outcome": request.AdmissionOutcome} {
		got, _ := filters[field].(string)
		if got != want {
			t.Fatal("native response: source or admission filter mismatch")
		}
	}
	limit, ok := execution["limit"].(json.Number)
	n, err := limit.Int64()
	if !ok || err != nil || n != int64(request.Limit) {
		t.Fatal("native response: limit mismatch")
	}
}
