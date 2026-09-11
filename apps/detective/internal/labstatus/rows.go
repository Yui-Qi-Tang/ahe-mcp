package labstatus

import (
	"context"
	"fmt"
	"strings"
)

const (
	rowStatusValidated    = "validated"
	rowStatusFailed       = "failed"
	rowStatusNotAttempted = "not_attempted"
)

type markdownTable struct {
	headerStartLine int
	headerEndLine   int
}

// ExtractTableRows extracts each data row from one Markdown table section in
// its own model invocation. A model or validation failure is returned inside
// the batch so the experiment can measure it without accepting the row.
func (e *Extractor) ExtractTableRows(ctx context.Context, document *Document, heading string) (RowBatch, error) {
	section, table, rows, err := document.tableRows(heading)
	if err != nil {
		return RowBatch{}, err
	}
	return e.extractRows(ctx, document, section, table, rows, nil)
}

// ExtractTableRow extracts one data row from a Markdown table section.
func (e *Extractor) ExtractTableRow(ctx context.Context, document *Document, heading string, line int) (RowBatch, error) {
	section, table, rows, err := document.tableRows(heading)
	if err != nil {
		return RowBatch{}, err
	}
	for _, row := range rows {
		if row.StartLine == line {
			return e.extractRows(ctx, document, section, table, []SourceRow{row}, nil)
		}
	}
	return RowBatch{}, fmt.Errorf("no Markdown table row at line %d in section %q", line, heading)
}

// ExtractTableRowStatusClause extracts one controller-selected status clause
// from a Markdown table row. The clause must have an existing status mapping.
func (e *Extractor) ExtractTableRowStatusClause(ctx context.Context, document *Document, heading string, line int, clause string) (RowBatch, error) {
	section, table, rows, err := document.tableRows(heading)
	if err != nil {
		return RowBatch{}, err
	}
	for _, row := range rows {
		if row.StartLine != line {
			continue
		}
		constraint, err := statusConstraintForRow(row, clause)
		if err != nil {
			return RowBatch{}, err
		}
		return e.extractRows(ctx, document, section, table, []SourceRow{row}, constraint)
	}
	return RowBatch{}, fmt.Errorf("no Markdown table row at line %d in section %q", line, heading)
}

func (e *Extractor) extractRows(ctx context.Context, document *Document, section SourceSection, table markdownTable, rows []SourceRow, constraint *statusConstraint) (RowBatch, error) {
	sectionDocument, err := document.SelectSections(section.Heading)
	if err != nil {
		return RowBatch{}, err
	}
	batch := RowBatch{
		SchemaVersion: RowBatchSchemaVersion,
		Source:        sectionDocument.Source(),
		Extractor: ExtractorInfo{
			Name:    ExtractorName,
			Version: ExtractorVersion,
			Model:   e.model.Name(),
		},
		Section: section,
		Rows:    make([]RowOutcome, 0, len(rows)),
	}

	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			for _, remaining := range rows[index:] {
				batch.Rows = append(batch.Rows, RowOutcome{
					Row:    remaining,
					Status: rowStatusNotAttempted,
					Error:  err.Error(),
					Cause:  err,
				})
				batch.Summary.NotAttempted++
			}
			break
		}

		batch.Summary.Attempted++
		rowDocument := document.rowDocument(section, table, row)
		rowConstraint := constraint
		if rowConstraint == nil {
			rowConstraint = mappedStatusConstraintForRow(row)
		}
		envelope, err := e.extract(ctx, rowDocument, rowConstraint)
		if err != nil {
			batch.Rows = append(batch.Rows, RowOutcome{
				Row:    row,
				Status: rowStatusFailed,
				Error:  err.Error(),
				Cause:  err,
			})
			batch.Summary.Failed++
			continue
		}

		batch.Rows = append(batch.Rows, RowOutcome{
			Row:    row,
			Status: rowStatusValidated,
			Result: &envelope.Result,
		})
		batch.Summary.Validated++
	}
	return batch, nil
}

