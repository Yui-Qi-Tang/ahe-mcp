package evidenceingestion

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	// GroundedEvidenceRepositoryContextContractV1 identifies post-retrieval repository context.
	GroundedEvidenceRepositoryContextContractV1 = "post-retrieval-repository-context-v1"

	// GroundedEvidenceRepositoryFileMaxBytesV1 caps one materialized repository file.
	GroundedEvidenceRepositoryFileMaxBytesV1 int64 = 1 << 20
	// GroundedEvidenceRepositoryDeclarationMaxBytesV1 caps one declaration package.
	GroundedEvidenceRepositoryDeclarationMaxBytesV1 = 16 << 10
	// GroundedEvidenceRepositoryRelationEndpointMaxBytesV1 caps one relation endpoint.
	GroundedEvidenceRepositoryRelationEndpointMaxBytesV1 = 16 << 10
	// GroundedEvidenceRepositoryRelationMaxBytesV1 caps one complete relation package.
	GroundedEvidenceRepositoryRelationMaxBytesV1 = 32 << 10
)

// GroundedEvidenceRepositoryContextStatus is a closed repository hydration outcome.
type GroundedEvidenceRepositoryContextStatus string

const (
	// GroundedEvidenceRepositoryContextStatusAvailable carries one complete typed payload.
	GroundedEvidenceRepositoryContextStatusAvailable GroundedEvidenceRepositoryContextStatus = "available"
	// GroundedEvidenceRepositoryContextStatusOverBudget withholds a complete context payload.
	GroundedEvidenceRepositoryContextStatusOverBudget GroundedEvidenceRepositoryContextStatus = "context_over_budget"
	// GroundedEvidenceRepositoryContextStatusFileViewOverBudget withholds an oversized file.
	GroundedEvidenceRepositoryContextStatusFileViewOverBudget GroundedEvidenceRepositoryContextStatus = "file_view_over_budget"
)

// GroundedEvidenceRepositoryRecordKind discriminates declaration and relation payloads.
type GroundedEvidenceRepositoryRecordKind string

const (
	GroundedEvidenceRepositoryRecordDeclaration GroundedEvidenceRepositoryRecordKind = "declaration"
	GroundedEvidenceRepositoryRecordRelation    GroundedEvidenceRepositoryRecordKind = "relation"
)

// GroundedEvidenceRepositoryContextRole identifies one typed relation endpoint.
type GroundedEvidenceRepositoryContextRole string

const (
	GroundedEvidenceRepositoryContextUsage  GroundedEvidenceRepositoryContextRole = "usage"
	GroundedEvidenceRepositoryContextCaller GroundedEvidenceRepositoryContextRole = "caller"
	GroundedEvidenceRepositoryContextTarget GroundedEvidenceRepositoryContextRole = "target"
)

// GroundedEvidenceRepositoryContextSlice is one exact immutable syntax slice.
type GroundedEvidenceRepositoryContextSlice struct {
	Role        string `json:"role"`
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	StartByte   int    `json:"start_byte"`
	EndByte     int    `json:"end_byte"`
	ExactText   string `json:"exact_text"`
	ContentHash string `json:"content_hash"`
}

// GroundedEvidenceRepositoryDeclarationContext contains one declaration source unit.
type GroundedEvidenceRepositoryDeclarationContext struct {
	ContainerKind   string                                  `json:"container_kind"`
	AtomicContainer GroundedEvidenceRepositoryContextSlice  `json:"atomic_container"`
	PackageClause   *GroundedEvidenceRepositoryContextSlice `json:"package_clause,omitempty"`
}

// GroundedEvidenceRepositoryRelationEndpoint preserves one independent relation endpoint.
type GroundedEvidenceRepositoryRelationEndpoint struct {
	Role            GroundedEvidenceRepositoryContextRole  `json:"role"`
	Path            string                                 `json:"path"`
	Anchor          GroundedEvidenceRepositoryContextSlice `json:"anchor"`
	AtomicContainer GroundedEvidenceRepositoryContextSlice `json:"atomic_container"`
	PackageClause   GroundedEvidenceRepositoryContextSlice `json:"package_clause"`
	ReferencedBytes int                                    `json:"referenced_bytes"`
}

