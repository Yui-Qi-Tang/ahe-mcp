package evidenceingestion

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestExtractRepositoryGoParserUsesUniqueLocalIDsAcrossFiles(t *testing.T) {
	input := repositoryParserTestInput(t)
	output, err := extractRepositoryGoParser(context.Background(), input)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser() error = %v", err)
	}
	if len(output.Proposals) != 2 {
		t.Fatalf("proposal count = %d, want 2", len(output.Proposals))
	}
	if output.Proposals[0].ProposalLocalID == output.Proposals[1].ProposalLocalID {
		t.Fatalf("proposal local IDs collide: %q", output.Proposals[0].ProposalLocalID)
	}

	definition, err := buildExtractorDefinition(repositoryGoParserExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:test", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	batch, err := materializeRepositoryBatch(attemptCtx, output)
	if err != nil {
		t.Fatalf("materializeRepositoryBatch() error = %v", err)
	}
	if len(batch.Occurrences) != 2 {
		t.Fatalf("occurrence count = %d, want 2", len(batch.Occurrences))
	}
	for _, occurrence := range batch.Occurrences {
		if len(occurrence.SourceRefs) != 1 {
			t.Fatalf("source refs = %+v, want one line ref", occurrence.SourceRefs)
		}
		ref := occurrence.SourceRefs[0]
		if ref.TargetKind != "file_snapshot" || ref.RepositorySnapshotID != input.RepositorySnapshot.ID || ref.FileSnapshotID == "" || ref.ExtractionViewID != "" {
			t.Fatalf("repository source ref = %+v", ref)
		}
	}

	var observed []string
	profiled, err := materializeRepositoryBatchProfiled(
		attemptCtx,
		output,
		func(subphase repositoryExtractionSubphase, _ time.Duration, succeeded bool) {
			if !succeeded {
				t.Fatalf("subphase %s failed", subphase)
			}
			observed = append(observed, string(subphase))
		},
	)
	if err != nil {
		t.Fatalf("materializeRepositoryBatchProfiled() error = %v", err)
	}
	if !reflect.DeepEqual(profiled, batch) {
		t.Fatalf("profiled batch differs:\nprofiled=%+v\nbatch=%+v", profiled, batch)
	}
	wantSubphases := []string{"batch_coverage_index", "batch_occurrences", "batch_output_hash"}
	if !reflect.DeepEqual(observed, wantSubphases) {
		t.Fatalf("observed subphases = %v, want %v", observed, wantSubphases)
	}

	type occurrenceSample struct {
		name      string
		duration  time.Duration
		count     int
		succeeded bool
	}
	var occurrenceSamples []occurrenceSample
	occurrenceProfiled, err := materializeRepositoryBatchOccurrenceProfiled(
		attemptCtx,
		output,
		nil,
		func(component repositoryOccurrenceComponent, duration time.Duration, count int, succeeded bool) {
			occurrenceSamples = append(occurrenceSamples, occurrenceSample{
				name:      string(component),
				duration:  duration,
				count:     count,
				succeeded: succeeded,
			})
		},
	)
	if err != nil {
		t.Fatalf("materializeRepositoryBatchOccurrenceProfiled() error = %v", err)
	}
	if !reflect.DeepEqual(occurrenceProfiled, batch) {
		t.Fatalf("occurrence-profiled batch differs:\nprofiled=%+v\nbatch=%+v", occurrenceProfiled, batch)
	}
	wantOccurrenceSamples := []occurrenceSample{
		{name: "fact_resolution_verification", count: 2, succeeded: true},
		{name: "relation_resolution_verification", count: 0, succeeded: true},
		{name: "fingerprint_generation", count: 2, succeeded: true},
		{name: "occurrence_assembly", count: 2, succeeded: true},
	}
	if len(occurrenceSamples) != len(wantOccurrenceSamples) {
		t.Fatalf("occurrence samples = %+v, want %+v", occurrenceSamples, wantOccurrenceSamples)
	}
	for index, sample := range occurrenceSamples {
		want := wantOccurrenceSamples[index]
		if sample.name != want.name || sample.count != want.count || sample.succeeded != want.succeeded {
			t.Fatalf("occurrence sample %d = %+v, want %+v", index, sample, want)
		}
		if sample.duration < 0 {
			t.Fatalf("occurrence sample %d has negative duration %s", index, sample.duration)
		}
	}

	failedOutput := output
	failedOutput.Proposals = append([]ExtractorProposalOutput(nil), output.Proposals...)
	failedOutput.Proposals[1].ProposalLocalID = failedOutput.Proposals[0].ProposalLocalID
	emissions := 0
	_, err = materializeRepositoryBatchOccurrenceProfiled(
		attemptCtx,
		failedOutput,
		nil,
		func(repositoryOccurrenceComponent, time.Duration, int, bool) {
			emissions++
		},
	)
	assertKind(t, err, ErrorDuplicateProposalLocalID)
	if emissions != 0 {
		t.Fatalf("failed occurrence profile emissions = %d, want 0", emissions)
	}
}

