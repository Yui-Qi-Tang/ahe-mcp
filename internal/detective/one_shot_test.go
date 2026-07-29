package detective

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestPrepareOneShotOrchestrationBuildsStableBoundScope(t *testing.T) {
	sourceID := "workspace-source:" + strings.Repeat("a", 64)
	input := OneShotOrchestrationInput{
		RequestID: " collect ", WorkspaceID: " workspace:test ",
		SourceBindingID: " " + sourceID + " ", MaxSteps: 2,
	}
	first, err := prepareOneShotOrchestration(input)
	if err != nil {
		t.Fatalf("prepareOneShotOrchestration() error = %v", err)
	}
	second, err := prepareOneShotOrchestration(input)
	if err != nil {
		t.Fatalf("second prepareOneShotOrchestration() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) || first.requestID != "collect" || first.workspaceID != "workspace:test" || first.sourceBindingID != sourceID {
		t.Fatalf("prepared requests = %+v / %+v", first, second)
	}
	if _, err := normalizeOptionalContentHash(first.scopeHash, "scope_hash"); err != nil {
		t.Fatalf("scope hash = %q: %v", first.scopeHash, err)
	}

	changed := input
	changed.SourceBindingID = "workspace-source:" + strings.Repeat("b", 64)
	different, err := prepareOneShotOrchestration(changed)
	if err != nil {
		t.Fatalf("changed prepareOneShotOrchestration() error = %v", err)
	}
	if first.scopeHash == different.scopeHash {
		t.Fatal("different source bindings produced the same scope hash")
	}
}

func TestPrepareOneShotOrchestrationRejectsInvalidInput(t *testing.T) {
	valid := OneShotOrchestrationInput{
		RequestID: "collect", WorkspaceID: "workspace:test",
		SourceBindingID: "workspace-source:" + strings.Repeat("a", 64), MaxSteps: 1,
	}
	tests := []struct {
		name   string
		mutate func(*OneShotOrchestrationInput)
	}{
		{name: "empty request", mutate: func(input *OneShotOrchestrationInput) { input.RequestID = "" }},
		{name: "invalid workspace", mutate: func(input *OneShotOrchestrationInput) { input.WorkspaceID = "test" }},
		{name: "invalid source", mutate: func(input *OneShotOrchestrationInput) { input.SourceBindingID = "source" }},
		{name: "zero budget", mutate: func(input *OneShotOrchestrationInput) { input.MaxSteps = 0 }},
		{name: "excess budget", mutate: func(input *OneShotOrchestrationInput) { input.MaxSteps = maxOrchestrationSteps + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := prepareOneShotOrchestration(input)
			assertDetectiveKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestBindOneShotSourcePolicyBindsFixedGitPolicy(t *testing.T) {
	sourceID := "workspace-source:" + strings.Repeat("a", 64)
	request, err := prepareOneShotOrchestration(OneShotOrchestrationInput{
		RequestID: "collect", WorkspaceID: "workspace:test",
		SourceBindingID: sourceID, MaxSteps: 3,
	})
	if err != nil {
		t.Fatalf("prepareOneShotOrchestration() error = %v", err)
	}
	local, err := bindOneShotSourcePolicy(request, WorkspaceSourceBinding{
		ID: sourceID, WorkspaceID: request.workspaceID,
		CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
	})
	if err != nil {
		t.Fatalf("bind local policy error = %v", err)
	}
	if local.scopeHash != request.scopeHash {
		t.Fatalf("local scope hash changed from %q to %q", request.scopeHash, local.scopeHash)
	}

	gitSource := WorkspaceSourceBinding{
		ID: sourceID, WorkspaceID: request.workspaceID,
		CapabilityName: SourceCapabilityGitGoRepository, CapabilityVersion: SourceCapabilityGitGoRepositoryVersion,
	}
	first, err := bindOneShotSourcePolicy(request, gitSource)
	if err != nil {
		t.Fatalf("bind Git policy error = %v", err)
	}
	second, err := bindOneShotSourcePolicy(request, gitSource)
	if err != nil {
		t.Fatalf("second bind Git policy error = %v", err)
	}
	if first.scopeHash == request.scopeHash || first.scopeHash != second.scopeHash {
		t.Fatalf("Git scope hashes = %q / %q, local = %q", first.scopeHash, second.scopeHash, request.scopeHash)
	}
	if workerID := oneShotGitGoWorkerID(sourceID); !strings.HasPrefix(workerID, "detective-git-go:") || len(workerID) > 200 {
		t.Fatalf("Git worker ID = %q", workerID)
	}
}

func TestOneShotGitWorkerRequestIdentityAdvancesAfterRequeue(t *testing.T) {
	work := evidenceingestion.RepositoryExtractionWork{WorkItemID: "repo-work:test"}
	firstResolution := evidenceingestion.RepositoryExtractionWorkResolution{
		Work: work, State: evidenceingestion.RepositoryExtractionWorkStatePending,
	}
	firstAttempt, err := oneShotGitWorkerAttemptNumber(firstResolution)
	if err != nil {
		t.Fatalf("first oneShotGitWorkerAttemptNumber() error = %v", err)
	}
	if firstAttempt != 1 {
		t.Fatalf("first attempt = %d, want 1", firstAttempt)
	}
	legacyRequestID := oneShotChildRequestID("one-shot", "git-worker", 2)
	if got := oneShotGitWorkerRequestID("one-shot", 2, firstAttempt); got != legacyRequestID {
		t.Fatalf("first worker request ID = %q, want legacy %q", got, legacyRequestID)
	}

	requeuedResolution := evidenceingestion.RepositoryExtractionWorkResolution{
		Work: work, State: evidenceingestion.RepositoryExtractionWorkStatePending, LatestAttemptNumber: 1,
	}
	secondAttempt, err := oneShotGitWorkerAttemptNumber(requeuedResolution)
	if err != nil {
		t.Fatalf("second oneShotGitWorkerAttemptNumber() error = %v", err)
	}
	secondRequestID := oneShotGitWorkerRequestID("one-shot", 2, secondAttempt)
	if secondAttempt != 2 || secondRequestID == legacyRequestID ||
		secondRequestID != oneShotGitWorkerRequestID("one-shot", 2, secondAttempt) {
		t.Fatalf("second attempt identity = %d/%q, first = %q", secondAttempt, secondRequestID, legacyRequestID)
	}

	runningResolution := requeuedResolution
	runningResolution.State = evidenceingestion.RepositoryExtractionWorkStateRunning
	runningResolution.LatestAttemptNumber = 2
	runningAttempt, err := oneShotGitWorkerAttemptNumber(runningResolution)
	if err != nil {
		t.Fatalf("running oneShotGitWorkerAttemptNumber() error = %v", err)
	}
	if runningAttempt != secondAttempt || oneShotGitWorkerRequestID("one-shot", 2, runningAttempt) != secondRequestID {
		t.Fatalf("running attempt identity = %d/%q, want %d/%q", runningAttempt, oneShotGitWorkerRequestID("one-shot", 2, runningAttempt), secondAttempt, secondRequestID)
	}
}

func TestCollectLocalTextSourceBuildsSortedBoundManifest(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(filepath.Join(docs, "nested"), 0o700); err != nil {
		t.Fatalf("create nested docs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(docs, ".git"), 0o700); err != nil {
		t.Fatalf("create ignored Git directory: %v", err)
	}
	files := map[string]string{
		filepath.Join(docs, "z.txt"):               "zeta\n",
		filepath.Join(docs, "nested", "a.MD"):      "alpha\n",
		filepath.Join(docs, "ignored.go"):          "package ignored\n",
		filepath.Join(docs, ".git", "ignored.txt"): "not source\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	workspace := Workspace{ID: "workspace:test", RootPath: root}
	source := WorkspaceSourceBinding{
		ID: "workspace-source:" + strings.Repeat("a", 64), RelativePath: "docs", PathKind: "directory",
	}
	first, err := collectLocalTextSource(workspace, source)
	if err != nil {
		t.Fatalf("collectLocalTextSource() error = %v", err)
	}
	second, err := collectLocalTextSource(workspace, source)
	if err != nil {
		t.Fatalf("second collectLocalTextSource() error = %v", err)
	}
	wantPaths := []string{"docs/nested/a.MD", "docs/z.txt"}
	gotPaths := make([]string, 0, len(first.units))
	for _, unit := range first.units {
		gotPaths = append(gotPaths, unit.relativePath)
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) || first.token != second.token {
		t.Fatalf("manifest paths/token = %#v/%q, want %#v/stable", gotPaths, first.token, wantPaths)
	}

	if err := os.WriteFile(filepath.Join(docs, "z.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatalf("change selected source: %v", err)
	}
	changed, err := collectLocalTextSource(workspace, source)
	if err != nil {
		t.Fatalf("changed collectLocalTextSource() error = %v", err)
	}
	if changed.token == first.token {
		t.Fatal("changed source bytes did not change manifest token")
	}
}

func TestCollectLocalTextSourceRejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "requirements.txt")
	if err := os.WriteFile(path, []byte{0xff}, 0o600); err != nil {
		t.Fatalf("write invalid UTF-8 source: %v", err)
	}
	_, err := collectLocalTextSource(
		Workspace{ID: "workspace:test", RootPath: root},
		WorkspaceSourceBinding{
			ID: "workspace-source:" + strings.Repeat("a", 64), RelativePath: "requirements.txt", PathKind: "file",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("collectLocalTextSource() error = %v, want invalid UTF-8", err)
	}
}

func TestCollectLocalTextSourceRejectsExcessLineCount(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "requirements.txt")
	content := strings.Repeat("x\n", maxOneShotLocalTextFileLines+1)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write excessive-line source: %v", err)
	}
	_, err := collectLocalTextSource(
		Workspace{ID: "workspace:test", RootPath: root},
		WorkspaceSourceBinding{
			ID: "workspace-source:" + strings.Repeat("a", 64), RelativePath: "requirements.txt", PathKind: "file",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "non-empty lines") {
		t.Fatalf("collectLocalTextSource() error = %v, want line bound", err)
	}
}

func TestNonEmptyLocalTextLineCountMatchesSpanContract(t *testing.T) {
	content := []byte("first\r\n\r\nsecond\nthird")
	if got := nonEmptyLocalTextLineCount(content); got != 3 {
		t.Fatalf("nonEmptyLocalTextLineCount() = %d, want 3", got)
	}
}

func TestRunOneShotOrchestrationRequiresPool(t *testing.T) {
	if _, err := RunOneShotOrchestration(t.Context(), nil, OneShotOrchestrationInput{}); err == nil {
		t.Fatal("RunOneShotOrchestration(nil) error = nil")
	}
}
