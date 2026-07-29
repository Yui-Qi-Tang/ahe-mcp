package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

const (
	// RepositoryDeltaAdapterGitGoParser identifies the only P4.5 delta-capable adapter.
	RepositoryDeltaAdapterGitGoParser = "git-go-parser-delta"
	// RepositoryDeltaAdapterGitGoParserVersion is the first durable proof contract.
	RepositoryDeltaAdapterGitGoParserVersion = "v1"

	// RepositoryDeltaDecisionVerified means delta assembly exactly matched full extraction.
	RepositoryDeltaDecisionVerified = "delta_verified"
	// RepositoryDeltaDecisionFullFallback means the full-snapshot result was selected.
	RepositoryDeltaDecisionFullFallback = "full_fallback"

	repositoryDeltaRevisionAuthorityContract = "git-immutable-revision-authority-v1"
	repositoryDeltaFileSetAuthorityContract  = "git-tracked-go-file-set-delta-v1"
	repositoryDeltaEndpointAuthorityContract = "go-parser-declaration-endpoints-v1"

	// RepositoryDeltaFallbackNoBaseGeneration means no prior eligible generation exists.
	RepositoryDeltaFallbackNoBaseGeneration = "no_base_generation"
	// RepositoryDeltaFallbackBaseAuthorityInvalid means the prior generation failed authority validation.
	RepositoryDeltaFallbackBaseAuthorityInvalid = "base_authority_invalid"
	// RepositoryDeltaFallbackCandidateInvalid means delta candidate assembly failed closed.
	RepositoryDeltaFallbackCandidateInvalid = "candidate_invalid"
	// RepositoryDeltaFallbackCandidateMismatch means the candidate did not equal full extraction.
	RepositoryDeltaFallbackCandidateMismatch = "candidate_mismatch"
)

type repositoryFileDelta struct {
	added     int
	modified  int
	deleted   int
	unchanged int
	status    map[string]string
}

type repositoryDeltaFileAuthority struct {
	Path       string `json:"path"`
	BlobHash   string `json:"blob_hash"`
	GitBlobOID string `json:"git_blob_oid"`
	ByteLength int    `json:"byte_length"`
}

type repositoryDeltaFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type repositoryDeltaRevisionAuthority struct {
	RepositorySnapshotID  string `json:"repository_snapshot_id"`
	RepoID                string `json:"repo_id"`
	CommitSHA             string `json:"commit_sha"`
	ManifestHash          string `json:"manifest_hash"`
	ManifestEntryCount    int    `json:"manifest_entry_count"`
	RevisionVerification  string `json:"revision_verification_method"`
	ManifestContract      string `json:"manifest_contract"`
	FileSelectionContract string `json:"file_selection_contract"`
	SelectedFileCount     int    `json:"selected_file_count"`
}

