package evidenceingestion

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// SourceStructureContractV1 identifies the deterministic manual-text structure compiler.
	SourceStructureContractV1 = "markdown-source-structure-v1"
	// SourceContextEnvelopeContractV1 identifies exact-span context selected by the compiler.
	SourceContextEnvelopeContractV1 = "source-span-context-envelope-v1"

	// SourceStructureMaxUnitBytesV1 caps one exact source unit.
	SourceStructureMaxUnitBytesV1 = 8192
	// SourceStructureMaxContextRefsV1 caps exact span references in one context envelope.
	SourceStructureMaxContextRefsV1 = 64
	// SourceStructureFenceContextRadiusV1 bounds neighboring fenced lines on either side.
	SourceStructureFenceContextRadiusV1 = 2
)

// SourceStructureUnitKind is a closed v1 source-unit classification.
type SourceStructureUnitKind string

const (
	SourceStructureUnitParagraph      SourceStructureUnitKind = "paragraph"
	SourceStructureUnitListItem       SourceStructureUnitKind = "list_item"
	SourceStructureUnitTableRow       SourceStructureUnitKind = "table_row"
	SourceStructureUnitFencedLine     SourceStructureUnitKind = "fenced_line"
	SourceStructureUnitHeading        SourceStructureUnitKind = "heading"
	SourceStructureUnitTableHeader    SourceStructureUnitKind = "table_header"
	SourceStructureUnitTableDelimiter SourceStructureUnitKind = "table_delimiter"
	SourceStructureUnitFenceMarker    SourceStructureUnitKind = "fence_marker"
	SourceStructureUnitThematicBreak  SourceStructureUnitKind = "thematic_break"
)

// SourceStructureUnitClass separates content-bearing units from source syntax.
type SourceStructureUnitClass string

const (
	SourceStructureUnitClassContent    SourceStructureUnitClass = "content"
	SourceStructureUnitClassStructural SourceStructureUnitClass = "structural"
)

// SourceStructureContextRole describes why an exact span is in an envelope.
type SourceStructureContextRole string

const (
	SourceStructureContextCore          SourceStructureContextRole = "core"
	SourceStructureContextHeading       SourceStructureContextRole = "heading"
	SourceStructureContextTableHeader   SourceStructureContextRole = "table_header"
	SourceStructureContextFenceOpener   SourceStructureContextRole = "fence_opener"
	SourceStructureContextFenceNeighbor SourceStructureContextRole = "fence_neighbor"
)

// SourceStructureContextRef points to one exact persisted source span.
type SourceStructureContextRef struct {
	SpanID string                     `json:"span_id"`
	Role   SourceStructureContextRole `json:"role"`
}

// SourceStructureContextEnvelope preserves bounded exact context outside retrieval.
type SourceStructureContextEnvelope struct {
	Contract            string                      `json:"contract"`
	Refs                []SourceStructureContextRef `json:"refs"`
	ReferencedBytes     int                         `json:"referenced_bytes"`
	BudgetTruncated     bool                        `json:"budget_truncated"`
	OmittedOptionalRefs int                         `json:"omitted_optional_refs"`
}

// SourceStructureUnit is one exact, bounded slice of the rendered source.
type SourceStructureUnit struct {
	ID          string                         `json:"id"`
	Kind        SourceStructureUnitKind        `json:"kind"`
	Class       SourceStructureUnitClass       `json:"class"`
	ExactText   string                         `json:"exact_text"`
	StartByte   int                            `json:"start_byte"`
	EndByte     int                            `json:"end_byte"`
	StartLine   int                            `json:"start_line"`
	EndLine     int                            `json:"end_line"`
	CoreSpanIDs []string                       `json:"core_span_ids"`
	Context     SourceStructureContextEnvelope `json:"context"`
}

// SourceStructureAssignment binds every source span to exactly one source unit.
type SourceStructureAssignment struct {
	SpanID string `json:"span_id"`
	UnitID string `json:"unit_id"`
}

