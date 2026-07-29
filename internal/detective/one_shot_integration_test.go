//go:build integration

package detective

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationOneShotLocalTextLifecycleAndTerminalReplay(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, sourcePath := registerOneShotFileWorkspace(t, ctx, pool, "lifecycle", "first requirement\n\nsecond requirement\n")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-lifecycle", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}

	result, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("RunOneShotOrchestration() error = %v", err)
	}
	if result.Replayed || !result.SourceInvoked || result.Run.Status != OrchestrationRunStatusCompleted ||
		result.Run.StopReason != OrchestrationStopReasonCoverageSatisfied || result.Step == nil {
		t.Fatalf("one-shot result = %+v", result)
	}
	coverage := result.Step.Coverage
	if coverage == nil || coverage.UnitKind != OrchestrationCoverageUnitTextFiles ||
		coverage.AttemptedUnitCount != 1 || coverage.CoveredUnitCount != 1 || !coverage.Complete {
		t.Fatalf("one-shot coverage = %+v", coverage)
	}
	if result.Step.InputAuthorityKind != OrchestrationInputAuthoritySourceToken || result.Step.InputAuthorityRef == "" {
		t.Fatalf("one-shot authority = %q/%q", result.Step.InputAuthorityKind, result.Step.InputAuthorityRef)
	}

	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 2)
	assertOneShotStatements(t, ctx, pool, []string{"first requirement", "second requirement"})

	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove source after terminal completion: %v", err)
	}
	replay, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("terminal RunOneShotOrchestration() replay error = %v", err)
	}
	if !replay.Replayed || replay.SourceInvoked || !reflect.DeepEqual(replay.Run, result.Run) {
		t.Fatalf("terminal replay = %+v, want run %+v", replay, result.Run)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 2)
}

func TestIntegrationOneShotLocalTextReusesUnchangedContentAcrossRuns(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOneShotFileWorkspace(t, ctx, pool, "cross-run", "stable requirement\n")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-cross-run-a", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}
	first, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("first RunOneShotOrchestration() error = %v", err)
	}
	input.RequestID = "one-shot-cross-run-b"
	second, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("second RunOneShotOrchestration() error = %v", err)
	}
	if first.Run.ID == second.Run.ID || second.Replayed || second.Run.Status != OrchestrationRunStatusCompleted {
		t.Fatalf("cross-run results = %+v / %+v", first, second)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)

	var hasRunID, hasStepID bool
	if err := pool.QueryRow(ctx, `
		SELECT origin_metadata ? 'detective_run_id', origin_metadata ? 'detective_step_id'
		FROM source_snapshots
	`).Scan(&hasRunID, &hasStepID); err != nil {
		t.Fatalf("read source snapshot origin metadata: %v", err)
	}
	if hasRunID || hasStepID {
		t.Fatalf("immutable source metadata contains orchestration identity: run=%v step=%v", hasRunID, hasStepID)
	}
}