func runRepositoryGoParserDeltaExtractor(ctx context.Context, db sqlDB, request RepositoryGoParserRequest) (RepositoryIngestResult, error) {
	if request.RequestID == "" {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	input, err := buildRepositoryExtractorInput(ctx, db, request.RepositorySnapshotID)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	definitionInput := repositoryGoParserExtractorDefinition()
	definition, err := buildExtractorDefinition(definitionInput)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	return runRepositoryExtractorExecution(
		ctx,
		db,
		input,
		request.RequestID,
		request.RetryFailedAttempt,
		definitionInput,
		true,
		nil,
		nil,
		nil,
		nil,
		func(ctx context.Context, input RepositoryExtractorInput) (repositoryExtractorExecution, error) {
			return extractRepositoryGoParserDelta(ctx, db, input, definition)
		},
	)
}

func extractRepositoryGoParserDelta(ctx context.Context, db sqlDB, current RepositoryExtractorInput, definition ExtractorDefinition) (repositoryExtractorExecution, error) {
	if err := validateRepositoryDeltaSnapshotAuthority(current); err != nil {
		return repositoryExtractorExecution{}, err
	}
	if definition.Name != ExtractorRepositoryGoParserCodeFact || definition.Version != ExtractorRepositoryGoParserCodeFactVersion {
		return repositoryExtractorExecution{}, newDomainError(ErrorInvalidInput, "extractor %s/%s is not eligible for repository delta extraction", definition.Name, definition.Version)
	}

	fullOutput, err := extractRepositoryGoParser(ctx, current)
	if err != nil {
		return repositoryExtractorExecution{}, err
	}
	fullHash, err := extractorOutputHash(fullOutput)
	if err != nil {
		return repositoryExtractorExecution{}, err
	}
	endpointProofHash, err := repositoryDeltaEndpointProofHash(current, fullOutput)
	if err != nil {
		return repositoryExtractorExecution{}, err
	}

	baseGeneration, found, err := readRepositoryDeltaBaseGeneration(ctx, db, current.RepositorySnapshot, definition.ID)
	if err != nil {
		return repositoryExtractorExecution{}, err
	}
	if !found {
		delta, err := buildRepositoryDeltaAudit(current, nil, RepositorySourceGeneration{}, repositoryFileDeltaForInputs(nil, current.Files), fullHash, endpointProofHash)
		if err != nil {
			return repositoryExtractorExecution{}, err
		}
		delta.Decision = RepositoryDeltaDecisionFullFallback
		delta.FallbackReason = RepositoryDeltaFallbackNoBaseGeneration
		return repositoryExtractorExecution{output: fullOutput, deltaExtraction: &delta}, nil
	}

	baseInput, baseOutput, err := loadRepositoryDeltaBase(ctx, db, baseGeneration)
	if err != nil {
		if !repositoryDeltaCanFallback(err) {
			return repositoryExtractorExecution{}, err
		}
		delta, buildErr := buildRepositoryDeltaAudit(current, nil, baseGeneration, repositoryFileDeltaForInputs(nil, current.Files), fullHash, endpointProofHash)
		if buildErr != nil {
			return repositoryExtractorExecution{}, buildErr
		}
		delta.Decision = RepositoryDeltaDecisionFullFallback
		delta.FallbackReason = RepositoryDeltaFallbackBaseAuthorityInvalid
		return repositoryExtractorExecution{output: fullOutput, deltaExtraction: &delta}, nil
	}

	fileDelta := repositoryFileDeltaForInputs(baseInput.Files, current.Files)
	delta, err := buildRepositoryDeltaAudit(current, &baseInput, baseGeneration, fileDelta, fullHash, endpointProofHash)
	if err != nil {
		return repositoryExtractorExecution{}, err
	}
	candidate, parsed, err := assembleRepositoryGoParserDelta(ctx, baseInput, baseOutput, current, fileDelta)
	delta.DeltaParsedFileCount = parsed
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return repositoryExtractorExecution{}, err
		}
		delta.Decision = RepositoryDeltaDecisionFullFallback
		delta.FallbackReason = RepositoryDeltaFallbackCandidateInvalid
		return repositoryExtractorExecution{output: fullOutput, deltaExtraction: &delta}, nil
	}
	candidateHash, err := extractorOutputHash(candidate)
	if err != nil {
		return repositoryExtractorExecution{}, err
	}
	delta.CandidateOutputHash = candidateHash
	if candidateHash != fullHash {
		delta.Decision = RepositoryDeltaDecisionFullFallback
		delta.FallbackReason = RepositoryDeltaFallbackCandidateMismatch
		return repositoryExtractorExecution{output: fullOutput, deltaExtraction: &delta}, nil
	}

	delta.Decision = RepositoryDeltaDecisionVerified
	return repositoryExtractorExecution{output: candidate, deltaExtraction: &delta}, nil
}

