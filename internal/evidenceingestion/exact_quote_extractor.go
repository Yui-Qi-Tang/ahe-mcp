package evidenceingestion

import "strings"

const maxExactQuoteStatementBytes = 64 << 10

// DecodeBoundedExactQuoteExtractorOutput strictly decodes one model response
// and verifies that every statement is copied from one referenced input span.
func DecodeBoundedExactQuoteExtractorOutput(
	data []byte,
	input ExtractorInput,
	maxProposals int,
) (FrozenExtractorOutput, error) {
	if maxProposals < 1 {
		return FrozenExtractorOutput{}, newDomainError(ErrorInvalidInput, "max_proposals must be positive")
	}
	output, err := decodeTrustedExtractorOutput(data)
	if err != nil {
		return FrozenExtractorOutput{}, err
	}
	if output.RepositoryGoplsCoverage != nil {
		return FrozenExtractorOutput{}, newDomainError(
			ErrorInvalidExtractorOutput,
			"repository gopls coverage is not supported for exact-quote extraction",
		)
	}
	if output.DocumentSectionCoverage != nil {
		return FrozenExtractorOutput{}, newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage must be produced by the extraction controller",
		)
	}
	if len(output.Proposals) > maxProposals {
		return FrozenExtractorOutput{}, newDomainError(
			ErrorInvalidExtractorOutput,
			"extractor returned %d proposals, limit is %d",
			len(output.Proposals),
			maxProposals,
		)
	}

	spans := make(map[string]string, len(input.Spans))
	for _, span := range input.Spans {
		if span.SpanID == "" {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidInput, "extractor input contains an empty span_id")
		}
		if _, exists := spans[span.SpanID]; exists {
			return FrozenExtractorOutput{}, newDomainError(
				ErrorInvalidInput,
				"extractor input contains duplicate span_id %q",
				span.SpanID,
			)
		}
		spans[span.SpanID] = span.Text
	}

	for _, proposal := range output.Proposals {
		if proposal.CodeFact != nil || proposal.CodeRelation != nil {
			return FrozenExtractorOutput{}, newDomainError(
				ErrorInvalidExtractorOutput,
				"proposal %q contains unsupported code material",
				proposal.ProposalLocalID,
			)
		}
		if len([]byte(proposal.StatementText)) > maxExactQuoteStatementBytes {
			return FrozenExtractorOutput{}, newDomainError(
				ErrorInvalidExtractorOutput,
				"proposal %q statement exceeds %d bytes",
				proposal.ProposalLocalID,
				maxExactQuoteStatementBytes,
			)
		}
		seenRefs := make(map[string]struct{}, len(proposal.EvidenceRefs))
		matched := false
		for _, spanID := range proposal.EvidenceRefs {
			if _, duplicate := seenRefs[spanID]; duplicate {
				return FrozenExtractorOutput{}, newDomainError(
					ErrorInvalidExtractorOutput,
					"proposal %q repeats evidence ref %q",
					proposal.ProposalLocalID,
					spanID,
				)
			}
			seenRefs[spanID] = struct{}{}
			text, exists := spans[spanID]
			if !exists {
				return FrozenExtractorOutput{}, newDomainError(ErrorUnknownSpan, "unknown span %q", spanID)
			}
			if proposal.StatementText != "" && strings.Contains(text, proposal.StatementText) {
				matched = true
			}
		}
		if !matched {
			return FrozenExtractorOutput{}, newDomainError(
				ErrorInvalidExtractorOutput,
				"proposal %q statement_text must be an exact substring of one referenced span",
				proposal.ProposalLocalID,
			)
		}
	}
	return output, nil
}
