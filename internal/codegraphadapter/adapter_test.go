package codegraphadapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

const testCommitSHA = "0123456789abcdef0123456789abcdef01234567"

func TestBackendReturnsRevisionAnchoredStableCandidates(t *testing.T) {
	root := writeAdapterFixture(t)
	fake := &fakeCommandRunner{
		root: root,
		query: `[
			{
				"node": {
					"id": "function:one",
					"kind": "function",
					"name": "CompleteRefund",
					"qualifiedName": "CompleteRefund",
					"filePath": "service/refund.go",
					"language": "go",
					"startLine": 3,
					"endLine": 5,
					"startColumn": 0,
					"endColumn": 1,
					"signature": "(orderID string) string",
					"visibility": null,
					"isExported": true,
					"isAsync": false,
					"isStatic": false,
					"isAbstract": false,
					"returnType": "string",
					"updatedAt": 100
				},
				"score": 42.5
			}
		]`,
		callers: `{
			"symbol": "CompleteRefund",
			"callers": [
				{
					"name": "HandleRefund",
					"kind": "function",
					"filePath": "api/handler.go",
					"startLine": 5
				}
			]
		}`,
	}
	backend, err := newBackend(testConfig(root), fake.run)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := backend.CallTool(
		context.Background(),
		ToolQueryFunction,
		json.RawMessage(`{"symbol":"CompleteRefund","limit":10,"include_callers":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Contract != ResultContract ||
		result.Repository.CommitSHA != testCommitSHA ||
		result.StableCandidateHash == "" {
		t.Fatalf("unexpected result identity: %#v", result)
	}
	if len(result.Definitions) != 1 ||
		result.Definitions[0].Source.FilePath != "service/refund.go" ||
		!strings.Contains(result.Definitions[0].Source.Content, "func CompleteRefund") {
		t.Fatalf("definitions = %#v", result.Definitions)
	}
	if len(result.CallerCandidates) != 1 ||
		result.CallerCandidates[0].Name != "HandleRefund" ||
		result.CallerCandidates[0].Source.StartLine != 5 {
		t.Fatalf("callers = %#v", result.CallerCandidates)
	}
	if result.Coverage.SearchCompleteWithinSurface ||
		result.Coverage.GlobalAbsenceInferenceAllowed ||
		result.Coverage.CompletionReason != "provider_total_count_unavailable" {
		t.Fatalf("coverage = %#v", result.Coverage)
	}
	if result.RawProvider.QueryHash == "" || result.RawProvider.CallersHash == "" {
		t.Fatalf("raw provider audit = %#v", result.RawProvider)
	}
	if fake.providerCalls != 4 {
		t.Fatalf("provider calls = %d, want version + index + query + callers", fake.providerCalls)
	}
	for _, spec := range fake.specs {
		if spec.path == "/opt/codegraph" && !spec.sandboxed {
			t.Fatalf("provider command was not sandboxed: %#v", spec)
		}
	}
}

func TestStableHashExcludesProviderUpdatedAtAndScore(t *testing.T) {
	root := writeAdapterFixture(t)
	config := testConfig(root)
	input := queryFunctionInput{Symbol: "CompleteRefund", Limit: 10}
	load := fixtureSourceLoader(root)
	first, err := normalizeResult(
		context.Background(),
		config,
		input,
		[]byte(searchFixture(100, 10.5)),
		nil,
		load,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeResult(
		context.Background(),
		config,
		input,
		[]byte(searchFixture(200, 99.5)),
		nil,
		load,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.StableCandidateHash != second.StableCandidateHash {
		t.Fatalf("stable hashes differ: %s != %s", first.StableCandidateHash, second.StableCandidateHash)
	}
	if first.RawProvider.QueryHash == second.RawProvider.QueryHash {
		t.Fatalf("raw hashes unexpectedly match: %s", first.RawProvider.QueryHash)
	}
	if !reflect.DeepEqual(first.Definitions, second.Definitions) {
		t.Fatalf("stable definitions differ: %#v != %#v", first.Definitions, second.Definitions)
	}
}

func TestBackendSupportsNestedAnalysisRoot(t *testing.T) {
	repositoryRoot := t.TempDir()
	analysisRoot := filepath.Join(repositoryRoot, "service-module")
	writeAdapterFixtureFiles(t, analysisRoot)
	if err := os.MkdirAll(filepath.Join(analysisRoot, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(analysisRoot, ".codegraph", "codegraph.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCommandRunner{
		root:  repositoryRoot,
		query: searchFixture(100, 10.5),
	}
	config := testConfig(repositoryRoot)
	config.AnalysisRoot = analysisRoot
	config.HomeDirectory = filepath.Join(repositoryRoot, "home")
	backend, err := newBackend(config, fake.run)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := backend.CallTool(
		context.Background(),
		ToolQueryFunction,
		json.RawMessage(`{"symbol":"CompleteRefund","limit":10,"include_callers":false}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Definitions) != 1 ||
		result.Definitions[0].Source.FilePath != "service-module/service/refund.go" {
		t.Fatalf("definitions = %#v", result.Definitions)
	}
	resolvedAnalysisRoot, err := filepath.EvalSymlinks(analysisRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range fake.specs {
		if spec.path == "/opt/codegraph" && spec.directory != resolvedAnalysisRoot {
			t.Fatalf("provider directory = %q, want %q", spec.directory, resolvedAnalysisRoot)
		}
	}
}

func TestBackendRejectsDirtyWorkspaceBeforeProvider(t *testing.T) {
	root := writeAdapterFixture(t)
	fake := &fakeCommandRunner{root: root, dirty: true}
	backend, err := newBackend(testConfig(root), fake.run)
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.CallTool(
		context.Background(),
		ToolQueryFunction,
		json.RawMessage(`{"symbol":"CompleteRefund","limit":10,"include_callers":false}`),
	)
	if err == nil || !strings.Contains(err.Error(), "tracked changes") {
		t.Fatalf("CallTool() error = %v", err)
	}
	if fake.providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", fake.providerCalls)
	}
}

func TestBackendRejectsTrailingAndUnknownInput(t *testing.T) {
	root := writeAdapterFixture(t)
	backend, err := newBackend(testConfig(root), (&fakeCommandRunner{root: root}).run)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"symbol":"CompleteRefund","limit":10,"include_callers":false,"extra":true}`,
		`{"symbol":"CompleteRefund","limit":10,"include_callers":false} {}`,
	} {
		if _, err := backend.CallTool(context.Background(), ToolQueryFunction, json.RawMessage(raw)); err == nil {
			t.Fatalf("CallTool(%s) succeeded", raw)
		}
	}
}

func TestNewBackendRejectsUnpinnedConfig(t *testing.T) {
	root := writeAdapterFixture(t)
	for name, mutate := range map[string]func(*Config){
		"release": func(config *Config) { config.ProviderRelease = "latest" },
		"commit":  func(config *Config) { config.CommitSHA = "HEAD" },
		"timeout": func(config *Config) { config.CommandTimeoutMS = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig(root)
			mutate(&config)
			if _, err := NewBackend(config); err == nil {
				t.Fatal("NewBackend() succeeded")
			}
		})
	}
}

func TestQueryFunctionSchemaHash(t *testing.T) {
	hash, err := mcpstdio.ToolInputSchemaHash(QueryFunctionInputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if hash != QueryFunctionSchemaHashV1 {
		t.Fatalf("schema hash = %s, want %s", hash, QueryFunctionSchemaHashV1)
	}
}

func writeAdapterFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeAdapterFixtureFiles(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codegraph", "codegraph.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeAdapterFixtureFiles(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"service/refund.go": `package service

func CompleteRefund(orderID string) string {
	return "completed:" + orderID
}
`,
		"api/handler.go": `package api

import "example.com/ahe/candidate/service"

func HandleRefund(orderID string) string {
	return service.CompleteRefund(orderID)
}
`,
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testConfig(root string) Config {
	return Config{
		ProviderExecutable: "/opt/codegraph",
		ProviderRelease:    ProviderRelease,
		GitExecutable:      "/usr/bin/git",
		RepositoryRoot:     root,
		AnalysisRoot:       root,
		HomeDirectory:      filepath.Join(root, "home"),
		RepositoryID:       "fixture",
		CommitSHA:          testCommitSHA,
		CommandTimeoutMS:   30_000,
	}
}

type fakeCommandRunner struct {
	root          string
	query         string
	callers       string
	dirty         bool
	providerCalls int
	specs         []commandSpec
}

func (f *fakeCommandRunner) run(_ context.Context, spec commandSpec) ([]byte, error) {
	f.specs = append(f.specs, spec)
	if spec.path == "/usr/bin/git" {
		return f.runGit(spec.args)
	}
	f.providerCalls++
	switch {
	case reflect.DeepEqual(spec.args, []string{"--version"}):
		return []byte("1.5.0\n"), nil
	case len(spec.args) == 2 && spec.args[0] == "index":
		return []byte("indexed\n"), nil
	case len(spec.args) > 0 && spec.args[0] == "query":
		if f.query == "" {
			f.query = "[]"
		}
		return []byte(f.query), nil
	case len(spec.args) > 0 && spec.args[0] == "callers":
		if f.callers == "" {
			f.callers = `{"symbol":"CompleteRefund","callers":[]}`
		}
		return []byte(f.callers), nil
	default:
		return nil, errors.New("unexpected provider command")
	}
}

func (f *fakeCommandRunner) runGit(args []string) ([]byte, error) {
	switch {
	case reflect.DeepEqual(args, []string{"rev-parse", "--show-toplevel"}):
		return []byte(f.root + "\n"), nil
	case reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}):
		return []byte(testCommitSHA + "\n"), nil
	case reflect.DeepEqual(args, []string{"status", "--porcelain=v1", "--untracked-files=no"}):
		if f.dirty {
			return []byte(" M service/refund.go\n"), nil
		}
		return nil, nil
	case len(args) == 2 && args[0] == "show":
		path := strings.TrimPrefix(args[1], testCommitSHA+":")
		return os.ReadFile(filepath.Join(f.root, filepath.FromSlash(path)))
	default:
		return nil, errors.New("unexpected git command")
	}
}

func fixtureSourceLoader(root string) sourceLoader {
	return func(_ context.Context, path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	}
}

func searchFixture(updatedAt int64, score float64) string {
	return `[
		{
			"node": {
				"id": "function:one",
				"kind": "function",
				"name": "CompleteRefund",
				"qualifiedName": "CompleteRefund",
				"filePath": "service/refund.go",
				"language": "go",
				"startLine": 3,
				"endLine": 5,
				"startColumn": 0,
				"endColumn": 1,
				"signature": "(orderID string) string",
				"visibility": null,
				"isExported": true,
				"isAsync": false,
				"isStatic": false,
				"isAbstract": false,
				"returnType": "string",
				"updatedAt": ` + jsonNumber(updatedAt) + `
			},
			"score": ` + jsonFloat(score) + `
		}
	]`
}

func jsonNumber(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func jsonFloat(value float64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