func readRepositoryDeltaBaseGeneration(ctx context.Context, db sqlDB, current RepositorySnapshot, extractorDefinitionID string) (RepositorySourceGeneration, bool, error) {
	generation, err := scanRepositorySourceGeneration(db.queryRow(ctx, repositorySourceGenerationSelect+`
		WHERE g.repo_id = $1
		  AND g.extractor_name = $2
		  AND g.extractor_definition_id = $3
		  AND g.repository_snapshot_id <> $4
		ORDER BY g.generation_number DESC
		LIMIT 1
	`, current.RepoID, ExtractorRepositoryGoParserCodeFact, extractorDefinitionID, current.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositorySourceGeneration{}, false, nil
	}
	if err != nil {
		return RepositorySourceGeneration{}, false, fmt.Errorf("reading repository delta base generation: %w", err)
	}
	return generation, true, nil
}

func loadRepositoryDeltaBase(ctx context.Context, db sqlDB, generation RepositorySourceGeneration) (RepositoryExtractorInput, FrozenExtractorOutput, error) {
	input, err := buildRepositoryExtractorInput(ctx, db, generation.RepositorySnapshotID)
	if err != nil {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, err
	}
	if err := validateRepositoryDeltaSnapshotAuthority(input); err != nil {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, err
	}
	if generation.RepoID != input.RepositorySnapshot.RepoID || generation.CommitSHA != input.RepositorySnapshot.CommitSHA || generation.ExtractorName != ExtractorRepositoryGoParserCodeFact {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta base generation %s does not match its immutable snapshot", generation.ID)
	}
	wantGenerationID, err := repositorySourceGenerationID(
		generation.RepoID,
		generation.ExtractorDefinitionID,
		generation.RepositorySnapshotID,
		generation.ExtractorOutputHash,
	)
	if err != nil {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, err
	}
	if generation.ID != wantGenerationID {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta base generation %s has an invalid durable identity", generation.ID)
	}

	var attemptStatus, batchStatus, outputHash string
	var proposalCount int
	var fixtureData []byte
	err = db.queryRow(ctx, `
		SELECT ea.status, pb.status, COALESCE(ea.output_hash, ''), pb.proposal_count, ea.fixture_output
		FROM extraction_attempts ea
		JOIN proposal_batches pb
		  ON pb.extraction_attempt_id = ea.extraction_attempt_id
		WHERE ea.extraction_attempt_id = $1
		  AND pb.proposal_batch_id = $2
	`, generation.ExtractionAttemptID, generation.ProposalBatchID).Scan(&attemptStatus, &batchStatus, &outputHash, &proposalCount, &fixtureData)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta base generation %s has no completed output", generation.ID)
	}
	if err != nil {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, fmt.Errorf("reading repository delta base output: %w", err)
	}
	if attemptStatus != attemptStatusSucceeded || batchStatus != batchStatusCompleted || outputHash != generation.ExtractorOutputHash || proposalCount != generation.ProposalCount {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta base generation %s has inconsistent attempt output", generation.ID)
	}
	var output FrozenExtractorOutput
	if err := json.Unmarshal(fixtureData, &output); err != nil {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta base generation %s has invalid fixture output: %v", generation.ID, err)
	}
	fixtureHash, err := extractorOutputHash(output)
	if err != nil {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, err
	}
	if fixtureHash != generation.ExtractorOutputHash ||
		len(output.Proposals) != generation.ProposalCount ||
		output.RepositoryGoplsCoverage != nil ||
		output.DocumentSectionCoverage != nil {
		return RepositoryExtractorInput{}, FrozenExtractorOutput{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta base generation %s fixture does not match its durable output", generation.ID)
	}
	return input, output, nil
}

func repositoryDeltaCanFallback(err error) bool {
	kind, ok := KindOf(err)
	return ok && (kind == ErrorRepositorySnapshotIntegrity || kind == ErrorMissingSourceViewAttempt || kind == ErrorInvalidExtractorOutput || kind == ErrorUnknownSpan || kind == ErrorQuotedHashMismatch)
}

func validateRepositoryDeltaSnapshotAuthority(input RepositoryExtractorInput) error {
	snapshot := input.RepositorySnapshot
	if snapshot.RevisionVerificationMethod != RepositoryRevisionVerificationGitV1 || snapshot.ManifestContract != RepositoryManifestGitTreeV1 || snapshot.FileSelectionContract != RepositoryFileSelectionTrackedGoV1 {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository snapshot %s does not satisfy the delta authority contracts", snapshot.ID)
	}
	if !isCanonicalGitObjectID(snapshot.CommitSHA) || !isSHA256ContentHash(snapshot.ManifestHash) || snapshot.ManifestEntryCount < snapshot.SelectedFileCount {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository snapshot %s has invalid revision or manifest authority", snapshot.ID)
	}
	wantSnapshotID, err := repositorySnapshotID(snapshot.RepoID, snapshot.CommitSHA, snapshot.ManifestHash)
	if err != nil {
		return err
	}
	if snapshot.ID != wantSnapshotID {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository snapshot %s has an invalid durable identity", snapshot.ID)
	}
	if snapshot.SelectedFileCount != len(input.Files) {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository snapshot %s selected_file_count is %d but delta input has %d files", snapshot.ID, snapshot.SelectedFileCount, len(input.Files))
	}
	seen := make(map[string]bool, len(input.Files))
	for _, file := range input.Files {
		if seen[file.FileSnapshot.Path] {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository snapshot %s repeats path %q", snapshot.ID, file.FileSnapshot.Path)
		}
		seen[file.FileSnapshot.Path] = true
		if err := validateRepositoryExtractorFile(snapshot, file); err != nil {
			return err
		}
		if !isCanonicalGitObjectID(file.FileSnapshot.GitBlobOID) || !isSHA256ContentHash(file.FileSnapshot.BlobHash) {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "file snapshot %s has invalid Git blob authority", file.FileSnapshot.ID)
		}
		wantFileID, err := repositoryFileSnapshotID(snapshot.RepoID, snapshot.CommitSHA, file.FileSnapshot.Path, file.FileSnapshot.BlobHash)
		if err != nil {
			return err
		}
		if file.FileSnapshot.ID != wantFileID {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "file snapshot %s has an invalid durable identity", file.FileSnapshot.ID)
		}
	}
	return nil
}

func repositoryFileDeltaForInputs(base, current []RepositoryExtractorFile) repositoryFileDelta {
	baseByPath := repositoryFilesByPath(base)
	currentByPath := repositoryFilesByPath(current)
	result := repositoryFileDelta{status: make(map[string]string, len(baseByPath)+len(currentByPath))}
	for path, file := range currentByPath {
		prior, found := baseByPath[path]
		switch {
		case !found:
			result.added++
			result.status[path] = "added"
		case prior.FileSnapshot.BlobHash != file.FileSnapshot.BlobHash:
			result.modified++
			result.status[path] = "modified"
		default:
			result.unchanged++
			result.status[path] = "unchanged"
		}
	}
	for path := range baseByPath {
		if _, found := currentByPath[path]; !found {
			result.deleted++
			result.status[path] = "deleted"
		}
	}
	return result
}

