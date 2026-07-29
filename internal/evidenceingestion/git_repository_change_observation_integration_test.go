//go:build integration

package evidenceingestion

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationGitRepositoryChangeObservationStabilityAndCoalescing(t *testing.T) {
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	trackedPath := filepath.Join(root, "main.go")
	cleanContent := []byte("package main\n")
	writeGitSnapshotTestFile(t, trackedPath, cleanContent)
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")

	clean := inspectGitRepositoryChangeForObservationTest(t, root)
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	window := 2 * time.Second
	first := recordGitRepositoryChangeObservationForTest(t, ctx, pool, "clean-1", clean, window, base)
	if !first.Changed || first.Coalesced || first.Stable || first.Replayed || first.ObservationNumber != 1 || first.ObservationCount != 1 || first.PreviousObservationID != "" {
		t.Fatalf("first observation = %+v", first)
	}
	if first.FirstObservedAt != base || first.LastObservedAt != base || first.StableAfter != base.Add(window) {
		t.Fatalf("first observation times = %+v", first)
	}

	replay, err := recordGitRepositoryChangeObservation(ctx, pgxDB{pool: pool}, gitRepositoryChangeObservationInput{
		RequestID:       "clean-1",
		StabilityWindow: window,
		Inspection:      clean,
	}, base.Add(10*time.Second))
	if err != nil {
		t.Fatalf("replay recordGitRepositoryChangeObservation() error = %v", err)
	}
	wantReplay := first
	wantReplay.Replayed = true
	if replay != wantReplay {
		t.Fatalf("replayed observation = %+v, want %+v", replay, wantReplay)
	}

	second := recordGitRepositoryChangeObservationForTest(t, ctx, pool, "clean-2", clean, window, base.Add(time.Second))
	if second.Changed || !second.Coalesced || second.Stable || second.ObservationID != first.ObservationID || second.ObservationCount != 2 {
		t.Fatalf("second observation = %+v", second)
	}
	third := recordGitRepositoryChangeObservationForTest(t, ctx, pool, "clean-3", clean, window, base.Add(2*time.Second))
	if third.Changed || !third.Coalesced || !third.Stable || third.ObservationID != first.ObservationID || third.ObservationCount != 3 {
		t.Fatalf("third observation = %+v", third)
	}

	writeGitSnapshotTestFile(t, trackedPath, []byte("package main\n\nfunc Changed() {}\n"))
	dirty := inspectGitRepositoryChangeForObservationTest(t, root)
	if !dirty.Dirty || dirty.ChangeToken == clean.ChangeToken {
		t.Fatalf("dirty inspection = %+v, clean = %+v", dirty, clean)
	}
	dirtyFirst := recordGitRepositoryChangeObservationForTest(t, ctx, pool, "dirty-1", dirty, window, base.Add(3*time.Second))
	if !dirtyFirst.Changed || dirtyFirst.Coalesced || dirtyFirst.Stable || dirtyFirst.ObservationNumber != 2 || dirtyFirst.PreviousObservationID != first.ObservationID || dirtyFirst.ObservationCount != 1 {
		t.Fatalf("first dirty observation = %+v", dirtyFirst)
	}
	dirtyStable := recordGitRepositoryChangeObservationForTest(t, ctx, pool, "dirty-2", dirty, window, base.Add(5*time.Second))
	if dirtyStable.Changed || !dirtyStable.Coalesced || !dirtyStable.Stable || dirtyStable.ObservationID != dirtyFirst.ObservationID || dirtyStable.ObservationCount != 2 {
		t.Fatalf("stable dirty observation = %+v", dirtyStable)
	}

	_, err = recordGitRepositoryChangeObservation(ctx, pgxDB{pool: pool}, gitRepositoryChangeObservationInput{
		RequestID:       "dirty-2",
		StabilityWindow: window,
		Inspection:      clean,
	}, base.Add(6*time.Second))
	assertKind(t, err, ErrorIdempotencyKeyReused)

	writeGitSnapshotTestFile(t, trackedPath, cleanContent)
	cleanAgain := inspectGitRepositoryChangeForObservationTest(t, root)
	if cleanAgain != clean {
		t.Fatalf("restored clean inspection = %+v, want %+v", cleanAgain, clean)
	}
	returned := recordGitRepositoryChangeObservationForTest(t, ctx, pool, "clean-return", cleanAgain, window, base.Add(6*time.Second))
	if !returned.Changed || returned.Coalesced || returned.Stable || returned.ObservationNumber != 3 || returned.ObservationID == first.ObservationID || returned.PreviousObservationID != dirtyFirst.ObservationID {
		t.Fatalf("returned clean observation = %+v", returned)
	}

	_, err = recordGitRepositoryChangeObservation(ctx, pgxDB{pool: pool}, gitRepositoryChangeObservationInput{
		RequestID:       "non-monotonic",
		StabilityWindow: window,
		Inspection:      cleanAgain,
	}, base.Add(4*time.Second))
	assertKind(t, err, ErrorInvalidInput)

	assertTableCount(t, ctx, pool, "repository_change_observations", 3)
	assertTableCount(t, ctx, pool, "repository_change_observation_requests", 6)
	for _, table := range []string{"source_snapshots", "repository_snapshots", "proposal_occurrences", "repository_source_generations"} {
		assertTableCount(t, ctx, pool, table, 0)
	}

	var firstCount, dirtyCount, returnedCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			MAX(observation_count) FILTER (WHERE observation_number = 1),
			MAX(observation_count) FILTER (WHERE observation_number = 2),
			MAX(observation_count) FILTER (WHERE observation_number = 3)
		FROM repository_change_observations
		WHERE repo_id = $1
	`, clean.RepoID).Scan(&firstCount, &dirtyCount, &returnedCount); err != nil {
		t.Fatalf("reading observation counts: %v", err)
	}
	if firstCount != 3 || dirtyCount != 2 || returnedCount != 1 {
		t.Fatalf("observation counts = %d/%d/%d, want 3/2/1", firstCount, dirtyCount, returnedCount)
	}
}

func inspectGitRepositoryChangeForObservationTest(t *testing.T, root string) GitRepositoryChangeInspection {
	t.Helper()
	inspection, err := InspectGitRepositoryChange(t.Context(), GitRepositoryChangeConfig{
		WorkspaceRoot: root,
		RepoID:        "change-observation-integration",
	})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange() error = %v", err)
	}
	return inspection
}

func recordGitRepositoryChangeObservationForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID string, inspection GitRepositoryChangeInspection, window time.Duration, observedAt time.Time) GitRepositoryChangeObservationResult {
	t.Helper()
	result, err := recordGitRepositoryChangeObservation(ctx, pgxDB{pool: pool}, gitRepositoryChangeObservationInput{
		RequestID:       requestID,
		StabilityWindow: window,
		Inspection:      inspection,
	}, observedAt)
	if err != nil {
		t.Fatalf("recordGitRepositoryChangeObservation(%s) error = %v", requestID, err)
	}
	return result
}