// GroundedEvidenceRepositoryRelationContext contains ordered typed endpoints.
type GroundedEvidenceRepositoryRelationContext struct {
	Endpoints []GroundedEvidenceRepositoryRelationEndpoint `json:"endpoints"`
}

// GroundedEvidenceRepositoryContext is one self-contained post-retrieval repository package.
type GroundedEvidenceRepositoryContext struct {
	ProposalOccurrenceID string                                        `json:"-"`
	Contract             string                                        `json:"contract"`
	Status               GroundedEvidenceRepositoryContextStatus       `json:"status"`
	RecordKind           GroundedEvidenceRepositoryRecordKind          `json:"record_kind"`
	RelationKind         string                                        `json:"relation_kind,omitempty"`
	SearchParticipation  bool                                          `json:"search_participation"`
	SearchCore           []ResolvedSourceRef                           `json:"search_core"`
	ReferencedBytes      int                                           `json:"referenced_bytes"`
	LimitReason          string                                        `json:"limit_reason,omitempty"`
	Declaration          *GroundedEvidenceRepositoryDeclarationContext `json:"declaration,omitempty"`
	Relation             *GroundedEvidenceRepositoryRelationContext    `json:"relation,omitempty"`
	Limitations          []string                                      `json:"limitations"`
}

type groundedEvidenceRepositoryFileViewStatus string

const (
	groundedEvidenceRepositoryFileAvailable  groundedEvidenceRepositoryFileViewStatus = "available"
	groundedEvidenceRepositoryFileOverBudget groundedEvidenceRepositoryFileViewStatus = "over_budget"
)

type groundedEvidenceRepositoryFileViewResult struct {
	status         groundedEvidenceRepositoryFileViewStatus
	persistedBytes int64
	snapshot       RepositorySnapshot
	file           *RepositoryExtractorFile
}

func loadGroundedEvidenceRepositoryContexts(
	ctx context.Context,
	db sqlQueryer,
	matches []ProposalSearchResult,
) ([]GroundedEvidenceRepositoryContext, error) {
	contexts := make([]GroundedEvidenceRepositoryContext, 0, len(matches))
	fileCache := make(map[string]groundedEvidenceRepositoryFileViewResult)
	compiler := newGroundedEvidenceRepositoryContextCompiler()
	for _, match := range matches {
		record := match.Record
		if record.SourceBindingKind != ProposalSourceBindingRepositorySnapshot {
			continue
		}
		requiredFiles, err := groundedEvidenceRepositoryContextFiles(record)
		if err != nil {
			return nil, err
		}
		filesByPath := make(map[string]RepositoryExtractorFile, len(requiredFiles))
		fileOverBudget := false
		for path, fileID := range requiredFiles {
			cacheKey := record.RepositorySnapshot.ID + "\x00" + fileID
			loaded, ok := fileCache[cacheKey]
			if !ok {
				loaded, err = loadBoundedGroundedEvidenceRepositoryFile(
					ctx,
					db,
					record.RepositorySnapshot.ID,
					fileID,
				)
				if err != nil {
					return nil, err
				}
				fileCache[cacheKey] = loaded
			}
			if loaded.snapshot != *record.RepositorySnapshot ||
				loaded.file != nil && loaded.file.FileSnapshot.Path != path {
				return nil, newDomainError(
					ErrorOccurrenceConflict,
					"proposal occurrence %s repository file identity differs",
					record.ProposalOccurrenceID,
				)
			}
			if loaded.status == groundedEvidenceRepositoryFileOverBudget {
				fileOverBudget = true
				continue
			}
			if loaded.file == nil {
				return nil, newDomainError(
					ErrorOccurrenceConflict,
					"proposal occurrence %s repository file %s has no payload",
					record.ProposalOccurrenceID,
					fileID,
				)
			}
			filesByPath[path] = *loaded.file
		}
		if fileOverBudget {
			contexts = append(
				contexts,
				groundedEvidenceRepositoryFileOverBudgetContext(record),
			)
			continue
		}
		context, err := compiler.compile(
			record,
			filesByPath,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"compiling proposal occurrence %s repository context: %w",
				record.ProposalOccurrenceID,
				err,
			)
		}
		contexts = append(contexts, context)
	}
	return contexts, nil
}