func repositoryFilesByPath(files []RepositoryExtractorFile) map[string]RepositoryExtractorFile {
	byPath := make(map[string]RepositoryExtractorFile, len(files))
	for _, file := range files {
		byPath[file.FileSnapshot.Path] = file
	}
	return byPath
}

func assembleRepositoryGoParserDelta(ctx context.Context, baseInput RepositoryExtractorInput, baseOutput FrozenExtractorOutput, current RepositoryExtractorInput, delta repositoryFileDelta) (FrozenExtractorOutput, int, error) {
	baseFiles := repositoryFilesByPath(baseInput.Files)
	baseByPath := make(map[string][]ExtractorProposalOutput, len(baseFiles))
	seenLocalIDs := make(map[string]bool, len(baseOutput.Proposals))
	for _, proposal := range baseOutput.Proposals {
		if proposal.ProposalLocalID == "" || seenLocalIDs[proposal.ProposalLocalID] {
			return FrozenExtractorOutput{}, 0, newDomainError(ErrorInvalidExtractorOutput, "repository delta base contains an empty or duplicate proposal_local_id")
		}
		seenLocalIDs[proposal.ProposalLocalID] = true
		if proposal.CodeFact == nil || proposal.CodeRelation != nil {
			return FrozenExtractorOutput{}, 0, newDomainError(ErrorInvalidExtractorOutput, "repository delta base proposal %s is not a declaration fact", proposal.ProposalLocalID)
		}
		file, found := baseFiles[proposal.CodeFact.Path]
		if !found {
			return FrozenExtractorOutput{}, 0, newDomainError(ErrorInvalidExtractorOutput, "repository delta base proposal %s references unknown path %q", proposal.ProposalLocalID, proposal.CodeFact.Path)
		}
		if err := validateRepositoryDeltaCodeFact(baseInput.RepositorySnapshot, file, proposal); err != nil {
			return FrozenExtractorOutput{}, 0, err
		}
		baseByPath[proposal.CodeFact.Path] = append(baseByPath[proposal.CodeFact.Path], proposal)
	}

	proposals := make([]ExtractorProposalOutput, 0, len(baseOutput.Proposals))
	parsed := 0
	for _, file := range current.Files {
		if err := ctx.Err(); err != nil {
			return FrozenExtractorOutput{}, parsed, err
		}
		path := file.FileSnapshot.Path
		if delta.status[path] == "unchanged" {
			for _, proposal := range baseByPath[path] {
				rebound := proposal
				rebound.EvidenceRefs = append([]string(nil), proposal.EvidenceRefs...)
				fact := *proposal.CodeFact
				fact.CommitSHA = current.RepositorySnapshot.CommitSHA
				fact.SymbolRef = codeSymbolRef(fact.RepoID, fact.CommitSHA, fact.Path, fact.QualifiedName)
				rebound.CodeFact = &fact
				proposals = append(proposals, rebound)
			}
			continue
		}
		parsed++
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    current.RepositorySnapshot.RepoID,
			CommitSHA: current.RepositorySnapshot.CommitSHA,
			Path:      path,
			Source:    file.Content,
		})
		if err != nil {
			return FrozenExtractorOutput{}, parsed, err
		}
		for _, proposal := range extraction.Output.Proposals {
			qualified, err := qualifyRepositoryProposalLocalID(path, proposal)
			if err != nil {
				return FrozenExtractorOutput{}, parsed, err
			}
			proposals = append(proposals, qualified)
		}
	}
	return FrozenExtractorOutput{Proposals: proposals}, parsed, nil
}

