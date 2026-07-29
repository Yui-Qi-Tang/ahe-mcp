//go:build acceptance && (darwin || linux)

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/detective"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	detectiveProcessHelperEnvironment      = "AHE_DETECTIVE_PROCESS_HELPER"
	detectiveCrashWorkspaceRootEnvironment = "AHE_P15_CRASH_WORKSPACE_ROOT"
)

func TestAcceptanceDetectiveHostRepairsSIGKILLDuringRepositoryExtraction(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	schemaURL, pool := processAcceptanceDatabase(t, databaseURL)
	repositoryRoot := processAcceptanceRepository(t, realGit)
	if selectedRoot := strings.TrimSpace(os.Getenv(detectiveCrashWorkspaceRootEnvironment)); selectedRoot != "" {
		repositoryRoot = processSelectedAcceptanceRepository(t, realGit, selectedRoot)
	}

	controlDir := t.TempDir()
	markerPath := filepath.Join(controlDir, "cat-file-blocked")
	releasePath := filepath.Join(controlDir, "release-cat-file")
	binDir := filepath.Join(controlDir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatalf("create wrapper bin: %v", err)
	}
	wrapperPath := filepath.Join(binDir, "git")
	wrapper := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "cat-file" ]; then
    : > "$AHE_TEST_BLOCK_MARKER"
    while [ ! -e "$AHE_TEST_RELEASE_FILE" ]; do
      sleep 0.05
    done
    break
  fi
done
exec "$AHE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	configPath := processAcceptanceConfig(t, repositoryRoot)
	extraEnvironment := []string{
		"DATABASE_DNS=" + schemaURL,
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"AHE_TEST_REAL_GIT=" + realGit,
		"AHE_TEST_BLOCK_MARKER=" + markerPath,
		"AHE_TEST_RELEASE_FILE=" + releasePath,
	}

	first := startDetectiveHostProcess(t, configPath, true, extraEnvironment)
	t.Cleanup(func() {
		_ = os.WriteFile(releasePath, []byte("release"), 0o600)
		first.killIfRunning()
	})
	waitForPath(t, markerPath, 30*time.Second, first)
	leaseExpiresAt, attemptNumber := waitForRunningExecution(t, pool, 10*time.Second)
	if attemptNumber != 1 {
		t.Fatalf("initial attempt number = %d, want 1", attemptNumber)
	}
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL first host: %v", err)
	}
	if err := first.wait(10 * time.Second); err == nil {
		t.Fatal("SIGKILL host exit error = nil")
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release blocked git wrapper: %v", err)
	}

	var currentLease time.Time
	if err := pool.QueryRow(context.Background(), `
		SELECT lease_expires_at
		FROM repository_extraction_work_claim_attempts
		WHERE attempt_number = 1 AND status = 'running'
	`).Scan(&currentLease); err != nil {
		t.Fatalf("read killed attempt lease: %v", err)
	}
	if currentLease.Before(leaseExpiresAt) {
		t.Fatalf("current lease %s moved before observed lease %s", currentLease, leaseExpiresAt)
	}
	wait := time.Until(currentLease) + 250*time.Millisecond
	if wait < 0 {
		wait = 0
	}
	if wait > 70*time.Second {
		t.Fatalf("killed attempt lease wait = %s, want at most 70s", wait)
	}
	t.Logf("waiting %s for the persisted killed-worker lease to expire", wait.Round(time.Millisecond))
	time.Sleep(wait)

	second := startDetectiveHostProcess(t, configPath, false, extraEnvironment)
	t.Cleanup(second.killIfRunning)
	waitForRecoveredRepositoryCycle(t, pool, second, 30*time.Second)
	if err := second.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM second host: %v", err)
	}
	if err := second.wait(10 * time.Second); err != nil {
		t.Fatalf("graceful second host exit: %v\nlog:\n%s", err, second.log())
	}
	logOutput := second.log()
	if !strings.Contains(logOutput, `"event":"host_started"`) || !strings.Contains(logOutput, `"event":"host_stopped"`) {
		t.Fatalf("second host lifecycle log is incomplete:\n%s", logOutput)
	}
}

