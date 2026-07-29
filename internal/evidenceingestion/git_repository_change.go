package evidenceingestion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// GitRepositoryChangeTokenV1 binds one Git HEAD to a bounded dirty-worktree fingerprint.
	GitRepositoryChangeTokenV1 = "git-head-dirty-token-v1"
	// GitRepositoryDirtyFingerprintV1 hashes porcelain status, tracked diff bytes, and untracked file identities.
	GitRepositoryDirtyFingerprintV1 = "git-status-diff-untracked-v1"

	defaultGitRepositoryChangeTimeout = 30 * time.Second
	maxGitObservationOutputBytes      = 64 << 20
)

var errGitObservationOutputLimit = errors.New("Git observation output exceeds limit")

// GitRepositoryChangeConfig identifies one local Git worktree for read-only change inspection.
type GitRepositoryChangeConfig struct {
	GitBinaryPath string
	WorkspaceRoot string
	RepoID        string
	Timeout       time.Duration
}

// GitRepositoryChangeInspection is a scheduling hint, not persisted source authority.
type GitRepositoryChangeInspection struct {
	TokenContract            string `json:"token_contract"`
	DirtyFingerprintContract string `json:"dirty_fingerprint_contract"`
	RepoID                   string `json:"repo_id"`
	HeadCommitSHA            string `json:"head_commit_sha"`
	Dirty                    bool   `json:"dirty"`
	DirtyFingerprint         string `json:"dirty_fingerprint,omitempty"`
	TrackedChangeCount       int    `json:"tracked_change_count"`
	UntrackedFileCount       int    `json:"untracked_file_count"`
	ChangeToken              string `json:"change_token"`
}

type gitUntrackedIdentity struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	ContentHash string `json:"content_hash,omitempty"`
	ByteLength  int    `json:"byte_length"`
}

// InspectGitRepositoryChange returns a bounded Git HEAD and dirty-worktree token without persistence.
func InspectGitRepositoryChange(ctx context.Context, config GitRepositoryChangeConfig) (GitRepositoryChangeInspection, error) {
	repoID := strings.TrimSpace(config.RepoID)
	if repoID == "" {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "git repository repo_id is required")
	}
	workspaceRoot, err := canonicalDirectory(config.WorkspaceRoot)
	if err != nil {
		return GitRepositoryChangeInspection{}, err
	}
	gitBinary := strings.TrimSpace(config.GitBinaryPath)
	if gitBinary == "" {
		gitBinary, err = exec.LookPath("git")
		if err != nil {
			return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "git binary is required: %v", err)
		}
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultGitRepositoryChangeTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	topLevelOutput, err := runGitObservationOutput(runCtx, gitBinary, workspaceRoot, 1<<20, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "workspace_root is not a Git worktree: %v", err)
	}
	topLevel, err := canonicalDirectory(strings.TrimSpace(string(topLevelOutput)))
	if err != nil {
		return GitRepositoryChangeInspection{}, err
	}
	if topLevel != workspaceRoot {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "workspace_root %q is not Git top-level %q", workspaceRoot, topLevel)
	}

	headOutput, err := runGitObservationOutput(runCtx, gitBinary, workspaceRoot, 1<<20, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "resolving Git HEAD commit: %v", err)
	}
	headCommit := strings.TrimSpace(string(headOutput))
	if !isCanonicalGitObjectID(headCommit) {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "Git HEAD did not resolve to a lowercase full SHA")
	}

	status, err := runGitObservationOutput(runCtx, gitBinary, workspaceRoot, maxGitObservationOutputBytes, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return GitRepositoryChangeInspection{}, fmt.Errorf("reading Git worktree status: %w", err)
	}
	trackedChangeCount, untrackedPaths, err := parseGitPorcelainV2(status)
	if err != nil {
		return GitRepositoryChangeInspection{}, err
	}
	trackedDiff, err := runGitObservationOutput(runCtx, gitBinary, workspaceRoot, maxGitObservationOutputBytes,
		"diff",
		"--binary",
		"--full-index",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		"--no-renames",
		"--diff-algorithm=myers",
		"--no-indent-heuristic",
		"--unified=3",
		"--src-prefix=a/",
		"--dst-prefix=b/",
		"HEAD",
		"--",
	)
	if err != nil {
		return GitRepositoryChangeInspection{}, fmt.Errorf("reading Git tracked worktree diff: %w", err)
	}
	untracked, err := inspectGitUntrackedIdentities(workspaceRoot, untrackedPaths)
	if err != nil {
		return GitRepositoryChangeInspection{}, err
	}

	dirty := trackedChangeCount > 0 || len(untracked) > 0
	var dirtyFingerprint string
	if dirty {
		material, err := deterministicJSON(struct {
			Contract        string                 `json:"contract"`
			StatusHash      string                 `json:"status_hash"`
			TrackedDiffHash string                 `json:"tracked_diff_hash"`
			Untracked       []gitUntrackedIdentity `json:"untracked"`
		}{
			Contract:        GitRepositoryDirtyFingerprintV1,
			StatusHash:      contentHash(status),
			TrackedDiffHash: contentHash(trackedDiff),
			Untracked:       untracked,
		})
		if err != nil {
			return GitRepositoryChangeInspection{}, err
		}
		dirtyFingerprint = contentHash(material)
	}
	changeToken, err := gitRepositoryChangeToken(repoID, headCommit, dirtyFingerprint)
	if err != nil {
		return GitRepositoryChangeInspection{}, err
	}
	return GitRepositoryChangeInspection{
		TokenContract:            GitRepositoryChangeTokenV1,
		DirtyFingerprintContract: GitRepositoryDirtyFingerprintV1,
		RepoID:                   repoID,
		HeadCommitSHA:            headCommit,
		Dirty:                    dirty,
		DirtyFingerprint:         dirtyFingerprint,
		TrackedChangeCount:       trackedChangeCount,
		UntrackedFileCount:       len(untracked),
		ChangeToken:              changeToken,
	}, nil
}

