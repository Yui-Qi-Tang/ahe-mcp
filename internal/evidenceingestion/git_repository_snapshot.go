package evidenceingestion

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultGitRepositorySnapshotTimeout = 2 * time.Minute
	maxGitSnapshotGoFiles               = 10_000
	maxGitSnapshotFileBytes             = 16 << 20
	maxGitSnapshotTotalBytes            = 256 << 20
)

// GitRepositorySnapshotConfig identifies one canonical Git commit for repository intake.
type GitRepositorySnapshotConfig struct {
	GitBinaryPath string
	WorkspaceRoot string
	RepoID        string
	CommitSHA     string
	RequestID     string
	Timeout       time.Duration
}

type gitTreeEntry struct {
	mode       string
	objectType string
	objectID   string
	path       string
}

type repositorySnapshotContext struct {
	snapshot           RepositorySnapshot
	files              []SourceFileSnapshot
	blobs              map[string][]byte
	requestID          string
	requestPayloadHash string
}

// CaptureGitRepositorySnapshot verifies and persists one Git revision and its tracked Go files.
func CaptureGitRepositorySnapshot(ctx context.Context, pool *pgxpool.Pool, config GitRepositorySnapshotConfig) (RepositorySnapshotCaptureResult, error) {
	if pool == nil {
		return RepositorySnapshotCaptureResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return captureGitRepositorySnapshot(ctx, pgxDB{pool: pool}, config)
}

func captureGitRepositorySnapshot(ctx context.Context, db sqlDB, config GitRepositorySnapshotConfig) (RepositorySnapshotCaptureResult, error) {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultGitRepositorySnapshotTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	snapshotCtx, err := buildGitRepositorySnapshotContext(runCtx, config)
	if err != nil {
		return RepositorySnapshotCaptureResult{}, err
	}
	changed, err := persistRepositorySnapshot(runCtx, db, snapshotCtx)
	if err != nil {
		return RepositorySnapshotCaptureResult{}, err
	}
	return RepositorySnapshotCaptureResult{
		RepositorySnapshot: snapshotCtx.snapshot,
		FileSnapshots:      append([]SourceFileSnapshot(nil), snapshotCtx.files...),
		Replayed:           !changed,
	}, nil
}

func buildGitRepositorySnapshotContext(ctx context.Context, config GitRepositorySnapshotConfig) (repositorySnapshotContext, error) {
	repoID := strings.TrimSpace(config.RepoID)
	if repoID == "" {
		return repositorySnapshotContext{}, newDomainError(ErrorInvalidInput, "git repository repo_id is required")
	}
	requestID := strings.TrimSpace(config.RequestID)
	if requestID == "" {
		return repositorySnapshotContext{}, newDomainError(ErrorInvalidInput, "git repository request_id is required")
	}
	commitSHA := strings.TrimSpace(config.CommitSHA)
	if !isCanonicalGitObjectID(commitSHA) {
		return repositorySnapshotContext{}, newDomainError(ErrorInvalidInput, "git repository commit_sha must be a lowercase full SHA")
	}
	workspaceRoot, err := canonicalDirectory(config.WorkspaceRoot)
	if err != nil {
		return repositorySnapshotContext{}, err
	}
	gitBinary := strings.TrimSpace(config.GitBinaryPath)
	if gitBinary == "" {
		gitBinary, err = exec.LookPath("git")
		if err != nil {
			return repositorySnapshotContext{}, newDomainError(ErrorInvalidInput, "git binary is required: %v", err)
		}
	}

	topLevelOutput, err := runGitOutput(ctx, gitBinary, workspaceRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return repositorySnapshotContext{}, newDomainError(ErrorInvalidInput, "workspace_root is not a Git worktree: %v", err)
	}
	topLevel, err := canonicalDirectory(strings.TrimSpace(string(topLevelOutput)))
	if err != nil {
		return repositorySnapshotContext{}, err
	}
	if topLevel != workspaceRoot {
		return repositorySnapshotContext{}, newDomainError(ErrorInvalidInput, "workspace_root %q is not Git top-level %q", workspaceRoot, topLevel)
	}

	verifiedOutput, err := runGitOutput(ctx, gitBinary, workspaceRoot, "rev-parse", "--verify", commitSHA+"^{commit}")
	if err != nil {
		return repositorySnapshotContext{}, newDomainError(ErrorInvalidInput, "verifying git commit %s: %v", commitSHA, err)
	}
	verifiedCommit := strings.TrimSpace(string(verifiedOutput))
	if verifiedCommit != commitSHA {
		return repositorySnapshotContext{}, newDomainError(ErrorRepositoryRevisionMismatch, "git resolved commit %s, want %s", verifiedCommit, commitSHA)
	}

	manifest, err := runGitOutput(ctx, gitBinary, workspaceRoot, "ls-tree", "-r", "-z", "--full-tree", verifiedCommit)
	if err != nil {
		return repositorySnapshotContext{}, fmt.Errorf("reading Git tree for %s: %w", verifiedCommit, err)
	}
	entries, err := parseGitTree(manifest)
	if err != nil {
		return repositorySnapshotContext{}, err
	}
	selected, err := selectTrackedGoFiles(entries)
	if err != nil {
		return repositorySnapshotContext{}, err
	}
	blobsByOID, err := readGitBlobs(ctx, gitBinary, workspaceRoot, selected)
	if err != nil {
		return repositorySnapshotContext{}, err
	}

	manifestHash := contentHash(manifest)
	snapshotID, err := repositorySnapshotID(repoID, verifiedCommit, manifestHash)
	if err != nil {
		return repositorySnapshotContext{}, err
	}
	snapshot := RepositorySnapshot{
		ID:                         snapshotID,
		RepoID:                     repoID,
		CommitSHA:                  verifiedCommit,
		ManifestHash:               manifestHash,
		ManifestEntryCount:         len(entries),
		RevisionVerificationMethod: RepositoryRevisionVerificationGitV1,
		ManifestContract:           RepositoryManifestGitTreeV1,
		FileSelectionContract:      RepositoryFileSelectionTrackedGoV1,
		SelectedFileCount:          len(selected),
	}
	files := make([]SourceFileSnapshot, 0, len(selected))
	blobs := make(map[string][]byte, len(selected))
	for _, entry := range selected {
		content := blobsByOID[entry.objectID]
		blobHash := contentHash(content)
		fileID, err := repositoryFileSnapshotID(repoID, verifiedCommit, entry.path, blobHash)
		if err != nil {
			return repositorySnapshotContext{}, err
		}
		files = append(files, SourceFileSnapshot{
			ID:                   fileID,
			RepositorySnapshotID: snapshotID,
			RepoID:               repoID,
			CommitSHA:            verifiedCommit,
			Path:                 entry.path,
			BlobHash:             blobHash,
			GitBlobOID:           entry.objectID,
			ByteLength:           len(content),
		})
		if _, ok := blobs[blobHash]; !ok {
			blobs[blobHash] = append([]byte(nil), content...)
		}
	}
	requestData, err := deterministicJSON(struct {
		RepoID                string `json:"repo_id"`
		CommitSHA             string `json:"commit_sha"`
		ManifestContract      string `json:"manifest_contract"`
		FileSelectionContract string `json:"file_selection_contract"`
	}{
		RepoID:                repoID,
		CommitSHA:             verifiedCommit,
		ManifestContract:      RepositoryManifestGitTreeV1,
		FileSelectionContract: RepositoryFileSelectionTrackedGoV1,
	})
	if err != nil {
		return repositorySnapshotContext{}, err
	}
	return repositorySnapshotContext{
		snapshot:           snapshot,
		files:              files,
		blobs:              blobs,
		requestID:          requestID,
		requestPayloadHash: contentHash(requestData),
	}, nil
}

func canonicalDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", newDomainError(ErrorInvalidInput, "workspace_root is required")
	}
	root, err := filepath.Abs(value)
	if err != nil {
		return "", newDomainError(ErrorInvalidInput, "resolving workspace_root: %v", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", newDomainError(ErrorInvalidInput, "resolving workspace_root symlinks: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", newDomainError(ErrorInvalidInput, "reading workspace_root: %v", err)
	}
	if !info.IsDir() {
		return "", newDomainError(ErrorInvalidInput, "workspace_root %q is not a directory", root)
	}
	return root, nil
}

