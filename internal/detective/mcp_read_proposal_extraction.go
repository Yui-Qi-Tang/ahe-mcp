package detective

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MCPReadProposalExtractionContract        = "detective-mcp-read-proposal-extraction-v1"
	MCPReadProposalSectionExtractionContract = "detective-mcp-read-proposal-extraction-v2"

	MCPReadProposalExtractionStatusExtracted = "extracted"
	MCPReadProposalExtractionStatusAbstained = "abstained"
	MCPReadProposalExtractionStatusRejected  = "rejected"
)

// MCPReadProposalExtractionInput binds one exact persisted source to one
// operator-selected model runner. The runner is invoked outside PostgreSQL.
type MCPReadProposalExtractionInput struct {
	SourceSnapshotID    string
	MaxProposals        int
	SectionMode         string
	MaxSections         int
	ExtractorDefinition evidenceingestion.ExtractorDefinitionInput
	Runner              evidenceingestion.ExtractorRunner
}

// MCPReadProposalExtractionResult reports source-bound model proposals or an
// audited abstention. Neither outcome performs admission or graph activation.
type MCPReadProposalExtractionResult struct {
	Contract                string                            `json:"contract"`
	Status                  string                            `json:"status"`
	Reason                  string                            `json:"reason,omitempty"`
	SourceSnapshotID        string                            `json:"source_snapshot_id"`
	ExtractionViewID        string                            `json:"extraction_view_id"`
	ExtractionRequestID     string                            `json:"extraction_request_id"`
	ProposalCount           int                               `json:"proposal_count"`
	ProposalOccurrenceID    string                            `json:"proposal_occurrence_id,omitempty"`
	ExtractionRunID         string                            `json:"extraction_run_id,omitempty"`
	ExtractionAttemptID     string                            `json:"extraction_attempt_id,omitempty"`
	ModelInvoked            bool                              `json:"model_invoked"`
	ModelCallCount          int                               `json:"model_call_count"`
	SectionCount            int                               `json:"section_count"`
	SectionCoverageComplete bool                              `json:"section_coverage_complete"`
	Replayed                bool                              `json:"replayed"`
	GlobalAbsenceInference  bool                              `json:"global_absence_inference_allowed"`
	SelectionCoverage       *MCPReadProposalSelectionCoverage `json:"selection_coverage,omitempty"`
}

// MCPReadProposalSelectionCoverage counts controller units, not facts or claims.
// Section processing and selection do not assess extraction completeness.
type MCPReadProposalSelectionCoverage struct {
	EligibleUnitCount        int  `json:"eligible_unit_count"`
	SelectedUnitCount        int  `json:"selected_unit_count"`
	UnselectedUnitCount      int  `json:"unselected_unit_count"`
	MaxProposalsPerCall      int  `json:"max_proposals_per_call"`
	FactCompletenessAssessed bool `json:"fact_completeness_assessed"`
}

