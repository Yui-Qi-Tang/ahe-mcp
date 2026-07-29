package evidenceingestion

import (
	"context"
	"reflect"
	"slices"
	"testing"
)

func TestAssembleRepositoryGoParserDeltaMatchesFullExtraction(t *testing.T) {
	base := repositoryDeltaTestInput(t, "a", map[string]string{
		"deleted.go":  "package sample\n\nfunc Deleted() {}\n",
		"modified.go": "package sample\n\nfunc Before() {}\n",
		"same.go":     "package sample\n\ntype Stable struct{}\n",
	})
	current := repositoryDeltaTestInput(t, "b", map[string]string{
		"added.go":    "package sample\n\nfunc Added() {}\n",
		"modified.go": "package sample\n\nfunc After() {}\n",
		"same.go":     "package sample\n\ntype Stable struct{}\n",
	})
	baseOutput, err := extractRepositoryGoParser(context.Background(), base)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser(base) error = %v", err)
	}
	delta := repositoryFileDeltaForInputs(base.Files, current.Files)
	if delta.added != 1 || delta.modified != 1 || delta.deleted != 1 || delta.unchanged != 1 {
		t.Fatalf("file delta = %+v, want added/modified/deleted/unchanged = 1", delta)
	}
	candidate, parsed, err := assembleRepositoryGoParserDelta(context.Background(), base, baseOutput, current, delta)
	if err != nil {
		t.Fatalf("assembleRepositoryGoParserDelta() error = %v", err)
	}
	if parsed != 2 {
		t.Fatalf("delta parsed files = %d, want 2", parsed)
	}
	full, err := extractRepositoryGoParser(context.Background(), current)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser(current) error = %v", err)
	}
	if !reflect.DeepEqual(candidate, full) {
		t.Fatalf("delta candidate = %+v, want full output %+v", candidate, full)
	}
	for _, proposal := range candidate.Proposals {
		if proposal.CodeFact == nil || proposal.CodeFact.CommitSHA != current.RepositorySnapshot.CommitSHA {
			t.Fatalf("candidate proposal was not rebound to current commit: %+v", proposal)
		}
	}
	if _, err := repositoryDeltaEndpointProofHash(current, candidate); err != nil {
		t.Fatalf("repositoryDeltaEndpointProofHash() error = %v", err)
	}
}

func TestAssembleRepositoryGoParserDeltaRejectsUngroundedBase(t *testing.T) {
	base := repositoryDeltaTestInput(t, "a", map[string]string{
		"same.go": "package sample\n\nfunc Stable() {}\n",
	})
	current := repositoryDeltaTestInput(t, "b", map[string]string{
		"same.go": "package sample\n\nfunc Stable() {}\n",
	})
	baseOutput, err := extractRepositoryGoParser(context.Background(), base)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser(base) error = %v", err)
	}
	baseOutput.Proposals[0].CodeFact.FileContentHash = contentHash([]byte("tampered"))
	_, _, err = assembleRepositoryGoParserDelta(
		context.Background(),
		base,
		baseOutput,
		current,
		repositoryFileDeltaForInputs(base.Files, current.Files),
	)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestAssembleRepositoryGoParserDeltaExposesCandidateMismatch(t *testing.T) {
	base := repositoryDeltaTestInput(t, "a", map[string]string{
		"same.go": "package sample\n\nfunc Stable() {}\n",
	})
	current := repositoryDeltaTestInput(t, "b", map[string]string{
		"same.go": "package sample\n\nfunc Stable() {}\n",
	})
	baseOutput, err := extractRepositoryGoParser(context.Background(), base)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser(base) error = %v", err)
	}
	baseOutput.Proposals[0].StatementText = "durably altered but endpoint-valid"
	candidate, _, err := assembleRepositoryGoParserDelta(
		context.Background(),
		base,
		baseOutput,
		current,
		repositoryFileDeltaForInputs(base.Files, current.Files),
	)
	if err != nil {
		t.Fatalf("assembleRepositoryGoParserDelta() error = %v", err)
	}
	full, err := extractRepositoryGoParser(context.Background(), current)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser(current) error = %v", err)
	}
	candidateHash, err := extractorOutputHash(candidate)
	if err != nil {
		t.Fatalf("extractorOutputHash(candidate) error = %v", err)
	}
	fullHash, err := extractorOutputHash(full)
	if err != nil {
		t.Fatalf("extractorOutputHash(full) error = %v", err)
	}
	if candidateHash == fullHash {
		t.Fatalf("candidate hash = full hash %s, want mismatch", fullHash)
	}
}

func TestExtractRepositoryGoParserDeltaRejectsUnsupportedExtractor(t *testing.T) {
	input := repositoryDeltaTestInput(t, "a", map[string]string{
		"main.go": "package sample\n\nfunc Main() {}\n",
	})
	definition, err := buildExtractorDefinition(ExtractorDefinitionInput{
		Name:    ExtractorRepositoryGoplsCodeFact,
		Version: ExtractorRepositoryGoplsCodeFactVersion,
		Config:  map[string]string{"backend": "gopls/lsp"},
	})
	if err != nil {
		t.Fatalf("buildExtractorDefinition(gopls) error = %v", err)
	}
	_, err = extractRepositoryGoParserDelta(context.Background(), nil, input, definition)
	assertKind(t, err, ErrorInvalidInput)
}

