package evidenceingestion

import (
	"context"
	"errors"
	"testing"
)

func TestMockSQLRunTrustedExtractorSuccessAndReplay(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("trusted-success-source"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	outputData := mustJSONBytes(t, testFixture())
	calls := 0
	runner := func(_ context.Context, input ExtractorInput) ([]byte, error) {
		calls++
		if input.ExtractionViewID != source.ExtractionViewID {
			t.Fatalf("runner input view = %q, want %q", input.ExtractionViewID, source.ExtractionViewID)
		}
		return outputData, nil
	}

	request := TrustedExtractorRequest{
		RequestID:        "trusted-success",
		ExtractionViewID: source.ExtractionViewID,
	}
	first, err := runTrustedExtractor(ctx, db, request, runner)
	if err != nil {
		t.Fatalf("runTrustedExtractor() error = %v", err)
	}
	second, err := runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		calls++
		return nil, errors.New("runner should not be called on replay")
	})
	if err != nil {
		t.Fatalf("replay runTrustedExtractor() error = %v", err)
	}

	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
	if !second.Replayed {
		t.Fatalf("second Replayed = false, want true")
	}
	if first.ProposalOccurrenceID != second.ProposalOccurrenceID {
		t.Fatalf("replay occurrence = %q, want %q", second.ProposalOccurrenceID, first.ProposalOccurrenceID)
	}
	if first.ProposalCount != 1 || second.ProposalCount != 1 {
		t.Fatalf("proposal counts = %d/%d, want 1/1", first.ProposalCount, second.ProposalCount)
	}
	if len(db.extractionAttempts) != 1 || len(db.proposalBatches) != 1 || len(db.proposalOccurrences) != 1 {
		t.Fatalf("counts = attempts %d batches %d occurrences %d, want 1/1/1",
			len(db.extractionAttempts), len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func TestMockSQLRunTrustedExtractorAbstentionSuccessAndReplay(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("trusted-abstention-source"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	outputData := []byte(`{"proposals":[]}`)
	calls := 0
	request := TrustedExtractorRequest{
		RequestID:        "trusted-abstention",
		ExtractionViewID: source.ExtractionViewID,
	}

	first, err := runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		calls++
		return outputData, nil
	})
	if err != nil {
		t.Fatalf("runTrustedExtractor() error = %v", err)
	}
	second, err := runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		calls++
		return nil, errors.New("runner should not be called on abstention replay")
	})
	if err != nil {
		t.Fatalf("replay runTrustedExtractor() error = %v", err)
	}

	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
	if first.ProposalCount != 0 || first.ProposalOccurrenceID != "" {
		t.Fatalf("first result = %+v, want zero-proposal success", first)
	}
	if !second.Replayed || second.ProposalCount != 0 || second.ProposalOccurrenceID != "" {
		t.Fatalf("replayed result = %+v, want replayed zero-proposal success", second)
	}
	if len(db.extractionAttempts) != 1 || len(db.proposalBatches) != 1 || len(db.proposalOccurrences) != 0 {
		t.Fatalf("counts = attempts %d batches %d occurrences %d, want 1/1/0",
			len(db.extractionAttempts), len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func TestMockSQLRunTrustedExtractorRunnerFailurePersistsAttemptOnly(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("trusted-failure-source"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:        "trusted-failure",
		ExtractionViewID: source.ExtractionViewID,
	}

	_, err = runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return nil, errors.New("local runner exited")
	})
	assertKind(t, err, ErrorRunnerInvocationFailed)
	assertTrustedAttemptFailedOnly(t, ctx, db, source.ExtractionViewID, request.RequestID, 1, ErrorRunnerInvocationFailed)

	calls := 0
	_, err = runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		calls++
		return mustJSONBytes(t, testFixture()), nil
	})
	assertKind(t, err, ErrorPersistedAttemptFailed)
	if calls != 0 {
		t.Fatalf("runner calls after failed replay = %d, want 0", calls)
	}
}