// SourceStructure is the deterministic read-only structure projection of one view.
type SourceStructure struct {
	Contract            string                      `json:"contract"`
	ContextContract     string                      `json:"context_contract"`
	SourceSnapshotID    string                      `json:"source_snapshot_id"`
	ExtractionViewID    string                      `json:"extraction_view_id"`
	SpanCatalogVersion  string                      `json:"span_catalog_version"`
	SearchParticipation bool                        `json:"search_participation"`
	Units               []SourceStructureUnit       `json:"units"`
	Assignments         []SourceStructureAssignment `json:"assignments"`
}

// UnitForSpan resolves the sole source unit assigned to spanID.
func (structure SourceStructure) UnitForSpan(spanID string) (SourceStructureUnit, bool) {
	var unitID string
	for _, assignment := range structure.Assignments {
		if assignment.SpanID == spanID {
			unitID = assignment.UnitID
			break
		}
	}
	if unitID == "" {
		return SourceStructureUnit{}, false
	}
	for _, unit := range structure.Units {
		if unit.ID == unitID {
			return unit, true
		}
	}
	return SourceStructureUnit{}, false
}

// CompileSourceStructure derives a versioned structure projection without database writes.
func CompileSourceStructure(input ExtractorInput) (SourceStructure, error) {
	if err := validateSourceStructureInput(input); err != nil {
		return SourceStructure{}, err
	}
	compiler := sourceStructureCompiler{
		raw:             []byte(input.RenderedText),
		spans:           input.Spans,
		result:          newSourceStructure(input),
		assignedSpanIDs: make(map[string]struct{}, len(input.Spans)),
	}
	if err := compiler.compile(); err != nil {
		return SourceStructure{}, err
	}
	return compiler.result, nil
}

func newSourceStructure(input ExtractorInput) SourceStructure {
	return SourceStructure{
		Contract:            SourceStructureContractV1,
		ContextContract:     SourceContextEnvelopeContractV1,
		SourceSnapshotID:    input.SourceSnapshotID,
		ExtractionViewID:    input.ExtractionViewID,
		SpanCatalogVersion:  input.SpanCatalogVersion,
		SearchParticipation: false,
		Units:               make([]SourceStructureUnit, 0),
		Assignments:         make([]SourceStructureAssignment, 0, len(input.Spans)),
	}
}

func validateSourceStructureInput(input ExtractorInput) error {
	if !hasStableIDPrefix(input.SourceSnapshotID, "srcsnap:") {
		return newDomainError(
			ErrorInvalidRecordID,
			"source_snapshot_id %q must start with srcsnap:",
			input.SourceSnapshotID,
		)
	}
	if !hasStableIDPrefix(input.ExtractionViewID, "view:") {
		return newDomainError(
			ErrorInvalidRecordID,
			"extraction_view_id %q must start with view:",
			input.ExtractionViewID,
		)
	}
	if input.SourceSystem != SourceSystemManualText {
		return newDomainError(
			ErrorInvalidInput,
			"source structure contract %s supports only source system %q",
			SourceStructureContractV1,
			SourceSystemManualText,
		)
	}
	if input.SourceID == "" || input.SourceVersion == "" {
		return newDomainError(ErrorInvalidInput, "source_id and source_version are required")
	}
	if input.Renderer.Name != RendererManualTextIdentity ||
		input.Renderer.Version != RendererManualTextIdentityVersion {
		return newDomainError(
			ErrorInvalidInput,
			"source structure contract %s requires renderer %s/%s",
			SourceStructureContractV1,
			RendererManualTextIdentity,
			RendererManualTextIdentityVersion,
		)
	}
	if input.SpanCatalogVersion != SpanCatalogManualLineV1 {
		return newDomainError(
			ErrorInvalidInput,
			"source structure contract %s requires span catalog %q",
			SourceStructureContractV1,
			SpanCatalogManualLineV1,
		)
	}
	raw := []byte(input.RenderedText)
	if !utf8.Valid(raw) {
		return newDomainError(ErrorInvalidUTF8, "rendered source view is not valid UTF-8")
	}
	if int64(len(raw)) > BoundedSourceViewMaxRenderedBytesV1 {
		return newDomainError(
			ErrorInvalidInput,
			"rendered source view exceeds %d bytes",
			BoundedSourceViewMaxRenderedBytesV1,
		)
	}
	if int64(len(input.Spans)) > BoundedSourceViewMaxSpansV1 {
		return newDomainError(
			ErrorInvalidInput,
			"source view exceeds %d spans",
			BoundedSourceViewMaxSpansV1,
		)
	}
	renderedHash := contentHash(raw)
	if input.RenderedContentHash != renderedHash {
		return newDomainError(
			ErrorQuotedHashMismatch,
			"rendered content hash %s, want %s",
			input.RenderedContentHash,
			renderedHash,
		)
	}
	if input.RawContentHash != renderedHash {
		return newDomainError(
			ErrorQuotedHashMismatch,
			"identity view hash %s does not match raw source hash %s",
			input.RenderedContentHash,
			input.RawContentHash,
		)
	}
	return validateSourceStructureSpans(raw, input.Spans)
}

