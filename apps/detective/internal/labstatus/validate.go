package labstatus

import (
	"fmt"
	"strings"
)

const (
	maxRecords       = 256
	maxAbstentions   = 128
	maxCitationLines = 12
	maxTextLength    = 2_000

	implementedExposedStatusPhrase = "IMPLEMENTED, EXPOSED"
)

// ValidateCandidateSet checks structural, semantic, and source-grounding
// invariants that cannot safely be delegated to the model.
func ValidateCandidateSet(document *Document, candidates CandidateSet) error {
	if document == nil {
		return fmt.Errorf("document is required")
	}
	if candidates.Records == nil || candidates.Abstentions == nil || candidates.Limitations == nil {
		return fmt.Errorf("records, abstentions, and limitations must be JSON arrays")
	}
	if len(candidates.Records) > maxRecords {
		return fmt.Errorf("records exceeds maximum %d", maxRecords)
	}
	if len(candidates.Abstentions) > maxAbstentions {
		return fmt.Errorf("abstentions exceeds maximum %d", maxAbstentions)
	}

	switch candidates.Outcome {
	case "extracted":
		if len(candidates.Records) == 0 {
			return fmt.Errorf("extracted outcome requires at least one record")
		}
		if candidates.AbstentionReason != "" {
			return fmt.Errorf("extracted outcome requires an empty abstention_reason")
		}
	case "abstained":
		if len(candidates.Records) != 0 {
			return fmt.Errorf("abstained outcome requires no records")
		}
		if err := validateText("abstention_reason", candidates.AbstentionReason); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported outcome %q", candidates.Outcome)
	}

	seenRecords := make(map[string]struct{}, len(candidates.Records))
	for i, record := range candidates.Records {
		if err := validateRecord(document, record); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		key := strings.Join([]string{
			record.RecordType,
			record.Subject,
			record.Scope,
			record.Status,
			fmt.Sprintf("%d:%d", record.Citation.StartLine, record.Citation.EndLine),
		}, "\x00")
		if _, exists := seenRecords[key]; exists {
			return fmt.Errorf("record %d duplicates an earlier atomic record", i)
		}
		seenRecords[key] = struct{}{}
	}

	for i, abstention := range candidates.Abstentions {
		if err := validateText("subject", abstention.Subject); err != nil {
			return fmt.Errorf("abstention %d: %w", i, err)
		}
		if err := validateText("reason", abstention.Reason); err != nil {
			return fmt.Errorf("abstention %d: %w", i, err)
		}
	}
	if err := validateList("limitations", candidates.Limitations, 32); err != nil {
		return err
	}
	return nil
}

func hydrateCitations(document *Document, candidates *CandidateSet) error {
	if document == nil || candidates == nil {
		return fmt.Errorf("document and candidates are required")
	}
	for i := range candidates.Records {
		citation := &candidates.Records[i].Citation
		if citation.ExactQuote != "" {
			return fmt.Errorf("record %d: model must not supply exact_quote", i)
		}
		if citation.StartLine < 1 || citation.EndLine < citation.StartLine {
			return fmt.Errorf("record %d: invalid citation range %d-%d", i, citation.StartLine, citation.EndLine)
		}
		if citation.EndLine-citation.StartLine+1 > maxCitationLines {
			return fmt.Errorf("record %d: citation exceeds %d lines", i, maxCitationLines)
		}
		if !document.containsSelectedRange(citation.StartLine, citation.EndLine) {
			return fmt.Errorf("record %d: citation range %d-%d is outside the selected sections", i, citation.StartLine, citation.EndLine)
		}
		quote, ok := document.ExactQuote(citation.StartLine, citation.EndLine)
		if !ok {
			return fmt.Errorf("record %d: citation range %d-%d is outside the document", i, citation.StartLine, citation.EndLine)
		}
		citation.ExactQuote = quote
	}
	return nil
}

func validateRecord(document *Document, record Record) error {
	if !contains(recordTypes, record.RecordType) {
		return fmt.Errorf("unsupported record_type %q", record.RecordType)
	}
	if !contains(epistemicClasses, record.EpistemicClass) {
		return fmt.Errorf("unsupported epistemic_class %q", record.EpistemicClass)
	}
	if !contains(statuses, record.Status) {
		return fmt.Errorf("unsupported status %q", record.Status)
	}
	if !contains(scopes, record.Scope) {
		return fmt.Errorf("unsupported scope %q", record.Scope)
	}
	if !contains(selectionStates, record.SelectionState) {
		return fmt.Errorf("unsupported selection_state %q", record.SelectionState)
	}
	if err := validateText("subject", record.Subject); err != nil {
		return err
	}
	if err := validateText("statement", record.Statement); err != nil {
		return err
	}
	if err := validateStatusFromSource(record); err != nil {
		return err
	}
	if err := validateStatusFromStatusCell(record); err != nil {
		return err
	}
	if record.EpistemicClass == "unknown" && record.Status != "unknown" {
		return fmt.Errorf("unknown epistemic_class requires unknown status")
	}
	if record.EpistemicClass == "blocked" && (record.RecordType != "release_gate" || record.Status != "open") {
		return fmt.Errorf("blocked epistemic_class requires release_gate record_type and open status")
	}
	if record.EpistemicClass == "claim" && record.Status == "unknown" {
		return fmt.Errorf("unknown status requires unknown epistemic_class")
	}

	citation := record.Citation
	if citation.StartLine < 1 || citation.EndLine < citation.StartLine {
		return fmt.Errorf("invalid citation range %d-%d", citation.StartLine, citation.EndLine)
	}
	if citation.EndLine-citation.StartLine+1 > maxCitationLines {
		return fmt.Errorf("citation exceeds %d lines", maxCitationLines)
	}
	if !document.containsSelectedRange(citation.StartLine, citation.EndLine) {
		return fmt.Errorf("citation range %d-%d is outside the selected sections", citation.StartLine, citation.EndLine)
	}
	wantQuote, ok := document.ExactQuote(citation.StartLine, citation.EndLine)
	if !ok {
		return fmt.Errorf("citation range %d-%d is outside the document", citation.StartLine, citation.EndLine)
	}
	if citation.ExactQuote != wantQuote {
		return fmt.Errorf("citation quote does not match source lines %d-%d", citation.StartLine, citation.EndLine)
	}

	if err := validateList("blocked_by", record.BlockedBy, 24); err != nil {
		return err
	}
	if err := validateList("does_not_establish", record.DoesNotEstablish, 24); err != nil {
		return err
	}
	if err := validateList("qualifiers", record.Qualifiers, 32); err != nil {
		return err
	}
	return nil
}