func statusConstraintForRow(row SourceRow, clause string) (*statusConstraint, error) {
	statusCell, _, ok := splitMarkdownTableCells(row.Text)
	if !ok {
		return nil, fmt.Errorf("table row at line %d has no status cell", row.StartLine)
	}
	for _, candidate := range strings.Split(statusCell, ";") {
		candidate = strings.TrimSpace(candidate)
		if normalizeStatusText(candidate) != normalizeStatusText(clause) {
			continue
		}
		allowed := allowedStatusesFromCell(candidate)
		if len(allowed) == 0 {
			return nil, fmt.Errorf("status clause %q at line %d has no supported status mapping", clause, row.StartLine)
		}
		return &statusConstraint{clause: candidate, allowed: allowed}, nil
	}
	return nil, fmt.Errorf("status clause %q not found at line %d", clause, row.StartLine)
}

func mappedStatusConstraintForRow(row SourceRow) *statusConstraint {
	statusCell, _, ok := splitMarkdownTableCells(row.Text)
	if !ok {
		return nil
	}
	if allowed := allowedStatusesFromCell(statusCell); len(allowed) == 1 {
		return &statusConstraint{clause: statusCell, allowed: allowed}
	}
	clauses := mappedStatusClauses(row)
	if len(clauses) == 0 {
		return nil
	}
	allowed := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		allowed = append(allowed, clause.allowed[0])
	}
	return &statusConstraint{clause: statusCell, allowed: allowed}
}

func mappedStatusClauses(row SourceRow) []*statusConstraint {
	statusCell, _, ok := splitMarkdownTableCells(row.Text)
	if !ok {
		return nil
	}
	clauses := strings.Split(statusCell, ";")
	if len(clauses) < 2 {
		return nil
	}
	constraints := make([]*statusConstraint, 0, len(clauses))
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		allowed := allowedStatusesFromCell(clause)
		if len(allowed) != 1 {
			return nil
		}
		constraints = append(constraints, &statusConstraint{clause: clause, allowed: allowed})
	}
	return constraints
}

func (d *Document) tableRows(heading string) (SourceSection, markdownTable, []SourceRow, error) {
	if d == nil {
		return SourceSection{}, markdownTable{}, nil, fmt.Errorf("document is required")
	}
	sectionDocument, err := d.SelectSections(heading)
	if err != nil {
		return SourceSection{}, markdownTable{}, nil, err
	}
	section := sectionDocument.source.SelectedSections[0]

	for lineNumber := section.StartLine; lineNumber < section.EndLine; lineNumber++ {
		header, ok := tableCells(d.lines[lineNumber-1])
		if !ok {
			continue
		}
		divider, ok := tableCells(d.lines[lineNumber])
		if !ok || len(header) != len(divider) || !isTableDivider(divider) {
			continue
		}

		rows := make([]SourceRow, 0)
		for rowLine := lineNumber + 2; rowLine <= section.EndLine; rowLine++ {
			text := d.lines[rowLine-1]
			cells, ok := tableCells(text)
			if !ok {
				break
			}
			if len(cells) != len(header) {
				return SourceSection{}, markdownTable{}, nil, fmt.Errorf("table row at line %d has %d cells, want %d", rowLine, len(cells), len(header))
			}
			rows = append(rows, SourceRow{StartLine: rowLine, EndLine: rowLine, Text: text})
		}
		if len(rows) == 0 {
			return SourceSection{}, markdownTable{}, nil, fmt.Errorf("Markdown table in section %q has no data rows", heading)
		}
		return section, markdownTable{
			headerStartLine: section.StartLine,
			headerEndLine:   lineNumber + 1,
		}, rows, nil
	}
	return SourceSection{}, markdownTable{}, nil, fmt.Errorf("no Markdown table found in section %q", heading)
}

func (d *Document) rowDocument(section SourceSection, table markdownTable, row SourceRow) *Document {
	view := &Document{source: d.source, lines: d.lines}
	view.source.SelectedSections = []SourceSection{
		{
			Heading:   section.Heading + " table context",
			StartLine: table.headerStartLine,
			EndLine:   table.headerEndLine,
		},
		{
			Heading:   section.Heading + " row",
			StartLine: row.StartLine,
			EndLine:   row.EndLine,
		},
	}
	return view
}

func tableCells(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 || !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return nil, false
	}

	parts := strings.Split(trimmed[1:len(trimmed)-1], "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts, true
}

func isTableDivider(cells []string) bool {
	for _, cell := range cells {
		cell = strings.Trim(cell, ":")
		if len(cell) < 3 {
			return false
		}
		for _, character := range cell {
			if character != '-' {
				return false
			}
		}
	}
	return true
}