func TestMockSQLRunTrustedExtractorCancellationPersistsFailedAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := newMockSQLDB()
	source, err := captureManualSource(context.Background(), db, testManualInput("trusted-cancel-source"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:        "trusted-cancel",
		ExtractionViewID: source.ExtractionViewID,
	}

	_, err = runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		cancel()
		return nil, context.Canceled
	})
	assertKind(t, err, ErrorRunnerInvocationCancelled)
	assertTrustedAttemptFailedOnly(t, context.Background(), db, source.ExtractionViewID, request.RequestID, 1, ErrorRunnerInvocationCancelled)
}

func TestMockSQLRunTrustedExtractorInvalidOutputPersistsAttemptOnly(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		output []byte
	}{
		{
			name:   "invalid-json",
			output: []byte(`{"proposals":[`),
		},
		{
			name:   "forbidden-source-refs",
			output: []byte(`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"],"source_refs":[{"span_id":"span:S1"}]}]}`),
		},
		{
			name:   "forbidden-canonical-id",
			output: []byte(`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"],"canonical_id":"evidence:1"}]}`),
		},
		{
			name:   "extra-document",
			output: []byte(`{"proposals":[]} {"proposals":[]}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newMockSQLDB()
			source, err := captureManualSource(ctx, db, testManualInput("trusted-invalid-"+tc.name))
			if err != nil {
				t.Fatalf("captureManualSource() error = %v", err)
			}
			request := TrustedExtractorRequest{
				RequestID:        "trusted-invalid-" + tc.name,
				ExtractionViewID: source.ExtractionViewID,
			}
			_, err = runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
				return tc.output, nil
			})
			assertKind(t, err, ErrorInvalidExtractorOutput)
			assertTrustedAttemptFailedOnly(t, ctx, db, source.ExtractionViewID, request.RequestID, 1, ErrorInvalidExtractorOutput)
		})
	}
}

func TestMockSQLRunTrustedExtractorMaterializationFailureUsesDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := newMockSQLDB()
	source, err := captureManualSource(context.Background(), db, testManualInput("trusted-cancel-materialization"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	output := FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: "stmt-1",
		StatementText:   "unverified statement",
		EvidenceRefs:    []string{"span:missing"},
	}}}
	outputData := append([]byte(" \n"), mustJSONBytes(t, output)...)
	outputData = append(outputData, '\n')
	request := TrustedExtractorRequest{
		RequestID:        "trusted-cancel-materialization",
		ExtractionViewID: source.ExtractionViewID,
	}

	_, err = runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		cancel()
		return outputData, nil
	})
	assertKind(t, err, ErrorUnknownSpan)
	attemptCtx := assertTrustedAttemptFailedOnly(t, context.Background(), db, source.ExtractionViewID, request.RequestID, 1, ErrorUnknownSpan)
	if got := db.extractionAttempts[attemptCtx.ExtractionAttempt.ID].outputHash; got != contentHash(outputData) {
		t.Fatalf("failed attempt output hash = %q, want %q", got, contentHash(outputData))
	}
}

func TestMockSQLRunTrustedExtractorRejectsTamperedCodeFact(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testCodeSourceInput("trusted-code-fact")
	source, err := captureManualSource(ctx, db, input)
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	runner, err := NewGoParserExtractorRunner(GoParserExtractorConfig{
		RepoID:    input.OriginMetadata["repo_id"],
		CommitSHA: input.OriginMetadata["commit_sha"],
		Path:      input.OriginMetadata["path"],
	})
	if err != nil {
		t.Fatalf("NewGoParserExtractorRunner() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:           "trusted-code-fact",
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: runner.ExtractorDefinition(),
	}
	var outputData []byte
	_, err = runTrustedExtractor(ctx, db, request, func(ctx context.Context, input ExtractorInput) ([]byte, error) {
		data, err := runner.Run(ctx, input)
		if err != nil {
			return nil, err
		}
		output, err := decodeTrustedExtractorOutput(data)
		if err != nil {
			return nil, err
		}
		output.Proposals[0].CodeFact.QuotedTextHash = "sha256:tampered"
		outputData = mustJSONBytes(t, output)
		return outputData, nil
	})
	assertKind(t, err, ErrorQuotedHashMismatch)

	sourceCtx, err := loadManualSourceContextByViewID(ctx, db, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContextByViewID() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSourceWithDefinition(sourceCtx, request.RequestID, 1, runner.ExtractorDefinition())
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinition() error = %v", err)
	}
	attempt := db.extractionAttempts[attemptCtx.ExtractionAttempt.ID]
	if attempt.status != attemptStatusFailed || attempt.failureClass != string(ErrorQuotedHashMismatch) {
		t.Fatalf("attempt status/class = %q/%q", attempt.status, attempt.failureClass)
	}
	if attempt.outputHash != contentHash(outputData) {
		t.Fatalf("attempt output hash = %q, want %q", attempt.outputHash, contentHash(outputData))
	}
	if len(db.proposalBatches) != 0 || len(db.proposalOccurrences) != 0 {
		t.Fatalf("tampered code fact created batch/occurrences: %d/%d", len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func TestMockSQLRunTrustedExtractorTechnicalRetryUsesSameRunNewAttempt(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("trusted-retry-source"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:        "trusted-retry",
		ExtractionViewID: source.ExtractionViewID,
	}
	_, err = runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return nil, errors.New("first attempt failed")
	})
	assertKind(t, err, ErrorRunnerInvocationFailed)
	failed := assertTrustedAttemptFailedOnly(t, ctx, db, source.ExtractionViewID, request.RequestID, 1, ErrorRunnerInvocationFailed)

	request.RetryFailedAttempt = true
	result, err := runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return mustJSONBytes(t, testFixture()), nil
	})
	if err != nil {
		t.Fatalf("retry runTrustedExtractor() error = %v", err)
	}
	if result.ExtractionRunID != failed.ExtractionRun.ID {
		t.Fatalf("retry run ID = %q, want same run %q", result.ExtractionRunID, failed.ExtractionRun.ID)
	}
	if result.ExtractionAttemptID == failed.ExtractionAttempt.ID {
		t.Fatalf("retry reused failed attempt %q", result.ExtractionAttemptID)
	}
	calls := 0
	replay, err := runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		calls++
		return nil, errors.New("runner should not be called after successful retry")
	})
	if err != nil {
		t.Fatalf("retry replay runTrustedExtractor() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("runner calls after successful retry = %d, want 0", calls)
	}
	if !replay.Replayed || replay.ProposalOccurrenceID != result.ProposalOccurrenceID {
		t.Fatalf("retry replay result = %+v, want occurrence %s replayed", replay, result.ProposalOccurrenceID)
	}
	if len(db.extractionRuns) != 1 || len(db.extractionAttempts) != 2 || len(db.proposalBatches) != 1 || len(db.proposalOccurrences) != 1 {
		t.Fatalf("counts = runs %d attempts %d batches %d occurrences %d, want 1/2/1/1",
			len(db.extractionRuns), len(db.extractionAttempts), len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := jsonBytes(value)
	if err != nil {
		t.Fatalf("jsonBytes() error = %v", err)
	}
	return data
}

func assertTrustedAttemptFailedOnly(t *testing.T, ctx context.Context, db *mockSQLDB, extractionViewID, requestID string, attemptNumber int, want ErrorKind) attemptContext {
	t.Helper()
	sourceCtx, err := loadManualSourceContextByViewID(ctx, db, extractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContextByViewID() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSource(sourceCtx, requestID, attemptNumber)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSource() error = %v", err)
	}
	attempt, ok := db.extractionAttempts[attemptCtx.ExtractionAttempt.ID]
	if !ok {
		t.Fatalf("attempt %s was not persisted", attemptCtx.ExtractionAttempt.ID)
	}
	if attempt.status != attemptStatusFailed || attempt.failureClass != string(want) {
		t.Fatalf("attempt status/class = %q/%q, want failed/%s", attempt.status, attempt.failureClass, want)
	}
	if len(db.proposalBatches) != 0 {
		t.Fatalf("proposal batch count = %d, want 0", len(db.proposalBatches))
	}
	if len(db.proposalOccurrences) != 0 {
		t.Fatalf("proposal occurrence count = %d, want 0", len(db.proposalOccurrences))
	}
	return attemptCtx
}
