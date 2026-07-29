package detective

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestListSourceCapabilitiesReturnsClosedCopy(t *testing.T) {
	want := []SourceCapability{
		{
			Name:         SourceCapabilityGitGoRepository,
			Version:      SourceCapabilityGitGoRepositoryVersion,
			SourceSystem: evidenceingestion.SourceSystemCodeRepository,
		},
		{
			Name:         SourceCapabilityLocalPRDText,
			Version:      SourceCapabilityLocalPRDTextVersion,
			SourceSystem: evidenceingestion.SourceSystemManualText,
		},
		{
			Name:         SourceCapabilityMCPReadDocument,
			Version:      SourceCapabilityMCPReadDocumentVersion,
			SourceSystem: evidenceingestion.SourceSystemMCPReadDocument,
		},
	}
	got := ListSourceCapabilities()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSourceCapabilities() = %+v, want %+v", got, want)
	}
	got[0].Name = "changed"
	if next := ListSourceCapabilities(); !reflect.DeepEqual(next, want) {
		t.Fatalf("capability catalog was mutated through returned slice: %+v", next)
	}
}

func TestPrepareWorkspaceRegistrationAcceptsDistinctRemoteMCPBindings(t *testing.T) {
	request, err := prepareWorkspaceRegistration(context.Background(), WorkspaceRegistrationInput{
		RequestID:     "register-mcp-sources",
		WorkspaceID:   "workspace:mcp-sources",
		WorkspaceRoot: t.TempDir(),
		Sources: []WorkspaceSourceRegistrationInput{
			{
				CapabilityName:    SourceCapabilityMCPReadDocument,
				CapabilityVersion: SourceCapabilityMCPReadDocumentVersion,
				SourceID:          "atlassian:jira:AHE-42",
				RelativePath:      ".",
			},
			{
				CapabilityName:    SourceCapabilityMCPReadDocument,
				CapabilityVersion: SourceCapabilityMCPReadDocumentVersion,
				SourceID:          "atlassian:confluence:12345",
				RelativePath:      ".",
			},
		},
	})
	if err != nil {
		t.Fatalf("prepareWorkspaceRegistration() error = %v", err)
	}
	if len(request.workspace.Sources) != 2 {
		t.Fatalf("remote source count = %d, want 2", len(request.workspace.Sources))
	}
	for _, source := range request.workspace.Sources {
		if source.SourceSystem != evidenceingestion.SourceSystemMCPReadDocument ||
			source.RelativePath != "." ||
			source.PathKind != "remote" {
			t.Fatalf("remote source = %+v", source)
		}
	}
}

func TestPrepareWorkspaceRegistrationCanonicalizesIdentity(t *testing.T) {
	root := gitWorkspaceFixture(t)
	docs := filepath.Join(root, "docs")
	if err := os.Mkdir(docs, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(docs, "requirements.txt"), []byte("bounded evidence\n"), 0o600); err != nil {
		t.Fatalf("write requirements: %v", err)
	}
	input := WorkspaceRegistrationInput{
		RequestID:     " register-workspace ",
		WorkspaceID:   " workspace:primary ",
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{
			{
				CapabilityName:    SourceCapabilityLocalPRDText,
				CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceID:          " requirements ",
				RelativePath:      "docs/requirements.txt",
			},
			{
				CapabilityName:    SourceCapabilityGitGoRepository,
				CapabilityVersion: SourceCapabilityGitGoRepositoryVersion,
				SourceID:          "repo-primary",
				RelativePath:      ".",
			},
		},
	}
	request, err := prepareWorkspaceRegistration(context.Background(), input)
	if err != nil {
		t.Fatalf("prepareWorkspaceRegistration() error = %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize fixture root: %v", err)
	}
	if request.requestID != "register-workspace" || request.workspace.ID != "workspace:primary" {
		t.Fatalf("normalized request identity = %q/%q", request.requestID, request.workspace.ID)
	}
	if request.workspace.RootPath != canonicalRoot {
		t.Fatalf("workspace root = %q, want %q", request.workspace.RootPath, canonicalRoot)
	}
	if !strings.HasPrefix(request.requestPayloadHash, "sha256:") || len(request.requestPayloadHash) != 71 {
		t.Fatalf("payload hash = %q", request.requestPayloadHash)
	}
	if len(request.workspace.Sources) != 2 {
		t.Fatalf("source count = %d, want 2", len(request.workspace.Sources))
	}
	gitSource := request.workspace.Sources[0]
	textSource := request.workspace.Sources[1]
	if gitSource.CapabilityName != SourceCapabilityGitGoRepository || gitSource.SourceSystem != evidenceingestion.SourceSystemCodeRepository || gitSource.RelativePath != "." || gitSource.PathKind != "directory" {
		t.Fatalf("Git source = %+v", gitSource)
	}
	if textSource.CapabilityName != SourceCapabilityLocalPRDText || textSource.SourceSystem != evidenceingestion.SourceSystemManualText || textSource.SourceID != "requirements" || textSource.RelativePath != "docs/requirements.txt" || textSource.PathKind != "file" {
		t.Fatalf("text source = %+v", textSource)
	}
	for _, source := range request.workspace.Sources {
		if !strings.HasPrefix(source.ID, "workspace-source:") || source.WorkspaceID != request.workspace.ID {
			t.Fatalf("source identity = %+v", source)
		}
	}

	reversed := input
	reversed.Sources = slices.Clone(input.Sources)
	slices.Reverse(reversed.Sources)
	second, err := prepareWorkspaceRegistration(context.Background(), reversed)
	if err != nil {
		t.Fatalf("reversed prepareWorkspaceRegistration() error = %v", err)
	}
	if second.requestPayloadHash != request.requestPayloadHash || !reflect.DeepEqual(second.workspace.Sources, request.workspace.Sources) {
		t.Fatalf("registration identity depends on source input order")
	}
}

