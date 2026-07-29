package evidenceingestion

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCompileSourceStructureClassifiesExactUnitsAndContext(t *testing.T) {
	raw := []byte(
		"# Runtime\n\n" +
			"## Retry policy\n\n" +
			"Failed work remains fenced.\n" +
			"An explicit retry creates a new attempt.\n\n" +
			"- Preserve the failed attempt.\n" +
			"  Keep its finish audit.\n" +
			"- Allocate the next attempt number.\n\n" +
			"| State | Rule |\n" +
			"| --- | --- |\n" +
			"| failed | retry after review |\n" +
			"| running | retain the lease |\n\n" +
			"---\n\n" +
			"```mermaid\n" +
			"FAILED --> RETRY\n" +
			"RETRY --> PENDING\n" +
			"```\n",
	)
	input := sourceStructureTestInput(t, raw)

	first, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	second, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() replay error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("source structure replay differs:\nfirst  %+v\nsecond %+v", first, second)
	}
	if first.Contract != SourceStructureContractV1 ||
		first.ContextContract != SourceContextEnvelopeContractV1 ||
		first.SearchParticipation {
		t.Fatalf("source structure contract = %+v", first)
	}

	wantKinds := []SourceStructureUnitKind{
		SourceStructureUnitHeading,
		SourceStructureUnitHeading,
		SourceStructureUnitParagraph,
		SourceStructureUnitListItem,
		SourceStructureUnitListItem,
		SourceStructureUnitTableHeader,
		SourceStructureUnitTableDelimiter,
		SourceStructureUnitTableRow,
		SourceStructureUnitTableRow,
		SourceStructureUnitThematicBreak,
		SourceStructureUnitFenceMarker,
		SourceStructureUnitFencedLine,
		SourceStructureUnitFencedLine,
		SourceStructureUnitFenceMarker,
	}
	if got := sourceStructureKinds(first.Units); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("unit kinds = %v, want %v", got, wantKinds)
	}
	if first.Units[2].ExactText !=
		"Failed work remains fenced.\nAn explicit retry creates a new attempt." {
		t.Fatalf("paragraph exact text = %q", first.Units[2].ExactText)
	}
	if first.Units[3].ExactText !=
		"- Preserve the failed attempt.\n  Keep its finish audit." {
		t.Fatalf("list item exact text = %q", first.Units[3].ExactText)
	}
	if first.Units[7].ExactText != "| failed | retry after review |" ||
		first.Units[8].ExactText != "| running | retain the lease |" {
		t.Fatalf("independent table rows = %q / %q", first.Units[7].ExactText, first.Units[8].ExactText)
	}
	if sourceStructureContextRoleCount(
		first.Units[7].Context,
		SourceStructureContextTableHeader,
	) != 1 ||
		sourceStructureContextRoleCount(
			first.Units[7].Context,
			SourceStructureContextHeading,
		) != 2 {
		t.Fatalf("table row context = %+v", first.Units[7].Context)
	}
	if sourceStructureContextRoleCount(
		first.Units[11].Context,
		SourceStructureContextFenceOpener,
	) != 1 ||
		sourceStructureContextRoleCount(
			first.Units[11].Context,
			SourceStructureContextFenceNeighbor,
		) != 1 {
		t.Fatalf("fenced line context = %+v", first.Units[11].Context)
	}
	if first.Units[0].Class != SourceStructureUnitClassStructural ||
		first.Units[2].Class != SourceStructureUnitClassContent {
		t.Fatalf("unit classes = %s / %s", first.Units[0].Class, first.Units[2].Class)
	}
	assertSourceStructureExactAndComplete(t, input, first)

	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded SourceStructure
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	paragraphSpanID := input.Spans[2].SpanID
	unit, ok := decoded.UnitForSpan(paragraphSpanID)
	if !ok || unit.ID != first.Units[2].ID {
		t.Fatalf("UnitForSpan(%s) = %+v, %t", paragraphSpanID, unit, ok)
	}
}

