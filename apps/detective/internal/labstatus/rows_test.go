package labstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestExtractTableRowsUsesOneRowPerModelRequest(t *testing.T) {
	document := loadRowFixture(t)
	llm := &rowLLM{responses: map[string]string{
		"Runtime core": candidateJSONWithOverrides(t, 7, CandidateOverride{
			Status: "implemented",
		}),
		"Canonical relation": candidateJSON(t, 8),
		"Deployment": candidateJSONWithOverrides(t, 9, CandidateOverride{
			Status: "not_claimed",
		}),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRows(context.Background(), document, "Status at a Glance")
	if err != nil {
		t.Fatalf("ExtractTableRows() error = %v", err)
	}
	if got, want := batch.SchemaVersion, RowBatchSchemaVersion; got != want {
		t.Fatalf("schema version = %q, want %q", got, want)
	}
	if got, want := batch.Summary, (RowSummary{Attempted: 3, Validated: 3}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if got, want := len(batch.Rows), 3; got != want {
		t.Fatalf("row count = %d, want %d", got, want)
	}
	for index, outcome := range batch.Rows {
		if got, want := outcome.Status, rowStatusValidated; got != want {
			t.Errorf("row %d status = %q, want %q", index, got, want)
		}
		if outcome.Result == nil {
			t.Errorf("row %d result = nil", index)
			continue
		}
		if got, want := outcome.Result.Records[0].Citation.ExactQuote, outcome.Row.Text; got != want {
			t.Errorf("row %d quote = %q, want %q", index, got, want)
		}
	}
	sections := batch.Source.SelectedSections
	if len(sections) != 1 || sections[0] != (SourceSection{Heading: "Status at a Glance", StartLine: 3, EndLine: 9}) {
		t.Fatalf("batch selection = %#v", sections)
	}
	if got, want := len(llm.requests), 3; got != want {
		t.Fatalf("model requests = %d, want %d", got, want)
	}
	if requestContains(llm.requests[0], "Canonical relation") || requestContains(llm.requests[0], "Deployment") {
		t.Fatal("first row request contains a different data row")
	}
	if requestContains(llm.requests[1], "Runtime core") || requestContains(llm.requests[1], "Deployment") {
		t.Fatal("second row request contains a different data row")
	}
}

func TestExtractTableRowExtractsOnlySelectedRow(t *testing.T) {
	document := loadRowFixture(t)
	llm := &rowLLM{responses: map[string]string{
		"Canonical relation": candidateJSON(t, 8),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRow(context.Background(), document, "Status at a Glance", 8)
	if err != nil {
		t.Fatalf("ExtractTableRow() error = %v", err)
	}
	if got, want := batch.Summary, (RowSummary{Attempted: 1, Validated: 1}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if got, want := batch.Rows[0].Row.StartLine, 8; got != want {
		t.Fatalf("row start line = %d, want %d", got, want)
	}
	if got, want := len(llm.requests), 1; got != want {
		t.Fatalf("model requests = %d, want %d", got, want)
	}
	if requestContains(llm.requests[0], "Runtime core") || requestContains(llm.requests[0], "Deployment") {
		t.Fatal("selected row request contains another data row")
	}
	if !requestContains(llm.requests[0], `Controller-selected status clause: "UNRELEASED LAB PROVEN"`) {
		t.Fatal("selected row request did not constrain its single mapped status cell")
	}
}

func TestExtractTableRowStatusClauseRejectsModelStatusFromAnotherClause(t *testing.T) {
	document := loadNamedRowFixture(t, "status_at_a_glance_edge_cases.md")
	llm := &rowLLM{responses: map[string]string{
		"Canonical `implements` relation": candidateJSONWithOverrides(t, 7, CandidateOverride{
			Subject: "canonical_implements_relation",
			Status:  "unreleased_lab_proven",
		}),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRowStatusClause(context.Background(), document, "Status at a Glance", 7, "runtime adoption OPEN and deferred")
	if err != nil {
		t.Fatalf("ExtractTableRowStatusClause() error = %v", err)
	}
	if got, want := batch.Summary, (RowSummary{Attempted: 1, Failed: 1}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if !strings.Contains(batch.Rows[0].Error, "validate status clause") {
		t.Fatalf("row error = %q, want controller clause validation", batch.Rows[0].Error)
	}
	if got, want := len(llm.requests), 1; got != want {
		t.Fatalf("model requests = %d, want %d", got, want)
	}
	if !requestContains(llm.requests[0], `Controller-selected status clause: "runtime adoption OPEN and deferred"`) {
		t.Fatal("model request did not include controller-selected clause")
	}
}

func TestExtractTableRowStatusClauseRejectsUnsupportedClauseBeforeModel(t *testing.T) {
	document := loadNamedRowFixture(t, "status_at_a_glance_edge_cases.md")
	llm := &rowLLM{}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	_, err = extractor.ExtractTableRowStatusClause(context.Background(), document, "Status at a Glance", 7, "Lab questions closed")
	if err == nil || !strings.Contains(err.Error(), "no supported status mapping") {
		t.Fatalf("ExtractTableRowStatusClause() error = %v, want unsupported clause", err)
	}
	if got := len(llm.requests); got != 0 {
		t.Fatalf("model requests = %d, want 0", got)
	}
}

func TestExtractTableRowsSupportsCanonicalRelationAndExposedStatusFixture(t *testing.T) {
	document := loadNamedRowFixture(t, "status_at_a_glance_edge_cases.md")
	llm := &rowLLM{responses: map[string]string{
		"Canonical `implements` relation": candidateJSONWithOverrides(t, 7, CandidateOverride{
			Subject: "canonical_implements_relation",
			Status:  "unreleased_lab_proven",
		}),
		"External-agent source handoff": candidateJSONWithOverrides(t, 8, CandidateOverride{
			Subject: "external_agent_source_handoff",
			Status:  "implemented_exposed",
		}),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRows(context.Background(), document, "Status at a Glance")
	if err != nil {
		t.Fatalf("ExtractTableRows() error = %v", err)
	}
	if got, want := len(batch.Rows), 2; got != want {
		t.Fatalf("row count = %d, want %d", got, want)
	}
	if got, want := batch.Rows[0].Result.Records[0].Citation.StartLine, 7; got != want {
		t.Fatalf("canonical row citation start = %d, want %d", got, want)
	}
	if batch.Rows[1].Result == nil {
		t.Fatalf("exposed row had no result: status=%q error=%q", batch.Rows[1].Status, batch.Rows[1].Error)
	}
	if got, want := batch.Rows[1].Result.Records[0].Status, "implemented_exposed"; got != want {
		t.Fatalf("exposed row status = %q, want %q", got, want)
	}
	if requestContains(llm.requests[0], "UNRELEASED LAB PROVEN") == false {
		t.Fatal("canonical row request did not include canonical fixture context")
	}
	if requestContains(llm.requests[0], "External-agent source handoff") {
		t.Fatal("canonical row request leaked another row")
	}
	if requestContains(llm.requests[1], "IMPLEMENTED, EXPOSED") == false {
		t.Fatal("exposed row request did not include exposed fixture context")
	}
	if requestContains(llm.requests[1], "Canonical `implements` relation") {
		t.Fatal("exposed row request leaked another row")
	}
}

func TestExtractTableRowsCapturesFailedRow(t *testing.T) {
	document := loadRowFixture(t)
	llm := &rowLLM{responses: map[string]string{
		"Runtime core": candidateJSONWithOverrides(t, 7, CandidateOverride{
			Status: "implemented",
		}),
		"Canonical relation": `{}`,
		"Deployment": candidateJSONWithOverrides(t, 9, CandidateOverride{
			Status: "not_claimed",
		}),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRows(context.Background(), document, "Status at a Glance")
	if err != nil {
		t.Fatalf("ExtractTableRows() error = %v", err)
	}
	if got, want := batch.Summary, (RowSummary{Attempted: 3, Validated: 2, Failed: 1}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if got, want := batch.Rows[1].Status, rowStatusFailed; got != want {
		t.Fatalf("failed row status = %q, want %q", got, want)
	}
	if batch.Rows[1].Error == "" || batch.Rows[1].Result != nil {
		t.Fatalf("failed row = %#v, want error without result", batch.Rows[1])
	}
}

func TestExtractTableRowConstrainsMappedCompoundStatus(t *testing.T) {
	document := loadTestDocument(t, "## Status at a Glance\n| Capability | Status | Boundary |\n| --- | --- | --- |\n| Capability | **LAB PROVEN; RUNTIME OPEN** | bounded |\n")
	llm := &rowLLM{responses: map[string]string{
		"Capability": candidateJSONWithOverrides(t, 4, CandidateOverride{Status: "lab_proven"}),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRow(context.Background(), document, "Status at a Glance", 4)
	if err != nil {
		t.Fatalf("ExtractTableRow() error = %v", err)
	}
	if got, want := batch.Summary, (RowSummary{Attempted: 1, Validated: 1}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if got, want := len(llm.requests), 1; got != want {
		t.Fatalf("model requests = %d, want %d", got, want)
	}
	if !requestContains(llm.requests[0], `Controller-selected status clause: "LAB PROVEN; RUNTIME OPEN"`) {
		t.Fatal("model request did not constrain the compound status cell")
	}
	if !requestContains(llm.requests[0], "every record status must be one of: lab_proven, open") {
		t.Fatal("model request did not constrain mapped compound statuses")
	}
}

func TestExtractTableRowRejectsImplementedExposedOutsideCompoundMapping(t *testing.T) {
	document := loadTestDocument(t, "## Status at a Glance\n| Capability | Status | Boundary |\n| --- | --- | --- |\n| Retrieval | **P0 + scoped local P1 IMPLEMENTED; RELEASE GATE OPEN** | bounded |\n")
	llm := &rowLLM{responses: map[string]string{
		"Retrieval": candidateJSONWithOverrides(t, 4, CandidateOverride{Status: "implemented_exposed"}),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRow(context.Background(), document, "Status at a Glance", 4)
	if err != nil {
		t.Fatalf("ExtractTableRow() error = %v", err)
	}
	if got, want := batch.Summary, (RowSummary{Attempted: 1, Failed: 1}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if !strings.Contains(batch.Rows[0].Error, "not in source status-cell mapping") {
		t.Fatalf("row error = %q, want source status-cell mapping failure", batch.Rows[0].Error)
	}
	if !requestContains(llm.requests[0], "every record status must be one of: implemented, open") {
		t.Fatal("model request did not constrain implemented/open compound statuses")
	}
}

func TestExtractTableRowsMarksRemainingRowsNotAttempted(t *testing.T) {
	document := loadRowFixture(t)
	llm := &rowLLM{responses: map[string]string{}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	batch, err := extractor.ExtractTableRows(ctx, document, "Status at a Glance")
	if err != nil {
		t.Fatalf("ExtractTableRows() error = %v", err)
	}
	if got, want := batch.Summary, (RowSummary{NotAttempted: 3}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	for index, outcome := range batch.Rows {
		if got, want := outcome.Status, rowStatusNotAttempted; got != want {
			t.Errorf("row %d status = %q, want %q", index, got, want)
		}
	}
	if got := len(llm.requests); got != 0 {
		t.Fatalf("model calls = %d, want 0", got)
	}
}

func TestTableRowsRejectsMalformedTable(t *testing.T) {
	document := loadTestDocument(t, "## Status at a Glance\n| Capability | Status |\nnot a divider\n")
	if _, _, _, err := document.tableRows("Status at a Glance"); err == nil {
		t.Fatal("tableRows() error = nil, want malformed table error")
	}
}

func loadRowFixture(t *testing.T) *Document {
	t.Helper()
	return loadNamedRowFixture(t, "status_at_a_glance.md")
}

func loadNamedRowFixture(t *testing.T, filename string) *Document {
	t.Helper()
	path := filepath.Join("testdata", filename)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	documentPath := filepath.Join(t.TempDir(), "STATUS.md")
	if err := os.WriteFile(documentPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := LoadDocument(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

type CandidateOverride struct {
	Subject string
	Status  string
}

func candidateJSONWithOverrides(t *testing.T, line int, override CandidateOverride) string {
	t.Helper()
	candidates := validCandidates()
	candidates.Records[0].Citation = Citation{StartLine: line, EndLine: line}
	if override.Subject != "" {
		candidates.Records[0].Subject = override.Subject
	}
	if override.Status != "" {
		candidates.Records[0].Status = override.Status
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func candidateJSON(t *testing.T, line int) string {
	t.Helper()
	candidates := validCandidates()
	candidates.Records[0].Citation = Citation{StartLine: line, EndLine: line}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type rowLLM struct {
	responses map[string]string
	requests  []*model.LLMRequest
}

func (m *rowLLM) Name() string { return "row-test-model" }

func (m *rowLLM) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.requests = append(m.requests, request)
	var response string
	for subject, candidate := range m.responses {
		if requestContains(request, subject) {
			response = candidate
			break
		}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if response == "" {
			yield(nil, fmt.Errorf("no response configured for request"))
			return
		}
		yield(&model.LLMResponse{
			Content:      genai.NewContentFromText(response, genai.RoleModel),
			TurnComplete: true,
		}, nil)
	}
}

func TestRowDocumentPreservesOnlyHeaderAndSelectedRow(t *testing.T) {
	document := loadRowFixture(t)
	section, table, rows, err := document.tableRows("Status at a Glance")
	if err != nil {
		t.Fatal(err)
	}
	view := document.rowDocument(section, table, rows[1])
	text := view.NumberedText()
	if !strings.Contains(text, "000005 | | Capability | Status | Boundary |") || !strings.Contains(text, "000008 | | Canonical relation |") {
		t.Fatalf("row document missing context or selected row: %q", text)
	}
	if strings.Contains(text, "Runtime core") || strings.Contains(text, "Deployment") {
		t.Fatalf("row document contains another data row: %q", text)
	}
}

func TestExtractTableRowsDedupesDuplicateModelRecordsInRow(t *testing.T) {
	document := loadNamedRowFixture(t, "status_at_a_glance.md")
	first := validCandidates()
	first.Records[0].Citation = Citation{StartLine: 7, EndLine: 7}
	first.Records[0].Subject = "runtime_core"
	first.Records[0].Status = "implemented"
	first.Records[0].Scope = "runtime_core"
	first.Records = append(first.Records, first.Records[0])
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}

	canonical := validCandidates()
	canonical.Records[0].Citation = Citation{StartLine: 8, EndLine: 8}
	canonical.Records[0].Subject = "canonical_implements_relation"
	canonical.Records[0].Status = "unreleased_lab_proven"
	canonical.Records[0].Scope = "lab_contract"
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}

	deploy := validCandidates()
	deploy.Records[0].Citation = Citation{StartLine: 9, EndLine: 9}
	deploy.Records[0].Subject = "deployment"
	deploy.Records[0].Status = "not_claimed"
	deploy.Records[0].Scope = "named_deployment"
	deployJSON, err := json.Marshal(deploy)
	if err != nil {
		t.Fatal(err)
	}

	llm := &rowLLM{responses: map[string]string{
		"Runtime core":       string(firstJSON),
		"Canonical relation": string(canonicalJSON),
		"Deployment":         string(deployJSON),
	}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := extractor.ExtractTableRows(context.Background(), document, "Status at a Glance")
	if err != nil {
		t.Fatalf("ExtractTableRows() error = %v", err)
	}
	if got, want := batch.Summary, (RowSummary{Attempted: 3, Validated: 3}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if got, want := len(batch.Rows[0].Result.Records), 1; got != want {
		t.Fatalf("runtime row record count = %d, want %d", got, want)
	}
}