func TestValidateRepositoryDeltaExtractionRejectsTamperedDecision(t *testing.T) {
	input := repositoryDeltaTestInput(t, "a", map[string]string{
		"main.go": "package sample\n\nfunc Main() {}\n",
	})
	definition, err := buildExtractorDefinition(repositoryGoParserExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:delta", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attempt, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	output, err := extractRepositoryGoParser(context.Background(), input)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser() error = %v", err)
	}
	batch, err := materializeRepositoryBatch(attempt, output)
	if err != nil {
		t.Fatalf("materializeRepositoryBatch() error = %v", err)
	}
	endpointHash, err := repositoryDeltaEndpointProofHash(input, output)
	if err != nil {
		t.Fatalf("repositoryDeltaEndpointProofHash() error = %v", err)
	}
	delta, err := buildRepositoryDeltaAudit(
		input,
		nil,
		RepositorySourceGeneration{},
		repositoryFileDeltaForInputs(nil, input.Files),
		batch.FixtureOutputHash,
		endpointHash,
	)
	if err != nil {
		t.Fatalf("buildRepositoryDeltaAudit() error = %v", err)
	}
	delta.Decision = RepositoryDeltaDecisionFullFallback
	delta.FallbackReason = RepositoryDeltaFallbackNoBaseGeneration
	delta, err = bindRepositoryDeltaExtraction(attempt, batch, delta)
	if err != nil {
		t.Fatalf("bindRepositoryDeltaExtraction() error = %v", err)
	}
	delta.SelectedOutputHash = contentHash([]byte("tampered"))
	err = validateRepositoryDeltaExtraction(attempt, repositoryIngestResult(batch, false), delta)
	assertKind(t, err, ErrorRepositorySnapshotIntegrity)
}

func TestValidateRepositoryDeltaExtractionRejectsCurrentSnapshotAsBase(t *testing.T) {
	input := repositoryDeltaTestInput(t, "a", map[string]string{
		"main.go": "package sample\n\nfunc Main() {}\n",
	})
	definition, err := buildExtractorDefinition(repositoryGoParserExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:delta-same-base", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attempt, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	output, err := extractRepositoryGoParser(context.Background(), input)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser() error = %v", err)
	}
	batch, err := materializeRepositoryBatch(attempt, output)
	if err != nil {
		t.Fatalf("materializeRepositoryBatch() error = %v", err)
	}
	endpointHash, err := repositoryDeltaEndpointProofHash(input, output)
	if err != nil {
		t.Fatalf("repositoryDeltaEndpointProofHash() error = %v", err)
	}
	base := RepositorySourceGeneration{
		ID:                   "generation:delta-same-base",
		RepositorySnapshotID: input.RepositorySnapshot.ID,
	}
	delta, err := buildRepositoryDeltaAudit(
		input,
		&input,
		base,
		repositoryFileDeltaForInputs(input.Files, input.Files),
		batch.FixtureOutputHash,
		endpointHash,
	)
	if err != nil {
		t.Fatalf("buildRepositoryDeltaAudit() error = %v", err)
	}
	delta.Decision = RepositoryDeltaDecisionVerified
	delta.CandidateOutputHash = delta.FullOutputHash
	delta, err = bindRepositoryDeltaExtraction(attempt, batch, delta)
	assertKind(t, err, ErrorRepositorySnapshotIntegrity)
}

func repositoryDeltaTestInput(t *testing.T, commitByte string, sources map[string]string) RepositoryExtractorInput {
	t.Helper()
	commitSHA := ""
	for range 40 {
		commitSHA += commitByte
	}
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	manifestHash := contentHash([]byte("manifest-" + commitByte))
	snapshotID, err := repositorySnapshotID("delta-test-repo", commitSHA, manifestHash)
	if err != nil {
		t.Fatalf("repositorySnapshotID() error = %v", err)
	}
	snapshot := RepositorySnapshot{
		ID:                         snapshotID,
		RepoID:                     "delta-test-repo",
		CommitSHA:                  commitSHA,
		ManifestHash:               manifestHash,
		ManifestEntryCount:         len(paths),
		RevisionVerificationMethod: RepositoryRevisionVerificationGitV1,
		ManifestContract:           RepositoryManifestGitTreeV1,
		FileSelectionContract:      RepositoryFileSelectionTrackedGoV1,
		SelectedFileCount:          len(paths),
	}
	files := make([]RepositoryExtractorFile, 0, len(paths))
	for i, path := range paths {
		content := []byte(sources[path])
		blobHash := contentHash(content)
		fileID, err := repositoryFileSnapshotID(snapshot.RepoID, snapshot.CommitSHA, path, blobHash)
		if err != nil {
			t.Fatalf("repositoryFileSnapshotID(%s) error = %v", path, err)
		}
		files = append(files, RepositoryExtractorFile{
			FileSnapshot: SourceFileSnapshot{
				ID:                   fileID,
				RepositorySnapshotID: snapshot.ID,
				RepoID:               snapshot.RepoID,
				CommitSHA:            snapshot.CommitSHA,
				Path:                 path,
				BlobHash:             blobHash,
				GitBlobOID:           gitOIDForTest(i),
				ByteLength:           len(content),
			},
			Content: content,
		})
	}
	return RepositoryExtractorInput{RepositorySnapshot: snapshot, Files: files}
}

func gitOIDForTest(index int) string {
	digit := byte('0' + index%10)
	result := make([]byte, 40)
	for i := range result {
		result[i] = digit
	}
	return string(result)
}
