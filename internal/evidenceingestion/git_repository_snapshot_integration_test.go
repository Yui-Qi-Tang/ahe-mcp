//go:build integration

package evidenceingestion

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationCaptureGitRepositorySnapshotUsesCommitObjects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	committedMain := []byte("package sample\n\nfunc Main() {}\n")
	committedWorker := []byte("package worker\n\ntype Worker struct{}\n")
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/snapshot\n\ngo 1.22\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), committedMain)
	writeGitSnapshotTestFile(t, filepath.Join(root, "internal", "worker", "worker.go"), committedWorker)
	writeGitSnapshotTestFile(t, filepath.Join(root, "README.md"), []byte("snapshot fixture\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")
	commitSHA := gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")

	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Dirty() {}\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "untracked.go"), []byte("package sample\n"))
	config := GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "snapshot-repo",
		CommitSHA:     commitSHA,
		RequestID:     "snapshot-request-1",
	}
	result, err := CaptureGitRepositorySnapshot(ctx, pool, config)
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("first capture replayed = true, want false")
	}
	if result.RepositorySnapshot.CommitSHA != commitSHA || result.RepositorySnapshot.ManifestEntryCount != 4 || result.RepositorySnapshot.SelectedFileCount != 2 {
		t.Fatalf("repository snapshot = %+v", result.RepositorySnapshot)
	}
	if result.RepositorySnapshot.RevisionVerificationMethod != RepositoryRevisionVerificationGitV1 || result.RepositorySnapshot.ManifestContract != RepositoryManifestGitTreeV1 {
		t.Fatalf("repository snapshot contracts = %+v", result.RepositorySnapshot)
	}
	if len(result.FileSnapshots) != 2 || result.FileSnapshots[0].Path != "internal/worker/worker.go" || result.FileSnapshots[1].Path != "main.go" {
		t.Fatalf("file snapshots = %+v", result.FileSnapshots)
	}
	if result.FileSnapshots[1].BlobHash != contentHash(committedMain) || result.FileSnapshots[1].ByteLength != len(committedMain) {
		t.Fatalf("main.go snapshot = %+v", result.FileSnapshots[1])
	}

	var persistedMain []byte
	if err := pool.QueryRow(ctx, `
		SELECT sb.raw_content
		FROM source_file_snapshots sfs
		JOIN source_blobs sb ON sb.raw_content_hash = sfs.blob_hash
		WHERE sfs.repository_snapshot_id = $1 AND sfs.path = 'main.go'
	`, result.RepositorySnapshot.ID).Scan(&persistedMain); err != nil {
		t.Fatalf("reading persisted main.go: %v", err)
	}
	if string(persistedMain) != string(committedMain) {
		t.Fatalf("persisted main.go = %q, want committed bytes %q", persistedMain, committedMain)
	}
	assertTableCount(t, ctx, pool, "repository_snapshots", 1)
	assertTableCount(t, ctx, pool, "source_file_snapshots", 2)
	assertTableCount(t, ctx, pool, "repository_snapshot_intake_requests", 1)

	extractorInput, err := BuildRepositoryExtractorInput(ctx, pool, result.RepositorySnapshot.ID)
	if err != nil {
		t.Fatalf("BuildRepositoryExtractorInput() error = %v", err)
	}
	if extractorInput.RepositorySnapshot != result.RepositorySnapshot {
		t.Fatalf("extractor input snapshot = %+v, want %+v", extractorInput.RepositorySnapshot, result.RepositorySnapshot)
	}
	if len(extractorInput.Files) != 2 || extractorInput.Files[0].FileSnapshot.Path != "internal/worker/worker.go" || extractorInput.Files[1].FileSnapshot.Path != "main.go" {
		t.Fatalf("extractor input files = %+v", extractorInput.Files)
	}
	if string(extractorInput.Files[0].Content) != string(committedWorker) || string(extractorInput.Files[1].Content) != string(committedMain) {
		t.Fatalf("extractor input did not preserve committed bytes: %+v", extractorInput.Files)
	}
	_, err = BuildRepositoryExtractorInput(ctx, pool, "bad-id")
	assertKind(t, err, ErrorInvalidRecordID)
	_, err = BuildRepositoryExtractorInput(ctx, pool, "repo-snapshot:missing")
	assertKind(t, err, ErrorMissingSourceViewAttempt)

	replay, err := CaptureGitRepositorySnapshot(ctx, pool, config)
	if err != nil {
		t.Fatalf("replay CaptureGitRepositorySnapshot() error = %v", err)
	}
	if !replay.Replayed || replay.RepositorySnapshot.ID != result.RepositorySnapshot.ID {
		t.Fatalf("replay = %+v", replay)
	}
	secondRequest := config
	secondRequest.RequestID = "snapshot-request-2"
	second, err := CaptureGitRepositorySnapshot(ctx, pool, secondRequest)
	if err != nil {
		t.Fatalf("second request CaptureGitRepositorySnapshot() error = %v", err)
	}
	if second.Replayed {
		t.Fatal("new request replayed = true, want false")
	}
	assertTableCount(t, ctx, pool, "repository_snapshot_intake_requests", 2)

	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Next() {}\n"))
	runGitSnapshotTestCommand(t, root, "add", "main.go")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "next")
	conflict := config
	conflict.CommitSHA = gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")
	_, err = CaptureGitRepositorySnapshot(ctx, pool, conflict)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	assertTableCount(t, ctx, pool, "repository_snapshots", 1)
	assertTableCount(t, ctx, pool, "source_file_snapshots", 2)

	if _, err := pool.Exec(ctx, `UPDATE source_blobs SET raw_content = 'corrupt' WHERE raw_content_hash = $1`, result.FileSnapshots[1].BlobHash); err != nil {
		t.Fatalf("corrupting source blob fixture: %v", err)
	}
	_, err = BuildRepositoryExtractorInput(ctx, pool, result.RepositorySnapshot.ID)
	assertKind(t, err, ErrorRepositorySnapshotIntegrity)
}

func TestIntegrationCaptureGitRepositorySnapshotRejectsAbbreviatedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")
	commitSHA := gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")

	_, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "snapshot-repo",
		CommitSHA:     commitSHA[:12],
		RequestID:     "abbreviated-commit",
	})
	assertKind(t, err, ErrorInvalidInput)
	assertTableCount(t, ctx, pool, "repository_snapshots", 0)
}

func writeGitSnapshotTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}

func runGitSnapshotTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v: %s", args, err, output)
	}
}

func gitSnapshotTestOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v error = %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
