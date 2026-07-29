package evidenceingestion

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInspectGitRepositoryChangeTracksHeadAndDirtyContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	writeGitChangeTestFile(t, filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"))
	writeGitChangeTestFile(t, filepath.Join(root, "main.go"), []byte("package main\n"))
	runGitChangeTestCommand(t, root, "init", "--quiet")
	runGitChangeTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitChangeTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitChangeTestCommand(t, root, "add", ".")
	runGitChangeTestCommand(t, root, "commit", "--quiet", "-m", "initial")

	clean, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(clean) error = %v", err)
	}
	if clean.TokenContract != GitRepositoryChangeTokenV1 || clean.DirtyFingerprintContract != GitRepositoryDirtyFingerprintV1 || !isCanonicalGitObjectID(clean.HeadCommitSHA) || clean.Dirty || clean.DirtyFingerprint != "" || clean.TrackedChangeCount != 0 || clean.UntrackedFileCount != 0 || clean.ChangeToken == "" {
		t.Fatalf("clean inspection = %+v", clean)
	}
	replay, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(clean replay) error = %v", err)
	}
	if replay != clean {
		t.Fatalf("clean replay = %+v, want %+v", replay, clean)
	}

	writeGitChangeTestFile(t, filepath.Join(root, "main.go"), []byte("package main\n\nfunc First() {}\n"))
	tracked, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(tracked) error = %v", err)
	}
	if !tracked.Dirty || tracked.TrackedChangeCount != 1 || tracked.UntrackedFileCount != 0 || tracked.DirtyFingerprint == "" || tracked.ChangeToken == clean.ChangeToken {
		t.Fatalf("tracked inspection = %+v", tracked)
	}
	writeGitChangeTestFile(t, filepath.Join(root, "main.go"), []byte("package main\n\nfunc Second() {}\n"))
	trackedChanged, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(tracked changed) error = %v", err)
	}
	if trackedChanged.DirtyFingerprint == tracked.DirtyFingerprint || trackedChanged.ChangeToken == tracked.ChangeToken {
		t.Fatalf("tracked content change did not change token: before %+v after %+v", tracked, trackedChanged)
	}

	writeGitChangeTestFile(t, filepath.Join(root, "notes.txt"), []byte("one\n"))
	untracked, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(untracked) error = %v", err)
	}
	if !untracked.Dirty || untracked.TrackedChangeCount != 1 || untracked.UntrackedFileCount != 1 || untracked.DirtyFingerprint == trackedChanged.DirtyFingerprint {
		t.Fatalf("untracked inspection = %+v", untracked)
	}
	writeGitChangeTestFile(t, filepath.Join(root, "notes.txt"), []byte("two\n"))
	untrackedChanged, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(untracked changed) error = %v", err)
	}
	if untrackedChanged.DirtyFingerprint == untracked.DirtyFingerprint {
		t.Fatalf("untracked content change did not change fingerprint: before %+v after %+v", untracked, untrackedChanged)
	}

	writeGitChangeTestFile(t, filepath.Join(root, "ignored.txt"), []byte("ignored\n"))
	ignored, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(ignored) error = %v", err)
	}
	if ignored != untrackedChanged {
		t.Fatalf("ignored file changed inspection: before %+v after %+v", untrackedChanged, ignored)
	}

	runGitChangeTestCommand(t, root, "add", "main.go", "notes.txt")
	runGitChangeTestCommand(t, root, "commit", "--quiet", "-m", "second")
	secondClean, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: root, RepoID: "change-test"})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange(second clean) error = %v", err)
	}
	if secondClean.Dirty || secondClean.HeadCommitSHA == clean.HeadCommitSHA || secondClean.ChangeToken == clean.ChangeToken {
		t.Fatalf("second clean inspection = %+v, first = %+v", secondClean, clean)
	}
}

func TestInspectGitRepositoryChangeValidatesBoundary(t *testing.T) {
	_, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{})
	assertKind(t, err, ErrorInvalidInput)

	if _, lookErr := exec.LookPath("git"); lookErr != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	runGitChangeTestCommand(t, root, "init", "--quiet")
	runGitChangeTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitChangeTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	writeGitChangeTestFile(t, filepath.Join(root, "main.go"), []byte("package main\n"))
	runGitChangeTestCommand(t, root, "add", ".")
	runGitChangeTestCommand(t, root, "commit", "--quiet", "-m", "initial")
	subdir := filepath.Join(root, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	_, err = InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{WorkspaceRoot: subdir, RepoID: "change-test"})
	assertKind(t, err, ErrorInvalidInput)
}

func TestParseGitPorcelainV2RejectsMalformedInput(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("? file.go"),
		[]byte("# unsupported\x00"),
		[]byte("2 R. N... 100644 100644 100644 a b R100 renamed.go\x00"),
		[]byte("? ../outside.go\x00"),
	} {
		_, _, err := parseGitPorcelainV2(data)
		if err == nil {
			t.Fatalf("parseGitPorcelainV2(%q) error = nil", data)
		}
	}
}

func TestParseGitPorcelainV2CountsAndOrdersEntries(t *testing.T) {
	data := []byte("1 changed\x002 renamed\x00old-name\x00? z.txt\x00? a.txt\x00")
	tracked, untracked, err := parseGitPorcelainV2(data)
	if err != nil {
		t.Fatalf("parseGitPorcelainV2() error = %v", err)
	}
	if tracked != 2 || len(untracked) != 2 || untracked[0] != "a.txt" || untracked[1] != "z.txt" {
		t.Fatalf("parseGitPorcelainV2() = tracked %d untracked %v", tracked, untracked)
	}
}

func writeGitChangeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGitChangeTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