func validateStatusFromSource(record Record) error {
	statusCell, ok := parseStatusCellFromExactQuote(record.Citation.ExactQuote)
	if !ok {
		return nil
	}
	normalized := normalizeStatusText(statusCell)
	if containsImplementedAndExposed(normalized) && record.Status != "implemented_exposed" {
		return fmt.Errorf("status %q requires implemented_exposed when source contains %q", record.Status, implementedExposedStatusPhrase)
	}
	return nil
}

func validateStatusFromStatusCell(record Record) error {
	statusCell, ok := parseStatusCellFromExactQuote(record.Citation.ExactQuote)
	if !ok {
		return nil
	}
	allowed := allowedStatusesFromCell(statusCell)
	if len(allowed) == 0 {
		return nil
	}
	if !contains(allowed, record.Status) {
		return fmt.Errorf("status %q not in source status-cell mapping %v", record.Status, allowed)
	}
	return nil
}

func parseStatusCellFromExactQuote(quote string) (string, bool) {
	statusCell, _, ok := splitMarkdownTableCells(quote)
	return statusCell, ok
}

func allowedStatusesFromCell(statusCell string) []string {
	normalized := normalizeStatusText(statusCell)

	allowed := make(map[string]struct{})
	if containsImplementedAndExposed(normalized) {
		allowed["implemented_exposed"] = struct{}{}
	} else if strings.Contains(normalized, "IMPLEMENTED") {
		allowed["implemented"] = struct{}{}
	}
	if strings.Contains(normalized, "UNRELEASED LAB") && strings.Contains(normalized, "PROVEN") {
		allowed["unreleased_lab_proven"] = struct{}{}
	}
	if strings.Contains(normalized, "UNRELEASED CORE") && strings.Contains(normalized, "PROVEN") {
		allowed["unreleased_core_proven"] = struct{}{}
	}
	if !strings.Contains(normalized, "UNRELEASED LAB") && (strings.Contains(normalized, "LAB DOMAIN PROVEN") || strings.Contains(normalized, "LAB PROVEN")) {
		allowed["lab_proven"] = struct{}{}
	}
	if strings.Contains(normalized, "BACKLOG") {
		allowed["backlog"] = struct{}{}
	}
	if strings.Contains(normalized, "EXPERIMENT READY") {
		allowed["experiment_ready"] = struct{}{}
	}
	if strings.Contains(normalized, "OPTIONAL EXPERIMENTAL") {
		allowed["optional_experimental"] = struct{}{}
	}
	if strings.Contains(normalized, "NOT CLAIMED") {
		allowed["not_claimed"] = struct{}{}
	}
	if strings.Contains(normalized, "RELEASED") && !strings.Contains(normalized, "UNRELEASED") {
		allowed["released"] = struct{}{}
	}
	if strings.Contains(normalized, "OPEN") {
		allowed["open"] = struct{}{}
	}
	if strings.Contains(normalized, "OPTIONAL") {
		allowed["optional_experimental"] = struct{}{}
	}

	result := make([]string, 0, len(allowed))
	for value := range allowed {
		result = append(result, value)
	}
	return result
}

func splitMarkdownTableCells(line string) (string, string, bool) {
	cells, ok := tableCells(line)
	if !ok {
		return "", "", false
	}
	if len(cells) < 2 {
		return "", "", false
	}
	return stripMarkdownWrap(cells[1]), stripMarkdownWrap(cells[0]), true
}

func stripMarkdownWrap(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`")
	value = strings.ReplaceAll(value, "**", "")
	return strings.TrimSpace(value)
}

func validateText(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be blank", name)
	}
	if len(value) > maxTextLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maxTextLength)
	}
	return nil
}

func validateList(name string, values []string, maximum int) error {
	if values == nil {
		return fmt.Errorf("%s must be a JSON array", name)
	}
	if len(values) > maximum {
		return fmt.Errorf("%s exceeds maximum %d", name, maximum)
	}
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		if err := validateText(fmt.Sprintf("%s[%d]", name, i), value); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s[%d] duplicates an earlier item", name, i)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeStatusText(value string) string {
	value = strings.ToUpper(value)
	value = strings.ReplaceAll(value, "*", "")
	value = strings.ReplaceAll(value, "`", "")
	return value
}

func containsImplementedAndExposed(normalizedStatusCell string) bool {
	return strings.Contains(normalizedStatusCell, implementedExposedStatusPhrase) ||
		(strings.Contains(normalizedStatusCell, "IMPLEMENTED") && strings.Contains(normalizedStatusCell, "EXPOSED"))
}