func TestAcceptanceDetectiveHostPlannerTransportAndRestart(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	schemaURL, pool := processAcceptanceDatabase(t, databaseURL)
	repositoryRoot := processPlannerAcceptanceRepository(t, realGit)

	var providerCalls atomic.Int32
	providerErrors := make(chan error, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/generate" {
			providerErrors <- fmt.Errorf("planner request = %s %s", request.Method, request.URL.Path)
			http.Error(writer, "unsupported request", http.StatusNotFound)
			return
		}
		var envelope struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Stream bool   `json:"stream"`
			Think  bool   `json:"think"`
		}
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			providerErrors <- fmt.Errorf("decode planner request: %w", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		if envelope.Model != "acceptance-planner" || envelope.Stream || envelope.Think {
			providerErrors <- fmt.Errorf("planner request controls = %+v", envelope)
			http.Error(writer, "invalid controls", http.StatusBadRequest)
			return
		}
		if strings.Contains(envelope.Prompt, repositoryRoot) ||
			strings.Contains(envelope.Prompt, "Planner process requirement remains source backed.") {
			providerErrors <- errors.New("planner prompt exposed local authority or evidence text")
			http.Error(writer, "authority exposure", http.StatusBadRequest)
			return
		}
		choice, err := processPlannerLocalTextChoice(envelope.Prompt)
		if err != nil {
			providerErrors <- err
			http.Error(writer, "invalid planner input", http.StatusBadRequest)
			return
		}
		providerCalls.Add(1)
		decision, err := json.Marshal(map[string]string{
			"schema_version": detective.PlannerDecisionSchemaVersion,
			"classification": detective.PlannerClassificationQuerySource,
			"source_choice":  choice,
		})
		if err != nil {
			providerErrors <- fmt.Errorf("marshal planner decision: %w", err)
			http.Error(writer, "decision failure", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"response":    string(decision),
			"done_reason": "stop",
		})
	}))
	t.Cleanup(provider.Close)

	configPath := processPlannerAcceptanceConfig(t, repositoryRoot, provider.URL)
	extraEnvironment := []string{"DATABASE_DNS=" + schemaURL}
	first := startDetectiveHostProcess(t, configPath, true, extraEnvironment)
	t.Cleanup(first.killIfRunning)
	waitForPlannerProcessCompletion(t, pool, first, 15*time.Second)
	if providerCalls.Load() != 1 {
		t.Fatalf("first process planner calls = %d, want 1", providerCalls.Load())
	}
	assertNoProcessPlannerProviderError(t, providerErrors)
	if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM first planner host: %v", err)
	}
	if err := first.wait(10 * time.Second); err != nil {
		t.Fatalf("first planner host exit: %v\nlog:\n%s", err, first.log())
	}
	if !strings.Contains(first.log(), `"event":"planner_recommendation_consumed"`) {
		t.Fatalf("first planner host log has no consumption event:\n%s", first.log())
	}

	second := startDetectiveHostProcess(t, configPath, false, extraEnvironment)
	t.Cleanup(second.killIfRunning)
	waitForProcessSourceOutcomes(t, second, 2, 10*time.Second)
	if providerCalls.Load() != 1 {
		t.Fatalf("restarted process planner calls = %d, want 1", providerCalls.Load())
	}
	assertNoProcessPlannerProviderError(t, providerErrors)
	assertPlannerProcessCounts(t, pool)
	if err := second.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM restarted planner host: %v", err)
	}
	if err := second.wait(10 * time.Second); err != nil {
		t.Fatalf("restarted planner host exit: %v\nlog:\n%s", err, second.log())
	}
}

