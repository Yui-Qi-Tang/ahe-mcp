// Package codegraphadapter exposes a pinned CodeGraph CLI as a bounded,
// non-authoritative MCP candidate lookup.
package codegraphadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

const (
	// AdapterVersion identifies the exact AHE conversion contract.
	AdapterVersion = "codegraph-cli-candidate-v1"
	// ProviderRelease is the CodeGraph release audited by this adapter.
	ProviderRelease = "colbymchenry/codegraph@v1.5.0"
	// ResultContract identifies the normalized candidate result.
	ResultContract = "ahe-codegraph-candidate-v1"
	// ToolQueryFunction is the single bounded logical capability.
	ToolQueryFunction = "query_codegraph_function"
	// QueryFunctionSchemaHashV1 pins the AHE-owned logical tool schema.
	QueryFunctionSchemaHashV1 = "sha256:0e8877104477fefe2bc35c5046c5f9e31a93f0fac1e0c77555342dcd8f43e93a"

	maxCommandOutput     = 128 << 10
	maxResultBytes       = 512 << 10
	maxSourceFileBytes   = 2 << 20
	maxQueryBytes        = 200
	maxRepositoryIDBytes = 200
	maxResults           = 20
)

// Config pins one local repository and CodeGraph executable.
type Config struct {
	ProviderExecutable string `json:"provider_executable"`
	ProviderRelease    string `json:"provider_release"`
	GitExecutable      string `json:"git_executable"`
	RepositoryRoot     string `json:"repository_root"`
	AnalysisRoot       string `json:"analysis_root"`
	HomeDirectory      string `json:"home_directory"`
	RepositoryID       string `json:"repository_id"`
	CommitSHA          string `json:"commit_sha"`
	CommandTimeoutMS   int    `json:"command_timeout_ms"`
	sourcePrefix       string
}

// Backend implements one AHE-owned MCP tool over the pinned CodeGraph CLI.
type Backend struct {
	config Config
	run    commandRunner
}

type commandSpec struct {
	path        string
	args        []string
	directory   string
	environment []string
	sandboxed   bool
	timeout     time.Duration
}

type commandRunner func(context.Context, commandSpec) ([]byte, error)

// NewBackend validates the fixed repository and provider boundary.
func NewBackend(config Config) (*Backend, error) {
	return newBackend(config, runCommand)
}

func newBackend(config Config, run commandRunner) (*Backend, error) {
	prepared, err := prepareConfig(config)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("codegraph command runner is required")
	}
	return &Backend{config: prepared, run: run}, nil
}

func prepareConfig(config Config) (Config, error) {
	if config.ProviderRelease != ProviderRelease {
		return Config{}, fmt.Errorf("unsupported CodeGraph provider release %q", config.ProviderRelease)
	}
	for label, path := range map[string]string{
		"provider executable": config.ProviderExecutable,
		"git executable":      config.GitExecutable,
		"repository root":     config.RepositoryRoot,
		"analysis root":       config.AnalysisRoot,
		"home directory":      config.HomeDirectory,
	} {
		if !filepath.IsAbs(path) {
			return Config{}, fmt.Errorf("CodeGraph %s must be absolute", label)
		}
	}
	repositoryID := strings.TrimSpace(config.RepositoryID)
	if repositoryID == "" || len(repositoryID) > maxRepositoryIDBytes {
		return Config{}, fmt.Errorf("CodeGraph repository_id must contain 1 to %d bytes", maxRepositoryIDBytes)
	}
	commitSHA := strings.ToLower(strings.TrimSpace(config.CommitSHA))
	if !validCommitSHA(commitSHA) {
		return Config{}, errors.New("CodeGraph commit_sha must be a full 40-character hexadecimal Git commit")
	}
	if config.CommandTimeoutMS < 1_000 || config.CommandTimeoutMS > 120_000 {
		return Config{}, errors.New("CodeGraph command_timeout_ms must be between 1000 and 120000")
	}
	config.ProviderExecutable = filepath.Clean(config.ProviderExecutable)
	config.GitExecutable = filepath.Clean(config.GitExecutable)
	config.RepositoryRoot = filepath.Clean(config.RepositoryRoot)
	config.AnalysisRoot = filepath.Clean(config.AnalysisRoot)
	config.HomeDirectory = filepath.Clean(config.HomeDirectory)
	config.RepositoryID = repositoryID
	config.CommitSHA = commitSHA
	repositoryRoot, err := filepath.EvalSymlinks(config.RepositoryRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolving CodeGraph repository root: %w", err)
	}
	analysisRoot, err := filepath.EvalSymlinks(config.AnalysisRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolving CodeGraph analysis root: %w", err)
	}
	prefix, err := filepath.Rel(repositoryRoot, analysisRoot)
	if err != nil || prefix == ".." || strings.HasPrefix(prefix, ".."+string(filepath.Separator)) {
		return Config{}, errors.New("CodeGraph analysis root must be inside repository root")
	}
	if prefix == "." {
		prefix = ""
	}
	config.RepositoryRoot = repositoryRoot
	config.AnalysisRoot = analysisRoot
	config.sourcePrefix = filepath.ToSlash(prefix)
	return config, nil
}

func validCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// Tools returns the single bounded candidate lookup.
func (b *Backend) Tools() []mcpstdio.Tool {
	readOnly := true
	destructive := false
	openWorld := false
	return []mcpstdio.Tool{{
		Name:        ToolQueryFunction,
		Title:       "Query CodeGraph Function",
		Description: "Returns bounded, revision-anchored function and caller candidates from a pinned local CodeGraph index.",
		InputSchema: QueryFunctionInputSchema(),
		Annotations: mcpstdio.Annotations{
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
	}}
}

// QueryFunctionInputSchema returns the stable adapter input schema.
func QueryFunctionInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"symbol": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": maxQueryBytes,
			},
			"limit": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"maximum": maxResults,
			},
			"include_callers": map[string]any{
				"type": "boolean",
			},
		},
		"required": []string{"symbol", "limit", "include_callers"},
	}
}

type queryFunctionInput struct {
	Symbol         string `json:"symbol"`
	Limit          int    `json:"limit"`
	IncludeCallers bool   `json:"include_callers"`
}

// CallTool rebuilds the bounded local index and returns non-authoritative,
// source-anchored candidates.
func (b *Backend) CallTool(
	ctx context.Context,
	name string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	if ctx == nil {
		return nil, errors.New("CodeGraph adapter context is required")
	}
	if name != ToolQueryFunction {
		return nil, fmt.Errorf("unsupported CodeGraph adapter tool %q", name)
	}
	input, err := decodeQueryFunctionInput(arguments)
	if err != nil {
		return nil, err
	}
	if err := b.verifyRepository(ctx); err != nil {
		return nil, err
	}
	version, err := b.runProvider(ctx, "--version")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(version)) != "1.5.0" {
		return nil, fmt.Errorf("CodeGraph version %q does not match pinned 1.5.0", strings.TrimSpace(string(version)))
	}
	indexCommand := "index"
	if _, err := os.Stat(filepath.Join(b.config.AnalysisRoot, ".codegraph", "codegraph.db")); errors.Is(err, os.ErrNotExist) {
		indexCommand = "init"
	} else if err != nil {
		return nil, fmt.Errorf("checking CodeGraph index: %w", err)
	}
	if _, err := b.runProvider(ctx, indexCommand, b.config.AnalysisRoot); err != nil {
		return nil, err
	}
	if err := b.verifyRepository(ctx); err != nil {
		return nil, err
	}

	limit := strconv.Itoa(input.Limit)
	queryRaw, err := b.runProvider(
		ctx,
		"query", input.Symbol,
		"-p", b.config.AnalysisRoot,
		"-l", limit,
		"-k", "function",
		"-j",
	)
	if err != nil {
		return nil, err
	}
	var callersRaw []byte
	if input.IncludeCallers {
		callersRaw, err = b.runProvider(
			ctx,
			"callers", input.Symbol,
			"-p", b.config.AnalysisRoot,
			"-l", limit,
			"-j",
		)
		if err != nil {
			return nil, err
		}
	}
	result, err := normalizeResult(
		ctx,
		b.config,
		input,
		queryRaw,
		callersRaw,
		b.loadSourceFile,
	)
	if err != nil {
		return nil, err
	}
	if err := b.verifyRepository(ctx); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding CodeGraph candidate result: %w", err)
	}
	if len(raw) > maxResultBytes {
		return nil, fmt.Errorf("CodeGraph candidate result exceeds %d bytes", maxResultBytes)
	}
	return raw, nil
}

func decodeQueryFunctionInput(raw []byte) (queryFunctionInput, error) {
	var input queryFunctionInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return queryFunctionInput{}, fmt.Errorf("invalid CodeGraph query input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return queryFunctionInput{}, errors.New("CodeGraph query input must contain one JSON object")
	}
	input.Symbol = strings.TrimSpace(input.Symbol)
	if input.Symbol == "" || len(input.Symbol) > maxQueryBytes {
		return queryFunctionInput{}, fmt.Errorf("CodeGraph symbol must contain 1 to %d bytes", maxQueryBytes)
	}
	if strings.ContainsAny(input.Symbol, "\x00\r\n") {
		return queryFunctionInput{}, errors.New("CodeGraph symbol contains an unsupported control character")
	}
	if input.Limit < 1 || input.Limit > maxResults {
		return queryFunctionInput{}, fmt.Errorf("CodeGraph limit must be between 1 and %d", maxResults)
	}
	return input, nil
}

