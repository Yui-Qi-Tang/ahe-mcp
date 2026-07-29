package evidenceingestion

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// ExtractorGoplsCodeFact identifies the local gopls document-symbol extractor.
	ExtractorGoplsCodeFact = "gopls-code-fact"
	// ExtractorGoplsCodeFactVersion is the first real-LSP declaration contract.
	ExtractorGoplsCodeFactVersion = "v1"

	defaultGoplsTimeout                   = 30 * time.Second
	defaultGoplsWorkspaceInventoryTimeout = 2 * time.Minute
	defaultGoplsSessionShutdownTimeout    = 5 * time.Second
	repositoryGoplsSessionShutdownTimeout = 30 * time.Second
	maxLSPMessageBytes                    = 16 << 20
	maxGoplsGoFiles                       = 10_000
	goplsPackagesCommand                  = "gopls.packages"
	goplsPackageSymbolsCommand            = "gopls.package_symbols"
	goplsRevisionBinding                  = "caller-asserted"
)

// GoplsWorkspaceInventoryConfig identifies one bounded repository workspace.
type GoplsWorkspaceInventoryConfig struct {
	BinaryPath    string
	WorkspaceRoot string
	RepoID        string
	CommitSHA     string
	Timeout       time.Duration
}

// GoplsWorkspaceInventory records gopls package metadata and controller-observed Go files.
type GoplsWorkspaceInventory struct {
	RepoID                 string                  `json:"repo_id"`
	CommitSHA              string                  `json:"commit_sha"`
	GoplsVersion           string                  `json:"gopls_version"`
	RevisionBindingMethod  string                  `json:"revision_binding_method"`
	PackageDiscoveryMethod string                  `json:"package_discovery_method"`
	FileDiscoveryMethod    string                  `json:"file_discovery_method"`
	Modules                []GoplsWorkspaceModule  `json:"modules"`
	Packages               []GoplsWorkspacePackage `json:"packages"`
	Files                  []GoplsWorkspaceFile    `json:"files"`
}

// GoplsWorkspaceModule is module metadata returned by gopls.packages.
type GoplsWorkspaceModule struct {
	Path      string `json:"path"`
	Version   string `json:"version,omitempty"`
	GoModPath string `json:"go_mod_path"`
}

// GoplsWorkspacePackage is package metadata returned by gopls.packages.
type GoplsWorkspacePackage struct {
	Path       string `json:"path"`
	ModulePath string `json:"module_path,omitempty"`
	ForTest    string `json:"for_test,omitempty"`
}

// GoplsWorkspaceFile is one regular Go file observed under a gopls-reported module root.
type GoplsWorkspaceFile struct {
	Path        string `json:"path"`
	ContentHash string `json:"content_hash"`
	SizeBytes   int64  `json:"size_bytes"`
}

// GoplsExtractorConfig identifies one grounded Go file and the local gopls process.
type GoplsExtractorConfig struct {
	BinaryPath    string
	WorkspaceRoot string
	RepoID        string
	CommitSHA     string
	Path          string
	Timeout       time.Duration
}

// GoplsExtractorRunner extracts gopls-confirmed declarations from one grounded source view.
type GoplsExtractorRunner struct {
	binaryPath    string
	workspaceRoot string
	repoID        string
	commitSHA     string
	path          string
	timeout       time.Duration
	goplsVersion  string
}