func groundedEvidenceRepositoryContextFiles(
	record ProposalQueryResult,
) (map[string]string, error) {
	if record.RepositorySnapshot == nil {
		return nil, newDomainError(
			ErrorOccurrenceConflict,
			"proposal occurrence %s has no repository snapshot",
			record.ProposalOccurrenceID,
		)
	}
	if (record.CodeFact == nil) == (record.CodeRelation == nil) {
		return nil, newDomainError(
			ErrorOccurrenceConflict,
			"proposal occurrence %s must have exactly one code fact or relation",
			record.ProposalOccurrenceID,
		)
	}
	if len(record.SourceRefs) == 0 {
		return nil, newDomainError(
			ErrorOccurrenceConflict,
			"proposal occurrence %s has no repository source refs",
			record.ProposalOccurrenceID,
		)
	}
	files := make(map[string]string)
	for _, ref := range record.SourceRefs {
		if ref.TargetKind != "file_snapshot" ||
			ref.RepositorySnapshotID != record.RepositorySnapshot.ID ||
			ref.FileSnapshotID == "" ||
			ref.Path == "" {
			return nil, newDomainError(
				ErrorOccurrenceConflict,
				"proposal occurrence %s has incompatible repository source refs",
				record.ProposalOccurrenceID,
			)
		}
		if existing, ok := files[ref.Path]; ok && existing != ref.FileSnapshotID {
			return nil, newDomainError(
				ErrorOccurrenceConflict,
				"proposal occurrence %s path %s resolves to multiple files",
				record.ProposalOccurrenceID,
				ref.Path,
			)
		}
		files[ref.Path] = ref.FileSnapshotID
	}
	return files, nil
}