func runGitOutput(ctx context.Context, binaryPath, workspaceRoot string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", workspaceRoot}, args...)
	cmd := exec.CommandContext(ctx, binaryPath, commandArgs...)
	cmd.Dir = workspaceRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return stdout.Bytes(), nil
}

func parseGitTree(data []byte) ([]gitTreeEntry, error) {
	if len(data) > 0 && data[len(data)-1] != 0 {
		return nil, newDomainError(ErrorInvalidInput, "Git tree listing is not NUL terminated")
	}
	records := bytes.Split(data, []byte{0})
	entries := make([]gitTreeEntry, 0, len(records))
	for idx, record := range records {
		if len(record) == 0 {
			if idx != len(records)-1 {
				return nil, newDomainError(ErrorInvalidInput, "Git tree listing contains an empty entry")
			}
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, newDomainError(ErrorInvalidInput, "Git tree entry has no path separator")
		}
		header := strings.Fields(string(parts[0]))
		if len(header) != 3 {
			return nil, newDomainError(ErrorInvalidInput, "Git tree entry header is malformed")
		}
		entries = append(entries, gitTreeEntry{
			mode:       header[0],
			objectType: header[1],
			objectID:   header[2],
			path:       string(parts[1]),
		})
	}
	return entries, nil
}

func selectTrackedGoFiles(entries []gitTreeEntry) ([]gitTreeEntry, error) {
	selected := make([]gitTreeEntry, 0)
	for _, entry := range entries {
		if entry.objectType != "blob" || (entry.mode != "100644" && entry.mode != "100755") || !strings.HasSuffix(entry.path, ".go") {
			continue
		}
		if !utf8.ValidString(entry.path) {
			return nil, newDomainError(ErrorInvalidUTF8, "tracked Go path is not valid UTF-8")
		}
		if entry.path == "" || pathpkg.IsAbs(entry.path) || pathpkg.Clean(entry.path) != entry.path || entry.path == ".." || strings.HasPrefix(entry.path, "../") {
			return nil, newDomainError(ErrorInvalidInput, "tracked Go path %q is not repository-relative", entry.path)
		}
		if !isCanonicalGitObjectID(entry.objectID) {
			return nil, newDomainError(ErrorInvalidInput, "tracked Go file %q has invalid Git blob object ID", entry.path)
		}
		selected = append(selected, entry)
		if len(selected) > maxGitSnapshotGoFiles {
			return nil, newDomainError(ErrorInvalidInput, "Git snapshot exceeds %d tracked Go files", maxGitSnapshotGoFiles)
		}
	}
	slices.SortFunc(selected, func(a, b gitTreeEntry) int {
		return strings.Compare(a.path, b.path)
	})
	return selected, nil
}