func validateSourceStructureSpans(raw []byte, spans []ExtractorInputSpan) error {
	seen := make(map[string]struct{}, len(spans))
	for i, span := range spans {
		if !hasStableIDPrefix(span.SpanID, "span:") {
			return newDomainError(
				ErrorInvalidRecordID,
				"span_id %q must start with span:",
				span.SpanID,
			)
		}
		if _, duplicate := seen[span.SpanID]; duplicate {
			return newDomainError(ErrorInvalidInput, "span_id %q is duplicated", span.SpanID)
		}
		seen[span.SpanID] = struct{}{}
		if span.StartByte < 0 || span.EndByte <= span.StartByte || span.EndByte > len(raw) {
			return newDomainError(
				ErrorSpanOutOfBounds,
				"span %s [%d,%d) outside rendered bytes length %d",
				span.SpanID,
				span.StartByte,
				span.EndByte,
				len(raw),
			)
		}
		if span.EndByte-span.StartByte > SourceStructureMaxUnitBytesV1 {
			return newDomainError(
				ErrorInvalidInput,
				"span %s exceeds the %d-byte source-unit limit",
				span.SpanID,
				SourceStructureMaxUnitBytesV1,
			)
		}
		if i > 0 {
			previous := spans[i-1]
			if previous.EndByte >= span.StartByte {
				return newDomainError(
					ErrorInvalidInput,
					"span %s is not strictly ordered after %s",
					span.SpanID,
					previous.SpanID,
				)
			}
			if previous.DisplayLine >= span.DisplayLine {
				return newDomainError(
					ErrorInvalidInput,
					"span %s display line is not strictly ordered",
					span.SpanID,
				)
			}
		}
		if span.DisplayLine <= 0 ||
			span.DisplayLine != 1+bytes.Count(raw[:span.StartByte], []byte{'\n'}) {
			return newDomainError(
				ErrorInvalidInput,
				"span %s display line %d does not match its byte offset",
				span.SpanID,
				span.DisplayLine,
			)
		}
		if span.StartByte > 0 && raw[span.StartByte-1] != '\n' {
			return newDomainError(
				ErrorInvalidInput,
				"span %s does not begin at a line boundary",
				span.SpanID,
			)
		}
		if span.EndByte < len(raw) &&
			raw[span.EndByte] != '\n' &&
			!(raw[span.EndByte] == '\r' &&
				(span.EndByte+1 == len(raw) ||
					raw[span.EndByte+1] == '\n')) {
			return newDomainError(
				ErrorInvalidInput,
				"span %s does not end at a line boundary",
				span.SpanID,
			)
		}
		quoted := raw[span.StartByte:span.EndByte]
		if string(quoted) != span.Text {
			return newDomainError(
				ErrorQuotedHashMismatch,
				"span %s quoted text does not match rendered bytes",
				span.SpanID,
			)
		}
		if got := contentHash(quoted); got != span.QuotedTextHash {
			return newDomainError(
				ErrorQuotedHashMismatch,
				"span %s quoted hash %s, want %s",
				span.SpanID,
				span.QuotedTextHash,
				got,
			)
		}
	}
	return nil
}