func loadBoundedGroundedEvidenceRepositoryFile(
	ctx context.Context,
	db sqlQueryer,
	repositorySnapshotID string,
	fileSnapshotID string,
) (groundedEvidenceRepositoryFileViewResult, error) {
	var result groundedEvidenceRepositoryFileViewResult
	var meta SourceFileSnapshot
	err := db.queryRow(ctx, `
		SELECT
			rs.repository_snapshot_id,
			rs.repo_id,
			rs.commit_sha,
			rs.manifest_hash,
			rs.manifest_entry_count,
			rs.revision_verification_method,
			rs.manifest_contract,
			rs.file_selection_contract,
			rs.selected_file_count,
			sfs.file_snapshot_id,
			sfs.repository_snapshot_id,
			sfs.repo_id,
			sfs.commit_sha,
			sfs.path,
			sfs.blob_hash,
			sfs.git_blob_oid,
			sfs.byte_length,
			octet_length(sb.raw_content)::bigint
		FROM repository_snapshots rs
		JOIN source_file_snapshots sfs
			ON sfs.repository_snapshot_id = rs.repository_snapshot_id
		JOIN source_blobs sb
			ON sb.raw_content_hash = sfs.blob_hash
		WHERE rs.repository_snapshot_id = $1
			AND sfs.file_snapshot_id = $2
	`, repositorySnapshotID, fileSnapshotID).Scan(
		&result.snapshot.ID,
		&result.snapshot.RepoID,
		&result.snapshot.CommitSHA,
		&result.snapshot.ManifestHash,
		&result.snapshot.ManifestEntryCount,
		&result.snapshot.RevisionVerificationMethod,
		&result.snapshot.ManifestContract,
		&result.snapshot.FileSelectionContract,
		&result.snapshot.SelectedFileCount,
		&meta.ID,
		&meta.RepositorySnapshotID,
		&meta.RepoID,
		&meta.CommitSHA,
		&meta.Path,
		&meta.BlobHash,
		&meta.GitBlobOID,
		&meta.ByteLength,
		&result.persistedBytes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return groundedEvidenceRepositoryFileViewResult{}, newDomainError(
				ErrorOccurrenceConflict,
				"repository file %s/%s was not found",
				repositorySnapshotID,
				fileSnapshotID,
			)
		}
		return groundedEvidenceRepositoryFileViewResult{}, fmt.Errorf(
			"preflighting repository context file: %w",
			err,
		)
	}
	if result.persistedBytes < 0 ||
		result.persistedBytes != int64(meta.ByteLength) ||
		meta.RepositorySnapshotID != result.snapshot.ID ||
		meta.RepoID != result.snapshot.RepoID ||
		meta.CommitSHA != result.snapshot.CommitSHA {
		return groundedEvidenceRepositoryFileViewResult{}, newDomainError(
			ErrorRepositorySnapshotIntegrity,
			"repository file %s preflight identity differs",
			fileSnapshotID,
		)
	}
	if result.persistedBytes > GroundedEvidenceRepositoryFileMaxBytesV1 {
		result.status = groundedEvidenceRepositoryFileOverBudget
		return result, nil
	}

	var content []byte
	err = db.queryRow(ctx, `
		SELECT sb.raw_content
		FROM source_file_snapshots sfs
		JOIN source_blobs sb
			ON sb.raw_content_hash = sfs.blob_hash
		WHERE sfs.repository_snapshot_id = $1
			AND sfs.file_snapshot_id = $2
			AND sfs.blob_hash = $3
	`, repositorySnapshotID, fileSnapshotID, meta.BlobHash).Scan(&content)
	if err != nil {
		return groundedEvidenceRepositoryFileViewResult{}, fmt.Errorf(
			"loading repository context file: %w",
			err,
		)
	}
	file := RepositoryExtractorFile{FileSnapshot: meta, Content: content}
	if err := validateRepositoryExtractorFile(result.snapshot, file); err != nil {
		return groundedEvidenceRepositoryFileViewResult{}, err
	}
	result.status = groundedEvidenceRepositoryFileAvailable
	result.file = &file
	return result, nil
}

func groundedEvidenceRepositoryFileOverBudgetContext(
	record ProposalQueryResult,
) GroundedEvidenceRepositoryContext {
	result := newGroundedEvidenceRepositoryContext(record)
	result.Status = GroundedEvidenceRepositoryContextStatusFileViewOverBudget
	result.LimitReason = "repository_file_view_bytes"
	return result
}

func newGroundedEvidenceRepositoryContext(
	record ProposalQueryResult,
) GroundedEvidenceRepositoryContext {
	result := GroundedEvidenceRepositoryContext{
		ProposalOccurrenceID: record.ProposalOccurrenceID,
		Contract:             GroundedEvidenceRepositoryContextContractV1,
		SearchParticipation:  false,
		SearchCore:           append([]ResolvedSourceRef(nil), record.SourceRefs...),
	}
	if record.CodeFact != nil {
		result.RecordKind = GroundedEvidenceRepositoryRecordDeclaration
		result.Limitations = []string{
			sourceContextLimitationPostRetrievalOnly,
			"repository_syntax_is_source_context_not_an_evidence_claim",
			"declaration_context_does_not_establish_runtime_behavior",
		}
	} else {
		result.RecordKind = GroundedEvidenceRepositoryRecordRelation
		result.RelationKind = record.CodeRelation.RelationKind
		result.Limitations = []string{
			sourceContextLimitationPostRetrievalOnly,
			"repository_syntax_is_source_context_not_an_evidence_claim",
			"relation_endpoints_remain_separate_source_contexts",
			"relation_context_does_not_establish_causality_or_truth",
			"ambiguous_targets_are_coverage_diagnostics_not_relation_records",
		}
	}
	return result
}