func TestCompileSourceStructureDoesNotJoinIndependentTableRows(t *testing.T) {
	raw := []byte(
		"# Ownership\n\n" +
			"| Plane | Responsibility |\n" +
			"| --- | --- |\n" +
			"| source | Content-addressed bytes |\n" +
			"| runtime | periodic collection |\n",
	)
	structure, err := CompileSourceStructure(sourceStructureTestInput(t, raw))
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	var rows []SourceStructureUnit
	for _, unit := range structure.Units {
		if unit.Kind == SourceStructureUnitTableRow {
			rows = append(rows, unit)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("table row count = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if strings.Contains(row.ExactText, "Content-addressed") &&
			strings.Contains(row.ExactText, "periodic") {
			t.Fatalf("independent table rows share one unit: %q", row.ExactText)
		}
	}
}

func TestCompileSourceStructureBoundsLongParagraph(t *testing.T) {
	var source strings.Builder
	source.WriteString("# Bounded\n\n")
	for index := 0; index < 70; index++ {
		source.WriteString(fmt.Sprintf("line %02d %s\n", index, strings.Repeat("x", 100)))
	}
	input := sourceStructureTestInput(t, []byte(source.String()))
	structure, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	var paragraphs []SourceStructureUnit
	for _, unit := range structure.Units {
		if unit.Kind == SourceStructureUnitParagraph {
			paragraphs = append(paragraphs, unit)
		}
	}
	if len(paragraphs) < 2 {
		t.Fatalf("paragraph count = %d, want bounded chunks", len(paragraphs))
	}
	for _, paragraph := range paragraphs {
		if len([]byte(paragraph.ExactText)) > SourceStructureMaxUnitBytesV1 ||
			len(paragraph.CoreSpanIDs) > SourceStructureMaxContextRefsV1 ||
			paragraph.Context.ReferencedBytes > SourceStructureMaxUnitBytesV1 ||
			len(paragraph.Context.Refs) > SourceStructureMaxContextRefsV1 {
			t.Fatalf("paragraph exceeds source-structure caps: %+v", paragraph)
		}
	}
	assertSourceStructureExactAndComplete(t, input, structure)
}

func TestCompileSourceStructureReportsOptionalContextBudgetTruncation(t *testing.T) {
	raw := []byte("# Heading\n\n" + strings.Repeat("x", SourceStructureMaxUnitBytesV1))
	input := sourceStructureTestInput(t, raw)
	structure, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	if len(structure.Units) != 2 {
		t.Fatalf("unit count = %d, want 2", len(structure.Units))
	}
	paragraph := structure.Units[1]
	if paragraph.Kind != SourceStructureUnitParagraph ||
		paragraph.Context.ReferencedBytes != SourceStructureMaxUnitBytesV1 ||
		!paragraph.Context.BudgetTruncated ||
		paragraph.Context.OmittedOptionalRefs != 1 ||
		len(paragraph.Context.Refs) != 1 ||
		paragraph.Context.Refs[0].Role != SourceStructureContextCore {
		t.Fatalf("budget-truncated paragraph = %+v", paragraph)
	}
}

func TestCompileSourceStructureHandlesUnclosedFenceDeterministically(t *testing.T) {
	raw := []byte(
		"# Runtime\n\n" +
			"```text\n" +
			"first line\n" +
			"second line\n",
	)
	input := sourceStructureTestInput(t, raw)
	first, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	second, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() replay error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("unclosed fence replay differs")
	}
	want := []SourceStructureUnitKind{
		SourceStructureUnitHeading,
		SourceStructureUnitFenceMarker,
		SourceStructureUnitFencedLine,
		SourceStructureUnitFencedLine,
	}
	if got := sourceStructureKinds(first.Units); !reflect.DeepEqual(got, want) {
		t.Fatalf("unit kinds = %v, want %v", got, want)
	}
	assertSourceStructureExactAndComplete(t, input, first)
}

func TestCompileSourceStructurePreservesCRLFExactSlices(t *testing.T) {
	raw := []byte("# Runtime\r\n\r\nfirst line\r\nsecond line\r\n")
	input := sourceStructureTestInput(t, raw)
	structure, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	if len(structure.Units) != 2 ||
		structure.Units[1].ExactText != "first line\r\nsecond line" ||
		structure.Units[1].StartLine != 3 ||
		structure.Units[1].EndLine != 4 {
		t.Fatalf("CRLF structure = %+v", structure.Units)
	}
	assertSourceStructureExactAndComplete(t, input, structure)
}

func TestCompileSourceStructureAcceptsTerminalCarriageReturn(t *testing.T) {
	raw := []byte("terminal line\r")
	input := sourceStructureTestInput(t, raw)
	structure, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	if len(structure.Units) != 1 ||
		structure.Units[0].ExactText != "terminal line" {
		t.Fatalf("terminal-CR structure = %+v", structure.Units)
	}
	assertSourceStructureExactAndComplete(t, input, structure)
}

func TestCompileSourceStructureAcceptsEmptyView(t *testing.T) {
	input := sourceStructureTestInput(t, nil)
	structure, err := CompileSourceStructure(input)
	if err != nil {
		t.Fatalf("CompileSourceStructure() error = %v", err)
	}
	if len(structure.Units) != 0 || len(structure.Assignments) != 0 {
		t.Fatalf("empty structure = %+v", structure)
	}
}

func TestCompileSourceStructureRejectsCorruptOrUnsupportedInput(t *testing.T) {
	base := sourceStructureTestInput(t, []byte("first line\nsecond line\n"))
	tests := []struct {
		name     string
		mutate   func(ExtractorInput) ExtractorInput
		wantKind ErrorKind
	}{
		{
			name: "unsupported source system",
			mutate: func(input ExtractorInput) ExtractorInput {
				input.SourceSystem = SourceSystemCodeFile
				return input
			},
			wantKind: ErrorInvalidInput,
		},
		{
			name: "unsupported renderer",
			mutate: func(input ExtractorInput) ExtractorInput {
				input.Renderer.Version = "corrupt"
				return input
			},
			wantKind: ErrorInvalidInput,
		},
		{
			name: "rendered hash drift",
			mutate: func(input ExtractorInput) ExtractorInput {
				input.RenderedContentHash = contentHash([]byte("other"))
				return input
			},
			wantKind: ErrorQuotedHashMismatch,
		},
		{
			name: "overlapping spans",
			mutate: func(input ExtractorInput) ExtractorInput {
				input.Spans[1].StartByte = input.Spans[0].EndByte
				return input
			},
			wantKind: ErrorInvalidInput,
		},
		{
			name: "quoted text drift",
			mutate: func(input ExtractorInput) ExtractorInput {
				input.Spans[0].Text = "changed"
				return input
			},
			wantKind: ErrorQuotedHashMismatch,
		},
		{
			name: "display line drift",
			mutate: func(input ExtractorInput) ExtractorInput {
				input.Spans[1].DisplayLine = 3
				return input
			},
			wantKind: ErrorInvalidInput,
		},
		{
			name: "invalid UTF-8",
			mutate: func(input ExtractorInput) ExtractorInput {
				raw := []byte{0xff}
				input.RenderedText = string(raw)
				input.RawContentHash = contentHash(raw)
				input.RenderedContentHash = contentHash(raw)
				input.Spans = nil
				return input
			},
			wantKind: ErrorInvalidUTF8,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := sourceStructureCloneInput(base)
			_, err := CompileSourceStructure(test.mutate(input))
			assertKind(t, err, test.wantKind)
		})
	}
}