func TestDetectiveHostProcessHelper(t *testing.T) {
	if os.Getenv(detectiveProcessHelperEnvironment) != "1" {
		return
	}
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		fmt.Fprintln(os.Stderr, "missing helper argument separator")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[separator+1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

type detectiveHostProcess struct {
	cmd     *exec.Cmd
	waitCh  chan error
	logPath string
	logFile *os.File
	exited  bool
	waitErr error
}

func startDetectiveHostProcess(
	t *testing.T,
	configPath string,
	applyMigrations bool,
	extraEnvironment []string,
) *detectiveHostProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	args := []string{"-test.run=^TestDetectiveHostProcessHelper$", "--", "--config", configPath}
	if applyMigrations {
		args = append(args, "--migrate")
	}
	command := exec.Command(executable, args...)
	command.Env = append(os.Environ(), detectiveProcessHelperEnvironment+"=1")
	command.Env = append(command.Env, extraEnvironment...)
	logFile, err := os.CreateTemp(t.TempDir(), "ahe-detective-*.log")
	if err != nil {
		t.Fatalf("create host log: %v", err)
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start detective host: %v", err)
	}
	process := &detectiveHostProcess{
		cmd: command, waitCh: make(chan error, 1), logPath: logFile.Name(), logFile: logFile,
	}
	go func() { process.waitCh <- command.Wait() }()
	return process
}

func (p *detectiveHostProcess) wait(timeout time.Duration) error {
	if p.exited {
		return p.waitErr
	}
	select {
	case p.waitErr = <-p.waitCh:
		p.exited = true
		_ = p.logFile.Close()
		return p.waitErr
	case <-time.After(timeout):
		return errors.New("detective host did not exit before timeout")
	}
}

func (p *detectiveHostProcess) killIfRunning() {
	if p == nil || p.exited {
		return
	}
	_ = p.cmd.Process.Kill()
	_ = p.wait(10 * time.Second)
}

func (p *detectiveHostProcess) log() string {
	data, _ := os.ReadFile(p.logPath)
	return string(data)
}

func waitForPath(t *testing.T, path string, timeout time.Duration, process *detectiveHostProcess) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if process.cmd.ProcessState != nil && process.cmd.ProcessState.Exited() {
			t.Fatalf("host exited before marker %s:\n%s", path, process.log())
		}
		time.Sleep(50 * time.Millisecond)
	}
	process.killIfRunning()
	t.Fatalf("timed out waiting for %s\nlog:\n%s", path, process.log())
}

func waitForRunningExecution(t *testing.T, pool *pgxpool.Pool, timeout time.Duration) (time.Time, int) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var leaseExpiresAt time.Time
		var attemptNumber int
		err := pool.QueryRow(context.Background(), `
			SELECT a.lease_expires_at, a.attempt_number
			FROM repository_extraction_work_claim_attempts a
			JOIN repository_extraction_work_execution_requests e
			  ON e.work_item_id = a.work_item_id AND e.claim_id = a.claim_id
			WHERE a.status = 'running'
			ORDER BY a.attempt_number DESC
			LIMIT 1
		`).Scan(&leaseExpiresAt, &attemptNumber)
		if err == nil {
			return leaseExpiresAt, attemptNumber
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for running repository execution")
	return time.Time{}, 0
}

func waitForRecoveredRepositoryCycle(
	t *testing.T,
	pool *pgxpool.Pool,
	process *detectiveHostProcess,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var succeededAttempts, expiredAttempts, repairs, completedCycles, generations, heads int
		err := pool.QueryRow(context.Background(), `
			SELECT
			  (SELECT COUNT(*) FROM repository_extraction_work_claim_attempts WHERE attempt_number = 2 AND status = 'succeeded'),
			  (SELECT COUNT(*) FROM repository_extraction_work_claim_attempts WHERE attempt_number = 1 AND status = 'expired'),
			  (SELECT COUNT(*) FROM repository_extraction_work_execution_repairs),
			  (SELECT COUNT(*) FROM detective_periodic_source_cycles WHERE status = 'completed'),
			  (SELECT COUNT(*) FROM repository_source_generations),
			  (SELECT COUNT(*) FROM repository_source_heads)
		`).Scan(&succeededAttempts, &expiredAttempts, &repairs, &completedCycles, &generations, &heads)
		if err == nil && succeededAttempts == 1 && expiredAttempts == 1 && repairs == 1 &&
			completedCycles == 1 && generations == 1 && heads == 0 {
			return
		}
		if process.cmd.ProcessState != nil && process.cmd.ProcessState.Exited() {
			t.Fatalf("restarted host exited before recovery:\n%s", process.log())
		}
		time.Sleep(100 * time.Millisecond)
	}
	process.killIfRunning()
	t.Fatalf("timed out waiting for recovered repository cycle\nlog:\n%s", process.log())
}