func validateRepositoryDeltaCodeFact(snapshot RepositorySnapshot, file RepositoryExtractorFile, proposal ExtractorProposalOutput) error {
	fact := proposal.CodeFact
	if fact.SchemaVersion != CodeFactSchemaV1 || fact.FactKind != CodeFactKindDeclaration || fact.RepoID != snapshot.RepoID || fact.CommitSHA != snapshot.CommitSHA || fact.Path != file.FileSnapshot.Path || fact.FileContentHash != file.FileSnapshot.BlobHash {
		return newDomainError(ErrorInvalidExtractorOutput, "repository delta proposal %s does not match its immutable file authority", proposal.ProposalLocalID)
	}
	if fact.SymbolRef != codeSymbolRef(fact.RepoID, fact.CommitSHA, fact.Path, fact.QualifiedName) {
		return newDomainError(ErrorInvalidExtractorOutput, "repository delta proposal %s has an invalid symbol_ref", proposal.ProposalLocalID)
	}
	if fact.StartByte < 0 || fact.EndByte <= fact.StartByte || fact.EndByte > len(file.Content) || contentHash(file.Content[fact.StartByte:fact.EndByte]) != fact.QuotedTextHash {
		return newDomainError(ErrorQuotedHashMismatch, "repository delta proposal %s does not match exact declaration bytes", proposal.ProposalLocalID)
	}
	spans, err := buildLineSpanCatalog(ExtractionView{ID: file.FileSnapshot.ID, Rendered: file.Content, RenderedContentHash: file.FileSnapshot.BlobHash}, SpanCatalogCodeLineV1)
	if err != nil {
		return err
	}
	spansByID := make(map[string]bool, len(spans))
	for _, span := range spans {
		spansByID[span.SpanID] = true
	}
	if len(proposal.EvidenceRefs) == 0 {
		return newDomainError(ErrorUnknownSpan, "repository delta proposal %s has no evidence refs", proposal.ProposalLocalID)
	}
	for _, spanID := range proposal.EvidenceRefs {
		if !spansByID[spanID] {
			return newDomainError(ErrorUnknownSpan, "repository delta proposal %s references unknown span %s", proposal.ProposalLocalID, spanID)
		}
	}
	return nil
}

func repositoryDeltaEndpointProofHash(input RepositoryExtractorInput, output FrozenExtractorOutput) (string, error) {
	files := repositoryFilesByPath(input.Files)
	seenLocalIDs := make(map[string]bool, len(output.Proposals))
	for _, proposal := range output.Proposals {
		if proposal.ProposalLocalID == "" || seenLocalIDs[proposal.ProposalLocalID] || proposal.CodeFact == nil || proposal.CodeRelation != nil {
			return "", newDomainError(ErrorInvalidExtractorOutput, "repository delta full output contains an unsupported proposal")
		}
		seenLocalIDs[proposal.ProposalLocalID] = true
		file, found := files[proposal.CodeFact.Path]
		if !found {
			return "", newDomainError(ErrorInvalidExtractorOutput, "repository delta full output references unknown path %q", proposal.CodeFact.Path)
		}
		if err := validateRepositoryDeltaCodeFact(input.RepositorySnapshot, file, proposal); err != nil {
			return "", err
		}
	}
	data, err := deterministicJSON(struct {
		Contract             string                `json:"contract"`
		RepositorySnapshotID string                `json:"repository_snapshot_id"`
		Output               FrozenExtractorOutput `json:"output"`
	}{repositoryDeltaEndpointAuthorityContract, input.RepositorySnapshot.ID, output})
	if err != nil {
		return "", err
	}
	return contentHash(data), nil
}

func buildRepositoryDeltaAudit(current RepositoryExtractorInput, base *RepositoryExtractorInput, baseGeneration RepositorySourceGeneration, fileDelta repositoryFileDelta, fullHash, endpointProofHash string) (RepositoryDeltaExtraction, error) {
	revisionProofHash, err := repositoryDeltaRevisionProofHash(current, base, baseGeneration)
	if err != nil {
		return RepositoryDeltaExtraction{}, err
	}
	fileSetProofHash, err := repositoryDeltaFileSetProofHash(current, base, fileDelta)
	if err != nil {
		return RepositoryDeltaExtraction{}, err
	}
	delta := RepositoryDeltaExtraction{
		AdapterName:               RepositoryDeltaAdapterGitGoParser,
		AdapterVersion:            RepositoryDeltaAdapterGitGoParserVersion,
		RevisionAuthorityContract: repositoryDeltaRevisionAuthorityContract,
		FileSetAuthorityContract:  repositoryDeltaFileSetAuthorityContract,
		EndpointAuthorityContract: repositoryDeltaEndpointAuthorityContract,
		RepoID:                    current.RepositorySnapshot.RepoID,
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		BaseSourceGenerationID:    baseGeneration.ID,
		BaseRepositorySnapshotID:  baseGeneration.RepositorySnapshotID,
		RepositorySnapshotID:      current.RepositorySnapshot.ID,
		AddedFileCount:            fileDelta.added,
		ModifiedFileCount:         fileDelta.modified,
		DeletedFileCount:          fileDelta.deleted,
		UnchangedFileCount:        fileDelta.unchanged,
		FullVerifiedFileCount:     len(current.Files),
		RevisionProofHash:         revisionProofHash,
		FileSetProofHash:          fileSetProofHash,
		EndpointProofHash:         endpointProofHash,
		FullOutputHash:            fullHash,
		SelectedOutputHash:        fullHash,
	}
	return delta, nil
}