func TestRunRepositoryGoParserValidatesBeforeDatabaseMutation(t *testing.T) {
	var db sqlDB
	_, err := runRepositoryGoParserExtractor(context.Background(), db, RepositoryGoParserRequest{})
	assertKind(t, err, ErrorInvalidInput)
}

func TestRepositoryGroundingIndexMatchesLegacyMaterialization(t *testing.T) {
	tests := []struct {
		name  string
		build func(*testing.T) (RepositoryExtractorInput, FrozenExtractorOutput, ExtractorDefinitionInput)
	}{
		{
			name: "facts",
			build: func(t *testing.T) (RepositoryExtractorInput, FrozenExtractorOutput, ExtractorDefinitionInput) {
				input := repositoryParserTestInput(t)
				output, err := extractRepositoryGoParser(context.Background(), input)
				if err != nil {
					t.Fatalf("extractRepositoryGoParser() error = %v", err)
				}
				return input, output, repositoryGoParserExtractorDefinition()
			},
		},
		{
			name: "cross-file definition",
			build: func(t *testing.T) (RepositoryExtractorInput, FrozenExtractorOutput, ExtractorDefinitionInput) {
				input, proposal := repositoryCodeDefinitionTestFixture(t)
				return input, repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, nil), (&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition()
			},
		},
		{
			name: "same-file definition",
			build: func(t *testing.T) (RepositoryExtractorInput, FrozenExtractorOutput, ExtractorDefinitionInput) {
				input, proposal := repositorySameFileDefinitionTestFixture(t)
				return input, repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, []ExtractorProposalOutput{proposal}), (&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition()
			},
		},
		{
			name: "cross-file call",
			build: func(t *testing.T) (RepositoryExtractorInput, FrozenExtractorOutput, ExtractorDefinitionInput) {
				input, proposal := repositoryCodeCallTestFixture(t)
				return input, repositoryGoplsTestOutputWithCalls(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal}), (&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition()
			},
		},
		{
			name: "same-file call",
			build: func(t *testing.T) (RepositoryExtractorInput, FrozenExtractorOutput, ExtractorDefinitionInput) {
				input, proposal := repositorySameFileCallTestFixture(t)
				return input, repositoryGoplsTestOutputWithCallCoverage(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal}, []ExtractorProposalOutput{proposal}), (&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, output, definitionInput := test.build(t)
			attemptCtx := repositoryGroundingTestAttempt(t, input, definitionInput, test.name)
			indexed, err := materializeRepositoryBatch(attemptCtx, output)
			if err != nil {
				t.Fatalf("materializeRepositoryBatch() error = %v", err)
			}
			legacy, err := materializeRepositoryBatchLegacy(attemptCtx, output)
			if err != nil {
				t.Fatalf("materializeRepositoryBatchLegacy() error = %v", err)
			}
			if !reflect.DeepEqual(indexed, legacy) {
				t.Fatalf("indexed batch differs from legacy:\nindexed=%+v\nlegacy=%+v", indexed, legacy)
			}
		})
	}
}

