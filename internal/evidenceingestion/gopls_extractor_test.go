package evidenceingestion

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestGoplsSessionFailurePreservesUnderlyingErrorBeforeCancellation(t *testing.T) {
	sessionErr := errors.New("workspace request failed")
	err := goplsSessionFailure(
		nil,
		nil,
		false,
		sessionErr,
		"gopls diagnostic",
	)
	if !errors.Is(err, sessionErr) {
		t.Fatalf("goplsSessionFailure() error = %v, want session error", err)
	}
	if got := err.Error(); got != "workspace request failed: gopls diagnostic" {
		t.Fatalf("goplsSessionFailure() error = %q", got)
	}

	err = goplsSessionFailure(
		context.Canceled,
		nil,
		false,
		sessionErr,
		"gopls diagnostic",
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation error = %v", err)
	}

	err = goplsSessionFailure(
		nil,
		nil,
		true,
		sessionErr,
		"gopls diagnostic",
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestRepositoryGoplsSessionBudgetsSeparateWorkAndShutdown(t *testing.T) {
	if defaultRepositoryGoplsTimeout != 2*time.Minute {
		t.Fatalf(
			"repository gopls work timeout = %s, want 2m",
			defaultRepositoryGoplsTimeout,
		)
	}
	if repositoryGoplsSessionShutdownTimeout != 30*time.Second {
		t.Fatalf(
			"repository gopls shutdown timeout = %s, want 30s",
			repositoryGoplsSessionShutdownTimeout,
		)
	}
	if defaultGoplsSessionShutdownTimeout != 5*time.Second {
		t.Fatalf(
			"default gopls shutdown timeout = %s, want 5s",
			defaultGoplsSessionShutdownTimeout,
		)
	}
}

func TestGoplsSessionPhaseFinishStopsProcessCancellation(t *testing.T) {
	processCancelled := make(chan struct{}, 1)
	phase := newGoplsSessionPhase(
		t.Context(),
		25*time.Millisecond,
		func() {
			processCancelled <- struct{}{}
		},
	)

	phaseErr, deadlineExceeded := phase.finish()
	if phaseErr != nil || deadlineExceeded {
		t.Fatalf(
			"finished phase error/deadline = %v/%t",
			phaseErr,
			deadlineExceeded,
		)
	}
	select {
	case <-processCancelled:
		t.Fatal("finished phase cancelled the process")
	default:
	}
}

func TestGoplsSessionPhaseDeadlineCancelsProcess(t *testing.T) {
	processCancelled := make(chan struct{}, 1)
	phase := newGoplsSessionPhase(
		t.Context(),
		10*time.Millisecond,
		func() {
			processCancelled <- struct{}{}
		},
	)

	select {
	case <-processCancelled:
	case <-time.After(time.Second):
		t.Fatal("phase deadline did not cancel the process")
	}
	phaseErr, deadlineExceeded := phase.finish()
	if phaseErr != context.DeadlineExceeded || !deadlineExceeded {
		t.Fatalf(
			"expired phase error/deadline = %v/%t",
			phaseErr,
			deadlineExceeded,
		)
	}
}

func TestLSPPositionByteOffsetUsesUTF16CodeUnits(t *testing.T) {
	source := []byte("package sample\n// 中文😀 end\nfunc 建立() {}\n")

	start := bytes.Index(source, []byte("建立"))
	got, err := lspPositionByteOffset(source, lspPosition{Line: 2, Character: 5})
	if err != nil {
		t.Fatalf("lspPositionByteOffset() error = %v", err)
	}
	if got != start {
		t.Fatalf("identifier start = %d, want %d", got, start)
	}
	got, err = lspPositionByteOffset(source, lspPosition{Line: 2, Character: 7})
	if err != nil {
		t.Fatalf("lspPositionByteOffset() end error = %v", err)
	}
	if got != start+len([]byte("建立")) {
		t.Fatalf("identifier end = %d, want %d", got, start+len([]byte("建立")))
	}

	emojiLineStart := bytes.Index(source, []byte("// 中文"))
	emojiEnd := emojiLineStart + len([]byte("// 中文😀"))
	got, err = lspPositionByteOffset(source, lspPosition{Line: 1, Character: 7})
	if err != nil {
		t.Fatalf("emoji end error = %v", err)
	}
	if got != emojiEnd {
		t.Fatalf("emoji end = %d, want %d", got, emojiEnd)
	}
	if _, err := lspPositionByteOffset(source, lspPosition{Line: 1, Character: 6}); err == nil {
		t.Fatalf("position inside emoji surrogate pair succeeded")
	}
}

func TestMaterializeGoplsDeclarationsMatchesGroundedParserDeclarations(t *testing.T) {
	source := []byte(strings.Join([]string{
		"package refund",
		"",
		"type RefundService struct{}",
		"",
		"const retry = 3",
		"var limit = 7",
		"func Build() {}",
		"func (s *RefundService) Create() {}",
		"",
	}, "\n"))
	symbols := []lspDocumentSymbol{
		testDocumentSymbol(t, source, "RefundService", 23),
		testDocumentSymbol(t, source, "retry", 14),
		testDocumentSymbol(t, source, "limit", 13),
		testDocumentSymbol(t, source, "Build", 12),
		testDocumentSymbol(t, source, "Create", 6),
	}

	output, err := materializeGoplsDeclarations(GoParserFileInput{
		RepoID:    "repo",
		CommitSHA: "commit",
		Path:      "refund/service.go",
		Source:    source,
	}, symbols)
	if err != nil {
		t.Fatalf("materializeGoplsDeclarations() error = %v", err)
	}
	if got := len(output.Proposals); got != 5 {
		t.Fatalf("len(proposals) = %d, want 5", got)
	}
	wantStatements := []string{
		"Go type refund.RefundService is declared in refund/service.go:3.",
		"Go const refund.retry is declared in refund/service.go:5.",
		"Go var refund.limit is declared in refund/service.go:6.",
		"Go function refund.Build is declared in refund/service.go:7.",
		"Go method refund.RefundService.Create is declared in refund/service.go:8.",
	}
	gotStatements := make([]string, 0, len(output.Proposals))
	for _, proposal := range output.Proposals {
		gotStatements = append(gotStatements, proposal.StatementText)
	}
	if !reflect.DeepEqual(gotStatements, wantStatements) {
		t.Fatalf("statements = %#v, want %#v", gotStatements, wantStatements)
	}
	if output.Proposals[4].CodeFact == nil || output.Proposals[4].CodeFact.QualifiedName != "refund.RefundService.Create" {
		t.Fatalf("method code fact = %+v", output.Proposals[4].CodeFact)
	}
}

func TestMaterializeGoplsDeclarationsIgnoresBlankIdentifierDeclaration(t *testing.T) {
	source := []byte(strings.Join([]string{
		"package sample",
		"",
		"type Runner interface{}",
		"type implementation struct{}",
		"var _ Runner = (*implementation)(nil)",
		"",
	}, "\n"))
	symbols := []lspDocumentSymbol{
		testDocumentSymbol(t, source, "Runner", 23),
		testDocumentSymbol(t, source, "implementation", 23),
	}

	output, err := materializeGoplsDeclarations(GoParserFileInput{
		RepoID:    "repo",
		CommitSHA: "commit",
		Path:      "sample.go",
		Source:    source,
	}, symbols)
	if err != nil {
		t.Fatalf("materializeGoplsDeclarations() error = %v", err)
	}
	if len(output.Proposals) != 2 {
		t.Fatalf("blank-identifier proposals = %+v, want two addressable declarations", output.Proposals)
	}
	for _, proposal := range output.Proposals {
		if proposal.CodeFact == nil || proposal.CodeFact.QualifiedName == "sample._" {
			t.Fatalf("blank identifier escaped gopls grounding: %+v", proposal)
		}
	}
}

func TestMaterializeGoplsDeclarationsRejectsUngroundedSymbol(t *testing.T) {
	source := []byte("package sample\nfunc Build() {}\n")
	symbol := testDocumentSymbol(t, source, "Build", 12)
	symbol.SelectionRange.Start.Character++

	_, err := materializeGoplsDeclarations(GoParserFileInput{
		RepoID:    "repo",
		CommitSHA: "commit",
		Path:      "build.go",
		Source:    source,
	}, []lspDocumentSymbol{symbol})
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestMaterializeGoplsDeclarationsIgnoresGroundedInterfaceMethods(t *testing.T) {
	source := []byte(strings.Join([]string{
		"package sample",
		"",
		"type actionPlan interface {",
		"\tcanPatch() bool",
		"}",
		"",
		"type patchPlan struct{}",
		"",
		"func (p *patchPlan) applyPatch() bool { return true }",
		"",
	}, "\n"))
	interfaceSymbol := testDocumentSymbol(t, source, "actionPlan", 23)
	interfaceSymbol.Children = []lspDocumentSymbol{
		testDocumentSymbol(t, source, "canPatch", 6),
	}
	symbols := []lspDocumentSymbol{
		interfaceSymbol,
		testDocumentSymbol(t, source, "patchPlan", 23),
		testDocumentSymbol(t, source, "applyPatch", 6),
	}

	output, err := materializeGoplsDeclarations(GoParserFileInput{
		RepoID:    "repo",
		CommitSHA: "commit",
		Path:      "plan.go",
		Source:    source,
	}, symbols)
	if err != nil {
		t.Fatalf("materializeGoplsDeclarations() error = %v", err)
	}
	if got := len(output.Proposals); got != 3 {
		t.Fatalf("len(proposals) = %d, want 3", got)
	}
	for _, proposal := range output.Proposals {
		if strings.Contains(proposal.StatementText, "canPatch") {
			t.Fatalf("interface method was promoted to a code fact: %+v", proposal)
		}
	}
}

func TestMaterializeGoplsDeclarationsUsesParserKindForNamedFunctionType(t *testing.T) {
	source := []byte(strings.Join([]string{
		"package sample",
		"",
		"type roundTripFunc func() error",
		"",
	}, "\n"))
	symbol := testDocumentSymbol(t, source, "roundTripFunc", 12)

	output, err := materializeGoplsDeclarations(GoParserFileInput{
		RepoID:    "repo",
		CommitSHA: "commit",
		Path:      "transport.go",
		Source:    source,
	}, []lspDocumentSymbol{symbol})
	if err != nil {
		t.Fatalf("materializeGoplsDeclarations() error = %v", err)
	}
	if len(output.Proposals) != 1 ||
		output.Proposals[0].CodeFact == nil ||
		output.Proposals[0].CodeFact.SymbolKind != "type" {
		t.Fatalf("named function type proposal = %+v", output.Proposals)
	}
}

func TestLSPClientHandlesWorkspaceConfigurationRequest(t *testing.T) {
	responses := append(
		lspTestFrame(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      91,
			"method":  "workspace/configuration",
			"params": map[string]any{
				"items": []map[string]string{{"section": "gopls"}},
			},
		}),
		lspTestFrame(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"capabilities": map[string]string{"positionEncoding": "utf-16"}},
		})...,
	)
	var written bytes.Buffer
	client := &lspClient{
		reader: textproto.NewReader(bufio.NewReader(bytes.NewReader(responses))),
		writer: &written,
		workspaceFolder: lspWorkspaceFolder{
			URI:  "file:///workspace",
			Name: "workspace",
		},
	}
	var result struct {
		Capabilities struct {
			PositionEncoding string `json:"positionEncoding"`
		} `json:"capabilities"`
	}
	if err := client.call("initialize", map[string]any{}, &result); err != nil {
		t.Fatalf("call() error = %v", err)
	}
	if result.Capabilities.PositionEncoding != "utf-16" {
		t.Fatalf("position encoding = %q", result.Capabilities.PositionEncoding)
	}
	if !bytes.Contains(written.Bytes(), []byte(`"id":91`)) || !bytes.Contains(written.Bytes(), []byte(`"result":[null]`)) {
		t.Fatalf("workspace/configuration response missing from %q", written.String())
	}
}

