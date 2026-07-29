//go:build integration

package evidenceingestion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRepositorySourceGenerationLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/source-generation\n\ngo 1.22\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc First() {}\nfunc Extra() {}\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "first")

	firstSnapshot, firstRun := captureAndRunRepositoryGenerationFixture(t, ctx, pool, root, "first")
	first := firstRun.SourceGeneration
	if firstRun.SourceGenerationReplayed || first.Number != 1 || first.ExtractorName != ExtractorRepositoryGoParserCodeFact || first.RepositorySnapshotID != firstSnapshot.RepositorySnapshot.ID || first.ProposalBatchID != firstRun.ProposalBatchID || first.ExtractionAttemptID != firstRun.ExtractionAttemptID || first.ProposalCount != firstRun.ProposalCount || first.ExtractorOutputHash != firstRun.OutputHash {
		t.Fatalf("first automatic repository source generation = %+v, run = %+v", first, firstRun)
	}
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
	firstDiscovery, err := ListRepositorySourceGenerations(ctx, pool, RepositorySourceGenerationListInput{
		RepoID:        first.RepoID,
		ExtractorName: first.ExtractorName,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("ListRepositorySourceGenerations(first) error = %v", err)
	}
	assertRepositorySourceGenerationStatuses(t, firstDiscovery, []RepositorySourceGenerationStatus{{
		RepositorySourceGeneration: first,
		Active:                     false,
	}})
	firstDefaultBeforeActivation, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: first.RepositorySnapshotID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(first before activation) error = %v", err)
	}
	if len(firstDefaultBeforeActivation) != 0 {
		t.Fatalf("first default records before activation = %+v, want none", firstDefaultBeforeActivation)
	}
	firstHistoricalBeforeActivation, err := ListProposalRecords(ctx, pool, ProposalListInput{
		SourceGenerationID: first.ID,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(first historical before activation) error = %v", err)
	}
	assertRepositoryGenerationRecords(t, firstHistoricalBeforeActivation, first, false)

	firstReplay, err := CreateRepositorySourceGeneration(ctx, pool, RepositorySourceGenerationCreateInput{ProposalBatchID: firstRun.ProposalBatchID})
	if err != nil {
		t.Fatalf("replay CreateRepositorySourceGeneration(first) error = %v", err)
	}
	if !firstReplay.Replayed || firstReplay.Generation != first {
		t.Fatalf("first generation replay = %+v, want %+v", firstReplay, first)
	}
	conflictingInput, err := BuildRepositoryExtractorInput(ctx, pool, firstSnapshot.RepositorySnapshot.ID)
	if err != nil {
		t.Fatalf("BuildRepositoryExtractorInput(conflicting) error = %v", err)
	}
	_, err = runRepositoryExtractor(ctx, pgxDB{pool: pool}, conflictingInput, "generation-run-conflicting", false, repositoryGoParserExtractorDefinition(), func(ctx context.Context, input RepositoryExtractorInput) (FrozenExtractorOutput, error) {
		output, err := extractRepositoryGoParser(ctx, input)
		slices.Reverse(output.Proposals)
		return output, err
	})
	assertKind(t, err, ErrorSourceGenerationConflict)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)

	firstActivationInput := RepositorySourceGenerationActivationInput{RequestID: "activate-first", SourceGenerationID: first.ID}
	firstActivation, err := ActivateRepositorySourceGeneration(ctx, pool, firstActivationInput)
	if err != nil {
		t.Fatalf("ActivateRepositorySourceGeneration(first) error = %v", err)
	}
	if !firstActivation.Changed || firstActivation.Replayed || firstActivation.PreviousGenerationID != "" || firstActivation.ActivatedGenerationID != first.ID {
		t.Fatalf("first generation activation = %+v", firstActivation)
	}
	assertRepositoryGenerationReconciliation(t, firstActivation.Reconciliation, first.ID, "", 0, first.ProposalCount, 0)
	firstDefaultAfterActivation, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: first.RepositorySnapshotID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(first after activation) error = %v", err)
	}
	assertRepositoryGenerationRecords(t, firstDefaultAfterActivation, first, true)
	assertRepositoryLifecycleState(t, firstDefaultAfterActivation, first.ID, RepositoryProposalLifecycleNew)
	firstActivationReplay, err := ActivateRepositorySourceGeneration(ctx, pool, firstActivationInput)
	if err != nil {
		t.Fatalf("replay ActivateRepositorySourceGeneration(first) error = %v", err)
	}
	if !firstActivationReplay.Replayed || firstActivationReplay.Changed != firstActivation.Changed || firstActivationReplay.PreviousGenerationID != firstActivation.PreviousGenerationID || firstActivationReplay.ActivatedGenerationID != firstActivation.ActivatedGenerationID || firstActivationReplay.Reconciliation != firstActivation.Reconciliation {
		t.Fatalf("first activation replay = %+v, want %+v", firstActivationReplay, firstActivation)
	}

	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Second() {}\n"))
	runGitSnapshotTestCommand(t, root, "add", "main.go")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "second")
	secondSnapshot, secondRun := captureAndRunRepositoryGenerationFixture(t, ctx, pool, root, "second")
	second := secondRun.SourceGeneration
	if secondRun.SourceGenerationReplayed || second.Number != 2 || second.RepositorySnapshotID != secondSnapshot.RepositorySnapshot.ID {
		t.Fatalf("second automatic repository source generation = %+v, run = %+v", second, secondRun)
	}
	assertRepositorySourceHead(t, ctx, pool, first.RepoID, first.ExtractorName, first.ID)
	secondDiscovery, err := ListRepositorySourceGenerations(ctx, pool, RepositorySourceGenerationListInput{
		RepoID:        second.RepoID,
		ExtractorName: second.ExtractorName,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("ListRepositorySourceGenerations(second) error = %v", err)
	}
	assertRepositorySourceGenerationStatuses(t, secondDiscovery, []RepositorySourceGenerationStatus{
		{RepositorySourceGeneration: second, Active: false},
		{RepositorySourceGeneration: first, Active: true},
	})
	limitedDiscovery, err := ListRepositorySourceGenerations(ctx, pool, RepositorySourceGenerationListInput{
		RepoID:        second.RepoID,
		ExtractorName: second.ExtractorName,
		Limit:         1,
	})
	if err != nil {
		t.Fatalf("ListRepositorySourceGenerations(limited) error = %v", err)
	}
	assertRepositorySourceGenerationStatuses(t, limitedDiscovery, []RepositorySourceGenerationStatus{
		{RepositorySourceGeneration: second, Active: false},
	})
	secondDefaultBeforeActivation, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: second.RepositorySnapshotID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(second before activation) error = %v", err)
	}
	if len(secondDefaultBeforeActivation) != 0 {
		t.Fatalf("second default records before activation = %+v, want none", secondDefaultBeforeActivation)
	}
	secondHistoricalBeforeActivation, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: second.RepositorySnapshotID,
		SourceGenerationID:   second.ID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(second historical before activation) error = %v", err)
	}
	assertRepositoryGenerationRecords(t, secondHistoricalBeforeActivation, second, false)
	firstStillActive, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: first.RepositorySnapshotID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(first while second candidate) error = %v", err)
	}
	assertRepositoryGenerationRecords(t, firstStillActive, first, true)

	secondActivation, err := ActivateRepositorySourceGeneration(ctx, pool, RepositorySourceGenerationActivationInput{RequestID: "activate-second", SourceGenerationID: second.ID})
	if err != nil {
		t.Fatalf("ActivateRepositorySourceGeneration(second) error = %v", err)
	}
	if !secondActivation.Changed || secondActivation.PreviousGenerationID != first.ID || secondActivation.ActivatedGenerationID != second.ID {
		t.Fatalf("second generation activation = %+v", secondActivation)
	}
	assertRepositoryGenerationReconciliation(t, secondActivation.Reconciliation, second.ID, first.ID, 0, second.ProposalCount, first.ProposalCount)
	assertRepositorySourceHead(t, ctx, pool, second.RepoID, second.ExtractorName, second.ID)
	activatedDiscovery, err := ListRepositorySourceGenerations(ctx, pool, RepositorySourceGenerationListInput{
		RepoID:        second.RepoID,
		ExtractorName: second.ExtractorName,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("ListRepositorySourceGenerations(activated) error = %v", err)
	}
	assertRepositorySourceGenerationStatuses(t, activatedDiscovery, []RepositorySourceGenerationStatus{
		{RepositorySourceGeneration: second, Active: true},
		{RepositorySourceGeneration: first, Active: false},
	})
	firstDefaultAfterSwitch, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: first.RepositorySnapshotID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(first after head switch) error = %v", err)
	}
	if len(firstDefaultAfterSwitch) != 0 {
		t.Fatalf("first default records after head switch = %+v, want none", firstDefaultAfterSwitch)
	}
	secondDefaultAfterSwitch, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: second.RepositorySnapshotID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(second after head switch) error = %v", err)
	}
	assertRepositoryGenerationRecords(t, secondDefaultAfterSwitch, second, true)
	assertRepositoryLifecycleState(t, secondDefaultAfterSwitch, second.ID, RepositoryProposalLifecycleNew)
	firstHistoricalAfterSwitch, err := ListProposalRecords(ctx, pool, ProposalListInput{
		SourceGenerationID: first.ID,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(first historical after head switch) error = %v", err)
	}
	assertRepositoryGenerationRecords(t, firstHistoricalAfterSwitch, first, false)
	assertRepositoryLifecycleState(t, firstHistoricalAfterSwitch, second.ID, RepositoryProposalLifecycleStale)
	firstExactAfterSwitch, err := GetProposalByOccurrenceID(ctx, pool, firstRun.ProposalOccurrenceIDs[0])
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID(first historical) error = %v", err)
	}
	if firstExactAfterSwitch.SourceGeneration == nil || *firstExactAfterSwitch.SourceGeneration != first || firstExactAfterSwitch.SourceGenerationActive {
		t.Fatalf("first exact historical lifecycle = generation %+v active %t", firstExactAfterSwitch.SourceGeneration, firstExactAfterSwitch.SourceGenerationActive)
	}
	assertRepositoryLifecycle(t, firstExactAfterSwitch, second.ID, RepositoryProposalLifecycleStale)

	_, err = ActivateRepositorySourceGeneration(ctx, pool, RepositorySourceGenerationActivationInput{RequestID: "activate-older", SourceGenerationID: first.ID})
	assertKind(t, err, ErrorSourceGenerationConflict)
	assertRepositorySourceHead(t, ctx, pool, second.RepoID, second.ExtractorName, second.ID)

	_, err = ActivateRepositorySourceGeneration(ctx, pool, RepositorySourceGenerationActivationInput{RequestID: "activate-second", SourceGenerationID: first.ID})
	assertKind(t, err, ErrorIdempotencyKeyReused)
	assertRepositorySourceHead(t, ctx, pool, second.RepoID, second.ExtractorName, second.ID)

	firstRunAgain, err := RunRepositoryGoParserExtractor(ctx, pool, RepositoryGoParserRequest{
		RequestID:            "generation-run-first-again",
		RepositorySnapshotID: firstSnapshot.RepositorySnapshot.ID,
	})
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor(first again) error = %v", err)
	}
	if firstRunAgain.Replayed || !firstRunAgain.SourceGenerationReplayed || firstRunAgain.SourceGeneration.ID != first.ID || firstRunAgain.SourceGeneration.ProposalBatchID != first.ProposalBatchID {
		t.Fatalf("same snapshot automatic generation = %+v, want replay of %+v", firstRunAgain, first)
	}

	upgradedInput, err := BuildRepositoryExtractorInput(ctx, pool, secondSnapshot.RepositorySnapshot.ID)
	if err != nil {
		t.Fatalf("BuildRepositoryExtractorInput(upgraded) error = %v", err)
	}
	upgradedDefinition := repositoryGoParserExtractorDefinition()
	upgradedDefinition.Version = "v2"
	upgradedRun, err := runRepositoryExtractor(ctx, pgxDB{pool: pool}, upgradedInput, "generation-run-upgraded", false, upgradedDefinition, extractRepositoryGoParser)
	if err != nil {
		t.Fatalf("runRepositoryExtractor(upgraded) error = %v", err)
	}
	upgraded := upgradedRun.SourceGeneration
	if upgradedRun.SourceGenerationReplayed || upgraded.Number != 3 || upgraded.ExtractorName != first.ExtractorName || upgraded.ExtractorDefinitionID == first.ExtractorDefinitionID {
		t.Fatalf("upgraded automatic extractor generation = %+v, first = %+v", upgradedRun, first)
	}
	assertRepositorySourceHead(t, ctx, pool, second.RepoID, second.ExtractorName, second.ID)
	upgradedActivation, err := ActivateRepositorySourceGeneration(ctx, pool, RepositorySourceGenerationActivationInput{RequestID: "activate-upgraded", SourceGenerationID: upgraded.ID})
	if err != nil {
		t.Fatalf("ActivateRepositorySourceGeneration(upgraded) error = %v", err)
	}
	if !upgradedActivation.Changed || upgradedActivation.PreviousGenerationID != second.ID || upgradedActivation.ActivatedGenerationID != upgraded.ID {
		t.Fatalf("upgraded generation activation = %+v", upgradedActivation)
	}
	assertRepositoryGenerationReconciliation(t, upgradedActivation.Reconciliation, upgraded.ID, second.ID, upgraded.ProposalCount, 0, 0)
	assertRepositorySourceHead(t, ctx, pool, upgraded.RepoID, upgraded.ExtractorName, upgraded.ID)
	secondHistoricalAfterUpgrade, err := ListProposalRecords(ctx, pool, ProposalListInput{SourceGenerationID: second.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListProposalRecords(second after upgrade) error = %v", err)
	}
	assertRepositoryLifecycleState(t, secondHistoricalAfterUpgrade, upgraded.ID, RepositoryProposalLifecycleUnchanged)
	upgradedDefault, err := ListProposalRecords(ctx, pool, ProposalListInput{RepositorySnapshotID: upgraded.RepositorySnapshotID, Limit: 10})
	if err != nil {
		t.Fatalf("ListProposalRecords(upgraded active) error = %v", err)
	}
	assertRepositoryGenerationRecords(t, upgradedDefault, upgraded, true)
	assertRepositoryLifecycleState(t, upgradedDefault, upgraded.ID, RepositoryProposalLifecycleUnchanged)

	writeGitSnapshotTestFile(t, filepath.Join(root, "broken.go"), []byte("package sample\n\nfunc Broken( {\n"))
	runGitSnapshotTestCommand(t, root, "add", "broken.go")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "broken")
	brokenSnapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "source-generation",
		CommitSHA:     gitSnapshotTestOutput(t, root, "rev-parse", "HEAD"),
		RequestID:     "generation-snapshot-broken",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot(broken) error = %v", err)
	}
	_, err = RunRepositoryGoParserExtractor(ctx, pool, RepositoryGoParserRequest{
		RequestID:            "generation-run-broken",
		RepositorySnapshotID: brokenSnapshot.RepositorySnapshot.ID,
	})
	assertKind(t, err, ErrorInvalidInput)
	assertRepositorySourceHead(t, ctx, pool, upgraded.RepoID, upgraded.ExtractorName, upgraded.ID)
	assertTableCount(t, ctx, pool, "repository_source_generations", 3)
	assertTableCount(t, ctx, pool, "repository_generation_reconciliations", 3)

	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatalf("remove main.go: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "broken.go")); err != nil {
		t.Fatalf("remove broken.go: %v", err)
	}
	runGitSnapshotTestCommand(t, root, "add", "-A")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "empty")
	emptySnapshot, emptyRun := captureAndRunRepositoryGenerationFixture(t, ctx, pool, root, "empty")
	empty := emptyRun.SourceGeneration
	if emptyRun.ProposalCount != 0 || empty.ProposalCount != 0 || empty.Number != 4 || empty.RepositorySnapshotID != emptySnapshot.RepositorySnapshot.ID {
		t.Fatalf("empty automatic repository source generation = %+v, run = %+v", empty, emptyRun)
	}
	emptyActivation, err := ActivateRepositorySourceGeneration(ctx, pool, RepositorySourceGenerationActivationInput{RequestID: "activate-empty", SourceGenerationID: empty.ID})
	if err != nil {
		t.Fatalf("ActivateRepositorySourceGeneration(empty) error = %v", err)
	}
	assertRepositoryGenerationReconciliation(t, emptyActivation.Reconciliation, empty.ID, upgraded.ID, 0, 0, upgraded.ProposalCount)
	assertRepositorySourceHead(t, ctx, pool, empty.RepoID, empty.ExtractorName, empty.ID)
	emptyDefault, err := ListProposalRecords(ctx, pool, ProposalListInput{RepositorySnapshotID: empty.RepositorySnapshotID, Limit: 10})
	if err != nil {
		t.Fatalf("ListProposalRecords(empty active) error = %v", err)
	}
	if len(emptyDefault) != 0 {
		t.Fatalf("empty active generation records = %+v, want none", emptyDefault)
	}
	upgradedHistoricalAfterEmpty, err := ListProposalRecords(ctx, pool, ProposalListInput{SourceGenerationID: upgraded.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListProposalRecords(upgraded after empty) error = %v", err)
	}
	assertRepositoryLifecycleState(t, upgradedHistoricalAfterEmpty, empty.ID, RepositoryProposalLifecycleStale)

	assertTableCount(t, ctx, pool, "repository_source_generations", 4)
	assertTableCount(t, ctx, pool, "repository_source_heads", 1)
	assertTableCount(t, ctx, pool, "repository_generation_activation_requests", 4)
	assertTableCount(t, ctx, pool, "repository_generation_reconciliations", 4)
	assertTableCount(t, ctx, pool, "repository_generation_proposal_reconciliations", 2*first.ProposalCount+3*second.ProposalCount)
}

