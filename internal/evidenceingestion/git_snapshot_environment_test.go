package evidenceingestion

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineGitDisallowsTransportAndInheritedAuthority(t *testing.T) {
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	repo := t.TempDir()
	command := exec.Command(binary, "init", "-q", repo)
	command.Env = offlineGitEnvironment()
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	// file:// is deliberately used: this denial test never attempts a network connection.
	if _, err := runGitOutput(t.Context(), binary, repo, "-c", "protocol.file.allow=always", "fetch", "file://"+repo); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatal("transport allow override escaped closed Git environment")
	}
	t.Setenv("GIT_DIR", "/synthetic/invalid")
	if _, err := runGitOutput(t.Context(), binary, repo, "rev-parse", "--show-toplevel"); err != nil {
		t.Fatal("inherited GIT_DIR changed scope")
	}
	if strings.Contains(strings.Join(offlineGitEnvironment(), "\n"), "DATABASE_DSN") {
		t.Fatal("credential inherited")
	}
}
func TestOfflineGitIgnoresReplacementObjects(t *testing.T) {
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command(binary, append([]string{"-C", repo}, args...)...)
		cmd.Env = offlineGitEnvironment()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("synthetic Git failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	original := "package original\n"
	alternative := "package replaced\n"
	for _, f := range []struct{ name, text string }{{"original.go", original}, {"alternative.go", alternative}} {
		if err := os.WriteFile(filepath.Join(repo, f.name), []byte(f.text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oid := run("hash-object", "-w", "original.go")
	replacement := run("hash-object", "-w", "alternative.go")
	run("replace", oid, replacement)
	blobs, err := readGitBlobs(t.Context(), binary, repo, []gitTreeEntry{{objectID: oid}})
	if err != nil || string(blobs[oid]) != original {
		t.Fatal("replacement object contaminated immutable source")
	}
}
