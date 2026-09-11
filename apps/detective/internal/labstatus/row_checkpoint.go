package labstatus

import (
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

// ValidateRowBatch rechecks a saved one-row extraction against its exact source.
// It does not invoke a model or authenticate the original extraction. Checkpoint
// callers apply their own candidate-count and pending-submission limits as well.
func ValidateRowBatch(document *Document, batch RowBatch, statusClause string) error {
	if document == nil || batch.SchemaVersion != RowBatchSchemaVersion || len(batch.Rows) != 1 ||
		batch.Extractor.Name != ExtractorName || (batch.Extractor.Version != "0.1.2" && batch.Extractor.Version != "0.1.1" && batch.Extractor.Version != "0.1.0") ||
		batch.Summary != (RowSummary{Attempted: 1, Validated: 1}) {
		return fmt.Errorf("checkpoint requires a supported one-row validated batch")
	}
	if !utf8.ValidString(statusClause) || strings.ContainsRune(statusClause, 0) || len(statusClause) > maxTextLength {
		return fmt.Errorf("checkpoint status clause is invalid")
	}
	section, table, rows, err := document.tableRows(batch.Section.Heading)
	if err != nil {
		return fmt.Errorf("checkpoint source has no matching table section")
	}
	selected, err := document.SelectSections(section.Heading)
	if err != nil || section != batch.Section || !reflect.DeepEqual(selected.Source(), batch.Source) {
		return fmt.Errorf("checkpoint source coordinates do not match captured bytes")
	}
	outcome := batch.Rows[0]
	if outcome.Status != rowStatusValidated || outcome.Error != "" || outcome.Result == nil || outcome.Result.Outcome != "extracted" {
		return fmt.Errorf("checkpoint row is not a validated extracted result")
	}
	var found bool
	for _, row := range rows {
		if row == outcome.Row {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("checkpoint row does not match the source table")
	}
	rowDocument := document.rowDocument(section, table, outcome.Row)
	if err := ValidateCandidateSet(rowDocument, *outcome.Result); err != nil {
		return fmt.Errorf("checkpoint candidate validation failed")
	}
	constraint := mappedStatusConstraintForRow(outcome.Row)
	if statusClause != "" {
		constraint, err = statusConstraintForRow(outcome.Row, statusClause)
		if err != nil {
			return fmt.Errorf("checkpoint status clause is not supported by the source row")
		}
	}
	for _, record := range outcome.Result.Records {
		if batch.Extractor.Version == "0.1.2" {
			if err := ValidateStatementForm(record); err != nil {
				return err
			}
		}
		// Resume is row-grounded, not a way to promote surrounding table headers
		// to evidence for the selected row.
		if record.Citation.StartLine != outcome.Row.StartLine || record.Citation.EndLine != outcome.Row.EndLine {
			return fmt.Errorf("checkpoint candidate must cite the selected row exactly")
		}
		if constraint != nil && !contains(constraint.allowed, record.Status) {
			return fmt.Errorf("checkpoint candidate status does not match the selected source clause")
		}
	}
	return nil
}