// ExtractMCPReadDocumentSnapshot runs a bounded source-quote extractor over
// adapter-selected text from one complete persisted MCP document.
func ExtractMCPReadDocumentSnapshot(
	ctx context.Context,
	pool *pgxpool.Pool,
	input MCPReadProposalExtractionInput,
) (MCPReadProposalExtractionResult, error) {
	if pool == nil {
		return MCPReadProposalExtractionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if input.Runner == nil {
		return MCPReadProposalExtractionResult{}, newDomainError(ErrorInvalidInput, "proposal extractor runner is required")
	}
	contract, err := validateMCPReadProposalExtractionInput(input)
	if err != nil {
		return MCPReadProposalExtractionResult{}, err
	}
	sourceSnapshotID, err := normalizeDetectiveHashID(input.SourceSnapshotID, "srcsnap:", "source_snapshot_id")
	if err != nil {
		return MCPReadProposalExtractionResult{}, err
	}
	source, err := loadMCPReadProposalSource(ctx, pool, sourceSnapshotID)
	if err != nil {
		return MCPReadProposalExtractionResult{}, err
	}
	requestID := mcpReadProposalExtractionRequestID(contract, sourceSnapshotID)
	result := MCPReadProposalExtractionResult{
		Contract:               contract,
		SourceSnapshotID:       sourceSnapshotID,
		ExtractionViewID:       source.extractionViewID,
		ExtractionRequestID:    requestID,
		GlobalAbsenceInference: false,
	}
	if reason, err := validateMCPReadProposalSource(ctx, pool, sourceSnapshotID, source); err != nil {
		return MCPReadProposalExtractionResult{}, err
	} else if reason != "" {
		result.Status = MCPReadProposalExtractionStatusRejected
		result.Reason = reason
		return result, nil
	}
	if len(source.candidates) == 0 {
		result.Status = MCPReadProposalExtractionStatusRejected
		result.Reason = MCPReadProposalConversionReasonNoProposalCandidates
		return result, nil
	}

	extractorInput, err := evidenceingestion.BuildExtractorInput(ctx, pool, source.extractionViewID)
	if err != nil {
		return MCPReadProposalExtractionResult{}, err
	}
	if err := validateMCPReadProposalExtractionView(sourceSnapshotID, source, extractorInput); err != nil {
		return MCPReadProposalExtractionResult{}, err
	}
	resolved, err := resolveMCPReadProposalCandidates(extractorInput, source.candidates)
	if err != nil {
		return MCPReadProposalExtractionResult{}, newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s proposal candidates contradict exact source bytes: %v",
			sourceSnapshotID,
			err,
		)
	}
	definition := mcpReadProposalExtractionDefinition(
		input.ExtractorDefinition,
		contract,
		input.SectionMode,
		input.MaxSections,
		input.MaxProposals,
	)
	modelCallCount := 0
	eligibleUnitCount := 0
	var runner evidenceingestion.ExtractorRunner
	if input.SectionMode == "" {
		boundedInput, err := boundedMCPReadProposalExtractorInput(extractorInput, resolved)
		if err != nil {
			return MCPReadProposalExtractionResult{}, err
		}
		eligibleUnitCount = len(boundedInput.Spans)
		runner = func(ctx context.Context, _ evidenceingestion.ExtractorInput) ([]byte, error) {
			modelCallCount++
			data, err := input.Runner(ctx, boundedInput)
			if err != nil {
				return data, err
			}
			if _, err := decodeMCPReadProposalExtractionOutput(
				data,
				boundedInput,
				input.MaxProposals,
				definition,
			); err != nil {
				return data, err
			}
			return data, nil
		}
	} else {
		sections, err := sectionBoundMCPReadProposalExtractorInputs(
			extractorInput,
			resolved,
			input.MaxSections,
		)
		if err != nil {
			return MCPReadProposalExtractionResult{}, err
		}
		result.SectionCount = len(sections)
		eligibleUnitCount = len(sections)
		runner = func(ctx context.Context, _ evidenceingestion.ExtractorInput) ([]byte, error) {
			output := evidenceingestion.FrozenExtractorOutput{
				Proposals: make([]evidenceingestion.ExtractorProposalOutput, 0),
				DocumentSectionCoverage: &evidenceingestion.DocumentSectionCoverage{
					SchemaVersion:            evidenceingestion.DocumentSectionCoverageSchemaV1,
					SegmentationContract:     input.SectionMode,
					OffsetBasis:              evidenceingestion.DocumentSectionCoverageOffsetAdapterSelectedUTF8,
					CoverageComplete:         true, // Every section is processed; facts are not counted.
					NegativeInferenceAllowed: false,
					SelectedSourceCount:      len(resolved),
					SectionCount:             len(sections),
					ModelCallCount:           len(sections),
					MaxSections:              input.MaxSections,
					MaxProposalsPerSection:   input.MaxProposals,
					Sections:                 make([]evidenceingestion.DocumentSectionCoverageItem, 0, len(sections)),
				},
			}
			for _, section := range sections {
				modelCallCount++
				data, err := input.Runner(ctx, section.input)
				if err != nil {
					return data, err
				}
				decoded, err := decodeMCPReadProposalExtractionOutput(
					data,
					section.input,
					input.MaxProposals,
					definition,
				)
				if err != nil {
					return data, err
				}
				localIDs := make([]string, 0, len(decoded.Proposals))
				for index := range decoded.Proposals {
					localID := fmt.Sprintf("%s-proposal-%02d", section.id, index+1)
					decoded.Proposals[index].ProposalLocalID = localID
					localIDs = append(localIDs, localID)
				}
				output.Proposals = append(output.Proposals, decoded.Proposals...)
				output.DocumentSectionCoverage.Sections = append(
					output.DocumentSectionCoverage.Sections,
					evidenceingestion.DocumentSectionCoverageItem{
						SectionID:        section.id,
						CandidateLocalID: section.candidateLocalID,
						Heading:          section.heading,
						StartByte:        section.startByte,
						EndByte:          section.endByte,
						ContentHash:      mcpReadSectionContentHash([]byte(section.text)),
						ResponseHash:     mcpReadSectionContentHash(data),
						ProposalLocalIDs: localIDs,
						Abstained:        len(localIDs) == 0,
					},
				)
			}
			return json.Marshal(output)
		}
	}
	ingested, err := evidenceingestion.RunTrustedExtractor(
		ctx,
		pool,
		evidenceingestion.TrustedExtractorRequest{
			RequestID:           requestID,
			ExtractionViewID:    source.extractionViewID,
			ExtractorDefinition: definition,
		},
		runner,
	)
	if err != nil {
		return MCPReadProposalExtractionResult{}, err
	}
	result.ProposalCount = ingested.ProposalCount
	if definition.Config["output_contract"] == evidenceingestion.OllamaExtractorPromptWholeSpanExactQuote {
		result.SelectionCoverage = &MCPReadProposalSelectionCoverage{
			EligibleUnitCount:   eligibleUnitCount,
			SelectedUnitCount:   ingested.ProposalCount,
			UnselectedUnitCount: eligibleUnitCount - ingested.ProposalCount,
			MaxProposalsPerCall: input.MaxProposals,
		}
	}
	result.ProposalOccurrenceID = ingested.ProposalOccurrenceID
	result.ExtractionRunID = ingested.ExtractionRunID
	result.ExtractionAttemptID = ingested.ExtractionAttemptID
	result.ModelInvoked = modelCallCount > 0
	result.ModelCallCount = modelCallCount
	result.SectionCoverageComplete = input.SectionMode != ""
	result.Replayed = ingested.Replayed
	if ingested.ProposalCount == 0 {
		result.Status = MCPReadProposalExtractionStatusAbstained
	} else {
		result.Status = MCPReadProposalExtractionStatusExtracted
	}
	return result, nil
}