func TestIntegrationOneShotLocalTextResumesPlannedStep(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOneShotFileWorkspace(t, ctx, pool, "resume", "resume requirement\n")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-resume", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}
	request, err := prepareOneShotOrchestration(input)
	if err != nil {
		t.Fatalf("prepareOneShotOrchestration() error = %v", err)
	}
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: request.requestID, WorkspaceID: request.workspaceID,
		MaxSteps: request.maxSteps, ScopeHash: request.scopeHash,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	manifest, err := collectLocalTextSource(workspace, workspace.Sources[0])
	if err != nil {
		t.Fatalf("collectLocalTextSource() error = %v", err)
	}
	planned, err := planOneShotStep(
		ctx, pool, request, started.Run, workspace.Sources[0],
		OrchestrationInputAuthoritySourceToken, manifest.token,
	)
	if err != nil {
		t.Fatalf("planOneShotStep() error = %v", err)
	}

	result, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("resumed RunOneShotOrchestration() error = %v", err)
	}
	if result.Run.Status != OrchestrationRunStatusCompleted || result.Step == nil || result.Step.ID != planned.ID || len(result.Run.Steps) != 1 {
		t.Fatalf("resumed result = %+v", result)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationOneShotLocalTextFailsClosedWhenPlannedSourceChanges(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, sourcePath := registerOneShotFileWorkspace(t, ctx, pool, "changed", "original requirement\n")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-changed", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}
	request, err := prepareOneShotOrchestration(input)
	if err != nil {
		t.Fatalf("prepareOneShotOrchestration() error = %v", err)
	}
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: request.requestID, WorkspaceID: request.workspaceID,
		MaxSteps: request.maxSteps, ScopeHash: request.scopeHash,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	manifest, err := collectLocalTextSource(workspace, workspace.Sources[0])
	if err != nil {
		t.Fatalf("collectLocalTextSource() error = %v", err)
	}
	if _, err := planOneShotStep(
		ctx, pool, request, started.Run, workspace.Sources[0],
		OrchestrationInputAuthoritySourceToken, manifest.token,
	); err != nil {
		t.Fatalf("planOneShotStep() error = %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("changed requirement\n"), 0o600); err != nil {
		t.Fatalf("change planned source: %v", err)
	}

	result, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("changed RunOneShotOrchestration() error = %v", err)
	}
	if result.SourceInvoked || result.Run.Status != OrchestrationRunStatusFailed ||
		result.Run.StopReason != OrchestrationStopReasonSourceFailed || result.Step == nil || result.Step.Coverage == nil {
		t.Fatalf("changed-source result = %+v", result)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationOneShotLocalTextClosesPlannedStepWhenWorkspaceBecomesUnavailable(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, sourcePath := registerOneShotFileWorkspace(t, ctx, pool, "unavailable", "available requirement\n")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-unavailable", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}
	request, err := prepareOneShotOrchestration(input)
	if err != nil {
		t.Fatalf("prepareOneShotOrchestration() error = %v", err)
	}
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: request.requestID, WorkspaceID: request.workspaceID,
		MaxSteps: request.maxSteps, ScopeHash: request.scopeHash,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	manifest, err := collectLocalTextSource(workspace, workspace.Sources[0])
	if err != nil {
		t.Fatalf("collectLocalTextSource() error = %v", err)
	}
	if _, err := planOneShotStep(
		ctx, pool, request, started.Run, workspace.Sources[0],
		OrchestrationInputAuthoritySourceToken, manifest.token,
	); err != nil {
		t.Fatalf("planOneShotStep() error = %v", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove planned source: %v", err)
	}

	result, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("unavailable RunOneShotOrchestration() error = %v", err)
	}
	if result.SourceInvoked || result.Run.Status != OrchestrationRunStatusFailed ||
		result.Run.StopReason != OrchestrationStopReasonWorkspaceUnavailable || result.Step == nil {
		t.Fatalf("unavailable result = %+v", result)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationOneShotLocalTextDirectoryCoverage(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(filepath.Join(docs, "nested"), 0o700); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	for path, content := range map[string]string{
		filepath.Join(docs, "requirements.md"):     "requirement one\nrequirement two\n",
		filepath.Join(docs, "nested", "notes.txt"): "note one\n",
		filepath.Join(docs, "ignored.go"):          "package ignored\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	registered, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID: "register-one-shot-directory", WorkspaceID: "workspace:one-shot-directory", WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
			SourceID: "docs", RelativePath: "docs",
		}},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	result, err := RunOneShotOrchestration(ctx, pool, OneShotOrchestrationInput{
		RequestID: "one-shot-directory", WorkspaceID: registered.Workspace.ID,
		SourceBindingID: registered.Workspace.Sources[0].ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("RunOneShotOrchestration() error = %v", err)
	}
	if result.Step == nil || result.Step.Coverage == nil ||
		result.Step.Coverage.AttemptedUnitCount != 2 || result.Step.Coverage.CoveredUnitCount != 2 || !result.Step.Coverage.Complete {
		t.Fatalf("directory result = %+v", result)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 2)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 3)
}

func TestIntegrationOneShotLocalTextStopsForEmptyDirectory(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatalf("create empty docs: %v", err)
	}
	registered, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID: "register-one-shot-empty", WorkspaceID: "workspace:one-shot-empty", WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
			SourceID: "docs", RelativePath: "docs",
		}},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	result, err := RunOneShotOrchestration(ctx, pool, OneShotOrchestrationInput{
		RequestID: "one-shot-empty", WorkspaceID: registered.Workspace.ID,
		SourceBindingID: registered.Workspace.Sources[0].ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("RunOneShotOrchestration() error = %v", err)
	}
	if result.SourceInvoked || result.Run.Status != OrchestrationRunStatusStopped ||
		result.Run.StopReason != OrchestrationStopReasonNoAllowlistedSource || result.Step == nil || result.Step.Coverage == nil {
		t.Fatalf("empty-directory result = %+v", result)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationOneShotRequestBindsSourceScope(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name+".txt"), []byte(name+" requirement\n"), 0o600); err != nil {
			t.Fatalf("write source %s: %v", name, err)
		}
	}
	registered, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID: "register-one-shot-scope", WorkspaceID: "workspace:one-shot-scope", WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{
			{CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion, SourceID: "a", RelativePath: "a.txt"},
			{CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion, SourceID: "b", RelativePath: "b.txt"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	sourceA, _ := workspaceSourceByLogicalID(registered.Workspace.Sources, "a")
	sourceB, _ := workspaceSourceByLogicalID(registered.Workspace.Sources, "b")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-scope", WorkspaceID: registered.Workspace.ID,
		SourceBindingID: sourceA.ID, MaxSteps: 1,
	}
	if _, err := RunOneShotOrchestration(ctx, pool, input); err != nil {
		t.Fatalf("first RunOneShotOrchestration() error = %v", err)
	}
	input.SourceBindingID = sourceB.ID
	_, err = RunOneShotOrchestration(ctx, pool, input)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
}

func TestIntegrationConcurrentOneShotLocalTextReplaysExactWrites(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOneShotFileWorkspace(t, ctx, pool, "concurrent", "concurrent requirement\n")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-concurrent", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}
	type outcome struct {
		result OneShotOrchestrationResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := RunOneShotOrchestration(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent RunOneShotOrchestration() error = %v", got.err)
		}
		if got.result.Run.Status != OrchestrationRunStatusCompleted || len(got.result.Run.Steps) != 1 {
			t.Fatalf("concurrent result = %+v", got.result)
		}
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func registerOneShotFileWorkspace(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	content string,
) (Workspace, string) {
	t.Helper()
	root := t.TempDir()
	sourceName := "requirements-" + suffix
	sourcePath := filepath.Join(root, sourceName+".txt")
	if err := os.WriteFile(sourcePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write one-shot source: %v", err)
	}
	registered, err := RegisterWorkspace(ctx, pool, localTextWorkspaceInput(
		"register-one-shot-"+suffix,
		"workspace:one-shot-"+suffix,
		root,
		sourceName,
	))
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	return registered.Workspace, sourcePath
}

func assertOneShotStatements(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT statement_text, source_refs -> 0 ->> 'quoted_text'
		FROM proposal_occurrences
		ORDER BY proposal_local_id
	`)
	if err != nil {
		t.Fatalf("query one-shot statements: %v", err)
	}
	defer rows.Close()
	var statements []string
	for rows.Next() {
		var statement, quoted string
		if err := rows.Scan(&statement, &quoted); err != nil {
			t.Fatalf("scan one-shot statement: %v", err)
		}
		if statement != quoted {
			t.Fatalf("statement %q does not equal exact quote %q", statement, quoted)
		}
		statements = append(statements, statement)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate one-shot statements: %v", err)
	}
	if !reflect.DeepEqual(statements, want) {
		t.Fatalf("one-shot statements = %#v, want %#v", statements, want)
	}
}

func workspaceSourceByLogicalID(sources []WorkspaceSourceBinding, sourceID string) (WorkspaceSourceBinding, bool) {
	for _, source := range sources {
		if source.SourceID == sourceID {
			return source, true
		}
	}
	return WorkspaceSourceBinding{}, false
}