func TestLSPClientCallObserverReportsSuccessAndFailure(t *testing.T) {
	tests := []struct {
		name      string
		response  map[string]any
		wantError bool
	}{
		{
			name: "success",
			response: map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  map[string]any{},
			},
		},
		{
			name: "failure",
			response: map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"error": map[string]any{
					"code":    -32603,
					"message": "injected",
				},
			},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var written bytes.Buffer
			client := &lspClient{
				reader: textproto.NewReader(bufio.NewReader(bytes.NewReader(
					lspTestFrame(t, tt.response),
				))),
				writer: &written,
			}
			var observedMethod string
			var observedDuration time.Duration
			var observedSuccess bool
			var observationCount int
			client.callObserver = func(method string, duration time.Duration, succeeded bool) {
				observationCount++
				observedMethod = method
				observedDuration = duration
				observedSuccess = succeeded
			}

			err := client.call("test/method", map[string]any{}, nil)
			if (err != nil) != tt.wantError {
				t.Fatalf("call() error = %v, want_error %t", err, tt.wantError)
			}
			if observationCount != 1 ||
				observedMethod != "test/method" ||
				observedDuration <= 0 ||
				observedSuccess == tt.wantError {
				t.Fatalf(
					"call observation = count %d method %q duration %s success %t",
					observationCount,
					observedMethod,
					observedDuration,
					observedSuccess,
				)
			}
		})
	}
}