func gitRepositoryChangeToken(repoID, headCommitSHA, dirtyFingerprint string) (string, error) {
	tokenMaterial, err := deterministicJSON(struct {
		Contract         string `json:"contract"`
		RepoID           string `json:"repo_id"`
		HeadCommitSHA    string `json:"head_commit_sha"`
		DirtyFingerprint string `json:"dirty_fingerprint,omitempty"`
	}{
		Contract:         GitRepositoryChangeTokenV1,
		RepoID:           repoID,
		HeadCommitSHA:    headCommitSHA,
		DirtyFingerprint: dirtyFingerprint,
	})
	if err != nil {
		return "", err
	}
	return contentHash(tokenMaterial), nil
}

func parseGitPorcelainV2(data []byte) (int, []string, error) {
	if len(data) > 0 && data[len(data)-1] != 0 {
		return 0, nil, newDomainError(ErrorInvalidInput, "Git status listing is not NUL terminated")
	}
	records := bytes.Split(data, []byte{0})
	trackedCount := 0
	untracked := make([]string, 0)
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) == 0 {
			if index != len(records)-1 {
				return 0, nil, newDomainError(ErrorInvalidInput, "Git status listing contains an empty entry")
			}
			continue
		}
		switch {
		case bytes.HasPrefix(record, []byte("1 ")):
			trackedCount++
		case bytes.HasPrefix(record, []byte("2 ")):
			trackedCount++
			index++
			if index >= len(records)-1 || len(records[index]) == 0 {
				return 0, nil, newDomainError(ErrorInvalidInput, "Git renamed status entry has no original path")
			}
		case bytes.HasPrefix(record, []byte("u ")):
			trackedCount++
		case bytes.HasPrefix(record, []byte("? ")):
			path := string(record[2:])
			if err := validateGitObservationPath(path); err != nil {
				return 0, nil, err
			}
			untracked = append(untracked, path)
		default:
			return 0, nil, newDomainError(ErrorInvalidInput, "Git status listing contains unsupported entry")
		}
	}
	if len(untracked) > maxGitSnapshotGoFiles {
		return 0, nil, newDomainError(ErrorInvalidInput, "Git observation exceeds %d untracked files", maxGitSnapshotGoFiles)
	}
	slices.Sort(untracked)
	return trackedCount, untracked, nil
}

func validateGitObservationPath(path string) error {
	if !utf8.ValidString(path) {
		return newDomainError(ErrorInvalidUTF8, "untracked Git path is not valid UTF-8")
	}
	if path == "" || pathpkg.IsAbs(path) || pathpkg.Clean(path) != path || path == ".." || strings.HasPrefix(path, "../") {
		return newDomainError(ErrorInvalidInput, "untracked Git path is not repository-relative")
	}
	return nil
}

func inspectGitUntrackedIdentities(workspaceRoot string, paths []string) ([]gitUntrackedIdentity, error) {
	identities := make([]gitUntrackedIdentity, 0, len(paths))
	totalBytes := int64(0)
	for index, path := range paths {
		fullPath := filepath.Join(workspaceRoot, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if err != nil {
			return nil, newDomainError(ErrorInvalidInput, "untracked Git entry %d changed during inspection: %v", index, err)
		}
		identity := gitUntrackedIdentity{Path: path}
		var content []byte
		switch {
		case info.Mode().IsRegular():
			if info.Size() > maxGitSnapshotFileBytes {
				return nil, newDomainError(ErrorInvalidInput, "untracked Git entry %d exceeds %d bytes", index, maxGitSnapshotFileBytes)
			}
			content, err = os.ReadFile(fullPath)
			if err != nil {
				return nil, newDomainError(ErrorInvalidInput, "reading untracked Git entry %d: %v", index, err)
			}
			if int64(len(content)) != info.Size() {
				return nil, newDomainError(ErrorInvalidInput, "untracked Git entry %d changed during inspection", index)
			}
			identity.Mode = "100644"
			if info.Mode().Perm()&0o111 != 0 {
				identity.Mode = "100755"
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(fullPath)
			if err != nil {
				return nil, newDomainError(ErrorInvalidInput, "reading untracked Git symlink entry %d: %v", index, err)
			}
			content = []byte(target)
			identity.Mode = "120000"
		default:
			return nil, newDomainError(ErrorInvalidInput, "untracked Git entry %d has unsupported file type", index)
		}
		totalBytes += int64(len(content))
		if totalBytes > maxGitSnapshotTotalBytes {
			return nil, newDomainError(ErrorInvalidInput, "Git observation exceeds %d bytes of untracked content", maxGitSnapshotTotalBytes)
		}
		identity.ContentHash = contentHash(content)
		identity.ByteLength = len(content)
		identities = append(identities, identity)
	}
	return identities, nil
}

func runGitObservationOutput(ctx context.Context, binaryPath, workspaceRoot string, maxBytes int, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-c", "color.ui=false", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-C", workspaceRoot}, args...)
	cmd := exec.CommandContext(ctx, binaryPath, commandArgs...)
	cmd.Dir = workspaceRoot
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening Git observation stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(maxBytes)+1))
	if len(data) > maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errGitObservationOutputLimit
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, waitErr
		}
		return nil, fmt.Errorf("%w: %s", waitErr, message)
	}
	return data, nil
}
