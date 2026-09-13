package evidenceingestion

import "strings"

type exactQuoteSelectionUnit struct {
	StatementText string   `json:"statement_text"`
	EvidenceRefs  []string `json:"evidence_refs"`
}

// wholeSpanExactQuoteUnits preserves the controller's current span boundaries.
// A whole span can contain several claims or depend on context outside the span;
// this catalogue does not establish semantic completeness or split source text.
func wholeSpanExactQuoteUnits(input ExtractorInput) ([]exactQuoteSelectionUnit, error) {
	units := make([]exactQuoteSelectionUnit, 0, len(input.Spans))
	seen := make(map[string]bool, len(input.Spans))
	for _, span := range input.Spans {
		if span.SpanID == "" || seen[span.SpanID] {
			return nil, newDomainError(ErrorInvalidInput, "selection input requires non-empty, unique span IDs")
		}
		seen[span.SpanID] = true
		if strings.TrimSpace(span.Text) == "" {
			return nil, newDomainError(ErrorInvalidInput, "selection unit %q is empty", span.SpanID)
		}
		if len(span.Text) > maxExactQuoteStatementBytes {
			return nil, newDomainError(ErrorInvalidInput,
				"whole selection unit %q exceeds %d bytes; it cannot be truncated or omitted", span.SpanID, maxExactQuoteStatementBytes)
		}
		units = append(units, exactQuoteSelectionUnit{
			StatementText: span.Text,
			EvidenceRefs:  []string{span.SpanID},
		})
	}
	return units, nil
}

// DecodeWholeSpanExactQuoteSelectionOutput validates model selection against
// whole current input spans, independently of provider schema enforcement. The
// existing exact-substring validator remains an additional grounding check.
func DecodeWholeSpanExactQuoteSelectionOutput(data []byte, input ExtractorInput, maxProposals int) (FrozenExtractorOutput, error) {
	units, err := wholeSpanExactQuoteUnits(input)
	if err != nil {
		return FrozenExtractorOutput{}, err
	}
	output, err := DecodeBoundedExactQuoteExtractorOutput(data, input, maxProposals)
	if err != nil {
		return FrozenExtractorOutput{}, err
	}
	if output.Proposals == nil {
		return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "selection output requires a proposals array")
	}
	allowed := make(map[string]string, len(units))
	for _, unit := range units {
		allowed[unit.EvidenceRefs[0]] = unit.StatementText
	}
	selected := make(map[string]bool, len(output.Proposals))
	localIDs := make(map[string]bool, len(output.Proposals))
	for _, proposal := range output.Proposals {
		if strings.TrimSpace(proposal.ProposalLocalID) == "" || localIDs[proposal.ProposalLocalID] {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "selection requires non-empty, unique proposal_local_id values")
		}
		localIDs[proposal.ProposalLocalID] = true
		if len(proposal.EvidenceRefs) != 1 || allowed[proposal.EvidenceRefs[0]] != proposal.StatementText {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput,
				"proposal %q must select one complete allowed unit with its exact single span ref", proposal.ProposalLocalID)
		}
		spanID := proposal.EvidenceRefs[0]
		if selected[spanID] {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "selection repeats unit for span %q", spanID)
		}
		selected[spanID] = true
	}
	return output, nil
}