// NewGoplsExtractorRunner validates the local gopls process and source location.
func NewGoplsExtractorRunner(ctx context.Context, config GoplsExtractorConfig) (*GoplsExtractorRunner, error) {
	path, err := cleanRepositoryPath(config.Path)
	if err != nil {
		return nil, err
	}
	validated, err := validateGoplsWorkspaceConfig(ctx, GoplsWorkspaceInventoryConfig{
		BinaryPath:    config.BinaryPath,
		WorkspaceRoot: config.WorkspaceRoot,
		RepoID:        config.RepoID,
		CommitSHA:     config.CommitSHA,
		Timeout:       config.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return &GoplsExtractorRunner{
		binaryPath:    validated.binaryPath,
		workspaceRoot: validated.workspaceRoot,
		repoID:        validated.repoID,
		commitSHA:     validated.commitSHA,
		path:          path,
		timeout:       validated.timeout,
		goplsVersion:  validated.goplsVersion,
	}, nil
}

// CollectGoplsWorkspaceInventory returns deterministic package, module, and Go-file inventory.
func CollectGoplsWorkspaceInventory(ctx context.Context, config GoplsWorkspaceInventoryConfig) (GoplsWorkspaceInventory, error) {
	if config.Timeout <= 0 {
		config.Timeout = defaultGoplsWorkspaceInventoryTimeout
	}
	validated, err := validateGoplsWorkspaceConfig(ctx, config)
	if err != nil {
		return GoplsWorkspaceInventory{}, err
	}

	packages, err := runGoplsPackages(ctx, validated.binaryPath, validated.workspaceRoot, validated.timeout)
	if err != nil {
		return GoplsWorkspaceInventory{}, err
	}
	modules, moduleRoots, err := normalizeGoplsModules(validated.workspaceRoot, packages.Module)
	if err != nil {
		return GoplsWorkspaceInventory{}, err
	}
	normalizedPackages, err := normalizeGoplsPackages(packages.Packages)
	if err != nil {
		return GoplsWorkspaceInventory{}, err
	}
	files, err := collectGoplsWorkspaceFiles(validated.workspaceRoot, moduleRoots)
	if err != nil {
		return GoplsWorkspaceInventory{}, err
	}

	return GoplsWorkspaceInventory{
		RepoID:                 validated.repoID,
		CommitSHA:              validated.commitSHA,
		GoplsVersion:           validated.goplsVersion,
		RevisionBindingMethod:  goplsRevisionBinding,
		PackageDiscoveryMethod: goplsPackagesCommand,
		FileDiscoveryMethod:    "controller/module-root-walk",
		Modules:                modules,
		Packages:               normalizedPackages,
		Files:                  files,
	}, nil
}

type validatedGoplsWorkspaceConfig struct {
	binaryPath    string
	workspaceRoot string
	repoID        string
	commitSHA     string
	timeout       time.Duration
	goplsVersion  string
}

func validateGoplsWorkspaceConfig(ctx context.Context, config GoplsWorkspaceInventoryConfig) (validatedGoplsWorkspaceConfig, error) {
	repoID := strings.TrimSpace(config.RepoID)
	if repoID == "" {
		return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "gopls repo_id is required")
	}
	commitSHA := strings.TrimSpace(config.CommitSHA)
	if commitSHA == "" {
		return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "gopls commit_sha is required")
	}
	workspaceValue := strings.TrimSpace(config.WorkspaceRoot)
	if workspaceValue == "" {
		return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "gopls workspace_root is required")
	}
	workspaceRoot, err := filepath.Abs(workspaceValue)
	if err != nil {
		return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "resolving gopls workspace_root: %v", err)
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "resolving gopls workspace_root symlinks: %v", err)
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "reading gopls workspace_root: %v", err)
	}
	if !info.IsDir() {
		return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "gopls workspace_root %q is not a directory", workspaceRoot)
	}
	binaryPath := strings.TrimSpace(config.BinaryPath)
	if binaryPath == "" {
		binaryPath, err = exec.LookPath("gopls")
		if err != nil {
			return validatedGoplsWorkspaceConfig{}, newDomainError(ErrorInvalidInput, "gopls binary is required: %v", err)
		}
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultGoplsTimeout
	}
	version, err := readGoplsVersion(ctx, binaryPath, workspaceRoot)
	if err != nil {
		return validatedGoplsWorkspaceConfig{}, err
	}
	return validatedGoplsWorkspaceConfig{
		binaryPath:    binaryPath,
		workspaceRoot: workspaceRoot,
		repoID:        repoID,
		commitSHA:     commitSHA,
		timeout:       timeout,
		goplsVersion:  version,
	}, nil
}

// ExtractorDefinition returns the stable provenance identity for this gopls contract.
func (r *GoplsExtractorRunner) ExtractorDefinition() ExtractorDefinitionInput {
	if r == nil {
		return ExtractorDefinitionInput{}
	}
	return ExtractorDefinitionInput{
		Name:    ExtractorGoplsCodeFact,
		Version: ExtractorGoplsCodeFactVersion,
		Config: map[string]string{
			"backend":           "gopls/lsp",
			"gopls_version":     r.goplsVersion,
			"lsp_method":        "textDocument/documentSymbol",
			"output_schema":     CodeFactSchemaV1,
			"position_encoding": "utf-16",
			"proposal_kind":     ProposalKindStatement,
			"source_kind":       "go_file",
			"span_contract":     SpanCatalogCodeLineV1,
			"symbol_schema":     "repo-commit-path-package-symbol-v1",
			"topology_scope":    "declarations_only",
		},
	}
}

// Run asks a real local gopls process for document symbols and returns transient proposal JSON.
func (r *GoplsExtractorRunner) Run(ctx context.Context, input ExtractorInput) ([]byte, error) {
	if r == nil {
		return nil, newDomainError(ErrorInvalidInput, "gopls extractor runner is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input.SourceSystem != SourceSystemCodeFile {
		return nil, newDomainError(ErrorInvalidInput, "gopls extractor requires source system %q", SourceSystemCodeFile)
	}
	if input.SpanCatalogVersion != SpanCatalogCodeLineV1 {
		return nil, newDomainError(ErrorInvalidInput, "gopls extractor requires span catalog %q", SpanCatalogCodeLineV1)
	}
	source := []byte(input.RenderedText)
	if !utf8.Valid(source) {
		return nil, newDomainError(ErrorInvalidUTF8, "go source is not valid UTF-8")
	}
	if got := contentHash(source); got != input.RawContentHash || got != input.RenderedContentHash {
		return nil, newDomainError(ErrorQuotedHashMismatch, "gopls input hash %s does not match source/view hashes %s/%s", got, input.RawContentHash, input.RenderedContentHash)
	}

	filePath := filepath.Join(r.workspaceRoot, filepath.FromSlash(r.path))
	symbols, err := runGoplsDocumentSymbols(ctx, r.binaryPath, r.workspaceRoot, filePath, source, r.timeout)
	if err != nil {
		return nil, err
	}
	output, err := materializeGoplsDeclarations(GoParserFileInput{
		RepoID:    r.repoID,
		CommitSHA: r.commitSHA,
		Path:      r.path,
		Source:    source,
		Spans:     input.Spans,
	}, symbols)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("encoding gopls extractor output: %w", err)
	}
	return data, nil
}

func cleanRepositoryPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", newDomainError(ErrorInvalidInput, "gopls path is required")
	}
	localPath := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(localPath) || localPath == ".." || strings.HasPrefix(localPath, ".."+string(filepath.Separator)) {
		return "", newDomainError(ErrorInvalidInput, "gopls path %q must stay inside workspace_root", path)
	}
	return filepath.ToSlash(localPath), nil
}