func assertRepositorySourceGenerationStatuses(
	t *testing.T,
	got []RepositorySourceGenerationStatus,
	want []RepositorySourceGenerationStatus,
) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("repository source generations = %+v, want %+v", got, want)
	}
}

func captureAndRunRepositoryGenerationFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, root, suffix string) (RepositorySnapshotCaptureResult, RepositoryIngestResult) {
	t.Helper()
	snapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "source-generation",
		CommitSHA:     gitSnapshotTestOutput(t, root, "rev-parse", "HEAD"),
		RequestID:     "generation-snapshot-" + suffix,
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot(%s) error = %v", suffix, err)
	}
	result, err := RunRepositoryGoParserExtractor(ctx, pool, RepositoryGoParserRequest{
		RequestID:            "generation-run-" + suffix,
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
	})
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor(%s) error = %v", suffix, err)
	}
	return snapshot, result
}

func assertRepositorySourceHead(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoID, extractorName, wantGenerationID string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `
		SELECT active_generation_id
		FROM repository_source_heads
		WHERE repo_id = $1 AND extractor_name = $2
	`, repoID, extractorName).Scan(&got); err != nil {
		t.Fatalf("reading repository source head: %v", err)
	}
	if got != wantGenerationID {
		t.Fatalf("repository source head = %s, want %s", got, wantGenerationID)
	}
}

