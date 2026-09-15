//go:build integration

package detective

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRegisterWorkspaceRoundTripAndReplay(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte("source-backed requirements\n"), 0o600); err != nil {
		t.Fatalf("write requirements: %v", err)
	}
	input := localTextWorkspaceInput("register-workspace", "workspace:round-trip", root, "requirements")

	first, err := RegisterWorkspace(ctx, pool, input)
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	if first.Replayed || first.RequestID != input.RequestID || first.Workspace.ID != input.WorkspaceID || first.Workspace.RegisteredAt.IsZero() || len(first.Workspace.Sources) != 1 || first.Workspace.Sources[0].RegisteredAt.IsZero() {
		t.Fatalf("first registration = %+v", first)
	}
	resolved, err := ResolveWorkspace(ctx, pool, input.WorkspaceID)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	if !reflect.DeepEqual(resolved, first.Workspace) {
		t.Fatalf("resolved workspace = %+v, want %+v", resolved, first.Workspace)
	}

	replay, err := RegisterWorkspace(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay RegisterWorkspace() error = %v", err)
	}
	if !replay.Replayed || !reflect.DeepEqual(replay.Workspace, first.Workspace) {
		t.Fatalf("same-request replay = %+v, want workspace %+v", replay, first.Workspace)
	}

	secondRequest := input
	secondRequest.RequestID = "register-workspace-again"
	reused, err := RegisterWorkspace(ctx, pool, secondRequest)
	if err != nil {
		t.Fatalf("second-request RegisterWorkspace() error = %v", err)
	}
	if !reused.Replayed || !reflect.DeepEqual(reused.Workspace, first.Workspace) {
		t.Fatalf("second-request reuse = %+v, want workspace %+v", reused, first.Workspace)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_workspaces", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_workspace_sources", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_workspace_registration_requests", 2)
}

func TestIntegrationRegisterWorkspaceRejectsIdentityConflicts(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte("requirements\n"), 0o600); err != nil {
		t.Fatalf("write requirements: %v", err)
	}
	base := localTextWorkspaceInput("register-conflict", "workspace:conflict", root, "requirements")
	if _, err := RegisterWorkspace(ctx, pool, base); err != nil {
		t.Fatalf("initial RegisterWorkspace() error = %v", err)
	}

	reusedRequest := base
	reusedRequest.Sources[0].SourceID = "different-source"
	_, err := RegisterWorkspace(ctx, pool, reusedRequest)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)

	differentRequest := reusedRequest
	differentRequest.RequestID = "register-conflict-different-request"
	_, err = RegisterWorkspace(ctx, pool, differentRequest)
	assertDetectiveKind(t, err, ErrorWorkspaceConflict)

	differentWorkspace := base
	differentWorkspace.RequestID = "register-conflict-root"
	differentWorkspace.WorkspaceID = "workspace:other"
	_, err = RegisterWorkspace(ctx, pool, differentWorkspace)
	assertDetectiveKind(t, err, ErrorWorkspaceConflict)

	otherRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherRoot, "requirements.txt"), []byte("requirements\n"), 0o600); err != nil {
		t.Fatalf("write other requirements: %v", err)
	}
	differentRoot := localTextWorkspaceInput("register-conflict-workspace", base.WorkspaceID, otherRoot, "requirements")
	_, err = RegisterWorkspace(ctx, pool, differentRoot)
	assertDetectiveKind(t, err, ErrorWorkspaceConflict)

	assertDetectiveTableCount(t, ctx, pool, "detective_workspaces", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_workspace_sources", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_workspace_registration_requests", 1)
}

func TestIntegrationRegisterWorkspaceConcurrentReplayCommitsOnce(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "prd.txt"), []byte("bounded orchestration\n"), 0o600); err != nil {
		t.Fatalf("write PRD: %v", err)
	}
	input := localTextWorkspaceInput("register-concurrent", "workspace:concurrent", root, "prd")

	type outcome struct {
		result WorkspaceRegistrationResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := RegisterWorkspace(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)

	replayed := 0
	var workspace Workspace
	for range 2 {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("concurrent RegisterWorkspace() error = %v", outcome.err)
		}
		if outcome.result.Replayed {
			replayed++
		}
		if workspace.ID == "" {
			workspace = outcome.result.Workspace
		} else if !reflect.DeepEqual(workspace, outcome.result.Workspace) {
			t.Fatalf("concurrent workspace mismatch: %+v != %+v", workspace, outcome.result.Workspace)
		}
	}
	if replayed != 1 {
		t.Fatalf("concurrent replay count = %d, want 1", replayed)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_workspaces", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_workspace_sources", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_workspace_registration_requests", 1)
	var serializations int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM evidence_ingestion_request_serializations
		WHERE operation_name = 'detective-workspace-register'
	`).Scan(&serializations); err != nil {
		t.Fatalf("count workspace request serializations: %v", err)
	}
	if serializations != 1 {
		t.Fatalf("workspace request serializations = %d, want 1", serializations)
	}
}

func TestIntegrationRegisterAndResolveGitWorkspace(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := gitWorkspaceFixture(t)
	input := WorkspaceRegistrationInput{
		RequestID:     "register-git-workspace",
		WorkspaceID:   "workspace:git",
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName:    SourceCapabilityGitGoRepository,
			CapabilityVersion: SourceCapabilityGitGoRepositoryVersion,
			SourceID:          "repo-git",
			RelativePath:      ".",
		}},
	}
	registered, err := RegisterWorkspace(ctx, pool, input)
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	resolved, err := ResolveWorkspace(ctx, pool, input.WorkspaceID)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	if !reflect.DeepEqual(resolved, registered.Workspace) || resolved.Sources[0].SourceID != "repo-git" || resolved.Sources[0].RelativePath != "." {
		t.Fatalf("resolved Git workspace = %+v, want %+v", resolved, registered.Workspace)
	}
}

func TestIntegrationRegisterAndResolveMultipleRemoteMCPWorkspaceSources(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	input := WorkspaceRegistrationInput{
		RequestID:     "register-mcp-workspace",
		WorkspaceID:   "workspace:mcp",
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
				SourceID:          "atlassian:confluence:4242",
				RelativePath:      ".",
			},
		},
	}
	registered, err := RegisterWorkspace(ctx, pool, input)
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	resolved, err := ResolveWorkspace(ctx, pool, input.WorkspaceID)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	if !reflect.DeepEqual(resolved, registered.Workspace) || len(resolved.Sources) != 2 {
		t.Fatalf("resolved MCP workspace = %+v, want %+v", resolved, registered.Workspace)
	}
	for _, source := range resolved.Sources {
		if source.PathKind != "remote" || source.RelativePath != "." {
			t.Fatalf("resolved MCP source = %+v", source)
		}
	}
}

func TestIntegrationResolveWorkspaceRejectsUnknownIdentity(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	_, err := ResolveWorkspace(ctx, pool, "workspace:missing")
	assertDetectiveKind(t, err, ErrorWorkspaceNotFound)
}

func localTextWorkspaceInput(requestID, workspaceID, root, sourceID string) WorkspaceRegistrationInput {
	return WorkspaceRegistrationInput{
		RequestID:     requestID,
		WorkspaceID:   workspaceID,
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName:    SourceCapabilityLocalPRDText,
			CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
			SourceID:          sourceID,
			RelativePath:      sourceID + ".txt",
		}},
	}
}

func assertDetectiveTableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func detectiveIntegrationPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	schema := "ahe_detective_test_" + detectiveRandomHex(t, 8)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return ctx, pool
}

func detectiveRandomHex(t *testing.T, size int) string {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	return hex.EncodeToString(data)
}
