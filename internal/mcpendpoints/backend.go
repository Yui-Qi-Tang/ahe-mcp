// Package mcpendpoints exposes bounded repository intake and exact-reviewed endpoint admission.
package mcpendpoints

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ProfileRepositoryIntake = "repository-intake"
	ProfileEndpointReviewer = "endpoint-reviewer"
	ToolCapture             = "capture_repository_snapshot"
	ToolExtract             = "extract_repository_go"
	ToolReview              = "get_endpoint_review"
	ToolAdmit               = "admit_reviewed_endpoint"
)

type Backend struct {
	pool           *pgxpool.Pool
	principal      runtimeauth.Principal
	profile        string
	repositoryRoot string
	repositoryID   string
}

func NewBackend(pool *pgxpool.Pool, principal runtimeauth.Principal, profile, root, id string) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("endpoint runtime requires PostgreSQL")
	}
	p, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	if profile != ProfileRepositoryIntake && profile != ProfileEndpointReviewer {
		return nil, errors.New("unsupported endpoint profile")
	}
	if profile == ProfileRepositoryIntake {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || len(root) > 4096 || strings.ContainsRune(root, 0) || id == "" || len(id) > 200 || strings.TrimSpace(id) != id || strings.ContainsRune(id, 0) {
			return nil, errors.New("repository intake requires launcher-fixed repository root and ID")
		}
	} else if root != "" || id != "" {
		return nil, errors.New("endpoint reviewer cannot carry repository access configuration")
	}
	return &Backend{pool: pool, principal: p, profile: profile, repositoryRoot: root, repositoryID: id}, nil
}

type CaptureRequest struct {
	RequestID string `json:"request_id"`
	CommitSHA string `json:"commit_sha"`
}
type ExtractRequest struct {
	RequestID            string `json:"request_id"`
	RepositorySnapshotID string `json:"repository_snapshot_id"`
}

func (b *Backend) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("endpoint principal unavailable")
	}
	if ctx == nil {
		return nil, errors.New("context required")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var value any
	var err error
	switch {
	case b.profile == ProfileRepositoryIntake && name == ToolCapture:
		var req CaptureRequest
		if err = decodeRequest(args, &req); err != nil {
			break
		}
		if req.RequestID == "" || len(req.RequestID) > 200 {
			return nil, errors.New("bounded request ID is required")
		}
		value, err = evidenceingestion.CaptureGitRepositorySnapshot(ctx, b.pool, evidenceingestion.GitRepositorySnapshotConfig{
			// The tool cannot choose a command, path, identity or mutable branch.
			GitBinaryPath: "/usr/bin/git", WorkspaceRoot: b.repositoryRoot, RepoID: b.repositoryID,
			CommitSHA: req.CommitSHA, RequestID: req.RequestID, Timeout: 55 * time.Second})
	case b.profile == ProfileRepositoryIntake && name == ToolExtract:
		var req ExtractRequest
		if err = decodeRequest(args, &req); err != nil {
			break
		}
		if req.RequestID == "" || len(req.RequestID) > 200 || len(req.RepositorySnapshotID) > 200 {
			return nil, errors.New("bounded request and snapshot IDs are required")
		}
		var id string
		var count, size int
		err = b.pool.QueryRow(ctx, `SELECT r.repo_id,r.selected_file_count,coalesce(sum(f.byte_length),0)
   FROM repository_snapshots r LEFT JOIN source_file_snapshots f USING(repository_snapshot_id)
   WHERE r.repository_snapshot_id=$1 GROUP BY r.repo_id,r.selected_file_count`, req.RepositorySnapshotID).Scan(&id, &count, &size)
		if err != nil {
			break
		}
		if id != b.repositoryID {
			return nil, runtimeauth.NewUnauthorizedError("snapshot is outside the launcher repository")
		}
		if count > 256 || size > 8<<20 {
			return nil, errors.New("parser intake is limited to 256 tracked Go files and 8 MiB")
		}
		value, err = evidenceingestion.RunRepositoryGoParserExtractor(ctx, b.pool, evidenceingestion.RepositoryGoParserRequest{
			RequestID: req.RequestID, RepositorySnapshotID: req.RepositorySnapshotID})
	case b.profile == ProfileEndpointReviewer && name == ToolReview:
		var req evidenceingestion.EndpointReviewRequest
		if err = decodeRequest(args, &req); err == nil {
			value, err = evidenceingestion.LoadEndpointReview(ctx, b.pool, req)
		}
	case b.profile == ProfileEndpointReviewer && name == ToolAdmit:
		var req evidenceingestion.ReviewedEndpointAdmissionInput
		if err = decodeRequest(args, &req); err != nil {
			break
		}
		req.ReviewerID = b.principal.ID
		value, err = evidenceingestion.AdmitReviewedEndpoint(ctx, b.pool, req)
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation unavailable in this endpoint profile")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