func newOfflineGoplsCommand(ctx context.Context, binaryPath string, args ...string) *exec.Cmd {
	commandPath := binaryPath
	commandArgs := args
	if runtime.GOOS == "darwin" {
		commandPath = "/usr/bin/sandbox-exec"
		commandArgs = append([]string{
			"-p",
			"(version 1) (allow default) (deny network*)",
			binaryPath,
		}, args...)
	}
	cmd := exec.CommandContext(ctx, commandPath, commandArgs...)
	cmd.Env = goplsOfflineEnvironment(os.Environ())
	return cmd
}

func goplsOfflineEnvironment(environ []string) []string {
	const (
		goenv       = "GOENV"
		goproxy     = "GOPROXY"
		gosumdb     = "GOSUMDB"
		gotoolchain = "GOTOOLCHAIN"
		gotelemetry = "GOTELEMETRY"
	)
	blocked := map[string]struct{}{
		goenv: {}, goproxy: {}, gosumdb: {}, gotoolchain: {}, gotelemetry: {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {},
		"http_proxy": {}, "https_proxy": {}, "all_proxy": {},
	}
	result := make([]string, 0, len(environ)+5)
	for _, item := range environ {
		key, _, ok := strings.Cut(item, "=")
		if _, skip := blocked[key]; ok && skip {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		goenv+"=off",
		goproxy+"=off",
		gosumdb+"=off",
		gotoolchain+"=local",
		gotelemetry+"=off",
	)
}

func readGoplsVersion(ctx context.Context, binaryPath, workspaceRoot string) (string, error) {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := newOfflineGoplsCommand(versionCtx, binaryPath, "version")
	cmd.Dir = workspaceRoot
	output, err := cmd.Output()
	if err != nil {
		if versionCtx.Err() != nil {
			return "", versionCtx.Err()
		}
		return "", newDomainError(ErrorInvalidInput, "reading gopls version: %v", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", newDomainError(ErrorInvalidInput, "gopls version output is empty")
	}
	return fields[len(fields)-1], nil
}

type goplsPackagesResult struct {
	Packages []goplsPackage         `json:"Packages"`
	Module   map[string]goplsModule `json:"Module"`
}

type goplsPackage struct {
	Path       string `json:"Path"`
	ModulePath string `json:"ModulePath"`
	ForTest    string `json:"ForTest"`
}

type goplsModule struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	GoMod   string `json:"GoMod"`
}

func normalizeGoplsPackages(raw []goplsPackage) ([]GoplsWorkspacePackage, error) {
	byKey := make(map[string]GoplsWorkspacePackage, len(raw))
	for _, item := range raw {
		path := strings.TrimSpace(item.Path)
		if path == "" {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "gopls package path is empty")
		}
		pkg := GoplsWorkspacePackage{
			Path:       path,
			ModulePath: strings.TrimSpace(item.ModulePath),
			ForTest:    strings.TrimSpace(item.ForTest),
		}
		key := strings.Join([]string{pkg.Path, pkg.ModulePath, pkg.ForTest}, "\x00")
		byKey[key] = pkg
	}
	packages := make([]GoplsWorkspacePackage, 0, len(byKey))
	for _, pkg := range byKey {
		packages = append(packages, pkg)
	}
	slices.SortFunc(packages, func(a, b GoplsWorkspacePackage) int {
		if value := strings.Compare(a.Path, b.Path); value != 0 {
			return value
		}
		if value := strings.Compare(a.ModulePath, b.ModulePath); value != 0 {
			return value
		}
		return strings.Compare(a.ForTest, b.ForTest)
	})
	return packages, nil
}

func normalizeGoplsModules(workspaceRoot string, raw map[string]goplsModule) ([]GoplsWorkspaceModule, []string, error) {
	modules := make([]GoplsWorkspaceModule, 0, len(raw))
	rootSet := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		modulePath := strings.TrimSpace(item.Path)
		if modulePath == "" {
			return nil, nil, newDomainError(ErrorInvalidExtractorOutput, "gopls module path is empty")
		}
		goModPath, err := pathFromFileURI(item.GoMod)
		if err != nil {
			return nil, nil, newDomainError(ErrorInvalidExtractorOutput, "gopls module %q has invalid go.mod URI: %v", modulePath, err)
		}
		goModPath, err = filepath.EvalSymlinks(goModPath)
		if err != nil {
			return nil, nil, newDomainError(ErrorInvalidExtractorOutput, "gopls module %q go.mod cannot be resolved: %v", modulePath, err)
		}
		relativeGoMod, err := repositoryRelativePath(workspaceRoot, goModPath)
		if err != nil {
			return nil, nil, newDomainError(ErrorInvalidExtractorOutput, "gopls module %q go.mod: %v", modulePath, err)
		}
		if filepath.Base(goModPath) != "go.mod" {
			return nil, nil, newDomainError(ErrorInvalidExtractorOutput, "gopls module %q URI does not identify go.mod", modulePath)
		}
		moduleRoot := filepath.Dir(goModPath)
		rootSet[moduleRoot] = struct{}{}
		modules = append(modules, GoplsWorkspaceModule{
			Path:      modulePath,
			Version:   strings.TrimSpace(item.Version),
			GoModPath: relativeGoMod,
		})
	}
	slices.SortFunc(modules, func(a, b GoplsWorkspaceModule) int {
		if value := strings.Compare(a.Path, b.Path); value != 0 {
			return value
		}
		return strings.Compare(a.GoModPath, b.GoModPath)
	})
	moduleRoots := make([]string, 0, len(rootSet))
	for root := range rootSet {
		moduleRoots = append(moduleRoots, root)
	}
	slices.Sort(moduleRoots)
	return modules, moduleRoots, nil
}