func waitForPlannerProcessCompletion(
	t *testing.T,
	pool *pgxpool.Pool,
	process *detectiveHostProcess,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var consumptions, completedCycles, snapshots, proposals int
		err := pool.QueryRow(context.Background(), `
			SELECT
			  (SELECT COUNT(*) FROM detective_planner_recommendation_consumptions),
			  (SELECT COUNT(*) FROM detective_periodic_source_cycles WHERE status = 'completed'),
			  (SELECT COUNT(*) FROM source_snapshots),
			  (SELECT COUNT(*) FROM proposal_occurrences)
		`).Scan(&consumptions, &completedCycles, &snapshots, &proposals)
		if err == nil && consumptions == 1 && completedCycles == 2 && snapshots == 1 && proposals == 1 {
			return
		}
		if process.cmd.ProcessState != nil && process.cmd.ProcessState.Exited() {
			t.Fatalf("planner host exited before completion:\n%s", process.log())
		}
		time.Sleep(50 * time.Millisecond)
	}
	process.killIfRunning()
	t.Fatalf("timed out waiting for planner host completion\nlog:\n%s", process.log())
}

func waitForProcessSourceOutcomes(
	t *testing.T,
	process *detectiveHostProcess,
	want int,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Count(process.log(), `"event":"source_tick_completed"`) >= want {
			return
		}
		if process.cmd.ProcessState != nil && process.cmd.ProcessState.Exited() {
			t.Fatalf("planner host exited before replay outcomes:\n%s", process.log())
		}
		time.Sleep(50 * time.Millisecond)
	}
	process.killIfRunning()
	t.Fatalf("timed out waiting for %d source outcomes\nlog:\n%s", want, process.log())
}

func assertPlannerProcessCounts(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var consumptions, cycles, cursors, snapshots, proposals int
	if err := pool.QueryRow(context.Background(), `
		SELECT
		  (SELECT COUNT(*) FROM detective_planner_recommendation_consumptions),
		  (SELECT COUNT(*) FROM detective_periodic_source_cycles),
		  (SELECT COUNT(*) FROM detective_periodic_source_cursors),
		  (SELECT COUNT(*) FROM source_snapshots),
		  (SELECT COUNT(*) FROM proposal_occurrences)
	`).Scan(&consumptions, &cycles, &cursors, &snapshots, &proposals); err != nil {
		t.Fatalf("read planner process counts: %v", err)
	}
	if consumptions != 1 || cycles != 2 || cursors != 2 || snapshots != 1 || proposals != 1 {
		t.Fatalf(
			"planner process counts = consumptions %d cycles %d cursors %d snapshots %d proposals %d",
			consumptions,
			cycles,
			cursors,
			snapshots,
			proposals,
		)
	}
}

func assertNoProcessPlannerProviderError(t *testing.T, providerErrors <-chan error) {
	t.Helper()
	select {
	case err := <-providerErrors:
		t.Fatalf("planner provider error: %v", err)
	default:
	}
}

