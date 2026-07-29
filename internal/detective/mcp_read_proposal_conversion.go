package detective

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MCPReadProposalConversionContract = "detective-mcp-read-proposal-conversion-v1"

	MCPReadProposalConversionStatusConverted = "converted"
	MCPReadProposalConversionStatusRejected  = "rejected"

	MCPReadProposalConversionReasonIncompleteCoverage   = "incomplete_coverage"
	MCPReadProposalConversionReasonNoProposalCandidates = "no_proposal_candidates"

	mcpReadProposalExtractorName    = "detective-mcp-read-proposal-candidates"
	mcpReadProposalExtractorVersion = "v1"
)

// MCPReadProposalConversionInput selects one exact persisted MCP document
// snapshot. Conversion never invokes the provider.
type MCPReadProposalConversionInput struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
}

// MCPReadProposalConversionResult reports either a completed pending proposal
// batch or a bounded rejection that created no extraction attempt.
type MCPReadProposalConversionResult struct {
	Contract               string `json:"contract"`
	Status                 string `json:"status"`
	Reason                 string `json:"reason,omitempty"`
	SourceSnapshotID       string `json:"source_snapshot_id"`
	ExtractionViewID       string `json:"extraction_view_id"`
	ConversionRequestID    string `json:"conversion_request_id"`
	ProposalCount          int    `json:"proposal_count"`
	ProposalOccurrenceID   string `json:"proposal_occurrence_id,omitempty"`
	ExtractionRunID        string `json:"extraction_run_id,omitempty"`
	ExtractionAttemptID    string `json:"extraction_attempt_id,omitempty"`
	Replayed               bool   `json:"replayed"`
	GlobalAbsenceInference bool   `json:"global_absence_inference_allowed"`
}

type mcpReadProposalSource struct {
	extractionViewID string
	sourceVersion    string
	rawContentHash   string
	origin           map[string]string
	candidates       []MCPReadProposalCandidate
}

// ConvertMCPReadDocumentSnapshot deterministically resolves adapter-declared
// candidates against one complete persisted MCP document. It never calls the
// provider and creates pending proposals only.
func ConvertMCPReadDocumentSnapshot(
	ctx context.Context,
	pool *pgxpool.Pool,
	input MCPReadProposalConversionInput,
) (MCPReadProposalConversionResult, error) {
	if pool == nil {
		return MCPReadProposalConversionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	sourceSnapshotID, err := normalizeDetectiveHashID(input.SourceSnapshotID, "srcsnap:", "source_snapshot_id")
	if err != nil {
		return MCPReadProposalConversionResult{}, err
	}
	source, err := loadMCPReadProposalSource(ctx, pool, sourceSnapshotID)
	if err != nil {
		return MCPReadProposalConversionResult{}, err
	}
	requestID := mcpReadProposalConversionRequestID(sourceSnapshotID)
	result := MCPReadProposalConversionResult{
		Contract:               MCPReadProposalConversionContract,
		SourceSnapshotID:       sourceSnapshotID,
		ExtractionViewID:       source.extractionViewID,
		ConversionRequestID:    requestID,
		GlobalAbsenceInference: false,
	}
	if reason, err := validateMCPReadProposalSource(ctx, pool, sourceSnapshotID, source); err != nil {
		return MCPReadProposalConversionResult{}, err
	} else if reason != "" {
		result.Status = MCPReadProposalConversionStatusRejected
		result.Reason = reason
		return result, nil
	}

	extractorInput, err := evidenceingestion.BuildExtractorInput(ctx, pool, source.extractionViewID)
	if err != nil {
		return MCPReadProposalConversionResult{}, err
	}
	if extractorInput.SourceSnapshotID != sourceSnapshotID ||
		extractorInput.SourceSystem != evidenceingestion.SourceSystemMCPReadDocument ||
		extractorInput.SourceVersion != source.sourceVersion ||
		extractorInput.RawContentHash != source.rawContentHash ||
		extractorInput.Renderer.Name != evidenceingestion.RendererMCPReadDocumentIdentity ||
		extractorInput.Renderer.Version != evidenceingestion.RendererMCPReadDocumentIdentityVersion ||
		extractorInput.SpanCatalogVersion != evidenceingestion.SpanCatalogMCPReadDocumentLineV1 {
		return MCPReadProposalConversionResult{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s extraction view contradicts MCP document authority",
			sourceSnapshotID,
		)
	}
	if len(source.candidates) == 0 {
		result.Status = MCPReadProposalConversionStatusRejected
		result.Reason = MCPReadProposalConversionReasonNoProposalCandidates
		return result, nil
	}

	resolved, err := resolveMCPReadProposalCandidates(extractorInput, source.candidates)
	if err != nil {
		return MCPReadProposalConversionResult{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s proposal candidates contradict exact source bytes: %v",
			sourceSnapshotID,
			err,
		)
	}
	proposals := make([]evidenceingestion.ExtractorProposalOutput, 0, len(resolved))
	for _, candidate := range resolved {
		proposals = append(proposals, evidenceingestion.ExtractorProposalOutput{
			ProposalLocalID: candidate.localID,
			StatementText:   candidate.statement,
			EvidenceRefs:    []string{candidate.spanID},
		})
	}
	ingested, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, evidenceingestion.ExtractorOutputInput{
		RequestID:        requestID,
		SourceSnapshotID: sourceSnapshotID,
		ExtractionViewID: source.extractionViewID,
		ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{
			Name:    mcpReadProposalExtractorName,
			Version: mcpReadProposalExtractorVersion,
			Config: map[string]string{
				"conversion_contract": MCPReadProposalConversionContract,
				"proposal_contract":   "adapter-selected-source-statements-v1",
				"selector_contract":   "line-or-json-pointer-string-v1",
				"span_catalog":        evidenceingestion.SpanCatalogMCPReadDocumentLineV1,
				"coverage_contract":   "complete-document-only-v1",
			},
		},
		Output: evidenceingestion.FrozenExtractorOutput{Proposals: proposals},
	})
	if err != nil {
		return MCPReadProposalConversionResult{}, err
	}
	result.Status = MCPReadProposalConversionStatusConverted
	result.ProposalCount = len(proposals)
	result.ProposalOccurrenceID = ingested.ProposalOccurrenceID
	result.ExtractionRunID = ingested.ExtractionRunID
	result.ExtractionAttemptID = ingested.ExtractionAttemptID
	result.Replayed = ingested.Replayed
	return result, nil
}