func assertRepositoryGenerationRecords(t *testing.T, records []ProposalQueryResult, generation RepositorySourceGeneration, active bool) {
	t.Helper()
	if len(records) != generation.ProposalCount {
		t.Fatalf("generation %s records = %d, want %d: %+v", generation.ID, len(records), generation.ProposalCount, records)
	}
	for _, record := range records {
		if record.SourceGeneration == nil || *record.SourceGeneration != generation || record.SourceGenerationActive != active {
			t.Fatalf("record %s lifecycle = generation %+v active %t, want %+v active %t", record.ProposalOccurrenceID, record.SourceGeneration, record.SourceGenerationActive, generation, active)
		}
	}
}

func assertRepositoryGenerationReconciliation(
	t *testing.T,
	got RepositoryGenerationReconciliation,
	generationID, previousGenerationID string,
	unchangedCount, newCount, staleCount int,
) {
	t.Helper()
	want := RepositoryGenerationReconciliation{
		SourceGenerationID:   generationID,
		PreviousGenerationID: previousGenerationID,
		IdentityContract:     RepositoryProposalIdentityContractV1,
		UnchangedCount:       unchangedCount,
		NewCount:             newCount,
		StaleCount:           staleCount,
	}
	if got != want {
		t.Fatalf("repository generation reconciliation = %+v, want %+v", got, want)
	}
}

