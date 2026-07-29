package evidenceingestion

import (
	"strconv"
)

const (
	// DocumentSectionCoverageSchemaV1 identifies controller-produced document
	// section diagnostics persisted with one extractor attempt.
	DocumentSectionCoverageSchemaV1 = "ahe-document-section-coverage-v1"
	// DocumentSectionCoverageOffsetAdapterSelectedUTF8 states that byte offsets
	// are relative to the decoded adapter-selected UTF-8 value.
	DocumentSectionCoverageOffsetAdapterSelectedUTF8 = "adapter_selected_text_utf8_bytes"
)

func validateDocumentSectionCoverage(ctx attemptContext, fixture FrozenExtractorOutput) error {
	coverage := fixture.DocumentSectionCoverage
	if coverage == nil {
		return nil
	}
	if fixture.RepositoryGoplsCoverage != nil {
		return newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage cannot be combined with repository gopls coverage",
		)
	}
	if ctx.SourceSnapshot.SourceSystem != SourceSystemMCPReadDocument {
		return newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage requires an MCP read document source",
		)
	}
	if coverage.SchemaVersion != DocumentSectionCoverageSchemaV1 ||
		coverage.OffsetBasis != DocumentSectionCoverageOffsetAdapterSelectedUTF8 {
		return newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage schema or offset basis is unsupported",
		)
	}
	if !coverage.CoverageComplete || coverage.NegativeInferenceAllowed {
		return newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage must be complete and cannot allow negative inference",
		)
	}
	if coverage.SegmentationContract == "" {
		return newDomainError(ErrorInvalidExtractorOutput, "document section segmentation_contract is required")
	}
	config := ctx.ExtractorDefinition.Config
	if config["section_mode"] != coverage.SegmentationContract ||
		config["document_section_coverage_schema"] != coverage.SchemaVersion {
		return newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage contradicts extractor definition",
		)
	}
	maxSections, err := strconv.Atoi(config["max_sections"])
	if err != nil ||
		maxSections != coverage.MaxSections ||
		maxSections < 1 {
		return newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage max_sections contradicts extractor definition",
		)
	}
	maxProposals, err := strconv.Atoi(config["max_proposals_per_section"])
	if err != nil ||
		maxProposals != coverage.MaxProposalsPerSection ||
		maxProposals < 1 {
		return newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage max_proposals_per_section contradicts extractor definition",
		)
	}
	if coverage.SelectedSourceCount < 1 ||
		coverage.SectionCount != len(coverage.Sections) ||
		coverage.SectionCount < coverage.SelectedSourceCount ||
		coverage.SectionCount > coverage.MaxSections ||
		coverage.ModelCallCount != coverage.SectionCount {
		return newDomainError(ErrorInvalidExtractorOutput, "document section coverage counts are inconsistent")
	}

	proposals := make(map[string]struct{}, len(fixture.Proposals))
	for _, proposal := range fixture.Proposals {
		proposals[proposal.ProposalLocalID] = struct{}{}
	}
	coveredProposals := make(map[string]struct{}, len(fixture.Proposals))
	seenSections := make(map[string]struct{}, len(coverage.Sections))
	lastEndByCandidate := make(map[string]int, coverage.SelectedSourceCount)
	seenCandidates := make(map[string]struct{}, coverage.SelectedSourceCount)
	for _, section := range coverage.Sections {
		if section.SectionID == "" || section.CandidateLocalID == "" {
			return newDomainError(ErrorInvalidExtractorOutput, "document section coverage identity is required")
		}
		if _, exists := seenSections[section.SectionID]; exists {
			return newDomainError(
				ErrorInvalidExtractorOutput,
				"document section coverage duplicates section_id %q",
				section.SectionID,
			)
		}
		seenSections[section.SectionID] = struct{}{}
		seenCandidates[section.CandidateLocalID] = struct{}{}
		if section.StartByte < lastEndByCandidate[section.CandidateLocalID] ||
			section.EndByte <= section.StartByte {
			return newDomainError(
				ErrorInvalidExtractorOutput,
				"document section %s has invalid or overlapping byte bounds",
				section.SectionID,
			)
		}
		lastEndByCandidate[section.CandidateLocalID] = section.EndByte
		if !isSHA256ContentHash(section.ContentHash) || !isSHA256ContentHash(section.ResponseHash) {
			return newDomainError(
				ErrorInvalidExtractorOutput,
				"document section %s has invalid content or response hash",
				section.SectionID,
			)
		}
		if section.Abstained != (len(section.ProposalLocalIDs) == 0) ||
			len(section.ProposalLocalIDs) > coverage.MaxProposalsPerSection {
			return newDomainError(
				ErrorInvalidExtractorOutput,
				"document section %s proposal coverage is inconsistent",
				section.SectionID,
			)
		}
		for _, localID := range section.ProposalLocalIDs {
			if _, exists := proposals[localID]; !exists {
				return newDomainError(
					ErrorInvalidExtractorOutput,
					"document section %s references unknown proposal %q",
					section.SectionID,
					localID,
				)
			}
			if _, exists := coveredProposals[localID]; exists {
				return newDomainError(
					ErrorInvalidExtractorOutput,
					"document section coverage repeats proposal %q",
					localID,
				)
			}
			coveredProposals[localID] = struct{}{}
		}
	}
	if len(seenCandidates) != coverage.SelectedSourceCount ||
		len(coveredProposals) != len(proposals) {
		return newDomainError(ErrorInvalidExtractorOutput, "document section coverage is incomplete")
	}
	return nil
}