type sourceStructureCompiler struct {
	raw             []byte
	spans           []ExtractorInputSpan
	headings        [6]*ExtractorInputSpan
	result          SourceStructure
	assignedSpanIDs map[string]struct{}
}

func (compiler *sourceStructureCompiler) compile() error {
	for index := 0; index < len(compiler.spans); {
		line := strings.TrimSpace(compiler.spans[index].Text)
		if level, ok := sourceStructureATXHeadingLevel(line); ok {
			compiler.setHeading(level, compiler.spans[index])
			if err := compiler.addUnit(
				SourceStructureUnitHeading,
				[]ExtractorInputSpan{compiler.spans[index]},
				nil,
				nil,
			); err != nil {
				return err
			}
			index++
			continue
		}
		if marker, width, ok := sourceStructureMarkdownFence(line); ok {
			next, err := compiler.compileFence(index, marker, width)
			if err != nil {
				return err
			}
			index = next
			continue
		}
		if compiler.tableStartsAt(index) {
			next, err := compiler.compileTable(index)
			if err != nil {
				return err
			}
			index = next
			continue
		}
		if sourceStructureMarkdownTableDelimiter(line) {
			if err := compiler.addUnit(
				SourceStructureUnitTableDelimiter,
				[]ExtractorInputSpan{compiler.spans[index]},
				nil,
				nil,
			); err != nil {
				return err
			}
			index++
			continue
		}
		if sourceStructureMarkdownThematicBreak(line) {
			if err := compiler.addUnit(
				SourceStructureUnitThematicBreak,
				[]ExtractorInputSpan{compiler.spans[index]},
				nil,
				nil,
			); err != nil {
				return err
			}
			index++
			continue
		}
		if _, ok := sourceStructureMarkdownListMarker(compiler.spans[index].Text); ok {
			next, err := compiler.compileListItem(index)
			if err != nil {
				return err
			}
			index = next
			continue
		}
		next, err := compiler.compileParagraph(index)
		if err != nil {
			return err
		}
		index = next
	}
	if len(compiler.result.Assignments) != len(compiler.spans) {
		return newDomainError(
			ErrorInvalidInput,
			"source structure assigned %d of %d spans",
			len(compiler.result.Assignments),
			len(compiler.spans),
		)
	}
	return nil
}

func (compiler *sourceStructureCompiler) setHeading(level int, span ExtractorInputSpan) {
	current := span
	compiler.headings[level-1] = &current
	for index := level; index < len(compiler.headings); index++ {
		compiler.headings[index] = nil
	}
}

func (compiler *sourceStructureCompiler) headingPath() []ExtractorInputSpan {
	path := make([]ExtractorInputSpan, 0, len(compiler.headings))
	for _, heading := range compiler.headings {
		if heading != nil {
			path = append(path, *heading)
		}
	}
	return path
}