func collectGoplsWorkspaceFiles(workspaceRoot string, moduleRoots []string) ([]GoplsWorkspaceFile, error) {
	if len(moduleRoots) == 0 {
		moduleRoots = []string{workspaceRoot}
	}
	allowedRoots := make(map[string]struct{}, len(moduleRoots))
	for _, root := range moduleRoots {
		allowedRoots[filepath.Clean(root)] = struct{}{}
	}
	filesByPath := make(map[string]GoplsWorkspaceFile)
	for _, moduleRoot := range moduleRoots {
		walkRoot := filepath.Clean(moduleRoot)
		if _, err := repositoryRelativePath(workspaceRoot, walkRoot); err != nil {
			return nil, err
		}
		err := filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path == walkRoot {
					return nil
				}
				if _, isSeparateRoot := allowedRoots[path]; isSeparateRoot {
					return filepath.SkipDir
				}
				if skipGoplsInventoryDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("checking nested module boundary %s: %w", path, err)
				}
				return nil
			}
			if filepath.Ext(entry.Name()) != ".go" || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("reading Go file metadata %s: %w", path, err)
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			relativePath, err := repositoryRelativePath(workspaceRoot, path)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading Go file %s: %w", relativePath, err)
			}
			filesByPath[relativePath] = GoplsWorkspaceFile{
				Path:        relativePath,
				ContentHash: contentHash(content),
				SizeBytes:   int64(len(content)),
			}
			if len(filesByPath) > maxGoplsGoFiles {
				return newDomainError(ErrorInvalidInput, "gopls workspace exceeds %d Go files", maxGoplsGoFiles)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking gopls module root %s: %w", walkRoot, err)
		}
	}
	files := make([]GoplsWorkspaceFile, 0, len(filesByPath))
	for _, file := range filesByPath {
		files = append(files, file)
	}
	slices.SortFunc(files, func(a, b GoplsWorkspaceFile) int {
		return strings.Compare(a.Path, b.Path)
	})
	return files, nil
}