func repositoryDeltaRevisionProofHash(current RepositoryExtractorInput, base *RepositoryExtractorInput, generation RepositorySourceGeneration) (string, error) {
	var baseAuthority *repositoryDeltaRevisionAuthority
	if base != nil {
		authority := repositoryDeltaRevision(base.RepositorySnapshot)
		baseAuthority = &authority
	}
	data, err := deterministicJSON(struct {
		Contract                 string                            `json:"contract"`
		BaseSourceGenerationID   string                            `json:"base_source_generation_id,omitempty"`
		BaseRepositorySnapshotID string                            `json:"base_repository_snapshot_id,omitempty"`
		Base                     *repositoryDeltaRevisionAuthority `json:"base,omitempty"`
		Current                  repositoryDeltaRevisionAuthority  `json:"current"`
	}{
		Contract:                 repositoryDeltaRevisionAuthorityContract,
		BaseSourceGenerationID:   generation.ID,
		BaseRepositorySnapshotID: generation.RepositorySnapshotID,
		Base:                     baseAuthority,
		Current:                  repositoryDeltaRevision(current.RepositorySnapshot),
	})
	if err != nil {
		return "", err
	}
	return contentHash(data), nil
}

func repositoryDeltaRevision(snapshot RepositorySnapshot) repositoryDeltaRevisionAuthority {
	return repositoryDeltaRevisionAuthority{
		RepositorySnapshotID:  snapshot.ID,
		RepoID:                snapshot.RepoID,
		CommitSHA:             snapshot.CommitSHA,
		ManifestHash:          snapshot.ManifestHash,
		ManifestEntryCount:    snapshot.ManifestEntryCount,
		RevisionVerification:  snapshot.RevisionVerificationMethod,
		ManifestContract:      snapshot.ManifestContract,
		FileSelectionContract: snapshot.FileSelectionContract,
		SelectedFileCount:     snapshot.SelectedFileCount,
	}
}