func (compiler *sourceStructureCompiler) compileFence(
	start int,
	marker byte,
	width int,
) (int, error) {
	end := len(compiler.spans)
	for index := start + 1; index < len(compiler.spans); index++ {
		candidateMarker, candidateWidth, ok := sourceStructureMarkdownFence(
			strings.TrimSpace(compiler.spans[index].Text),
		)
		if ok && candidateMarker == marker && candidateWidth >= width {
			end = index
			break
		}
	}
	content := compiler.spans[start+1 : end]
	openerNeighbors := content
	if len(openerNeighbors) > SourceStructureFenceContextRadiusV1 {
		openerNeighbors = openerNeighbors[:SourceStructureFenceContextRadiusV1]
	}
	if err := compiler.addUnit(
		SourceStructureUnitFenceMarker,
		[]ExtractorInputSpan{compiler.spans[start]},
		nil,
		sourceStructureContextCandidates(
			openerNeighbors,
			SourceStructureContextFenceNeighbor,
		),
	); err != nil {
		return 0, err
	}
	for contentIndex, core := range content {
		from := contentIndex - SourceStructureFenceContextRadiusV1
		if from < 0 {
			from = 0
		}
		to := contentIndex + SourceStructureFenceContextRadiusV1 + 1
		if to > len(content) {
			to = len(content)
		}
		neighbors := make([]ExtractorInputSpan, 0, to-from-1)
		for neighborIndex := from; neighborIndex < to; neighborIndex++ {
			if neighborIndex != contentIndex {
				neighbors = append(neighbors, content[neighborIndex])
			}
		}
		if err := compiler.addUnit(
			SourceStructureUnitFencedLine,
			[]ExtractorInputSpan{core},
			[]sourceStructureContextCandidate{{
				span: compiler.spans[start],
				role: SourceStructureContextFenceOpener,
			}},
			sourceStructureContextCandidates(
				neighbors,
				SourceStructureContextFenceNeighbor,
			),
		); err != nil {
			return 0, err
		}
	}
	if end == len(compiler.spans) {
		return end, nil
	}
	closerNeighbors := content
	if len(closerNeighbors) > SourceStructureFenceContextRadiusV1 {
		closerNeighbors = closerNeighbors[len(closerNeighbors)-SourceStructureFenceContextRadiusV1:]
	}
	if err := compiler.addUnit(
		SourceStructureUnitFenceMarker,
		[]ExtractorInputSpan{compiler.spans[end]},
		[]sourceStructureContextCandidate{{
			span: compiler.spans[start],
			role: SourceStructureContextFenceOpener,
		}},
		sourceStructureContextCandidates(
			closerNeighbors,
			SourceStructureContextFenceNeighbor,
		),
	); err != nil {
		return 0, err
	}
	return end + 1, nil
}

func (compiler *sourceStructureCompiler) tableStartsAt(start int) bool {
	if start+1 >= len(compiler.spans) || compiler.blankBetween(start, start+1) {
		return false
	}
	return sourceStructureMarkdownTableRow(compiler.spans[start].Text) &&
		sourceStructureMarkdownTableDelimiter(compiler.spans[start+1].Text)
}

func (compiler *sourceStructureCompiler) compileTable(start int) (int, error) {
	header := compiler.spans[start]
	delimiter := compiler.spans[start+1]
	if err := compiler.addUnit(
		SourceStructureUnitTableHeader,
		[]ExtractorInputSpan{header},
		nil,
		nil,
	); err != nil {
		return 0, err
	}
	if err := compiler.addUnit(
		SourceStructureUnitTableDelimiter,
		[]ExtractorInputSpan{delimiter},
		[]sourceStructureContextCandidate{{
			span: header,
			role: SourceStructureContextTableHeader,
		}},
		nil,
	); err != nil {
		return 0, err
	}
	end := start + 2
	for end < len(compiler.spans) {
		if compiler.blankBetween(end-1, end) ||
			!sourceStructureMarkdownTableRow(compiler.spans[end].Text) {
			break
		}
		if err := compiler.addUnit(
			SourceStructureUnitTableRow,
			[]ExtractorInputSpan{compiler.spans[end]},
			[]sourceStructureContextCandidate{{
				span: header,
				role: SourceStructureContextTableHeader,
			}},
			nil,
		); err != nil {
			return 0, err
		}
		end++
	}
	return end, nil
}