func readGitBlobs(ctx context.Context, binaryPath, workspaceRoot string, entries []gitTreeEntry) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if len(entries) == 0 {
		return result, nil
	}
	cmd := exec.CommandContext(ctx, binaryPath, "-C", workspaceRoot, "cat-file", "--batch")
	cmd.Dir = workspaceRoot
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening git cat-file stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening git cat-file stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting git cat-file: %w", err)
	}
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	totalBytes := int64(0)
	for _, entry := range entries {
		if _, ok := result[entry.objectID]; ok {
			continue
		}
		if _, err := fmt.Fprintln(stdin, entry.objectID); err != nil {
			return nil, fmt.Errorf("requesting Git blob %s: %w", entry.objectID, err)
		}
		headerLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading Git blob %s header: %w", entry.objectID, err)
		}
		header := strings.Fields(strings.TrimSpace(headerLine))
		if len(header) != 3 || header[0] != entry.objectID || header[1] != "blob" {
			return nil, newDomainError(ErrorInvalidInput, "Git blob %s returned malformed header %q", entry.objectID, strings.TrimSpace(headerLine))
		}
		size, err := strconv.ParseInt(header[2], 10, 64)
		if err != nil || size < 0 || size > maxGitSnapshotFileBytes {
			return nil, newDomainError(ErrorInvalidInput, "Git blob %s has unsupported size %q", entry.objectID, header[2])
		}
		totalBytes += size
		if totalBytes > maxGitSnapshotTotalBytes {
			return nil, newDomainError(ErrorInvalidInput, "Git snapshot exceeds %d bytes of tracked Go source", maxGitSnapshotTotalBytes)
		}
		content := make([]byte, int(size))
		if _, err := io.ReadFull(reader, content); err != nil {
			return nil, fmt.Errorf("reading Git blob %s body: %w", entry.objectID, err)
		}
		delimiter, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("reading Git blob %s delimiter: %w", entry.objectID, err)
		}
		if delimiter != '\n' {
			return nil, newDomainError(ErrorInvalidInput, "Git blob %s has invalid batch delimiter", entry.objectID)
		}
		result[entry.objectID] = content
	}
	if err := stdin.Close(); err != nil {
		return nil, fmt.Errorf("closing git cat-file stdin: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		waited = true
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("waiting for git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	waited = true
	return result, nil
}

func isCanonicalGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func repositorySnapshotID(repoID, commitSHA, manifestHash string) (string, error) {
	return stableID("repo-snapshot:", "repository_snapshot", struct {
		RepoID       string `json:"repo_id"`
		CommitSHA    string `json:"commit_sha"`
		ManifestHash string `json:"manifest_hash"`
	}{RepoID: repoID, CommitSHA: commitSHA, ManifestHash: manifestHash})
}

func repositoryFileSnapshotID(repoID, commitSHA, path, blobHash string) (string, error) {
	return stableID("file:", "source_file_snapshot", struct {
		RepoID    string `json:"repo_id"`
		CommitSHA string `json:"commit_sha"`
		Path      string `json:"path"`
		BlobHash  string `json:"blob_hash"`
	}{RepoID: repoID, CommitSHA: commitSHA, Path: path, BlobHash: blobHash})
}

func persistRepositorySnapshot(ctx context.Context, db sqlDB, snapshotCtx repositorySnapshotContext) (bool, error) {
	changed := false
	err := withTx(ctx, db, func(tx sqlTx) error {
		for blobHash, content := range snapshotCtx.blobs {
			inserted, err := persistRepositoryBlobTx(ctx, tx, blobHash, content)
			if err != nil {
				return err
			}
			changed = changed || inserted
		}
		inserted, err := persistRepositorySnapshotTx(ctx, tx, snapshotCtx.snapshot)
		if err != nil {
			return err
		}
		changed = changed || inserted
		for _, file := range snapshotCtx.files {
			inserted, err := persistSourceFileSnapshotTx(ctx, tx, file)
			if err != nil {
				return err
			}
			changed = changed || inserted
		}
		inserted, err = persistRepositorySnapshotRequestTx(ctx, tx, snapshotCtx)
		if err != nil {
			return err
		}
		changed = changed || inserted
		return nil
	})
	return changed, err
}

func persistRepositoryBlobTx(ctx context.Context, tx sqlTx, blobHash string, content []byte) (bool, error) {
	tag, err := tx.exec(ctx, `
		INSERT INTO source_blobs (raw_content_hash, raw_content, byte_length)
		VALUES ($1, $2, $3)
		ON CONFLICT (raw_content_hash) DO NOTHING
	`, blobHash, content, len(content))
	if err != nil {
		return false, fmt.Errorf("upserting repository source blob: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var existing []byte
	var byteLength int
	if err := tx.queryRow(ctx, `
		SELECT raw_content, byte_length
		FROM source_blobs
		WHERE raw_content_hash = $1
	`, blobHash).Scan(&existing, &byteLength); err != nil {
		return false, fmt.Errorf("reading repository source blob %s: %w", blobHash, err)
	}
	if byteLength != len(content) || !bytes.Equal(existing, content) {
		return false, newDomainError(ErrorOccurrenceConflict, "source blob %s maps to different bytes", blobHash)
	}
	return false, nil
}

func persistRepositorySnapshotTx(ctx context.Context, tx sqlTx, snapshot RepositorySnapshot) (bool, error) {
	tag, err := tx.exec(ctx, `
		INSERT INTO repository_snapshots (
			repository_snapshot_id,
			repo_id,
			commit_sha,
			manifest_hash,
			manifest_entry_count,
			revision_verification_method,
			manifest_contract,
			file_selection_contract,
			selected_file_count
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
	`,
		snapshot.ID,
		snapshot.RepoID,
		snapshot.CommitSHA,
		snapshot.ManifestHash,
		snapshot.ManifestEntryCount,
		snapshot.RevisionVerificationMethod,
		snapshot.ManifestContract,
		snapshot.FileSelectionContract,
		snapshot.SelectedFileCount,
	)
	if err != nil {
		return false, fmt.Errorf("upserting repository snapshot: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var existing RepositorySnapshot
	if err := tx.queryRow(ctx, `
		SELECT
			repository_snapshot_id,
			repo_id,
			commit_sha,
			manifest_hash,
			manifest_entry_count,
			revision_verification_method,
			manifest_contract,
			file_selection_contract,
			selected_file_count
		FROM repository_snapshots
		WHERE repo_id = $1 AND commit_sha = $2
	`, snapshot.RepoID, snapshot.CommitSHA).Scan(
		&existing.ID,
		&existing.RepoID,
		&existing.CommitSHA,
		&existing.ManifestHash,
		&existing.ManifestEntryCount,
		&existing.RevisionVerificationMethod,
		&existing.ManifestContract,
		&existing.FileSelectionContract,
		&existing.SelectedFileCount,
	); err != nil {
		return false, fmt.Errorf("reading repository snapshot %s/%s: %w", snapshot.RepoID, snapshot.CommitSHA, err)
	}
	if existing != snapshot {
		return false, newDomainError(ErrorOccurrenceConflict, "repository snapshot %s/%s maps to different material", snapshot.RepoID, snapshot.CommitSHA)
	}
	return false, nil
}

func persistSourceFileSnapshotTx(ctx context.Context, tx sqlTx, file SourceFileSnapshot) (bool, error) {
	tag, err := tx.exec(ctx, `
		INSERT INTO source_file_snapshots (
			file_snapshot_id,
			repository_snapshot_id,
			repo_id,
			commit_sha,
			path,
			blob_hash,
			git_blob_oid,
			byte_length
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
	`,
		file.ID,
		file.RepositorySnapshotID,
		file.RepoID,
		file.CommitSHA,
		file.Path,
		file.BlobHash,
		file.GitBlobOID,
		file.ByteLength,
	)
	if err != nil {
		return false, fmt.Errorf("upserting source file snapshot %s: %w", file.Path, err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var existing SourceFileSnapshot
	if err := tx.queryRow(ctx, `
		SELECT
			file_snapshot_id,
			repository_snapshot_id,
			repo_id,
			commit_sha,
			path,
			blob_hash,
			git_blob_oid,
			byte_length
		FROM source_file_snapshots
		WHERE repo_id = $1 AND commit_sha = $2 AND path = $3
	`, file.RepoID, file.CommitSHA, file.Path).Scan(
		&existing.ID,
		&existing.RepositorySnapshotID,
		&existing.RepoID,
		&existing.CommitSHA,
		&existing.Path,
		&existing.BlobHash,
		&existing.GitBlobOID,
		&existing.ByteLength,
	); err != nil {
		return false, fmt.Errorf("reading source file snapshot %s: %w", file.Path, err)
	}
	if existing != file {
		return false, newDomainError(ErrorOccurrenceConflict, "source file snapshot %s maps to different material", file.Path)
	}
	return false, nil
}

func persistRepositorySnapshotRequestTx(ctx context.Context, tx sqlTx, snapshotCtx repositorySnapshotContext) (bool, error) {
	tag, err := tx.exec(ctx, `
		INSERT INTO repository_snapshot_intake_requests (
			request_id,
			repository_snapshot_id,
			request_payload_hash
		)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id) DO NOTHING
	`, snapshotCtx.requestID, snapshotCtx.snapshot.ID, snapshotCtx.requestPayloadHash)
	if err != nil {
		return false, fmt.Errorf("upserting repository snapshot request: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var existingSnapshotID, existingPayloadHash string
	if err := tx.queryRow(ctx, `
		SELECT repository_snapshot_id, request_payload_hash
		FROM repository_snapshot_intake_requests
		WHERE request_id = $1
	`, snapshotCtx.requestID).Scan(&existingSnapshotID, &existingPayloadHash); err != nil {
		return false, fmt.Errorf("reading repository snapshot request %s: %w", snapshotCtx.requestID, err)
	}
	if existingSnapshotID != snapshotCtx.snapshot.ID || existingPayloadHash != snapshotCtx.requestPayloadHash {
		return false, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository snapshot payload", snapshotCtx.requestID)
	}
	return false, nil
}