func TestRepositoryGroundingIndexMatchesLegacyFailure(t *testing.T) {
	input, proposal := repositoryCodeCallTestFixture(t)
	proposal.CodeRelation.Usage.FileContentHash = "sha256:tampered"
	output := repositoryGoplsTestOutputWithCalls(
		t,
		input,
		[]ExtractorProposalOutput{proposal},
		nil,
		[]ExtractorProposalOutput{proposal},
	)
	attemptCtx := repositoryGroundingTestAttempt(
		t,
		input,
		(&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition(),
		"failure",
	)
	_, indexedErr := materializeRepositoryBatch(attemptCtx, output)
	_, legacyErr := materializeRepositoryBatchLegacy(attemptCtx, output)
	assertKind(t, indexedErr, ErrorQuotedHashMismatch)
	assertKind(t, legacyErr, ErrorQuotedHashMismatch)
	if indexedErr.Error() != legacyErr.Error() {
		t.Fatalf("indexed error = %q, legacy error = %q", indexedErr, legacyErr)
	}
}

func TestRepositoryGroundingIndexReusesFileDerivations(t *testing.T) {
	input, proposal := repositoryCodeCallTestFixture(t)
	output := repositoryGoplsTestOutputWithCalls(
		t,
		input,
		[]ExtractorProposalOutput{proposal},
		nil,
		[]ExtractorProposalOutput{proposal},
	)
	attemptCtx := repositoryGroundingTestAttempt(
		t,
		input,
		(&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition(),
		"reuse",
	)
	filesByPath := make(map[string]RepositoryExtractorFile, len(input.Files))
	for _, file := range input.Files {
		filesByPath[file.FileSnapshot.Path] = file
	}
	index := newRepositoryGroundingIndex(input, filesByPath)
	if _, err := materializeRepositoryOccurrencesGrounded(
		attemptCtx,
		output.Proposals,
		filesByPath,
		nil,
		index,
	); err != nil {
		t.Fatalf("materializeRepositoryOccurrencesGrounded() error = %v", err)
	}
	if len(index.lineSpansByPath) != 2 ||
		len(index.fileBytesByPath) != 2 ||
		len(index.codeFactsByPath) != 2 ||
		len(index.goSyntaxByPath) != 1 {
		t.Fatalf(
			"grounding cache sizes = spans %d bytes %d facts %d syntax %d, want 2/2/2/1",
			len(index.lineSpansByPath),
			len(index.fileBytesByPath),
			len(index.codeFactsByPath),
			len(index.goSyntaxByPath),
		)
	}
}

func TestRepositoryGroundingIndexDoesNotParseUnusedFile(t *testing.T) {
	input := repositoryParserTestInput(t)
	output, err := extractRepositoryGoParser(
		context.Background(),
		RepositoryExtractorInput{
			RepositorySnapshot: input.RepositorySnapshot,
			Files:              input.Files[:1],
		},
	)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser() error = %v", err)
	}
	unused := input.Files[1]
	unused.Content = []byte("not valid Go")
	unused.FileSnapshot.BlobHash = contentHash(unused.Content)
	unused.FileSnapshot.ByteLength = len(unused.Content)
	input.Files[1] = unused
	attemptCtx := repositoryGroundingTestAttempt(
		t,
		input,
		repositoryGoParserExtractorDefinition(),
		"unused-file",
	)
	if _, err := materializeRepositoryBatch(attemptCtx, output); err != nil {
		t.Fatalf("materializeRepositoryBatch() parsed unused file: %v", err)
	}
}

func TestExtractRepositoryGoParserHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := extractRepositoryGoParser(ctx, repositoryParserTestInput(t))
	if err != context.Canceled {
		t.Fatalf("extractRepositoryGoParser() error = %v, want context.Canceled", err)
	}
}

func repositoryParserTestInput(t *testing.T) RepositoryExtractorInput {
	t.Helper()
	snapshot := RepositorySnapshot{ID: "repo-snapshot:test", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 2}
	contents := [][]byte{
		[]byte("package alpha\n\nfunc Run() {}\n"),
		[]byte("package beta\n\nfunc Run() {}\n"),
	}
	paths := []string{"alpha.go", "beta.go"}
	files := make([]RepositoryExtractorFile, 0, len(paths))
	for i, path := range paths {
		files = append(files, RepositoryExtractorFile{
			FileSnapshot: SourceFileSnapshot{
				ID:                   "file:" + path,
				RepositorySnapshotID: snapshot.ID,
				RepoID:               snapshot.RepoID,
				CommitSHA:            snapshot.CommitSHA,
				Path:                 path,
				BlobHash:             contentHash(contents[i]),
				ByteLength:           len(contents[i]),
			},
			Content: contents[i],
		})
	}
	return RepositoryExtractorInput{RepositorySnapshot: snapshot, Files: files}
}

func repositoryGroundingTestAttempt(
	t *testing.T,
	input RepositoryExtractorInput,
	definitionInput ExtractorDefinitionInput,
	name string,
) repositoryAttemptContext {
	t.Helper()
	definition, err := buildExtractorDefinition(definitionInput)
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{
		ID:                    "run:grounding-" + name,
		ExtractorDefinitionID: definition.ID,
		RepositorySnapshotID:  input.RepositorySnapshot.ID,
	}
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	return attemptCtx
}