func (compiler *sourceStructureCompiler) compileListItem(start int) (int, error) {
	markerIndent, _ := sourceStructureMarkdownListMarker(compiler.spans[start].Text)
	end := start + 1
	for end < len(compiler.spans) {
		if compiler.blankBetween(end-1, end) {
			break
		}
		line := compiler.spans[end].Text
		if _, ok := sourceStructureMarkdownListMarker(line); ok {
			break
		}
		if sourceStructureLeadingSpaces(line) <= markerIndent ||
			sourceStructureStructuralLine(strings.TrimSpace(line)) ||
			compiler.tableStartsAt(end) {
			break
		}
		end++
	}
	if err := compiler.addChunks(SourceStructureUnitListItem, start, end); err != nil {
		return 0, err
	}
	return end, nil
}

func (compiler *sourceStructureCompiler) compileParagraph(start int) (int, error) {
	end := start + 1
	for end < len(compiler.spans) {
		if compiler.blankBetween(end-1, end) {
			break
		}
		line := strings.TrimSpace(compiler.spans[end].Text)
		if sourceStructureStructuralLine(line) || compiler.tableStartsAt(end) {
			break
		}
		if _, ok := sourceStructureMarkdownListMarker(compiler.spans[end].Text); ok {
			break
		}
		end++
	}
	if err := compiler.addChunks(SourceStructureUnitParagraph, start, end); err != nil {
		return 0, err
	}
	return end, nil
}

func (compiler *sourceStructureCompiler) addChunks(
	kind SourceStructureUnitKind,
	start int,
	end int,
) error {
	for chunkStart := start; chunkStart < end; {
		chunkEnd := chunkStart + 1
		for chunkEnd < end &&
			chunkEnd-chunkStart < SourceStructureMaxContextRefsV1 &&
			compiler.spans[chunkEnd].EndByte-compiler.spans[chunkStart].StartByte <=
				SourceStructureMaxUnitBytesV1 {
			chunkEnd++
		}
		if err := compiler.addUnit(
			kind,
			compiler.spans[chunkStart:chunkEnd],
			nil,
			nil,
		); err != nil {
			return err
		}
		chunkStart = chunkEnd
	}
	return nil
}

type sourceStructureContextCandidate struct {
	span ExtractorInputSpan
	role SourceStructureContextRole
}

func sourceStructureContextCandidates(
	spans []ExtractorInputSpan,
	role SourceStructureContextRole,
) []sourceStructureContextCandidate {
	candidates := make([]sourceStructureContextCandidate, 0, len(spans))
	for _, span := range spans {
		candidates = append(candidates, sourceStructureContextCandidate{
			span: span,
			role: role,
		})
	}
	return candidates
}