func TestNormalizeGoplsPackagesIsDeterministic(t *testing.T) {
	raw := []goplsPackage{
		{Path: "example.com/workspace/z", ModulePath: "example.com/workspace"},
		{Path: "example.com/workspace/a", ModulePath: "example.com/workspace"},
		{Path: "example.com/workspace/a", ModulePath: "example.com/workspace"},
	}

	got, err := normalizeGoplsPackages(raw)
	if err != nil {
		t.Fatalf("normalizeGoplsPackages() error = %v", err)
	}
	want := []GoplsWorkspacePackage{
		{Path: "example.com/workspace/a", ModulePath: "example.com/workspace"},
		{Path: "example.com/workspace/z", ModulePath: "example.com/workspace"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %#v, want %#v", got, want)
	}
}

func TestCollectGoplsWorkspaceFilesUsesBoundedModuleRoots(t *testing.T) {
	root := t.TempDir()
	writeGoplsUnitFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/workspace\n"))
	mainSource := []byte("package workspace\n")
	writeGoplsUnitFile(t, filepath.Join(root, "main.go"), mainSource)
	workerSource := []byte("package worker\n")
	writeGoplsUnitFile(t, filepath.Join(root, "internal", "worker", "worker.go"), workerSource)
	writeGoplsUnitFile(t, filepath.Join(root, "vendor", "ignored.go"), []byte("package ignored\n"))
	writeGoplsUnitFile(t, filepath.Join(root, "testdata", "ignored.go"), []byte("package ignored\n"))
	writeGoplsUnitFile(t, filepath.Join(root, ".hidden", "ignored.go"), []byte("package ignored\n"))
	writeGoplsUnitFile(t, filepath.Join(root, "_generated", "ignored.go"), []byte("package ignored\n"))
	writeGoplsUnitFile(t, filepath.Join(root, "nested", "go.mod"), []byte("module example.com/nested\n"))
	writeGoplsUnitFile(t, filepath.Join(root, "nested", "ignored.go"), []byte("package nested\n"))

	files, err := collectGoplsWorkspaceFiles(root, []string{root})
	if err != nil {
		t.Fatalf("collectGoplsWorkspaceFiles() error = %v", err)
	}
	want := []GoplsWorkspaceFile{
		{Path: "internal/worker/worker.go", ContentHash: contentHash(workerSource), SizeBytes: int64(len(workerSource))},
		{Path: "main.go", ContentHash: contentHash(mainSource), SizeBytes: int64(len(mainSource))},
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
}

func TestNormalizeGoplsModulesRejectsGoModOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "go.mod")
	writeGoplsUnitFile(t, outside, []byte("module example.com/outside\n"))

	_, _, err := normalizeGoplsModules(root, map[string]goplsModule{
		"example.com/outside": {
			Path:  "example.com/outside",
			GoMod: fileURI(outside),
		},
	})
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestLSPPositionForByteOffsetRoundTripsUTF16(t *testing.T) {
	source := []byte("package sample\n\nvar 𐐀Value = 1\n")
	for _, offset := range []int{0, bytes.Index(source, []byte("𐐀")), bytes.Index(source, []byte("Value")), len(source)} {
		position, err := lspPositionForByteOffset(source, offset)
		if err != nil {
			t.Fatalf("lspPositionForByteOffset(%d) error = %v", offset, err)
		}
		got, err := lspPositionByteOffset(source, position)
		if err != nil {
			t.Fatalf("lspPositionByteOffset(%+v) error = %v", position, err)
		}
		if got != offset {
			t.Fatalf("round trip offset = %d, want %d", got, offset)
		}
	}
}

func TestLSPDefinitionResultAcceptsLocationsAndLocationLinks(t *testing.T) {
	data := []byte(`[
		{"uri":"file:///workspace/target.go","range":{"start":{"line":2,"character":5},"end":{"line":2,"character":11}}},
		{"targetUri":"file:///workspace/linked.go","targetSelectionRange":{"start":{"line":4,"character":2},"end":{"line":4,"character":8}}}
	]`)
	var result lspDefinitionResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(result) != 2 || result[0].URI != "file:///workspace/target.go" || result[1].URI != "file:///workspace/linked.go" {
		t.Fatalf("definition result = %+v", result)
	}
	if result[1].Range.Start.Line != 4 || result[1].Range.End.Character != 8 {
		t.Fatalf("definition link range = %+v", result[1].Range)
	}
}

func testDocumentSymbol(t *testing.T, source []byte, name string, kind int) lspDocumentSymbol {
	t.Helper()
	start := bytes.Index(source, []byte(name))
	if start < 0 {
		t.Fatalf("name %q missing from source", name)
	}
	return lspDocumentSymbol{
		Name: name,
		Kind: kind,
		SelectionRange: lspRange{
			Start: testLSPPosition(source, start),
			End:   testLSPPosition(source, start+len([]byte(name))),
		},
	}
}

func testLSPPosition(source []byte, offset int) lspPosition {
	line := bytes.Count(source[:offset], []byte{'\n'})
	lineStart := bytes.LastIndexByte(source[:offset], '\n') + 1
	units := 0
	for len(source[lineStart:offset]) > 0 {
		r, size := utf8.DecodeRune(source[lineStart:offset])
		if r > 0xffff {
			units += 2
		} else {
			units++
		}
		lineStart += size
	}
	return lspPosition{Line: line, Character: units}
}

func lspTestFrame(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return append([]byte("Content-Length: "+strconv.Itoa(len(data))+"\r\n\r\n"), data...)
}

func writeGoplsUnitFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}