// ConvertLatestMCPReadSourceSnapshot replays conversion for the latest
// completed cycle of one source. It returns found=false before the first
// successful collection and performs no provider call.
func ConvertLatestMCPReadSourceSnapshot(
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID string,
	sourceBindingID string,
) (MCPReadProposalConversionResult, bool, error) {
	if pool == nil {
		return MCPReadProposalConversionResult{}, false, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	workspaceID, err := normalizeWorkspaceID(workspaceID)
	if err != nil {
		return MCPReadProposalConversionResult{}, false, err
	}
	sourceBindingID, err = normalizeDetectiveHashID(sourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return MCPReadProposalConversionResult{}, false, err
	}
	var sourceSnapshotID string
	err = pool.QueryRow(ctx, `
		SELECT source_snapshot_id
		FROM detective_mcp_read_collection_cycles
		WHERE workspace_id = $1
			AND source_binding_id = $2
			AND status = 'completed'
		ORDER BY cycle_number DESC
		LIMIT 1
	`, workspaceID, sourceBindingID).Scan(&sourceSnapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPReadProposalConversionResult{}, false, nil
	}
	if err != nil {
		return MCPReadProposalConversionResult{}, false, fmt.Errorf("reading latest completed MCP source cycle: %w", err)
	}
	result, err := ConvertMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalConversionInput{
		SourceSnapshotID: sourceSnapshotID,
	})
	return result, true, err
}

func loadMCPReadProposalSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	sourceSnapshotID string,
) (mcpReadProposalSource, error) {
	var sourceSystem, originJSON string
	var source mcpReadProposalSource
	err := pool.QueryRow(ctx, `
		SELECT
			ss.source_system,
			ss.source_version,
			ss.raw_content_hash,
			ss.origin_metadata::text,
			ev.extraction_view_id
		FROM source_snapshots ss
		JOIN extraction_views ev
			ON ev.source_snapshot_id = ss.source_snapshot_id
			AND ev.renderer_name = $2
			AND ev.renderer_version = $3
		WHERE ss.source_snapshot_id = $1
	`, sourceSnapshotID,
		evidenceingestion.RendererMCPReadDocumentIdentity,
		evidenceingestion.RendererMCPReadDocumentIdentityVersion,
	).Scan(
		&sourceSystem,
		&source.sourceVersion,
		&source.rawContentHash,
		&originJSON,
		&source.extractionViewID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return mcpReadProposalSource{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s is not an MCP document with its pinned extraction view",
			sourceSnapshotID,
		)
	}
	if err != nil {
		return mcpReadProposalSource{}, fmt.Errorf("loading MCP document proposal source: %w", err)
	}
	if sourceSystem != evidenceingestion.SourceSystemMCPReadDocument {
		return mcpReadProposalSource{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s has source_system %q",
			sourceSnapshotID,
			sourceSystem,
		)
	}
	if err := json.Unmarshal([]byte(originJSON), &source.origin); err != nil {
		return mcpReadProposalSource{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s has invalid origin metadata",
			sourceSnapshotID,
		)
	}
	candidatesJSON := source.origin["mcp_proposal_candidates_json"]
	if candidatesJSON == "" {
		return mcpReadProposalSource{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s is missing proposal candidate provenance",
			sourceSnapshotID,
		)
	}
	if err := decodeStrictMCPJSON([]byte(candidatesJSON), &source.candidates); err != nil {
		return mcpReadProposalSource{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s has invalid proposal candidate provenance",
			sourceSnapshotID,
		)
	}
	return source, nil
}

func validateMCPReadProposalSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	sourceSnapshotID string,
	source mcpReadProposalSource,
) (string, error) {
	required := []string{
		"connector_delivery_id",
		"connector_id",
		"connector_payload_hash",
		"mcp_provider",
		"mcp_object_id",
		"mcp_revision",
		"mcp_document_id",
		"mcp_document_content_hash",
		"mcp_source_identity_contract",
		"mcp_proposal_candidate_contract",
		"mcp_proposal_candidates_json",
		"mcp_coverage_complete",
		"mcp_coverage_truncated",
		"mcp_coverage_completion_reason",
		"mcp_limitations_json",
		"global_absence_inference_allowed",
	}
	for _, field := range required {
		if source.origin[field] == "" {
			return "", newDomainError(
				ErrorMCPProposalConversionRejected,
				"source snapshot %s is missing %s provenance",
				sourceSnapshotID,
				field,
			)
		}
	}
	if source.origin["mcp_revision"] != source.sourceVersion ||
		source.origin["mcp_document_content_hash"] != source.rawContentHash ||
		source.origin["mcp_source_identity_contract"] != mcpReadSourceIdentityContract ||
		source.origin["mcp_proposal_candidate_contract"] != mcpReadProposalCandidateContract ||
		source.origin["global_absence_inference_allowed"] != "false" {
		return "", newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s has contradictory revision, content hash, or absence authority",
			sourceSnapshotID,
		)
	}
	complete, completeErr := strconv.ParseBool(source.origin["mcp_coverage_complete"])
	truncated, truncatedErr := strconv.ParseBool(source.origin["mcp_coverage_truncated"])
	if completeErr != nil || truncatedErr != nil {
		return "", newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s has invalid coverage provenance",
			sourceSnapshotID,
		)
	}
	var limitations []string
	if err := json.Unmarshal([]byte(source.origin["mcp_limitations_json"]), &limitations); err != nil {
		return "", newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s has invalid limitations provenance",
			sourceSnapshotID,
		)
	}
	if !complete || truncated ||
		source.origin["mcp_coverage_completion_reason"] != MCPReadCompletionComplete {
		return MCPReadProposalConversionReasonIncompleteCoverage, nil
	}

	var linked bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM detective_connector_inbox_deliveries
			WHERE connector_delivery_id = $1
				AND connector_id = $2
				AND payload_hash = $3
		)
	`,
		source.origin["connector_delivery_id"],
		source.origin["connector_id"],
		source.origin["connector_payload_hash"],
	).Scan(&linked); err != nil {
		return "", fmt.Errorf("validating MCP document connector provenance: %w", err)
	}
	if !linked {
		return "", newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s has no matching connector delivery authority",
			sourceSnapshotID,
		)
	}
	return "", nil
}

func mcpReadProposalConversionRequestID(sourceSnapshotID string) string {
	return "detective-mcp-proposal:" + hashHex([]byte(
		MCPReadProposalConversionContract+"\x00"+sourceSnapshotID,
	))
}