func skipGoplsInventoryDirectory(name string) bool {
	return name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func repositoryRelativePath(workspaceRoot, path string) (string, error) {
	relativePath, err := filepath.Rel(workspaceRoot, path)
	if err != nil {
		return "", fmt.Errorf("resolving repository-relative path: %w", err)
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace_root", path)
	}
	return filepath.ToSlash(relativePath), nil
}

func pathFromFileURI(value string) (string, error) {
	uri, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	if uri.Scheme != "file" || uri.Host != "" || uri.Path == "" {
		return "", fmt.Errorf("URI %q is not a local file URI", value)
	}
	return filepath.Clean(filepath.FromSlash(uri.Path)), nil
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspDocumentSymbol struct {
	Name           string              `json:"name"`
	Kind           int                 `json:"kind"`
	Range          lspRange            `json:"range"`
	SelectionRange lspRange            `json:"selectionRange"`
	Children       []lspDocumentSymbol `json:"children,omitempty"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type lspCallHierarchyItem struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	Detail         string          `json:"detail,omitempty"`
	URI            string          `json:"uri"`
	Range          lspRange        `json:"range"`
	SelectionRange lspRange        `json:"selectionRange"`
	Data           json.RawMessage `json:"data,omitempty"`
}

type lspCallHierarchyOutgoingCall struct {
	To         lspCallHierarchyItem `json:"to"`
	FromRanges []lspRange           `json:"fromRanges"`
}

type lspCallHierarchyIncomingCall struct {
	From       lspCallHierarchyItem `json:"from"`
	FromRanges []lspRange           `json:"fromRanges"`
}

type lspDefinitionResult []lspLocation

func (r *lspDefinitionResult) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*r = nil
		return nil
	}
	var items []json.RawMessage
	if len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
	} else {
		items = []json.RawMessage{append([]byte(nil), data...)}
	}
	locations := make([]lspLocation, 0, len(items))
	for _, item := range items {
		var raw struct {
			URI                  string   `json:"uri"`
			Range                lspRange `json:"range"`
			TargetURI            string   `json:"targetUri"`
			TargetSelectionRange lspRange `json:"targetSelectionRange"`
		}
		if err := json.Unmarshal(item, &raw); err != nil {
			return err
		}
		if raw.TargetURI != "" {
			locations = append(locations, lspLocation{URI: raw.TargetURI, Range: raw.TargetSelectionRange})
			continue
		}
		if raw.URI == "" {
			return fmt.Errorf("gopls definition location has no URI")
		}
		locations = append(locations, lspLocation{URI: raw.URI, Range: raw.Range})
	}
	*r = locations
	return nil
}

type lspWorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type lspClient struct {
	reader          *textproto.Reader
	writer          io.Writer
	nextID          int64
	workspaceFolder lspWorkspaceFolder
	callObserver    goplsCallObserver
}

type goplsSessionPhase struct {
	ctx                     context.Context
	cancel                  context.CancelFunc
	stopProcessCancellation func() bool
}

func newGoplsSessionPhase(
	parent context.Context,
	timeout time.Duration,
	cancelProcess context.CancelFunc,
) *goplsSessionPhase {
	phaseCtx, cancel := context.WithTimeout(parent, timeout)
	return &goplsSessionPhase{
		ctx:                     phaseCtx,
		cancel:                  cancel,
		stopProcessCancellation: context.AfterFunc(phaseCtx, cancelProcess),
	}
}

func (p *goplsSessionPhase) finish() (error, bool) {
	phaseErr := p.ctx.Err()
	stopped := p.stopProcessCancellation()
	deadlineExceeded := phaseErr == context.DeadlineExceeded ||
		(!stopped && phaseErr == nil)
	p.cancel()
	return phaseErr, deadlineExceeded
}

func runGoplsDocumentSymbols(ctx context.Context, binaryPath, workspaceRoot, filePath string, source []byte, timeout time.Duration) ([]lspDocumentSymbol, error) {
	var symbols []lspDocumentSymbol
	err := withGoplsSession(ctx, binaryPath, workspaceRoot, timeout, defaultGoplsSessionShutdownTimeout, func(client *lspClient) error {
		if err := openGoplsDocument(client, filePath, source); err != nil {
			return fmt.Errorf("opening gopls document: %w", err)
		}
		var err error
		symbols, err = requestGoplsDocumentSymbols(client, filePath)
		if err != nil {
			return fmt.Errorf("requesting gopls document symbols: %w", err)
		}
		if err := closeGoplsDocument(client, filePath); err != nil {
			return fmt.Errorf("closing gopls document: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return symbols, nil
}

func openGoplsDocument(client *lspClient, filePath string, source []byte) error {
	return client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        fileURI(filePath),
			"languageId": "go",
			"version":    1,
			"text":       string(source),
		},
	})
}

func requestGoplsDocumentSymbols(client *lspClient, filePath string) ([]lspDocumentSymbol, error) {
	var symbols []lspDocumentSymbol
	err := client.call("textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]string{"uri": fileURI(filePath)},
	}, &symbols)
	return symbols, err
}

func requestGoplsDefinition(client *lspClient, filePath string, position lspPosition) ([]lspLocation, error) {
	var result lspDefinitionResult
	err := client.call("textDocument/definition", map[string]any{
		"textDocument": map[string]string{"uri": fileURI(filePath)},
		"position":     position,
	}, &result)
	return result, err
}

func requestGoplsReferences(client *lspClient, filePath string, position lspPosition) ([]lspLocation, error) {
	var result []lspLocation
	err := client.call("textDocument/references", map[string]any{
		"textDocument": map[string]string{"uri": fileURI(filePath)},
		"position":     position,
		"context": map[string]bool{
			"includeDeclaration": false,
		},
	}, &result)
	return result, err
}

func requestGoplsPrepareCallHierarchy(client *lspClient, filePath string, position lspPosition) ([]lspCallHierarchyItem, error) {
	var result []lspCallHierarchyItem
	err := client.call("textDocument/prepareCallHierarchy", map[string]any{
		"textDocument": map[string]string{"uri": fileURI(filePath)},
		"position":     position,
	}, &result)
	return result, err
}

func requestGoplsOutgoingCalls(client *lspClient, item lspCallHierarchyItem) ([]lspCallHierarchyOutgoingCall, error) {
	var result []lspCallHierarchyOutgoingCall
	err := client.call("callHierarchy/outgoingCalls", map[string]any{
		"item": item,
	}, &result)
	return result, err
}

func requestGoplsIncomingCalls(client *lspClient, item lspCallHierarchyItem) ([]lspCallHierarchyIncomingCall, error) {
	var result []lspCallHierarchyIncomingCall
	err := client.call("callHierarchy/incomingCalls", map[string]any{
		"item": item,
	}, &result)
	return result, err
}

func closeGoplsDocument(client *lspClient, filePath string) error {
	return client.notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]string{"uri": fileURI(filePath)},
	})
}

func runGoplsPackages(ctx context.Context, binaryPath, workspaceRoot string, timeout time.Duration) (goplsPackagesResult, error) {
	var result goplsPackagesResult
	err := withGoplsSession(ctx, binaryPath, workspaceRoot, timeout, defaultGoplsSessionShutdownTimeout, func(client *lspClient) error {
		if err := client.call("workspace/executeCommand", map[string]any{
			"command": goplsPackagesCommand,
			"arguments": []any{map[string]any{
				"Files":     []string{fileURI(workspaceRoot)},
				"Recursive": true,
				"Mode":      0,
			}},
		}, &result); err != nil {
			return fmt.Errorf("requesting gopls workspace packages: %w", err)
		}
		return nil
	})
	if err != nil {
		return goplsPackagesResult{}, err
	}
	return result, nil
}

func withGoplsSession(
	ctx context.Context,
	binaryPath string,
	workspaceRoot string,
	timeout time.Duration,
	shutdownTimeout time.Duration,
	run func(*lspClient) error,
) error {
	return withGoplsSessionObserved(
		ctx,
		binaryPath,
		workspaceRoot,
		timeout,
		shutdownTimeout,
		nil,
		run,
	)
}

func withGoplsSessionObserved(
	ctx context.Context,
	binaryPath string,
	workspaceRoot string,
	timeout time.Duration,
	shutdownTimeout time.Duration,
	callObserver goplsCallObserver,
	run func(*lspClient) error,
) error {
	// The parent owns the process; each bounded phase may cancel it independently.
	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()

	cmd := newOfflineGoplsCommand(processCtx, binaryPath, "serve")
	cmd.Dir = workspaceRoot
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("opening gopls stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("opening gopls stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	workPhase := newGoplsSessionPhase(ctx, timeout, cancelProcess)
	if err := cmd.Start(); err != nil {
		_, _ = workPhase.finish()
		return fmt.Errorf("starting gopls: %w", err)
	}

	workspaceURI := fileURI(workspaceRoot)
	client := &lspClient{
		reader: textproto.NewReader(bufio.NewReader(stdout)),
		writer: stdin,
		workspaceFolder: lspWorkspaceFolder{
			URI:  workspaceURI,
			Name: filepath.Base(workspaceRoot),
		},
		callObserver: callObserver,
	}
	terminate := func(
		phaseErr error,
		deadlineExceeded bool,
		sessionErr error,
	) error {
		parentErr := ctx.Err()
		cancelProcess()
		_ = stdin.Close()
		_ = cmd.Wait()
		if err := ctx.Err(); err != nil {
			parentErr = err
		}
		return goplsSessionFailure(
			parentErr,
			phaseErr,
			deadlineExceeded,
			sessionErr,
			stderr.String(),
		)
	}
	fail := func(phase *goplsSessionPhase, sessionErr error) error {
		phaseErr, deadlineExceeded := phase.finish()
		return terminate(phaseErr, deadlineExceeded, sessionErr)
	}

	var initialized struct {
		Capabilities struct {
			PositionEncoding string `json:"positionEncoding"`
		} `json:"capabilities"`
	}
	if err := client.call("initialize", map[string]any{
		"processId": nil,
		"clientInfo": map[string]string{
			"name":    "ahe-mcp",
			"version": ExtractorGoplsCodeFactVersion,
		},
		"rootUri": workspaceURI,
		"capabilities": map[string]any{
			"general": map[string]any{
				"positionEncodings": []string{"utf-16"},
			},
			"workspace": map[string]any{
				"configuration":    true,
				"workspaceFolders": true,
			},
			"textDocument": map[string]any{
				"documentSymbol": map[string]any{
					"hierarchicalDocumentSymbolSupport": true,
				},
				"definition":    map[string]any{},
				"references":    map[string]any{},
				"callHierarchy": map[string]any{},
			},
		},
		"workspaceFolders": []lspWorkspaceFolder{client.workspaceFolder},
	}, &initialized); err != nil {
		return fail(workPhase, fmt.Errorf("initializing gopls: %w", err))
	}
	if encoding := initialized.Capabilities.PositionEncoding; encoding != "" && encoding != "utf-16" {
		return fail(
			workPhase,
			fmt.Errorf("gopls selected unsupported position encoding %q", encoding),
		)
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		return fail(
			workPhase,
			fmt.Errorf("notifying gopls initialization: %w", err),
		)
	}
	if err := run(client); err != nil {
		return fail(workPhase, err)
	}
	workErr, workDeadlineExceeded := workPhase.finish()
	if workErr != nil || workDeadlineExceeded {
		return terminate(
			workErr,
			workDeadlineExceeded,
			context.DeadlineExceeded,
		)
	}

	// Normal LSP cleanup must not consume or extend the active semantic-work budget.
	shutdownPhase := newGoplsSessionPhase(
		ctx,
		shutdownTimeout,
		cancelProcess,
	)
	if err := client.call("shutdown", nil, nil); err != nil {
		return fail(
			shutdownPhase,
			fmt.Errorf("shutting down gopls: %w", err),
		)
	}
	if err := client.notify("exit", nil); err != nil {
		return fail(shutdownPhase, fmt.Errorf("exiting gopls: %w", err))
	}
	if err := stdin.Close(); err != nil {
		return fail(
			shutdownPhase,
			fmt.Errorf("closing gopls stdin: %w", err),
		)
	}
	waitErr := cmd.Wait()
	shutdownErr, shutdownDeadlineExceeded := shutdownPhase.finish()
	if waitErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return parentErr
		}
		if shutdownErr != nil || shutdownDeadlineExceeded {
			return goplsSessionFailure(
				nil,
				shutdownErr,
				shutdownDeadlineExceeded,
				waitErr,
				stderr.String(),
			)
		}
		return fmt.Errorf(
			"waiting for gopls: %w: %s",
			waitErr,
			strings.TrimSpace(stderr.String()),
		)
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return parentErr
	}
	if shutdownErr != nil || shutdownDeadlineExceeded {
		return goplsSessionFailure(
			nil,
			shutdownErr,
			shutdownDeadlineExceeded,
			context.DeadlineExceeded,
			stderr.String(),
		)
	}
	return nil
}

func goplsSessionFailure(
	parentErr error,
	runErr error,
	deadlineExceeded bool,
	sessionErr error,
	stderr string,
) error {
	switch {
	case parentErr != nil:
		return parentErr
	case runErr != nil:
		return runErr
	case deadlineExceeded:
		return context.DeadlineExceeded
	default:
		message := strings.TrimSpace(stderr)
		if message == "" {
			return sessionErr
		}
		return fmt.Errorf("%w: %s", sessionErr, message)
	}
}

func (c *lspClient) call(method string, params any, result any) (returnErr error) {
	var started time.Time
	if c.callObserver != nil {
		started = time.Now()
		defer func() {
			c.callObserver(method, time.Since(started), returnErr == nil)
		}()
	}
	c.nextID++
	id := c.nextID
	if err := c.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return err
	}
	for {
		message, err := c.readMessage()
		if err != nil {
			return err
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(message, &envelope); err != nil {
			return fmt.Errorf("decoding LSP message: %w", err)
		}
		if methodData, ok := envelope["method"]; ok {
			if idData, isRequest := envelope["id"]; isRequest {
				if err := c.handleServerRequest(idData, methodData, envelope["params"]); err != nil {
					return err
				}
			}
			continue
		}
		idData, ok := envelope["id"]
		if !ok || strings.TrimSpace(string(idData)) != strconv.FormatInt(id, 10) {
			continue
		}
		if errorData := envelope["error"]; len(errorData) > 0 && string(errorData) != "null" {
			var rpcError struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(errorData, &rpcError); err != nil {
				return fmt.Errorf("decoding LSP error: %w", err)
			}
			return fmt.Errorf("LSP error %d: %s", rpcError.Code, rpcError.Message)
		}
		if result == nil {
			return nil
		}
		resultData := envelope["result"]
		if len(resultData) == 0 || string(resultData) == "null" {
			return nil
		}
		if err := json.Unmarshal(resultData, result); err != nil {
			return fmt.Errorf("decoding LSP result for %s: %w", method, err)
		}
		return nil
	}
}

func (c *lspClient) notify(method string, params any) error {
	return c.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *lspClient) handleServerRequest(idData, methodData, paramsData json.RawMessage) error {
	var method string
	if err := json.Unmarshal(methodData, &method); err != nil {
		return fmt.Errorf("decoding LSP server method: %w", err)
	}
	var result any
	switch method {
	case "workspace/configuration":
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(paramsData, &params); err != nil {
			return fmt.Errorf("decoding gopls workspace/configuration request: %w", err)
		}
		result = make([]any, len(params.Items))
	case "workspace/workspaceFolders":
		result = []lspWorkspaceFolder{c.workspaceFolder}
	case "client/registerCapability", "window/workDoneProgress/create":
		result = nil
	default:
		return c.writeMessage(map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(idData),
			"error": map[string]any{
				"code":    -32601,
				"message": "method not supported",
			},
		})
	}
	return c.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idData),
		"result":  result,
	})
}

func (c *lspClient) writeMessage(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encoding LSP message: %w", err)
	}
	var frame bytes.Buffer
	fmt.Fprintf(&frame, "Content-Length: %d\r\n\r\n", len(data))
	frame.Write(data)
	if _, err := c.writer.Write(frame.Bytes()); err != nil {
		return fmt.Errorf("writing LSP message: %w", err)
	}
	return nil
}

func (c *lspClient) readMessage() ([]byte, error) {
	header, err := c.reader.ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("reading LSP header: %w", err)
	}
	length, err := strconv.Atoi(header.Get("Content-Length"))
	if err != nil || length < 0 || length > maxLSPMessageBytes {
		return nil, fmt.Errorf("invalid LSP Content-Length %q", header.Get("Content-Length"))
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(c.reader.R, data); err != nil {
		return nil, fmt.Errorf("reading LSP body: %w", err)
	}
	return data, nil
}

func materializeGoplsDeclarations(input GoParserFileInput, symbols []lspDocumentSymbol) (FrozenExtractorOutput, error) {
	parsed, err := ExtractGoParserFile(input)
	if err != nil {
		return FrozenExtractorOutput{}, err
	}
	return materializeGoplsDeclarationsWithExtraction(input, symbols, parsed)
}

func materializeGoplsDeclarationsWithExtraction(
	input GoParserFileInput,
	symbols []lspDocumentSymbol,
	parsed GoParserFileExtraction,
) (FrozenExtractorOutput, error) {
	symbolShapes, err := inspectGoGoplsSymbolShapes(input.Path, input.Source)
	if err != nil {
		return FrozenExtractorOutput{}, err
	}
	type declarationKey struct {
		kind      string
		startByte int
		endByte   int
	}
	declarations := make(map[declarationKey]GoParserDeclaration, len(parsed.Declarations))
	for _, declaration := range parsed.Declarations {
		if declaration.Name == "_" {
			continue
		}
		declarations[declarationKey{
			kind:      declaration.Kind,
			startByte: declaration.StartByte,
			endByte:   declaration.EndByte,
		}] = declaration
	}
	seen := make(map[declarationKey]bool, len(declarations))
	for _, symbol := range flattenDocumentSymbols(symbols) {
		kind := codeKindForDocumentSymbol(symbol.Kind)
		if kind == "" {
			continue
		}
		startByte, err := lspPositionByteOffset(input.Source, symbol.SelectionRange.Start)
		if err != nil {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "mapping gopls symbol %q start: %v", symbol.Name, err)
		}
		endByte, err := lspPositionByteOffset(input.Source, symbol.SelectionRange.End)
		if err != nil {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "mapping gopls symbol %q end: %v", symbol.Name, err)
		}
		key := declarationKey{kind: kind, startByte: startByte, endByte: endByte}
		if _, ok := declarations[key]; !ok {
			span := goplsSymbolSpan{
				startByte: startByte,
				endByte:   endByte,
			}
			if kind == "method" && symbolShapes.interfaceMethods[span] {
				continue
			}
			typeKey := declarationKey{
				kind:      "type",
				startByte: startByte,
				endByte:   endByte,
			}
			if kind == "function" && symbolShapes.namedFunctionTypes[span] {
				if _, ok := declarations[typeKey]; ok {
					seen[typeKey] = true
					continue
				}
			}
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "gopls %s symbol %q at bytes [%d,%d) does not match a grounded Go declaration", kind, symbol.Name, startByte, endByte)
		}
		seen[key] = true
	}

	proposals := make([]ExtractorProposalOutput, 0, len(parsed.Declarations))
	for _, declaration := range parsed.Declarations {
		if declaration.Name == "_" {
			continue
		}
		key := declarationKey{kind: declaration.Kind, startByte: declaration.StartByte, endByte: declaration.EndByte}
		if !seen[key] {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "gopls omitted Go %s declaration %s at bytes [%d,%d)", declaration.Kind, declaration.Name, declaration.StartByte, declaration.EndByte)
		}
		fact := declaration.CodeFact
		proposals = append(proposals, ExtractorProposalOutput{
			ProposalLocalID: declaration.ProposalLocalID,
			StatementText:   declaration.StatementText,
			EvidenceRefs:    append([]string(nil), declaration.EvidenceRefs...),
			CodeFact:        &fact,
		})
	}
	return FrozenExtractorOutput{Proposals: proposals}, nil
}

type goplsSymbolSpan struct {
	startByte int
	endByte   int
}

type goGoplsSymbolShapes struct {
	interfaceMethods   map[goplsSymbolSpan]bool
	namedFunctionTypes map[goplsSymbolSpan]bool
}

func inspectGoGoplsSymbolShapes(path string, source []byte) (goGoplsSymbolShapes, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(
		fset,
		path,
		source,
		parser.SkipObjectResolution,
	)
	if err != nil {
		return goGoplsSymbolShapes{}, newDomainError(
			ErrorInvalidExtractorOutput,
			"parsing Go symbol shapes in %s: %v",
			path,
			err,
		)
	}
	shapes := goGoplsSymbolShapes{
		interfaceMethods:   make(map[goplsSymbolSpan]bool),
		namedFunctionTypes: make(map[goplsSymbolSpan]bool),
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.InterfaceType:
			for _, method := range node.Methods.List {
				if _, ok := method.Type.(*ast.FuncType); !ok {
					continue
				}
				for _, name := range method.Names {
					shapes.interfaceMethods[goplsSymbolSpan{
						startByte: fset.Position(name.Pos()).Offset,
						endByte:   fset.Position(name.End()).Offset,
					}] = true
				}
			}
			return false
		case *ast.TypeSpec:
			if _, ok := node.Type.(*ast.FuncType); !ok {
				return true
			}
			shapes.namedFunctionTypes[goplsSymbolSpan{
				startByte: fset.Position(node.Name.Pos()).Offset,
				endByte:   fset.Position(node.Name.End()).Offset,
			}] = true
			return true
		}
		return true
	})
	return shapes, nil
}

func flattenDocumentSymbols(symbols []lspDocumentSymbol) []lspDocumentSymbol {
	var flattened []lspDocumentSymbol
	var visit func([]lspDocumentSymbol)
	visit = func(items []lspDocumentSymbol) {
		for _, symbol := range items {
			flattened = append(flattened, symbol)
			visit(symbol.Children)
		}
	}
	visit(symbols)
	return flattened
}

func codeKindForDocumentSymbol(kind int) string {
	switch kind {
	case 5, 10, 11, 23:
		return "type"
	case 6:
		return "method"
	case 12:
		return "function"
	case 13:
		return "var"
	case 14:
		return "const"
	default:
		return ""
	}
}

func lspPositionByteOffset(source []byte, position lspPosition) (int, error) {
	if position.Line < 0 || position.Character < 0 {
		return 0, fmt.Errorf("negative LSP position %d:%d", position.Line, position.Character)
	}
	lineStart := 0
	for line := 0; line < position.Line; line++ {
		newline := bytes.IndexByte(source[lineStart:], '\n')
		if newline < 0 {
			return 0, fmt.Errorf("LSP line %d outside source", position.Line)
		}
		lineStart += newline + 1
	}
	lineEnd := len(source)
	if newline := bytes.IndexByte(source[lineStart:], '\n'); newline >= 0 {
		lineEnd = lineStart + newline
	}
	if lineEnd > lineStart && source[lineEnd-1] == '\r' {
		lineEnd--
	}
	units := 0
	for offset := lineStart; offset < lineEnd; {
		if units == position.Character {
			return offset, nil
		}
		r, size := utf8.DecodeRune(source[offset:lineEnd])
		if r == utf8.RuneError && size == 1 {
			return 0, fmt.Errorf("invalid UTF-8 at byte %d", offset)
		}
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units+runeUnits > position.Character {
			return 0, fmt.Errorf("LSP character %d splits a UTF-16 surrogate pair", position.Character)
		}
		units += runeUnits
		offset += size
	}
	if units == position.Character {
		return lineEnd, nil
	}
	return 0, fmt.Errorf("LSP character %d outside line %d", position.Character, position.Line)
}

func lspPositionForByteOffset(source []byte, offset int) (lspPosition, error) {
	if offset < 0 || offset > len(source) {
		return lspPosition{}, fmt.Errorf("byte offset %d outside source length %d", offset, len(source))
	}
	lineStart := bytes.LastIndexByte(source[:offset], '\n') + 1
	line := bytes.Count(source[:lineStart], []byte{'\n'})
	units := 0
	for current := lineStart; current < offset; {
		r, size := utf8.DecodeRune(source[current:offset])
		if r == utf8.RuneError && size == 1 {
			return lspPosition{}, fmt.Errorf("invalid UTF-8 at byte %d", current)
		}
		units++
		if r > 0xffff {
			units++
		}
		current += size
	}
	return lspPosition{Line: line, Character: units}, nil
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