func (compiler *sourceStructureCompiler) addUnit(
	kind SourceStructureUnitKind,
	core []ExtractorInputSpan,
	structural []sourceStructureContextCandidate,
	neighbors []sourceStructureContextCandidate,
) error {
	if len(core) == 0 {
		return newDomainError(ErrorInvalidInput, "%s source unit has no core spans", kind)
	}
	first := core[0]
	last := core[len(core)-1]
	if last.EndByte-first.StartByte > SourceStructureMaxUnitBytesV1 ||
		len(core) > SourceStructureMaxContextRefsV1 {
		return newDomainError(
			ErrorInvalidInput,
			"%s source unit [%d,%d) exceeds %d bytes or %d refs",
			kind,
			first.StartByte,
			last.EndByte,
			SourceStructureMaxUnitBytesV1,
			SourceStructureMaxContextRefsV1,
		)
	}
	coreSpanIDs := make([]string, 0, len(core))
	for _, span := range core {
		coreSpanIDs = append(coreSpanIDs, span.SpanID)
	}
	unitID, err := stableID("srcunit:", "source_structure_unit", struct {
		Contract         string                  `json:"contract"`
		ExtractionViewID string                  `json:"extraction_view_id"`
		Kind             SourceStructureUnitKind `json:"kind"`
		CoreSpanIDs      []string                `json:"core_span_ids"`
	}{
		Contract:         SourceStructureContractV1,
		ExtractionViewID: compiler.result.ExtractionViewID,
		Kind:             kind,
		CoreSpanIDs:      coreSpanIDs,
	})
	if err != nil {
		return fmt.Errorf("deriving source structure unit identity: %w", err)
	}
	context, err := sourceStructureContext(
		core,
		compiler.headingPath(),
		structural,
		neighbors,
	)
	if err != nil {
		return err
	}
	unit := SourceStructureUnit{
		ID:          unitID,
		Kind:        kind,
		Class:       sourceStructureUnitClass(kind),
		ExactText:   string(compiler.raw[first.StartByte:last.EndByte]),
		StartByte:   first.StartByte,
		EndByte:     last.EndByte,
		StartLine:   first.DisplayLine,
		EndLine:     last.DisplayLine,
		CoreSpanIDs: coreSpanIDs,
		Context:     context,
	}
	compiler.result.Units = append(compiler.result.Units, unit)
	for _, span := range core {
		if _, duplicate := compiler.assignedSpanIDs[span.SpanID]; duplicate {
			return newDomainError(
				ErrorInvalidInput,
				"span %s belongs to multiple source units",
				span.SpanID,
			)
		}
		compiler.assignedSpanIDs[span.SpanID] = struct{}{}
		compiler.result.Assignments = append(
			compiler.result.Assignments,
			SourceStructureAssignment{
				SpanID: span.SpanID,
				UnitID: unitID,
			},
		)
	}
	return nil
}

func sourceStructureUnitClass(kind SourceStructureUnitKind) SourceStructureUnitClass {
	switch kind {
	case SourceStructureUnitParagraph,
		SourceStructureUnitListItem,
		SourceStructureUnitTableRow,
		SourceStructureUnitFencedLine:
		return SourceStructureUnitClassContent
	default:
		return SourceStructureUnitClassStructural
	}
}

func sourceStructureContext(
	core []ExtractorInputSpan,
	headings []ExtractorInputSpan,
	structural []sourceStructureContextCandidate,
	neighbors []sourceStructureContextCandidate,
) (SourceStructureContextEnvelope, error) {
	selected := make(map[string]sourceStructureContextCandidate)
	referencedBytes := 0
	addRequired := func(candidate sourceStructureContextCandidate) error {
		if _, exists := selected[candidate.span.SpanID]; exists {
			return nil
		}
		referencedBytes += candidate.span.EndByte - candidate.span.StartByte
		if len(selected)+1 > SourceStructureMaxContextRefsV1 ||
			referencedBytes > SourceStructureMaxUnitBytesV1 {
			return newDomainError(
				ErrorInvalidInput,
				"core context exceeds %d bytes or %d refs",
				SourceStructureMaxUnitBytesV1,
				SourceStructureMaxContextRefsV1,
			)
		}
		selected[candidate.span.SpanID] = candidate
		return nil
	}
	for _, span := range core {
		if err := addRequired(sourceStructureContextCandidate{
			span: span,
			role: SourceStructureContextCore,
		}); err != nil {
			return SourceStructureContextEnvelope{}, err
		}
	}

	omitted := 0
	addOptional := func(candidate sourceStructureContextCandidate) {
		if _, exists := selected[candidate.span.SpanID]; exists {
			return
		}
		size := candidate.span.EndByte - candidate.span.StartByte
		if len(selected)+1 > SourceStructureMaxContextRefsV1 ||
			referencedBytes+size > SourceStructureMaxUnitBytesV1 {
			omitted++
			return
		}
		selected[candidate.span.SpanID] = candidate
		referencedBytes += size
	}
	for _, candidate := range structural {
		addOptional(candidate)
	}
	for index := len(headings) - 1; index >= 0; index-- {
		addOptional(sourceStructureContextCandidate{
			span: headings[index],
			role: SourceStructureContextHeading,
		})
	}
	for _, candidate := range neighbors {
		addOptional(candidate)
	}

	ordered := make([]sourceStructureContextCandidate, 0, len(selected))
	for _, candidate := range selected {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].span.StartByte == ordered[right].span.StartByte {
			return ordered[left].span.SpanID < ordered[right].span.SpanID
		}
		return ordered[left].span.StartByte < ordered[right].span.StartByte
	})
	envelope := SourceStructureContextEnvelope{
		Contract:            SourceContextEnvelopeContractV1,
		Refs:                make([]SourceStructureContextRef, 0, len(ordered)),
		ReferencedBytes:     referencedBytes,
		BudgetTruncated:     omitted > 0,
		OmittedOptionalRefs: omitted,
	}
	for _, candidate := range ordered {
		envelope.Refs = append(envelope.Refs, SourceStructureContextRef{
			SpanID: candidate.span.SpanID,
			Role:   candidate.role,
		})
	}
	return envelope, nil
}