func TestCompileSourceStructureRejectsSourceAndSpanCaps(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "rendered bytes",
			raw:  []byte(strings.Repeat("x", int(BoundedSourceViewMaxRenderedBytesV1)+1)),
		},
		{
			name: "span count",
			raw:  []byte(strings.Repeat("x\n", int(BoundedSourceViewMaxSpansV1)+1)),
		},
		{
			name: "atomic span",
			raw:  []byte(strings.Repeat("x", SourceStructureMaxUnitBytesV1+1)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := sourceStructureTestInput(t, test.raw)
			_, err := CompileSourceStructure(input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}

func sourceStructureTestInput(t *testing.T, raw []byte) ExtractorInput {
	t.Helper()
	snapshot, view, spans, err := buildManualSource(ManualTextInput{
		SourceSystem:  SourceSystemManualText,
		SourceID:      "source-structure-" + t.Name(),
		SourceVersion: "v1",
		Raw:           raw,
	})
	if err != nil {
		t.Fatalf("buildManualSource() error = %v", err)
	}
	return (manualSourceContext{
		SourceSnapshot:     snapshot,
		ExtractionView:     view,
		SpanCatalogVersion: SpanCatalogManualLineV1,
		Spans:              spans,
	}).extractorInput()
}

func sourceStructureCloneInput(input ExtractorInput) ExtractorInput {
	cloned := input
	cloned.Spans = append([]ExtractorInputSpan(nil), input.Spans...)
	return cloned
}

func sourceStructureKinds(units []SourceStructureUnit) []SourceStructureUnitKind {
	kinds := make([]SourceStructureUnitKind, 0, len(units))
	for _, unit := range units {
		kinds = append(kinds, unit.Kind)
	}
	return kinds
}

func sourceStructureContextRoleCount(
	context SourceStructureContextEnvelope,
	role SourceStructureContextRole,
) int {
	count := 0
	for _, ref := range context.Refs {
		if ref.Role == role {
			count++
		}
	}
	return count
}

func assertSourceStructureExactAndComplete(
	t *testing.T,
	input ExtractorInput,
	structure SourceStructure,
) {
	t.Helper()
	raw := []byte(input.RenderedText)
	spansByID := make(map[string]ExtractorInputSpan, len(input.Spans))
	assignments := make(map[string]string, len(structure.Assignments))
	for _, span := range input.Spans {
		spansByID[span.SpanID] = span
	}
	for _, assignment := range structure.Assignments {
		if _, duplicate := assignments[assignment.SpanID]; duplicate {
			t.Fatalf("span %s has duplicate assignments", assignment.SpanID)
		}
		assignments[assignment.SpanID] = assignment.UnitID
	}
	if len(assignments) != len(input.Spans) {
		t.Fatalf("assignment count = %d, want %d", len(assignments), len(input.Spans))
	}
	for _, unit := range structure.Units {
		if unit.ExactText != string(raw[unit.StartByte:unit.EndByte]) {
			t.Fatalf("unit %s is not an exact raw slice", unit.ID)
		}
		if len([]byte(unit.ExactText)) > SourceStructureMaxUnitBytesV1 ||
			len(unit.CoreSpanIDs) > SourceStructureMaxContextRefsV1 ||
			unit.Context.ReferencedBytes > SourceStructureMaxUnitBytesV1 ||
			len(unit.Context.Refs) > SourceStructureMaxContextRefsV1 {
			t.Fatalf("unit %s exceeds source-structure caps: %+v", unit.ID, unit)
		}
		contextBytes := 0
		contextSeen := make(map[string]struct{}, len(unit.Context.Refs))
		coreSeen := make(map[string]struct{}, len(unit.CoreSpanIDs))
		for _, spanID := range unit.CoreSpanIDs {
			coreSeen[spanID] = struct{}{}
			if assignments[spanID] != unit.ID {
				t.Fatalf("core span %s assignment = %s, want %s", spanID, assignments[spanID], unit.ID)
			}
		}
		for _, ref := range unit.Context.Refs {
			if _, duplicate := contextSeen[ref.SpanID]; duplicate {
				t.Fatalf("unit %s repeats context span %s", unit.ID, ref.SpanID)
			}
			contextSeen[ref.SpanID] = struct{}{}
			span, ok := spansByID[ref.SpanID]
			if !ok {
				t.Fatalf("unit %s references unknown span %s", unit.ID, ref.SpanID)
			}
			contextBytes += span.EndByte - span.StartByte
			if _, core := coreSeen[ref.SpanID]; core &&
				ref.Role != SourceStructureContextCore {
				t.Fatalf("unit %s core span %s role = %s", unit.ID, ref.SpanID, ref.Role)
			}
		}
		for spanID := range coreSeen {
			if _, ok := contextSeen[spanID]; !ok {
				t.Fatalf("unit %s core span %s missing from context", unit.ID, spanID)
			}
		}
		if contextBytes != unit.Context.ReferencedBytes {
			t.Fatalf(
				"unit %s context bytes = %d, declared %d",
				unit.ID,
				contextBytes,
				unit.Context.ReferencedBytes,
			)
		}
		if unit.Context.BudgetTruncated != (unit.Context.OmittedOptionalRefs > 0) {
			t.Fatalf("unit %s context truncation metadata = %+v", unit.ID, unit.Context)
		}
	}
	for _, span := range input.Spans {
		unit, ok := structure.UnitForSpan(span.SpanID)
		if !ok || assignments[span.SpanID] != unit.ID {
			t.Fatalf("span %s did not resolve to its assigned unit", span.SpanID)
		}
	}
}