func repositoryDeltaFileSetProofHash(current RepositoryExtractorInput, base *RepositoryExtractorInput, delta repositoryFileDelta) (string, error) {
	baseFiles := []repositoryDeltaFileAuthority{}
	if base != nil {
		baseFiles = repositoryDeltaFileAuthorities(base.Files)
	}
	paths := make([]string, 0, len(delta.status))
	for path := range delta.status {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	changes := make([]repositoryDeltaFileChange, 0, len(paths))
	for _, path := range paths {
		changes = append(changes, repositoryDeltaFileChange{Path: path, Status: delta.status[path]})
	}
	data, err := deterministicJSON(struct {
		Contract string                         `json:"contract"`
		Base     []repositoryDeltaFileAuthority `json:"base"`
		Current  []repositoryDeltaFileAuthority `json:"current"`
		Changes  []repositoryDeltaFileChange    `json:"changes"`
	}{repositoryDeltaFileSetAuthorityContract, baseFiles, repositoryDeltaFileAuthorities(current.Files), changes})
	if err != nil {
		return "", err
	}
	return contentHash(data), nil
}

func repositoryDeltaFileAuthorities(files []RepositoryExtractorFile) []repositoryDeltaFileAuthority {
	authorities := make([]repositoryDeltaFileAuthority, 0, len(files))
	for _, file := range files {
		authorities = append(authorities, repositoryDeltaFileAuthority{
			Path: file.FileSnapshot.Path, BlobHash: file.FileSnapshot.BlobHash,
			GitBlobOID: file.FileSnapshot.GitBlobOID, ByteLength: file.FileSnapshot.ByteLength,
		})
	}
	slices.SortFunc(authorities, func(a, b repositoryDeltaFileAuthority) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	return authorities
}

func bindRepositoryDeltaExtraction(attempt repositoryAttemptContext, batch MaterializedBatch, delta RepositoryDeltaExtraction) (RepositoryDeltaExtraction, error) {
	delta.ExtractorDefinitionID = attempt.extractorDefinition.ID
	delta.ExtractionRunID = attempt.extractionRun.ID
	delta.ExtractionAttemptID = attempt.extractionAttempt.ID
	delta.ProposalBatchID = batch.ProposalBatch.ID
	id, err := stableID("repo-delta:", "repository_delta_extraction", struct {
		ExtractionAttemptID string `json:"extraction_attempt_id"`
	}{delta.ExtractionAttemptID})
	if err != nil {
		return RepositoryDeltaExtraction{}, err
	}
	delta.ID = id
	result := repositoryIngestResult(batch, false)
	if err := validateRepositoryDeltaExtraction(attempt, result, delta); err != nil {
		return RepositoryDeltaExtraction{}, err
	}
	return delta, nil
}

func validateRepositoryDeltaExtraction(attempt repositoryAttemptContext, result RepositoryIngestResult, delta RepositoryDeltaExtraction) error {
	if delta.AdapterName != RepositoryDeltaAdapterGitGoParser || delta.AdapterVersion != RepositoryDeltaAdapterGitGoParserVersion || delta.RevisionAuthorityContract != repositoryDeltaRevisionAuthorityContract || delta.FileSetAuthorityContract != repositoryDeltaFileSetAuthorityContract || delta.EndpointAuthorityContract != repositoryDeltaEndpointAuthorityContract {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s has unsupported proof contracts", delta.ID)
	}
	if delta.RepoID != attempt.input.RepositorySnapshot.RepoID || delta.ExtractorName != attempt.extractorDefinition.Name || delta.ExtractorDefinitionID != attempt.extractorDefinition.ID || delta.RepositorySnapshotID != attempt.input.RepositorySnapshot.ID || delta.ExtractionRunID != attempt.extractionRun.ID || delta.ExtractionAttemptID != attempt.extractionAttempt.ID || delta.ProposalBatchID != result.ProposalBatchID {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s does not match its extraction authority", delta.ID)
	}
	wantID, err := stableID("repo-delta:", "repository_delta_extraction", struct {
		ExtractionAttemptID string `json:"extraction_attempt_id"`
	}{delta.ExtractionAttemptID})
	if err != nil {
		return err
	}
	if delta.ID != wantID {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s has an invalid durable identity", delta.ID)
	}
	if delta.AddedFileCount < 0 || delta.ModifiedFileCount < 0 || delta.DeletedFileCount < 0 || delta.UnchangedFileCount < 0 || delta.DeltaParsedFileCount < 0 || delta.FullVerifiedFileCount != len(attempt.input.Files) || delta.AddedFileCount+delta.ModifiedFileCount+delta.UnchangedFileCount != len(attempt.input.Files) {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s has inconsistent file counts", delta.ID)
	}
	if delta.FullOutputHash != result.OutputHash || delta.SelectedOutputHash != result.OutputHash || delta.RevisionProofHash == "" || delta.FileSetProofHash == "" || delta.EndpointProofHash == "" {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s does not match the selected full-snapshot output", delta.ID)
	}
	if delta.BaseRepositorySnapshotID != "" && delta.BaseRepositorySnapshotID == delta.RepositorySnapshotID {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s uses its current snapshot as the base", delta.ID)
	}
	switch delta.Decision {
	case RepositoryDeltaDecisionVerified:
		if delta.FallbackReason != "" || delta.BaseSourceGenerationID == "" || delta.BaseRepositorySnapshotID == "" || delta.CandidateOutputHash != delta.FullOutputHash || delta.DeltaParsedFileCount != delta.AddedFileCount+delta.ModifiedFileCount {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s has an invalid verified decision", delta.ID)
		}
	case RepositoryDeltaDecisionFullFallback:
		if !validRepositoryDeltaFallback(delta) {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s has an invalid fallback decision", delta.ID)
		}
	default:
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s has unsupported decision %q", delta.ID, delta.Decision)
	}
	return nil
}

func validRepositoryDeltaFallback(delta RepositoryDeltaExtraction) bool {
	switch delta.FallbackReason {
	case RepositoryDeltaFallbackNoBaseGeneration:
		return delta.BaseSourceGenerationID == "" && delta.BaseRepositorySnapshotID == "" && delta.CandidateOutputHash == ""
	case RepositoryDeltaFallbackBaseAuthorityInvalid, RepositoryDeltaFallbackCandidateInvalid:
		return delta.BaseSourceGenerationID != "" && delta.BaseRepositorySnapshotID != "" && delta.CandidateOutputHash == ""
	case RepositoryDeltaFallbackCandidateMismatch:
		return delta.BaseSourceGenerationID != "" && delta.BaseRepositorySnapshotID != "" && delta.CandidateOutputHash != "" && delta.CandidateOutputHash != delta.FullOutputHash
	default:
		return false
	}
}

func persistRepositoryDeltaExtraction(ctx context.Context, tx sqlTx, delta RepositoryDeltaExtraction) error {
	payloadHash, err := repositoryDeltaExtractionPayloadHash(delta)
	if err != nil {
		return err
	}
	result, err := tx.exec(ctx, `
		INSERT INTO repository_delta_extractions (
			delta_extraction_id, extraction_run_id, extraction_attempt_id,
			proposal_batch_id, repo_id, extractor_name, extractor_definition_id,
			base_source_generation_id, base_repository_snapshot_id,
			repository_snapshot_id, adapter_name, adapter_version,
			revision_authority_contract, file_set_authority_contract,
			endpoint_authority_contract, decision, fallback_reason,
			added_file_count, modified_file_count, deleted_file_count,
			unchanged_file_count, delta_parsed_file_count, full_verified_file_count,
			revision_proof_hash, file_set_proof_hash, endpoint_proof_hash,
			candidate_output_hash, full_output_hash, selected_output_hash,
			request_payload_hash
		)
		VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
			$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30
		)
		ON CONFLICT (delta_extraction_id) DO UPDATE
		SET delta_extraction_id = EXCLUDED.delta_extraction_id
		WHERE repository_delta_extractions.request_payload_hash = EXCLUDED.request_payload_hash
	`,
		delta.ID, delta.ExtractionRunID, delta.ExtractionAttemptID,
		delta.ProposalBatchID, delta.RepoID, delta.ExtractorName, delta.ExtractorDefinitionID,
		nullableString(delta.BaseSourceGenerationID), nullableString(delta.BaseRepositorySnapshotID),
		delta.RepositorySnapshotID, delta.AdapterName, delta.AdapterVersion,
		delta.RevisionAuthorityContract, delta.FileSetAuthorityContract,
		delta.EndpointAuthorityContract, delta.Decision, nullableString(delta.FallbackReason),
		delta.AddedFileCount, delta.ModifiedFileCount, delta.DeletedFileCount,
		delta.UnchangedFileCount, delta.DeltaParsedFileCount, delta.FullVerifiedFileCount,
		delta.RevisionProofHash, delta.FileSetProofHash, delta.EndpointProofHash,
		nullableString(delta.CandidateOutputHash), delta.FullOutputHash, delta.SelectedOutputHash,
		payloadHash,
	)
	if err != nil {
		return fmt.Errorf("inserting repository delta extraction %s: %w", delta.ID, err)
	}
	if result.RowsAffected() != 1 {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s already exists with different proof fields", delta.ID)
	}
	return nil
}

func readRepositoryDeltaExtraction(ctx context.Context, db sqlDB, extractionAttemptID string) (RepositoryDeltaExtraction, bool, error) {
	var delta RepositoryDeltaExtraction
	var baseGenerationID, baseSnapshotID, fallbackReason, candidateOutputHash *string
	var payloadHash string
	err := db.queryRow(ctx, `
		SELECT
			delta_extraction_id, extraction_run_id, extraction_attempt_id,
			proposal_batch_id, repo_id, extractor_name, extractor_definition_id,
			base_source_generation_id, base_repository_snapshot_id,
			repository_snapshot_id, adapter_name, adapter_version,
			revision_authority_contract, file_set_authority_contract,
			endpoint_authority_contract, decision, fallback_reason,
			added_file_count, modified_file_count, deleted_file_count,
			unchanged_file_count, delta_parsed_file_count, full_verified_file_count,
			revision_proof_hash, file_set_proof_hash, endpoint_proof_hash,
			candidate_output_hash, full_output_hash, selected_output_hash,
			request_payload_hash
		FROM repository_delta_extractions
		WHERE extraction_attempt_id = $1
	`, extractionAttemptID).Scan(
		&delta.ID, &delta.ExtractionRunID, &delta.ExtractionAttemptID,
		&delta.ProposalBatchID, &delta.RepoID, &delta.ExtractorName, &delta.ExtractorDefinitionID,
		&baseGenerationID, &baseSnapshotID,
		&delta.RepositorySnapshotID, &delta.AdapterName, &delta.AdapterVersion,
		&delta.RevisionAuthorityContract, &delta.FileSetAuthorityContract,
		&delta.EndpointAuthorityContract, &delta.Decision, &fallbackReason,
		&delta.AddedFileCount, &delta.ModifiedFileCount, &delta.DeletedFileCount,
		&delta.UnchangedFileCount, &delta.DeltaParsedFileCount, &delta.FullVerifiedFileCount,
		&delta.RevisionProofHash, &delta.FileSetProofHash, &delta.EndpointProofHash,
		&candidateOutputHash, &delta.FullOutputHash, &delta.SelectedOutputHash,
		&payloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryDeltaExtraction{}, false, nil
	}
	if err != nil {
		return RepositoryDeltaExtraction{}, false, fmt.Errorf("reading repository delta extraction for attempt %s: %w", extractionAttemptID, err)
	}
	if baseGenerationID != nil {
		delta.BaseSourceGenerationID = *baseGenerationID
	}
	if baseSnapshotID != nil {
		delta.BaseRepositorySnapshotID = *baseSnapshotID
	}
	if fallbackReason != nil {
		delta.FallbackReason = *fallbackReason
	}
	if candidateOutputHash != nil {
		delta.CandidateOutputHash = *candidateOutputHash
	}
	wantPayloadHash, err := repositoryDeltaExtractionPayloadHash(delta)
	if err != nil {
		return RepositoryDeltaExtraction{}, false, err
	}
	if payloadHash != wantPayloadHash {
		return RepositoryDeltaExtraction{}, false, newDomainError(ErrorRepositorySnapshotIntegrity, "repository delta extraction %s payload hash does not match durable fields", delta.ID)
	}
	return delta, true, nil
}

func repositoryDeltaExtractionPayloadHash(delta RepositoryDeltaExtraction) (string, error) {
	delta.Replayed = false
	data, err := deterministicJSON(delta)
	if err != nil {
		return "", fmt.Errorf("serializing repository delta extraction: %w", err)
	}
	return contentHash(data), nil
}