func assertRepositoryLifecycleState(t *testing.T, records []ProposalQueryResult, generationID, state string) {
	t.Helper()
	for _, record := range records {
		assertRepositoryLifecycle(t, record, generationID, state)
	}
}

func assertRepositoryLifecycle(t *testing.T, record ProposalQueryResult, generationID, state string) {
	t.Helper()
	lifecycle := record.RepositoryLifecycle
	if lifecycle == nil || lifecycle.SourceGenerationID != generationID || lifecycle.State != state || lifecycle.IdentityContract != RepositoryProposalIdentityContractV1 || lifecycle.ProposalIdentity == "" {
		t.Fatalf("record %s repository lifecycle = %+v, want generation %s state %s", record.ProposalOccurrenceID, lifecycle, generationID, state)
	}
	switch state {
	case RepositoryProposalLifecycleNew:
		if lifecycle.CurrentProposalOccurrenceID != record.ProposalOccurrenceID || lifecycle.PreviousProposalOccurrenceID != "" {
			t.Fatalf("record %s new lifecycle occurrence binding = %+v", record.ProposalOccurrenceID, lifecycle)
		}
	case RepositoryProposalLifecycleUnchanged:
		if lifecycle.CurrentProposalOccurrenceID == "" || lifecycle.PreviousProposalOccurrenceID == "" || (lifecycle.CurrentProposalOccurrenceID != record.ProposalOccurrenceID && lifecycle.PreviousProposalOccurrenceID != record.ProposalOccurrenceID) {
			t.Fatalf("record %s unchanged lifecycle occurrence binding = %+v", record.ProposalOccurrenceID, lifecycle)
		}
	case RepositoryProposalLifecycleStale:
		if lifecycle.CurrentProposalOccurrenceID != "" || lifecycle.PreviousProposalOccurrenceID != record.ProposalOccurrenceID {
			t.Fatalf("record %s stale lifecycle occurrence binding = %+v", record.ProposalOccurrenceID, lifecycle)
		}
	}
}