func (compiler *sourceStructureCompiler) blankBetween(left int, right int) bool {
	if left < 0 || right >= len(compiler.spans) || left >= right {
		return false
	}
	return bytes.Count(
		compiler.raw[compiler.spans[left].EndByte:compiler.spans[right].StartByte],
		[]byte{'\n'},
	) >= 2
}

func sourceStructureStructuralLine(line string) bool {
	if _, ok := sourceStructureATXHeadingLevel(line); ok {
		return true
	}
	if _, _, ok := sourceStructureMarkdownFence(line); ok {
		return true
	}
	return sourceStructureMarkdownThematicBreak(line) ||
		sourceStructureMarkdownTableDelimiter(line)
}

func sourceStructureATXHeadingLevel(line string) (int, bool) {
	hashes := 0
	for hashes < len(line) && line[hashes] == '#' {
		hashes++
	}
	return hashes, hashes > 0 &&
		hashes <= 6 &&
		hashes < len(line) &&
		(line[hashes] == ' ' || line[hashes] == '\t')
}

func sourceStructureMarkdownFence(line string) (byte, int, bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	marker := line[0]
	width := 0
	for width < len(line) && line[width] == marker {
		width++
	}
	return marker, width, width >= 3
}

func sourceStructureMarkdownThematicBreak(line string) bool {
	compact := strings.ReplaceAll(strings.ReplaceAll(line, " ", ""), "\t", "")
	if len(compact) < 3 {
		return false
	}
	marker := compact[0]
	if marker != '-' && marker != '*' && marker != '_' {
		return false
	}
	for index := 1; index < len(compact); index++ {
		if compact[index] != marker {
			return false
		}
	}
	return true
}

func sourceStructureMarkdownListMarker(line string) (int, bool) {
	indent := sourceStructureLeadingSpaces(line)
	if indent > 3 || indent >= len(line) {
		return 0, false
	}
	rest := line[indent:]
	if len(rest) >= 2 &&
		(rest[0] == '-' || rest[0] == '+' || rest[0] == '*') &&
		(rest[1] == ' ' || rest[1] == '\t') {
		return indent, true
	}
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 ||
		digits+1 >= len(rest) ||
		(rest[digits] != '.' && rest[digits] != ')') {
		return 0, false
	}
	return indent, rest[digits+1] == ' ' || rest[digits+1] == '\t'
}

func sourceStructureLeadingSpaces(line string) int {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	return spaces
}

func sourceStructureMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Contains(trimmed, "|") && trimmed != "|"
}

func sourceStructureMarkdownTableDelimiter(line string) bool {
	trimmed := strings.Trim(strings.TrimSpace(line), "|")
	cells := strings.Split(trimmed, "|")
	if len(cells) < 2 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if len(cell) > 0 && cell[0] == ':' {
			cell = cell[1:]
		}
		if len(cell) > 0 && cell[len(cell)-1] == ':' {
			cell = cell[:len(cell)-1]
		}
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}
