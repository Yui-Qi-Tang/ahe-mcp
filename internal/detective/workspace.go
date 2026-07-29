package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxWorkspaceSourceBindings = 32
	gitWorkspaceCheckTimeout   = 10 * time.Second
	detectiveIDVersion         = "ahe-detective-id-v1"
)

// WorkspaceRegistrationInput registers one immutable local workspace and its source bindings.
// This trusted operator input must not be exposed as a model-selectable source tool.
type WorkspaceRegistrationInput struct {
	RequestID     string                             `json:"request_id"`
	WorkspaceID   string                             `json:"workspace_id"`
	WorkspaceRoot string                             `json:"workspace_root"`
	Sources       []WorkspaceSourceRegistrationInput `json:"sources"`
}

// WorkspaceSourceRegistrationInput binds one logical source to an allowlisted
// local path or to the reserved "." locator for a remote MCP document.
type WorkspaceSourceRegistrationInput struct {
	CapabilityName    string `json:"capability_name"`
	CapabilityVersion string `json:"capability_version"`
	SourceID          string `json:"source_id"`
	RelativePath      string `json:"relative_path"`
}

// Workspace is one immutable local workspace resolved by stable identity.
type Workspace struct {
	ID           string                   `json:"workspace_id"`
	RootPath     string                   `json:"workspace_root"`
	Sources      []WorkspaceSourceBinding `json:"sources"`
	RegisteredAt time.Time                `json:"registered_at"`
}

// WorkspaceSourceBinding is one immutable source capability and local-path binding.
type WorkspaceSourceBinding struct {
	ID                string    `json:"source_binding_id"`
	WorkspaceID       string    `json:"workspace_id"`
	CapabilityName    string    `json:"capability_name"`
	CapabilityVersion string    `json:"capability_version"`
	SourceSystem      string    `json:"source_system"`
	SourceID          string    `json:"source_id"`
	RelativePath      string    `json:"relative_path"`
	PathKind          string    `json:"path_kind"`
	RegisteredAt      time.Time `json:"registered_at"`
}

// WorkspaceRegistrationResult reports whether immutable registry state was newly created or reused.
type WorkspaceRegistrationResult struct {
	RequestID string    `json:"request_id"`
	Workspace Workspace `json:"workspace"`
	Replayed  bool      `json:"replayed"`
}

type preparedWorkspaceRegistration struct {
	requestID          string
	workspace          Workspace
	requestPayloadHash string
}

type workspaceSourceIdentity struct {
	WorkspaceID       string `json:"workspace_id"`
	CapabilityName    string `json:"capability_name"`
	CapabilityVersion string `json:"capability_version"`
	SourceSystem      string `json:"source_system"`
	SourceID          string `json:"source_id"`
	RelativePath      string `json:"relative_path"`
	PathKind          string `json:"path_kind"`
}

type workspaceQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RegisterWorkspace creates or exactly reuses one immutable workspace registry entry.
func RegisterWorkspace(ctx context.Context, pool *pgxpool.Pool, input WorkspaceRegistrationInput) (WorkspaceRegistrationResult, error) {
	if pool == nil {
		return WorkspaceRegistrationResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := prepareWorkspaceRegistration(ctx, input)
	if err != nil {
		return WorkspaceRegistrationResult{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return WorkspaceRegistrationResult{}, fmt.Errorf("beginning workspace registration: %w", err)
	}
	defer tx.Rollback(context.Background())

	if err := lockWorkspaceRegistrationRequest(ctx, tx, request.requestID); err != nil {
		return WorkspaceRegistrationResult{}, err
	}
	persistedWorkspaceID, persistedPayloadHash, ok, err := readWorkspaceRegistrationRequest(ctx, tx, request.requestID)
	if err != nil {
		return WorkspaceRegistrationResult{}, err
	}
	if ok {
		if persistedPayloadHash != request.requestPayloadHash || persistedWorkspaceID != request.workspace.ID {
			return WorkspaceRegistrationResult{}, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different workspace registration payload", request.requestID)
		}
		workspace, found, err := readWorkspace(ctx, tx, request.workspace.ID, true)
		if err != nil {
			return WorkspaceRegistrationResult{}, err
		}
		if !found {
			return WorkspaceRegistrationResult{}, newDomainError(ErrorWorkspaceConflict, "workspace registration request %s references missing workspace %s", request.requestID, request.workspace.ID)
		}
		if err := ensureWorkspaceMatches(request.workspace, workspace); err != nil {
			return WorkspaceRegistrationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return WorkspaceRegistrationResult{}, fmt.Errorf("committing workspace registration replay: %w", err)
		}
		return WorkspaceRegistrationResult{RequestID: request.requestID, Workspace: workspace, Replayed: true}, nil
	}

	commandTag, err := tx.Exec(ctx, `
		INSERT INTO detective_workspaces (workspace_id, canonical_root)
		VALUES ($1,$2)
		ON CONFLICT DO NOTHING
	`, request.workspace.ID, request.workspace.RootPath)
	if err != nil {
		return WorkspaceRegistrationResult{}, fmt.Errorf("inserting detective workspace: %w", err)
	}
	created := commandTag.RowsAffected() == 1
	workspace, found, err := readWorkspace(ctx, tx, request.workspace.ID, true)
	if err != nil {
		return WorkspaceRegistrationResult{}, err
	}
	if !found {
		existingWorkspaceID, rootExists, err := readWorkspaceIDByRoot(ctx, tx, request.workspace.RootPath)
		if err != nil {
			return WorkspaceRegistrationResult{}, err
		}
		if rootExists {
			return WorkspaceRegistrationResult{}, newDomainError(ErrorWorkspaceConflict, "workspace_root %q is already registered as %s", request.workspace.RootPath, existingWorkspaceID)
		}
		return WorkspaceRegistrationResult{}, newDomainError(ErrorWorkspaceConflict, "workspace %s could not be created or resolved", request.workspace.ID)
	}
	if workspace.RootPath != request.workspace.RootPath {
		return WorkspaceRegistrationResult{}, newDomainError(ErrorWorkspaceConflict, "workspace %s is already registered with a different canonical root", request.workspace.ID)
	}

	if created {
		if len(workspace.Sources) != 0 {
			return WorkspaceRegistrationResult{}, newDomainError(ErrorWorkspaceConflict, "new workspace %s unexpectedly contains source bindings", request.workspace.ID)
		}
		if err := insertWorkspaceSources(ctx, tx, request.workspace.Sources); err != nil {
			return WorkspaceRegistrationResult{}, err
		}
		workspace, found, err = readWorkspace(ctx, tx, request.workspace.ID, false)
		if err != nil {
			return WorkspaceRegistrationResult{}, err
		}
		if !found {
			return WorkspaceRegistrationResult{}, newDomainError(ErrorWorkspaceConflict, "new workspace %s disappeared during registration", request.workspace.ID)
		}
	} else if err := ensureWorkspaceMatches(request.workspace, workspace); err != nil {
		return WorkspaceRegistrationResult{}, err
	}

	if err := insertWorkspaceRegistrationRequest(ctx, tx, request); err != nil {
		return WorkspaceRegistrationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkspaceRegistrationResult{}, fmt.Errorf("committing workspace registration: %w", err)
	}
	return WorkspaceRegistrationResult{RequestID: request.requestID, Workspace: workspace, Replayed: !created}, nil
}

// ResolveWorkspace returns one registered workspace after revalidating its local path boundary.
func ResolveWorkspace(ctx context.Context, pool *pgxpool.Pool, workspaceID string) (Workspace, error) {
	if pool == nil {
		return Workspace{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	workspaceID, err := normalizeWorkspaceID(workspaceID)
	if err != nil {
		return Workspace{}, err
	}
	workspace, found, err := readWorkspace(ctx, pool, workspaceID, false)
	if err != nil {
		return Workspace{}, err
	}
	if !found {
		return Workspace{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", workspaceID)
	}
	if err := validateResolvedWorkspace(ctx, workspace); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

func prepareWorkspaceRegistration(ctx context.Context, input WorkspaceRegistrationInput) (preparedWorkspaceRegistration, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return preparedWorkspaceRegistration{}, err
	}
	workspaceID, err := normalizeWorkspaceID(input.WorkspaceID)
	if err != nil {
		return preparedWorkspaceRegistration{}, err
	}
	root, err := canonicalDirectory(input.WorkspaceRoot)
	if err != nil {
		return preparedWorkspaceRegistration{}, err
	}
	if len(input.Sources) == 0 || len(input.Sources) > maxWorkspaceSourceBindings {
		return preparedWorkspaceRegistration{}, newDomainError(ErrorInvalidInput, "workspace sources must contain 1 to %d bindings", maxWorkspaceSourceBindings)
	}

	sources := make([]WorkspaceSourceBinding, 0, len(input.Sources))
	sourceIDs := make(map[string]struct{}, len(input.Sources))
	sourcePaths := make(map[string]struct{}, len(input.Sources))
	for index, sourceInput := range input.Sources {
		source, err := prepareWorkspaceSource(ctx, workspaceID, root, sourceInput)
		if err != nil {
			return preparedWorkspaceRegistration{}, fmt.Errorf("preparing workspace source %d: %w", index, err)
		}
		capabilityKey := source.CapabilityName + "\x00" + source.CapabilityVersion + "\x00"
		sourceIDKey := capabilityKey + source.SourceID
		if _, exists := sourceIDs[sourceIDKey]; exists {
			return preparedWorkspaceRegistration{}, newDomainError(ErrorInvalidInput, "workspace source bindings duplicate capability source_id")
		}
		if source.PathKind != "remote" {
			sourcePathKey := capabilityKey + source.RelativePath
			if _, exists := sourcePaths[sourcePathKey]; exists {
				return preparedWorkspaceRegistration{}, newDomainError(ErrorInvalidInput, "workspace source bindings duplicate capability relative_path")
			}
			sourcePaths[sourcePathKey] = struct{}{}
		}
		sourceIDs[sourceIDKey] = struct{}{}
		sources = append(sources, source)
	}
	sortWorkspaceSources(sources)
	workspace := Workspace{ID: workspaceID, RootPath: root, Sources: sources}
	sourceIdentities := make([]workspaceSourceIdentity, 0, len(workspace.Sources))
	for _, source := range workspace.Sources {
		sourceIdentities = append(sourceIdentities, source.identity())
	}
	payload, err := json.Marshal(struct {
		WorkspaceID string                    `json:"workspace_id"`
		RootPath    string                    `json:"workspace_root"`
		Sources     []workspaceSourceIdentity `json:"sources"`
	}{WorkspaceID: workspace.ID, RootPath: workspace.RootPath, Sources: sourceIdentities})
	if err != nil {
		return preparedWorkspaceRegistration{}, fmt.Errorf("serializing workspace registration identity: %w", err)
	}
	return preparedWorkspaceRegistration{
		requestID:          requestID,
		workspace:          workspace,
		requestPayloadHash: contentHash(payload),
	}, nil
}

func prepareWorkspaceSource(ctx context.Context, workspaceID, root string, input WorkspaceSourceRegistrationInput) (WorkspaceSourceBinding, error) {
	capabilityName, err := normalizeBoundedText(input.CapabilityName, "capability_name", 100)
	if err != nil {
		return WorkspaceSourceBinding{}, err
	}
	capabilityVersion, err := normalizeBoundedText(input.CapabilityVersion, "capability_version", 100)
	if err != nil {
		return WorkspaceSourceBinding{}, err
	}
	capability, ok := lookupSourceCapability(capabilityName, capabilityVersion)
	if !ok {
		return WorkspaceSourceBinding{}, newDomainError(ErrorUnsupportedCapability, "source capability %s/%s is not allowlisted", capabilityName, capabilityVersion)
	}
	sourceID, err := normalizeBoundedText(input.SourceID, "source_id", 500)
	if err != nil {
		return WorkspaceSourceBinding{}, err
	}
	relativePath := ""
	pathKind := ""
	if capability.Name == SourceCapabilityMCPReadDocument {
		relativePath = strings.TrimSpace(input.RelativePath)
		if relativePath != "." {
			return WorkspaceSourceBinding{}, newDomainError(
				ErrorInvalidInput,
				"%s must use the reserved remote locator .",
				capability.Name,
			)
		}
		pathKind = "remote"
	} else {
		relativePath, pathKind, err = resolveWorkspaceSourcePath(root, input.RelativePath)
		if err != nil {
			return WorkspaceSourceBinding{}, err
		}
	}

	switch capability.Name {
	case SourceCapabilityGitGoRepository:
		if relativePath != "." || pathKind != "directory" {
			return WorkspaceSourceBinding{}, newDomainError(ErrorInvalidInput, "%s must bind the workspace root directory", capability.Name)
		}
		if err := validateGitWorkspaceTopLevel(ctx, root); err != nil {
			return WorkspaceSourceBinding{}, err
		}
	case SourceCapabilityLocalPRDText:
		// Local text may bind one regular file or one directory inside the workspace.
	case SourceCapabilityMCPReadDocument:
		// The immutable MCP binding separately pins the exact provider invocation.
	default:
		return WorkspaceSourceBinding{}, newDomainError(ErrorUnsupportedCapability, "source capability %s/%s has no validator", capability.Name, capability.Version)
	}

	binding := WorkspaceSourceBinding{
		WorkspaceID:       workspaceID,
		CapabilityName:    capability.Name,
		CapabilityVersion: capability.Version,
		SourceSystem:      capability.SourceSystem,
		SourceID:          sourceID,
		RelativePath:      relativePath,
		PathKind:          pathKind,
	}
	binding.ID, err = workspaceSourceBindingID(binding)
	if err != nil {
		return WorkspaceSourceBinding{}, err
	}
	return binding, nil
}

func validateResolvedWorkspace(ctx context.Context, workspace Workspace) error {
	root, err := canonicalDirectory(workspace.RootPath)
	if err != nil {
		return newDomainError(ErrorWorkspaceUnavailable, "resolving workspace %s root: %v", workspace.ID, err)
	}
	if root != workspace.RootPath {
		return newDomainError(ErrorWorkspaceUnavailable, "workspace %s root no longer resolves to its registered path", workspace.ID)
	}
	if len(workspace.Sources) == 0 {
		return newDomainError(ErrorWorkspaceUnavailable, "workspace %s has no registered source bindings", workspace.ID)
	}
	for _, source := range workspace.Sources {
		if source.WorkspaceID != workspace.ID {
			return newDomainError(ErrorWorkspaceUnavailable, "registered source %s has inconsistent workspace identity", source.ID)
		}
		capability, ok := lookupSourceCapability(source.CapabilityName, source.CapabilityVersion)
		if !ok || capability.SourceSystem != source.SourceSystem {
			return newDomainError(ErrorUnsupportedCapability, "registered source %s uses unsupported capability %s/%s", source.ID, source.CapabilityName, source.CapabilityVersion)
		}
		expectedID, err := workspaceSourceBindingID(source)
		if err != nil {
			return newDomainError(ErrorWorkspaceUnavailable, "rebuilding registered source %s identity: %v", source.ID, err)
		}
		if source.ID != expectedID {
			return newDomainError(ErrorWorkspaceUnavailable, "registered source %s does not match its immutable identity", source.ID)
		}
		if source.CapabilityName == SourceCapabilityMCPReadDocument {
			if source.RelativePath != "." || source.PathKind != "remote" {
				return newDomainError(ErrorWorkspaceUnavailable, "registered MCP source %s has invalid remote locator", source.ID)
			}
		} else {
			relativePath, pathKind, err := resolveWorkspaceSourcePath(root, source.RelativePath)
			if err != nil {
				return newDomainError(ErrorWorkspaceUnavailable, "resolving registered source %s: %v", source.ID, err)
			}
			if relativePath != source.RelativePath || pathKind != source.PathKind {
				return newDomainError(ErrorWorkspaceUnavailable, "registered source %s path identity changed", source.ID)
			}
		}
		if source.CapabilityName == SourceCapabilityGitGoRepository {
			if err := validateGitWorkspaceTopLevel(ctx, root); err != nil {
				return newDomainError(ErrorWorkspaceUnavailable, "validating registered Git workspace %s: %v", workspace.ID, err)
			}
		}
	}
	return nil
}

func canonicalDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateText(value, "workspace_root", 4096); err != nil {
		return "", err
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

func resolveWorkspaceSourcePath(root, value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if err := validateText(value, "relative_path", 4096); err != nil {
		return "", "", err
	}
	if filepath.IsAbs(value) {
		return "", "", newDomainError(ErrorInvalidInput, "relative_path must not be absolute")
	}
	for _, segment := range strings.Split(filepath.ToSlash(value), "/") {
		if segment == ".." {
			return "", "", newDomainError(ErrorInvalidInput, "relative_path must not contain parent traversal")
		}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.Clean(filepath.FromSlash(value))))
	if err != nil {
		return "", "", newDomainError(ErrorInvalidInput, "resolving relative_path: %v", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", "", newDomainError(ErrorInvalidInput, "relating source path to workspace: %v", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", newDomainError(ErrorInvalidInput, "relative_path resolves outside workspace_root")
	}
	if relative == "" {
		relative = "."
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", newDomainError(ErrorInvalidInput, "reading source path: %v", err)
	}
	pathKind := ""
	switch {
	case info.IsDir():
		pathKind = "directory"
	case info.Mode().IsRegular():
		pathKind = "file"
	default:
		return "", "", newDomainError(ErrorInvalidInput, "relative_path has unsupported file type")
	}
	return filepath.ToSlash(relative), pathKind, nil
}

func validateGitWorkspaceTopLevel(ctx context.Context, root string) error {
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		return newDomainError(ErrorInvalidInput, "git binary is required: %v", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, gitWorkspaceCheckTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, gitBinary, "-C", root, "rev-parse", "--show-toplevel")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		if runCtx.Err() != nil {
			return newDomainError(ErrorInvalidInput, "validating Git workspace: %v", runCtx.Err())
		}
		return newDomainError(ErrorInvalidInput, "workspace_root is not a Git worktree top-level: %v", err)
	}
	topLevel, err := canonicalDirectory(strings.TrimSpace(string(output)))
	if err != nil {
		return err
	}
	if topLevel != root {
		return newDomainError(ErrorInvalidInput, "workspace_root %q is not Git top-level %q", root, topLevel)
	}
	return nil
}

func normalizeWorkspaceID(value string) (string, error) {
	value, err := normalizeBoundedText(value, "workspace_id", 300)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(value, "workspace:") || len(value) == len("workspace:") {
		return "", newDomainError(ErrorInvalidInput, "workspace_id %q must start with workspace:", value)
	}
	return value, nil
}

func normalizeBoundedText(value, field string, maxBytes int) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateText(value, field, maxBytes); err != nil {
		return "", err
	}
	return value, nil
}

func validateText(value, field string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return newDomainError(ErrorInvalidInput, "%s must contain 1 to %d valid UTF-8 bytes", field, maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return newDomainError(ErrorInvalidInput, "%s must not contain control characters", field)
		}
	}
	return nil
}

func workspaceSourceBindingID(binding WorkspaceSourceBinding) (string, error) {
	payload, err := json.Marshal(struct {
		Version string                  `json:"version"`
		Fields  workspaceSourceIdentity `json:"fields"`
	}{
		Version: detectiveIDVersion,
		Fields:  binding.identity(),
	})
	if err != nil {
		return "", fmt.Errorf("serializing workspace source identity: %w", err)
	}
	return "workspace-source:" + hashHex(payload), nil
}

func (b WorkspaceSourceBinding) identity() workspaceSourceIdentity {
	return workspaceSourceIdentity{
		WorkspaceID:       b.WorkspaceID,
		CapabilityName:    b.CapabilityName,
		CapabilityVersion: b.CapabilityVersion,
		SourceSystem:      b.SourceSystem,
		SourceID:          b.SourceID,
		RelativePath:      b.RelativePath,
		PathKind:          b.PathKind,
	}
}

func contentHash(data []byte) string {
	return "sha256:" + hashHex(data)
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func lockWorkspaceRegistrationRequest(ctx context.Context, tx pgx.Tx, requestID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO evidence_ingestion_request_serializations (operation_name, request_id)
		VALUES ('detective-workspace-register', $1)
		ON CONFLICT (operation_name, request_id) DO NOTHING
	`, requestID); err != nil {
		return fmt.Errorf("creating workspace registration serialization: %w", err)
	}
	var locked int
	if err := tx.QueryRow(ctx, `
		SELECT 1
		FROM evidence_ingestion_request_serializations
		WHERE operation_name = 'detective-workspace-register' AND request_id = $1
		FOR UPDATE
	`, requestID).Scan(&locked); err != nil {
		return fmt.Errorf("locking workspace registration request: %w", err)
	}
	return nil
}

func readWorkspaceRegistrationRequest(ctx context.Context, tx pgx.Tx, requestID string) (string, string, bool, error) {
	var workspaceID, payloadHash string
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, request_payload_hash
		FROM detective_workspace_registration_requests
		WHERE request_id = $1
	`, requestID).Scan(&workspaceID, &payloadHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("reading workspace registration request: %w", err)
	}
	return workspaceID, payloadHash, true, nil
}

func insertWorkspaceRegistrationRequest(ctx context.Context, tx pgx.Tx, request preparedWorkspaceRegistration) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO detective_workspace_registration_requests (
			request_id, workspace_id, request_payload_hash
		)
		VALUES ($1,$2,$3)
	`, request.requestID, request.workspace.ID, request.requestPayloadHash)
	if err != nil {
		return fmt.Errorf("inserting workspace registration request: %w", err)
	}
	return nil
}

func readWorkspace(ctx context.Context, db workspaceQuerier, workspaceID string, forUpdate bool) (Workspace, bool, error) {
	query := `
		SELECT workspace_id, canonical_root, registered_at
		FROM detective_workspaces
		WHERE workspace_id = $1
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var workspace Workspace
	err := db.QueryRow(ctx, query, workspaceID).Scan(&workspace.ID, &workspace.RootPath, &workspace.RegisteredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, fmt.Errorf("reading detective workspace: %w", err)
	}
	workspace.RegisteredAt = workspace.RegisteredAt.UTC()
	rows, err := db.Query(ctx, `
		SELECT
			source_binding_id,
			workspace_id,
			capability_name,
			capability_version,
			source_system,
			source_id,
			relative_path,
			path_kind,
			registered_at
		FROM detective_workspace_sources
		WHERE workspace_id = $1
		ORDER BY capability_name, capability_version, source_id, relative_path
	`, workspaceID)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("listing detective workspace sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source WorkspaceSourceBinding
		if err := rows.Scan(
			&source.ID,
			&source.WorkspaceID,
			&source.CapabilityName,
			&source.CapabilityVersion,
			&source.SourceSystem,
			&source.SourceID,
			&source.RelativePath,
			&source.PathKind,
			&source.RegisteredAt,
		); err != nil {
			return Workspace{}, false, fmt.Errorf("scanning detective workspace source: %w", err)
		}
		source.RegisteredAt = source.RegisteredAt.UTC()
		workspace.Sources = append(workspace.Sources, source)
	}
	if err := rows.Err(); err != nil {
		return Workspace{}, false, fmt.Errorf("iterating detective workspace sources: %w", err)
	}
	return workspace, true, nil
}

func readWorkspaceIDByRoot(ctx context.Context, tx pgx.Tx, root string) (string, bool, error) {
	var workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT workspace_id
		FROM detective_workspaces
		WHERE canonical_root = $1
		FOR UPDATE
	`, root).Scan(&workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading workspace identity by canonical root: %w", err)
	}
	return workspaceID, true, nil
}

func insertWorkspaceSources(ctx context.Context, tx pgx.Tx, sources []WorkspaceSourceBinding) error {
	for _, source := range sources {
		_, err := tx.Exec(ctx, `
			INSERT INTO detective_workspace_sources (
				source_binding_id,
				workspace_id,
				capability_name,
				capability_version,
				source_system,
				source_id,
				relative_path,
				path_kind
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		`, source.ID, source.WorkspaceID, source.CapabilityName, source.CapabilityVersion, source.SourceSystem, source.SourceID, source.RelativePath, source.PathKind)
		if err != nil {
			return fmt.Errorf("inserting workspace source %s: %w", source.ID, err)
		}
	}
	return nil
}

func ensureWorkspaceMatches(expected, actual Workspace) error {
	if expected.ID != actual.ID || expected.RootPath != actual.RootPath || len(expected.Sources) != len(actual.Sources) {
		return newDomainError(ErrorWorkspaceConflict, "workspace %s is already registered with different immutable material", expected.ID)
	}
	for index := range expected.Sources {
		left := expected.Sources[index]
		right := actual.Sources[index]
		if left.ID != right.ID ||
			left.WorkspaceID != right.WorkspaceID ||
			left.CapabilityName != right.CapabilityName ||
			left.CapabilityVersion != right.CapabilityVersion ||
			left.SourceSystem != right.SourceSystem ||
			left.SourceID != right.SourceID ||
			left.RelativePath != right.RelativePath ||
			left.PathKind != right.PathKind {
			return newDomainError(ErrorWorkspaceConflict, "workspace %s is already registered with different source bindings", expected.ID)
		}
	}
	return nil
}

func sortWorkspaceSources(sources []WorkspaceSourceBinding) {
	sort.Slice(sources, func(i, j int) bool {
		left := sources[i]
		right := sources[j]
		if left.CapabilityName != right.CapabilityName {
			return left.CapabilityName < right.CapabilityName
		}
		if left.CapabilityVersion != right.CapabilityVersion {
			return left.CapabilityVersion < right.CapabilityVersion
		}
		if left.SourceID != right.SourceID {
			return left.SourceID < right.SourceID
		}
		return left.RelativePath < right.RelativePath
	})
}