func TestPrepareWorkspaceRegistrationRejectsUnsafeOrUnsupportedSources(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.txt")
	secondPath := filepath.Join(root, "second.txt")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("source\n"), 0o600); err != nil {
			t.Fatalf("write source %s: %v", path, err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	base := WorkspaceRegistrationInput{
		RequestID:     "register-invalid",
		WorkspaceID:   "workspace:invalid",
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName:    SourceCapabilityLocalPRDText,
			CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
			SourceID:          "first",
			RelativePath:      "first.txt",
		}},
	}
	tests := []struct {
		name  string
		input WorkspaceRegistrationInput
		kind  ErrorKind
	}{
		{
			name: "unsupported capability",
			input: withWorkspaceSources(base, WorkspaceSourceRegistrationInput{
				CapabilityName:    "jira",
				CapabilityVersion: "v1",
				SourceID:          "issue",
				RelativePath:      "first.txt",
			}),
			kind: ErrorUnsupportedCapability,
		},
		{
			name: "unsupported version",
			input: withWorkspaceSources(base, WorkspaceSourceRegistrationInput{
				CapabilityName:    SourceCapabilityLocalPRDText,
				CapabilityVersion: "v2",
				SourceID:          "first",
				RelativePath:      "first.txt",
			}),
			kind: ErrorUnsupportedCapability,
		},
		{
			name: "mcp source uses local path",
			input: withWorkspaceSources(base, WorkspaceSourceRegistrationInput{
				CapabilityName:    SourceCapabilityMCPReadDocument,
				CapabilityVersion: SourceCapabilityMCPReadDocumentVersion,
				SourceID:          "atlassian:jira:AHE-42",
				RelativePath:      "first.txt",
			}),
			kind: ErrorInvalidInput,
		},
		{
			name: "absolute path",
			input: withWorkspaceSources(base, WorkspaceSourceRegistrationInput{
				CapabilityName:    SourceCapabilityLocalPRDText,
				CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceID:          "first",
				RelativePath:      firstPath,
			}),
			kind: ErrorInvalidInput,
		},
		{
			name: "parent traversal",
			input: withWorkspaceSources(base, WorkspaceSourceRegistrationInput{
				CapabilityName:    SourceCapabilityLocalPRDText,
				CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceID:          "first",
				RelativePath:      "folder/../first.txt",
			}),
			kind: ErrorInvalidInput,
		},
		{
			name: "symlink escape",
			input: withWorkspaceSources(base, WorkspaceSourceRegistrationInput{
				CapabilityName:    SourceCapabilityLocalPRDText,
				CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceID:          "outside",
				RelativePath:      "escape",
			}),
			kind: ErrorInvalidInput,
		},
		{
			name: "duplicate source ID",
			input: withWorkspaceSources(base,
				base.Sources[0],
				WorkspaceSourceRegistrationInput{
					CapabilityName:    SourceCapabilityLocalPRDText,
					CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
					SourceID:          "first",
					RelativePath:      "second.txt",
				},
			),
			kind: ErrorInvalidInput,
		},
		{
			name: "duplicate source path",
			input: withWorkspaceSources(base,
				base.Sources[0],
				WorkspaceSourceRegistrationInput{
					CapabilityName:    SourceCapabilityLocalPRDText,
					CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
					SourceID:          "second",
					RelativePath:      "first.txt",
				},
			),
			kind: ErrorInvalidInput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := prepareWorkspaceRegistration(context.Background(), test.input)
			assertDetectiveKind(t, err, test.kind)
		})
	}
}