// ExtractLatestMCPReadSourceSnapshot replays extraction for the latest
// completed source cycle, if one exists.
func ExtractLatestMCPReadSourceSnapshot(
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID string,
	sourceBindingID string,
	input MCPReadProposalExtractionInput,
) (MCPReadProposalExtractionResult, bool, error) {
	if pool == nil {
		return MCPReadProposalExtractionResult{}, false, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	workspaceID, err := normalizeWorkspaceID(workspaceID)
	if err != nil {
		return MCPReadProposalExtractionResult{}, false, err
	}
	sourceBindingID, err = normalizeDetectiveHashID(sourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return MCPReadProposalExtractionResult{}, false, err
	}
	err = pool.QueryRow(ctx, `
		SELECT source_snapshot_id
		FROM detective_mcp_read_collection_cycles
		WHERE workspace_id = $1
			AND source_binding_id = $2
			AND status = 'completed'
		ORDER BY cycle_number DESC
		LIMIT 1
	`, workspaceID, sourceBindingID).Scan(&input.SourceSnapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPReadProposalExtractionResult{}, false, nil
	}
	if err != nil {
		return MCPReadProposalExtractionResult{}, false, fmt.Errorf("reading latest completed MCP source cycle: %w", err)
	}
	result, err := ExtractMCPReadDocumentSnapshot(ctx, pool, input)
	return result, true, err
}

func validateMCPReadProposalExtractionView(
	sourceSnapshotID string,
	source mcpReadProposalSource,
	input evidenceingestion.ExtractorInput,
) error {
	if input.SourceSnapshotID != sourceSnapshotID ||
		input.SourceSystem != evidenceingestion.SourceSystemMCPReadDocument ||
		input.SourceVersion != source.sourceVersion ||
		input.RawContentHash != source.rawContentHash ||
		input.Renderer.Name != evidenceingestion.RendererMCPReadDocumentIdentity ||
		input.Renderer.Version != evidenceingestion.RendererMCPReadDocumentIdentityVersion ||
		input.SpanCatalogVersion != evidenceingestion.SpanCatalogMCPReadDocumentLineV1 {
		return newDomainError(
			ErrorMCPProposalConversionRejected,
			"source snapshot %s extraction view contradicts MCP document authority",
			sourceSnapshotID,
		)
	}
	return nil
}

func boundedMCPReadProposalExtractorInput(
	input evidenceingestion.ExtractorInput,
	resolved []resolvedMCPReadProposalCandidate,
) (evidenceingestion.ExtractorInput, error) {
	spansByID := make(map[string]evidenceingestion.ExtractorInputSpan, len(input.Spans))
	for _, span := range input.Spans {
		spansByID[span.SpanID] = span
	}
	bounded := input
	bounded.RenderedText = ""
	bounded.Spans = make([]evidenceingestion.ExtractorInputSpan, 0, len(resolved))
	seenSpanIDs := make(map[string]struct{}, len(resolved))
	for _, candidate := range resolved {
		if _, exists := seenSpanIDs[candidate.spanID]; exists {
			return evidenceingestion.ExtractorInput{}, newDomainError(
				ErrorMCPProposalConversionRejected,
				"multiple proposal candidates share source span %s",
				candidate.spanID,
			)
		}
		seenSpanIDs[candidate.spanID] = struct{}{}
		span, exists := spansByID[candidate.spanID]
		if !exists {
			return evidenceingestion.ExtractorInput{}, newDomainError(
				ErrorMCPProposalConversionRejected,
				"proposal candidate span %s is absent",
				candidate.spanID,
			)
		}
		span.Text = candidate.statement
		span.QuotedTextHash = ""
		bounded.Spans = append(bounded.Spans, span)
	}
	return bounded, nil
}

func mcpReadProposalExtractionDefinition(
	input evidenceingestion.ExtractorDefinitionInput,
	contract string,
	sectionMode string,
	maxSections int,
	maxProposals int,
) evidenceingestion.ExtractorDefinitionInput {
	config := make(map[string]string, len(input.Config)+8)
	for key, value := range input.Config {
		config[key] = value
	}
	config["extraction_contract"] = contract
	config["input_contract"] = "adapter-selected-source-spans-v1"
	config["grounding_contract"] = "exact-selected-span-substring-v1"
	if config["output_contract"] == evidenceingestion.OllamaExtractorPromptWholeSpanExactQuote {
		config["selection_unit"] = "whole_current_span"
		config["coverage_semantics"] = "selection_only_not_fact_completeness"
	}
	if sectionMode == "" {
		config["max_proposals"] = fmt.Sprintf("%d", maxProposals)
	} else {
		config["section_mode"] = sectionMode
		config["max_sections"] = fmt.Sprintf("%d", maxSections)
		config["max_proposals_per_section"] = fmt.Sprintf("%d", maxProposals)
		config["document_section_coverage_schema"] = evidenceingestion.DocumentSectionCoverageSchemaV1
	}
	input.Config = config
	return input
}

func mcpReadProposalExtractionRequestID(contract string, sourceSnapshotID string) string {
	return strings.Join([]string{
		"mcp-read-proposal-extraction",
		contract,
		sourceSnapshotID,
	}, ":")
}

func validateMCPReadProposalExtractionInput(input MCPReadProposalExtractionInput) (string, error) {
	if input.MaxProposals < 1 || input.MaxProposals > maxMCPReadProposalCandidates {
		return "", newDomainError(
			ErrorInvalidInput,
			"max_proposals must be between 1 and %d",
			maxMCPReadProposalCandidates,
		)
	}
	switch input.SectionMode {
	case "":
		if input.MaxSections != 0 {
			return "", newDomainError(ErrorInvalidInput, "max_sections requires section_mode")
		}
		return MCPReadProposalExtractionContract, nil
	case MCPReadProposalSectionModeHeadingV1:
		if input.MaxSections < 1 || input.MaxSections > maxMCPReadProposalSectionCount {
			return "", newDomainError(
				ErrorInvalidInput,
				"max_sections must be between 1 and %d",
				maxMCPReadProposalSectionCount,
			)
		}
		if input.MaxProposals > maxMCPReadProposalSectionCount/input.MaxSections {
			return "", newDomainError(
				ErrorInvalidInput,
				"max_sections multiplied by max_proposals must not exceed %d",
				maxMCPReadProposalSectionCount,
			)
		}
		return MCPReadProposalSectionExtractionContract, nil
	default:
		return "", newDomainError(ErrorInvalidInput, "unsupported section_mode %q", input.SectionMode)
	}
}

func decodeMCPReadProposalExtractionOutput(
	data []byte,
	input evidenceingestion.ExtractorInput,
	maxProposals int,
	definition evidenceingestion.ExtractorDefinitionInput,
) (evidenceingestion.FrozenExtractorOutput, error) {
	if definition.Config["output_contract"] == evidenceingestion.OllamaExtractorPromptWholeSpanExactQuote {
		return evidenceingestion.DecodeWholeSpanExactQuoteSelectionOutput(data, input, maxProposals)
	}
	return evidenceingestion.DecodeBoundedExactQuoteExtractorOutput(data, input, maxProposals)
}