func processAcceptanceDatabase(t *testing.T, databaseURL string) (string, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	schema := "ahe_detective_process_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	schemaURL := processSchemaDatabaseURL(t, databaseURL, schema)
	pool, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatalf("new schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return schemaURL, pool
}

func processSchemaDatabaseURL(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_DNS: %v", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("DATABASE_DNS must be a PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func processAcceptanceRepository(t *testing.T, gitPath string) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", root},
		{"-C", root, "config", "user.name", "Detective Acceptance"},
		{"-C", root, "config", "user.email", "detective@example.invalid"},
	} {
		command := exec.Command(gitPath, args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	files := map[string]string{
		"go.mod":  "module example.com/detective-acceptance\n\ngo 1.26\n",
		"main.go": "package acceptance\n\nfunc Collect() string { return \"evidence\" }\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, args := range [][]string{{"-C", root, "add", "."}, {"-C", root, "commit", "-m", "fixture"}} {
		command := exec.Command(gitPath, args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return root
}

func processSelectedAcceptanceRepository(t *testing.T, gitPath, rootValue string) string {
	t.Helper()
	root, err := filepath.Abs(rootValue)
	if err != nil {
		t.Fatalf("resolve selected acceptance repository: %v", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve selected acceptance repository symlinks: %v", err)
	}
	topLevel := strings.TrimSpace(processCommandOutput(t, gitPath, "-C", root, "rev-parse", "--show-toplevel"))
	topLevel, err = filepath.EvalSymlinks(topLevel)
	if err != nil {
		t.Fatalf("resolve selected acceptance Git top-level: %v", err)
	}
	if topLevel != root {
		t.Fatalf("selected acceptance repository = %q, want exact Git top-level %q", root, topLevel)
	}
	if status := strings.TrimSpace(processCommandOutput(t, gitPath, "-C", root, "status", "--porcelain")); status != "" {
		t.Fatalf("selected acceptance repository must be clean:\n%s", status)
	}
	if head := strings.TrimSpace(processCommandOutput(t, gitPath, "-C", root, "rev-parse", "--verify", "HEAD^{commit}")); head == "" {
		t.Fatal("selected acceptance repository has no committed HEAD")
	}
	return root
}

func processCommandOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

func processPlannerAcceptanceRepository(t *testing.T, gitPath string) string {
	t.Helper()
	root := processAcceptanceRepository(t, gitPath)
	if err := os.WriteFile(
		filepath.Join(root, "requirements.md"),
		[]byte("Planner process requirement remains source backed.\n"),
		0o600,
	); err != nil {
		t.Fatalf("write planner process requirement: %v", err)
	}
	for _, args := range [][]string{
		{"-C", root, "add", "requirements.md"},
		{"-C", root, "commit", "-q", "-m", "add planner source"},
	} {
		if output, err := exec.Command(gitPath, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return root
}

func processAcceptanceConfig(t *testing.T, repositoryRoot string) string {
	t.Helper()
	data, err := json.MarshalIndent(map[string]any{
		"schema_version":          "ahe-detective-host-v1",
		"host_id":                 "process-acceptance",
		"registration_request_id": "register-process-acceptance",
		"workspace_id":            "workspace:process-acceptance",
		"workspace_root":          repositoryRoot,
		"source_interval":         "1s",
		"max_steps":               3,
		"sources": []map[string]any{{
			"capability_name":    detective.SourceCapabilityGitGoRepository,
			"capability_version": detective.SourceCapabilityGitGoRepositoryVersion,
			"source_id":          "repository",
			"relative_path":      ".",
		}},
		"repository_maintenance": map[string]any{
			"enabled": true, "interval": "200ms", "batch_limit": 10,
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "detective.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func processPlannerAcceptanceConfig(t *testing.T, repositoryRoot, baseURL string) string {
	t.Helper()
	data, err := json.MarshalIndent(map[string]any{
		"schema_version":          "ahe-detective-host-v2",
		"host_id":                 "planner-process-acceptance",
		"registration_request_id": "register-planner-process-acceptance",
		"workspace_id":            "workspace:planner-process-acceptance",
		"workspace_root":          repositoryRoot,
		"source_interval":         "1h",
		"max_steps":               3,
		"sources": []map[string]any{
			{
				"capability_name":    detective.SourceCapabilityGitGoRepository,
				"capability_version": detective.SourceCapabilityGitGoRepositoryVersion,
				"source_id":          "repository",
				"relative_path":      ".",
			},
			{
				"capability_name":    detective.SourceCapabilityLocalPRDText,
				"capability_version": detective.SourceCapabilityLocalPRDTextVersion,
				"source_id":          "requirements",
				"relative_path":      "requirements.md",
			},
		},
		"repository_maintenance": map[string]any{
			"enabled": true, "interval": "1h", "batch_limit": 10,
		},
		"planner": map[string]any{
			"enabled": true, "base_url": baseURL, "model": "acceptance-planner",
			"timeout": "5s", "num_predict": 64,
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal planner process config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "detective-planner.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write planner process config: %v", err)
	}
	return path
}

func processPlannerLocalTextChoice(prompt string) (string, error) {
	const marker = "\nInput JSON:\n"
	index := strings.LastIndex(prompt, marker)
	if index < 0 {
		return "", errors.New("planner prompt has no input JSON")
	}
	var input detective.PlannerInput
	if err := json.Unmarshal([]byte(prompt[index+len(marker):]), &input); err != nil {
		return "", fmt.Errorf("decode planner input: %w", err)
	}
	if input.SchemaVersion != detective.PlannerInputSchemaVersion || len(input.ObservedCoverage) != 1 {
		return "", fmt.Errorf("planner input = %+v", input)
	}
	for _, source := range input.Sources {
		if source.CapabilityName == detective.SourceCapabilityLocalPRDText {
			return source.ChoiceKey, nil
		}
	}
	return "", errors.New("planner input has no local-text choice")
}