func TestPrepareWorkspaceRegistrationRequiresGitTopLevel(t *testing.T) {
	root := gitWorkspaceFixture(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	_, err := prepareWorkspaceRegistration(context.Background(), WorkspaceRegistrationInput{
		RequestID:     "register-nested-git",
		WorkspaceID:   "workspace:nested-git",
		WorkspaceRoot: nested,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName:    SourceCapabilityGitGoRepository,
			CapabilityVersion: SourceCapabilityGitGoRepositoryVersion,
			SourceID:          "repo-nested",
			RelativePath:      ".",
		}},
	})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func TestValidateResolvedWorkspaceRejectsMissingSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "prd.txt")
	if err := os.WriteFile(path, []byte("requirements\n"), 0o600); err != nil {
		t.Fatalf("write PRD: %v", err)
	}
	request, err := prepareWorkspaceRegistration(context.Background(), WorkspaceRegistrationInput{
		RequestID:     "register-missing-source",
		WorkspaceID:   "workspace:missing-source",
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName:    SourceCapabilityLocalPRDText,
			CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
			SourceID:          "prd",
			RelativePath:      "prd.txt",
		}},
	})
	if err != nil {
		t.Fatalf("prepareWorkspaceRegistration() error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove PRD: %v", err)
	}
	err = validateResolvedWorkspace(context.Background(), request.workspace)
	assertDetectiveKind(t, err, ErrorWorkspaceUnavailable)
}

func TestValidateResolvedWorkspaceRejectsTamperedBindingIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "prd.txt"), []byte("requirements\n"), 0o600); err != nil {
		t.Fatalf("write PRD: %v", err)
	}
	request, err := prepareWorkspaceRegistration(context.Background(), WorkspaceRegistrationInput{
		RequestID:     "register-tampered-source",
		WorkspaceID:   "workspace:tampered-source",
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName:    SourceCapabilityLocalPRDText,
			CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
			SourceID:          "prd",
			RelativePath:      "prd.txt",
		}},
	})
	if err != nil {
		t.Fatalf("prepareWorkspaceRegistration() error = %v", err)
	}
	request.workspace.Sources[0].ID = "workspace-source:tampered"
	err = validateResolvedWorkspace(context.Background(), request.workspace)
	assertDetectiveKind(t, err, ErrorWorkspaceUnavailable)
}

func withWorkspaceSources(input WorkspaceRegistrationInput, sources ...WorkspaceSourceRegistrationInput) WorkspaceRegistrationInput {
	input.Sources = sources
	return input
}

func assertDetectiveKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", want)
	}
	got, ok := KindOf(err)
	if !ok || got != want {
		t.Fatalf("error = %v, kind = %q/%v, want %q", err, got, ok, want)
	}
}

func gitWorkspaceFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	commands := [][]string{
		{"init", "-q"},
		{"config", "user.email", "detective@example.test"},
		{"config", "user.name", "Detective Test"},
	}
	for _, args := range commands {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/detective\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	for _, args := range [][]string{{"add", "go.mod"}, {"commit", "-q", "-m", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	return root
}

func TestKindOfWrappedDomainError(t *testing.T) {
	err := errors.Join(errors.New("outer"), newDomainError(ErrorWorkspaceConflict, "conflict"))
	kind, ok := KindOf(err)
	if !ok || kind != ErrorWorkspaceConflict {
		t.Fatalf("KindOf() = %q/%v, want %q/true", kind, ok, ErrorWorkspaceConflict)
	}
}