func (b *Backend) verifyRepository(ctx context.Context) error {
	rootRaw, err := b.runGit(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	actualRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(rootRaw)))
	if err != nil {
		return fmt.Errorf("resolving Git root: %w", err)
	}
	expectedRoot := b.config.RepositoryRoot
	if actualRoot != expectedRoot {
		return fmt.Errorf("Git root %q does not match configured repository %q", actualRoot, expectedRoot)
	}
	head, err := b.runGit(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != b.config.CommitSHA {
		return fmt.Errorf(
			"Git HEAD %q does not match configured revision %q",
			strings.TrimSpace(string(head)),
			b.config.CommitSHA,
		)
	}
	status, err := b.runGit(ctx, "status", "--porcelain=v1", "--untracked-files=no")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(status)) != 0 {
		return errors.New("CodeGraph workspace has tracked changes")
	}
	return nil
}

func (b *Backend) loadSourceFile(ctx context.Context, path string) ([]byte, error) {
	clean, err := cleanSourcePath(path)
	if err != nil {
		return nil, err
	}
	committed, err := b.runGit(ctx, "show", b.config.CommitSHA+":"+clean)
	if err != nil {
		return nil, err
	}
	if len(committed) > maxSourceFileBytes {
		return nil, fmt.Errorf("CodeGraph source file %q exceeds %d bytes", clean, maxSourceFileBytes)
	}
	workspaceBytes, err := os.ReadFile(filepath.Join(b.config.RepositoryRoot, filepath.FromSlash(clean)))
	if err != nil {
		return nil, fmt.Errorf("reading CodeGraph workspace source %q: %w", clean, err)
	}
	if !bytes.Equal(workspaceBytes, committed) {
		return nil, fmt.Errorf("CodeGraph workspace source %q does not match Git revision", clean)
	}
	return committed, nil
}

func (b *Backend) runGit(ctx context.Context, args ...string) ([]byte, error) {
	return b.run(ctx, commandSpec{
		path:        b.config.GitExecutable,
		args:        args,
		directory:   b.config.RepositoryRoot,
		environment: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "TZ=UTC"},
		timeout:     time.Duration(b.config.CommandTimeoutMS) * time.Millisecond,
	})
}

func (b *Backend) runProvider(ctx context.Context, args ...string) ([]byte, error) {
	return b.run(ctx, commandSpec{
		path:        b.config.ProviderExecutable,
		args:        args,
		directory:   b.config.AnalysisRoot,
		environment: b.providerEnvironment(),
		sandboxed:   true,
		timeout:     time.Duration(b.config.CommandTimeoutMS) * time.Millisecond,
	})
}

func (b *Backend) providerEnvironment() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + b.config.HomeDirectory,
		"TMPDIR=" + b.config.HomeDirectory,
		"TZ=UTC",
		"LC_ALL=C",
		"NO_COLOR=1",
		"DO_NOT_TRACK=1",
		"CODEGRAPH_TELEMETRY=0",
		"CODEGRAPH_NO_UPDATE_CHECK=1",
		"CODEGRAPH_NO_DAEMON=1",
		"CODEGRAPH_NO_WATCH=1",
		"CODEGRAPH_PARSE_WORKERS=1",
		"CODEGRAPH_RESOLVE_WORKERS=1",
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"ALL_PROXY=",
	}
}

func runCommand(ctx context.Context, spec commandSpec) ([]byte, error) {
	if runtime.GOOS != "darwin" && spec.sandboxed {
		return nil, errors.New("CodeGraph provider sandbox requires macOS")
	}
	runCtx, cancel := context.WithTimeout(ctx, spec.timeout)
	defer cancel()
	command := mcpstdio.CommandConfig{
		Path:        spec.path,
		Args:        spec.args,
		Directory:   spec.directory,
		Environment: spec.environment,
	}
	var err error
	if spec.sandboxed {
		command, err = mcpstdio.MacOSLoopbackOnlyCommand(command)
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(runCtx, command.Path, command.Args...)
	cmd.Dir = command.Directory
	cmd.Env = command.Environment
	cmd.WaitDelay = time.Second
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("CodeGraph command %q exceeded %s", spec.path, spec.timeout)
		}
		return nil, fmt.Errorf(
			"CodeGraph command %q failed: %w: %s",
			spec.path,
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

type boundedBuffer struct {
	bytes.Buffer
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if b.Len()+len(value) > maxCommandOutput {
		return 0, fmt.Errorf("CodeGraph command output exceeds %d bytes", maxCommandOutput)
	}
	return b.Buffer.Write(value)
}
