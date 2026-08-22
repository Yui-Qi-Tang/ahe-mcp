package evidenceingestion

import (
	"fmt"
	"strings"
	"testing"
)

func TestSourceSnapshotIDDeterminism(t *testing.T) {
	input := testManualInput("source-id")
	first, _, _, err := buildManualSource(input)
	if err != nil {
		t.Fatalf("buildManualSource() error = %v", err)
	}
	second, _, _, err := buildManualSource(input)
	if err != nil {
		t.Fatalf("buildManualSource() second error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("same input snapshot ID mismatch: %q != %q", first.ID, second.ID)
	}

	changedVersion := input
	changedVersion.SourceVersion = "v2"
	versionSnapshot, _, _, err := buildManualSource(changedVersion)
	if err != nil {
		t.Fatalf("buildManualSource() changed version error = %v", err)
	}
	if first.ID == versionSnapshot.ID {
		t.Fatalf("different source_version produced same snapshot ID %q", first.ID)
	}

	changedRaw := input
	changedRaw.Raw = []byte("Refunds must be completed within 8 days.\n")
	rawSnapshot, _, _, err := buildManualSource(changedRaw)
	if err != nil {
		t.Fatalf("buildManualSource() changed raw error = %v", err)
	}
	if first.ID == rawSnapshot.ID {
		t.Fatalf("different raw content produced same snapshot ID %q", first.ID)
	}
}

func TestExtractionViewIDDeterminism(t *testing.T) {
	input := testManualInput("view-source")
	_, first, _, err := buildManualSource(input)
	if err != nil {
		t.Fatalf("buildManualSource() error = %v", err)
	}
	_, second, _, err := buildManualSource(input)
	if err != nil {
		t.Fatalf("buildManualSource() second error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("same input view ID mismatch: %q != %q", first.ID, second.ID)
	}
	if first.ID == first.SourceSnapshotID {
		t.Fatalf("view ID should not equal source snapshot ID")
	}
}

func TestSpanCatalogUTF8LineOffsets(t *testing.T) {
	chinese := "退款必須在七天內完成。"
	english := "Refunds must be completed within 7 days."
	raw := []byte(chinese + "\n" + english + "\n\n")
	_, view, spans, err := buildManualSource(ManualTextInput{
		SourceID:      "mixed-language",
		SourceVersion: "v1",
		Raw:           raw,
	})
	if err != nil {
		t.Fatalf("buildManualSource() error = %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("len(spans) = %d, want 2", len(spans))
	}
	if spans[0].SpanID != "span:S1" || spans[1].SpanID != "span:S2" {
		t.Fatalf("span IDs = %q, %q", spans[0].SpanID, spans[1].SpanID)
	}
	chineseBytes := len([]byte(chinese))
	if spans[0].StartByte != 0 || spans[0].EndByte != chineseBytes {
		t.Fatalf("first span offset = [%d,%d), want [0,%d)", spans[0].StartByte, spans[0].EndByte, chineseBytes)
	}
	englishStart := chineseBytes + 1
	englishEnd := englishStart + len([]byte(english))
	if spans[1].StartByte != englishStart || spans[1].EndByte != englishEnd {
		t.Fatalf("second span offset = [%d,%d), want [%d,%d)", spans[1].StartByte, spans[1].EndByte, englishStart, englishEnd)
	}
	if spans[0].QuotedText != chinese || spans[1].QuotedText != english {
		t.Fatalf("quoted text mismatch: %#v", spans)
	}
	for _, span := range spans {
		if err := validateSpanBounds(view, span); err != nil {
			t.Fatalf("validateSpanBounds(%s) error = %v", span.SpanID, err)
		}
		if got := contentHash([]byte(span.QuotedText)); got != span.QuotedTextHash {
			t.Fatalf("span %s quote hash = %s, want %s", span.SpanID, span.QuotedTextHash, got)
		}
	}
}

func TestManualSourceCaptureResult(t *testing.T) {
	sourceCtx, err := buildManualSourceContext(testManualInput("source-capture"))
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	result := sourceCtx.result(false)

	if result.SourceSnapshotID != sourceCtx.SourceSnapshot.ID {
		t.Fatalf("source snapshot ID = %q, want %q", result.SourceSnapshotID, sourceCtx.SourceSnapshot.ID)
	}
	if result.ExtractionViewID != sourceCtx.ExtractionView.ID {
		t.Fatalf("extraction view ID = %q, want %q", result.ExtractionViewID, sourceCtx.ExtractionView.ID)
	}
	if result.RawContentHash != sourceCtx.SourceSnapshot.RawContentHash || result.RenderedContentHash != sourceCtx.ExtractionView.RenderedContentHash {
		t.Fatalf("hashes not populated: %+v", result)
	}
	if result.SpanCatalogVersion != SpanCatalogManualLineV1 {
		t.Fatalf("span catalog version = %q, want %q", result.SpanCatalogVersion, SpanCatalogManualLineV1)
	}
	if len(result.Spans) != 2 || result.Spans[0].SpanID != "span:S1" || result.Spans[1].SpanID != "span:S2" {
		t.Fatalf("spans = %+v, want span:S1/span:S2", result.Spans)
	}

	result.Spans[0].SpanID = "mutated"
	if sourceCtx.Spans[0].SpanID != "span:S1" {
		t.Fatalf("result spans aliased source context")
	}
}

func TestCodeSourceUsesCodeIdentityContract(t *testing.T) {
	input := ManualTextInput{
		SourceSystem:  SourceSystemCodeFile,
		SourceID:      "repo:ahe-wrap:internal/refund/service.go",
		SourceVersion: "abc123",
		Raw:           []byte("package refund\n\nfunc Build() {}\n"),
		OriginMetadata: map[string]string{
			"repo_id":    "ahe-wrap",
			"commit_sha": "abc123",
			"path":       "internal/refund/service.go",
		},
		RequestID: "code-source",
	}
	ctx, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	result := ctx.result(false)

	if result.SourceSystem != SourceSystemCodeFile {
		t.Fatalf("source system = %q, want %q", result.SourceSystem, SourceSystemCodeFile)
	}
	if result.RendererName != RendererCodeIdentity || result.RendererVersion != RendererCodeIdentityVersion {
		t.Fatalf("renderer = %s/%s, want %s/%s", result.RendererName, result.RendererVersion, RendererCodeIdentity, RendererCodeIdentityVersion)
	}
	if result.SpanCatalogVersion != SpanCatalogCodeLineV1 {
		t.Fatalf("span catalog = %q, want %q", result.SpanCatalogVersion, SpanCatalogCodeLineV1)
	}
	if len(result.Spans) != 2 || result.Spans[1].QuotedText != "func Build() {}" {
		t.Fatalf("code spans = %+v", result.Spans)
	}

	manual := input
	manual.SourceSystem = SourceSystemManualText
	manualCtx, err := buildManualSourceContext(manual)
	if err != nil {
		t.Fatalf("buildManualSourceContext(manual) error = %v", err)
	}
	if manualCtx.SourceSnapshot.ID == ctx.SourceSnapshot.ID || manualCtx.ExtractionView.ID == ctx.ExtractionView.ID {
		t.Fatalf("manual and code authority must not share snapshot/view identity")
	}
}

func TestProposalFingerprintAndOccurrenceSeparation(t *testing.T) {
	ctx1 := mustAttemptContext(t, testManualInput("fingerprint-source"))
	fixture := testFixture()
	batch1, err := materializeBatch(ctx1, fixture)
	if err != nil {
		t.Fatalf("materializeBatch() error = %v", err)
	}
	batch1Again, err := materializeBatch(ctx1, fixture)
	if err != nil {
		t.Fatalf("materializeBatch() second error = %v", err)
	}
	if batch1.Occurrences[0].ProposalFingerprint != batch1Again.Occurrences[0].ProposalFingerprint {
		t.Fatalf("same proposal fingerprint mismatch")
	}

	input2 := testManualInput("fingerprint-source")
	input2.RequestID = "fresh-comparison-run"
	ctx2 := mustAttemptContext(t, input2)
	batch2, err := materializeBatch(ctx2, fixture)
	if err != nil {
		t.Fatalf("materializeBatch() fresh run error = %v", err)
	}
	if batch1.Occurrences[0].ProposalFingerprint != batch2.Occurrences[0].ProposalFingerprint {
		t.Fatalf("different attempts over same source should keep same fingerprint")
	}
	if batch1.Occurrences[0].ID == batch2.Occurrences[0].ID {
		t.Fatalf("different attempts produced same occurrence ID %q", batch1.Occurrences[0].ID)
	}

	spanTwoFixture := FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: "stmt-1",
		StatementText:   "Refunds must be completed within 7 days.",
		EvidenceRefs:    []string{"span:S2"},
	}}}
	spanTwoBatch, err := materializeBatch(ctx1, spanTwoFixture)
	if err != nil {
		t.Fatalf("materializeBatch() span two error = %v", err)
	}
	if batch1.Occurrences[0].ProposalFingerprint == spanTwoBatch.Occurrences[0].ProposalFingerprint {
		t.Fatalf("different source span produced same fingerprint")
	}

	differentStatement := FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: "stmt-1",
		StatementText:   "Refunds must be completed within 8 days.",
		EvidenceRefs:    []string{"span:S1"},
	}}}
	differentStatementBatch, err := materializeBatch(ctx1, differentStatement)
	if err != nil {
		t.Fatalf("materializeBatch() different statement error = %v", err)
	}
	if batch1.Occurrences[0].ProposalFingerprint == differentStatementBatch.Occurrences[0].ProposalFingerprint {
		t.Fatalf("different statement text produced same fingerprint")
	}
}

func TestPreflightFailures(t *testing.T) {
	t.Run("invalid utf8", func(t *testing.T) {
		input := testManualInput("bad-utf8")
		input.Raw = []byte{0xff, 0xfe}
		_, err := buildAttemptContext(input)
		assertKind(t, err, ErrorInvalidUTF8)
	})

	t.Run("unknown span", func(t *testing.T) {
		ctx := mustAttemptContext(t, testManualInput("unknown-span"))
		_, err := materializeBatch(ctx, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "stmt-1",
			StatementText:   "Refunds must be completed within 7 days.",
			EvidenceRefs:    []string{"span:S404"},
		}}})
		assertKind(t, err, ErrorUnknownSpan)
	})

	t.Run("span out of bounds", func(t *testing.T) {
		ctx := mustAttemptContext(t, testManualInput("out-of-bounds"))
		ctx.Spans[0].EndByte = len(ctx.ExtractionView.Rendered) + 1
		_, err := materializeBatch(ctx, testFixture())
		assertKind(t, err, ErrorSpanOutOfBounds)
	})

	t.Run("quoted hash mismatch", func(t *testing.T) {
		ctx := mustAttemptContext(t, testManualInput("hash-mismatch"))
		ctx.Spans[0].QuotedTextHash = "sha256:bad"
		_, err := materializeBatch(ctx, testFixture())
		assertKind(t, err, ErrorQuotedHashMismatch)
	})

	t.Run("duplicate local id", func(t *testing.T) {
		ctx := mustAttemptContext(t, testManualInput("duplicate-local"))
		_, err := materializeBatch(ctx, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{
			{ProposalLocalID: "stmt-1", StatementText: "one", EvidenceRefs: []string{"span:S1"}},
			{ProposalLocalID: "stmt-1", StatementText: "two", EvidenceRefs: []string{"span:S1"}},
		}})
		assertKind(t, err, ErrorDuplicateProposalLocalID)
	})
}

func testManualInput(sourceID string) ManualTextInput {
	return ManualTextInput{
		SourceID:      sourceID,
		SourceVersion: "v1",
		Raw: []byte("Refunds must be completed within 7 days.\n" +
			"This rule applies only to overseas orders.\n"),
		OriginMetadata: map[string]string{"fixture": "refund-policy"},
		RequestID:      sourceID + "-request",
		AttemptNumber:  1,
	}
}

func TestProducerSessionRefDoesNotChangeRunIdentity(t *testing.T) {
	sourceCtx, err := buildManualSourceContext(testManualInput("producer-session-identity"))
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	definition := ExtractorDefinitionInput{Name: "external-agent", Version: "v1"}
	legacy, err := buildAttemptContextFromSourceWithDefinition(sourceCtx, "producer-session-request", 1, definition)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinition() error = %v", err)
	}
	empty, err := buildAttemptContextFromSourceWithDefinitionAndSession(sourceCtx, "producer-session-request", 1, definition, "")
	if err != nil {
		t.Fatalf("empty session build error = %v", err)
	}
	withSession, err := buildAttemptContextFromSourceWithDefinitionAndSession(sourceCtx, "producer-session-request", 1, definition, "claude-code-session:test")
	if err != nil {
		t.Fatalf("session build error = %v", err)
	}
	if legacy.ExtractionRun.ID != empty.ExtractionRun.ID {
		t.Fatalf("empty session run ID = %q, want legacy %q", empty.ExtractionRun.ID, legacy.ExtractionRun.ID)
	}
	if withSession.ExtractionRun.ID != legacy.ExtractionRun.ID {
		t.Fatalf("session run ID = %q, want semantic run ID %q", withSession.ExtractionRun.ID, legacy.ExtractionRun.ID)
	}
	if withSession.ExtractionRun.ProducerSessionRef != "claude-code-session:test" {
		t.Fatalf("producer session ref = %q", withSession.ExtractionRun.ProducerSessionRef)
	}
}

func TestBuildExtractorDefinitionRejectsUnboundedMetadata(t *testing.T) {
	tests := []struct {
		name  string
		input ExtractorDefinitionInput
	}{
		{
			name:  "name",
			input: ExtractorDefinitionInput{Name: strings.Repeat("n", 201), Version: "v1"},
		},
		{
			name:  "version",
			input: ExtractorDefinitionInput{Name: "agent", Version: strings.Repeat("v", 201)},
		},
		{
			name:  "name UTF-8",
			input: ExtractorDefinitionInput{Name: string([]byte{0xff}), Version: "v1"},
		},
		{
			name: "config entries",
			input: ExtractorDefinitionInput{
				Name:    "agent",
				Version: "v1",
				Config:  repeatedExtractorConfig(65, "value"),
			},
		},
		{
			name: "config key",
			input: ExtractorDefinitionInput{
				Name:    "agent",
				Version: "v1",
				Config:  map[string]string{strings.Repeat("k", 201): "value"},
			},
		},
		{
			name: "config value",
			input: ExtractorDefinitionInput{
				Name:    "agent",
				Version: "v1",
				Config:  map[string]string{"key": strings.Repeat("v", (4<<10)+1)},
			},
		},
		{
			name: "config UTF-8",
			input: ExtractorDefinitionInput{
				Name:    "agent",
				Version: "v1",
				Config:  map[string]string{"key": string([]byte{0xff})},
			},
		},
		{
			name: "config total",
			input: ExtractorDefinitionInput{
				Name:    "agent",
				Version: "v1",
				Config:  repeatedExtractorConfig(17, strings.Repeat("v", 4<<10)),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildExtractorDefinition(test.input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestBuildExtractorDefinitionAcceptsMetadataBoundaries(t *testing.T) {
	config := repeatedExtractorConfig(ExtractorDefinitionConfigMaxEntries, "value")
	delete(config, "key-000")
	config[strings.Repeat("k", ExtractorDefinitionConfigKeyMaxBytes)] = strings.Repeat("v", ExtractorDefinitionConfigValueMaxBytes)
	definition, err := buildExtractorDefinition(ExtractorDefinitionInput{
		Name:    strings.Repeat("n", ExtractorDefinitionNameMaxBytes),
		Version: strings.Repeat("v", ExtractorDefinitionVersionMaxBytes),
		Config:  config,
	})
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	if len(definition.Config) != ExtractorDefinitionConfigMaxEntries {
		t.Fatalf("config entries = %d, want %d", len(definition.Config), ExtractorDefinitionConfigMaxEntries)
	}
}

func repeatedExtractorConfig(entries int, value string) map[string]string {
	config := make(map[string]string, entries)
	for i := range entries {
		config[fmt.Sprintf("key-%03d", i)] = value
	}
	return config
}

func testFixture() FrozenExtractorOutput {
	return FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: "stmt-1",
		StatementText:   "Refunds must be completed within 7 days.",
		EvidenceRefs:    []string{"span:S1"},
	}}}
}

func testExternalExtractorDefinition() ExtractorDefinitionInput {
	return ExtractorDefinitionInput{Name: "test-external-agent", Version: "v1"}
}

func mustAttemptContext(t *testing.T, input ManualTextInput) attemptContext {
	t.Helper()
	ctx, err := buildAttemptContext(input)
	if err != nil {
		t.Fatalf("buildAttemptContext() error = %v", err)
	}
	return ctx
}

func assertKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want kind %s", want)
	}
	got, ok := KindOf(err)
	if !ok {
		t.Fatalf("KindOf(%v) not found, want %s", err, want)
	}
	if got != want {
		t.Fatalf("error kind = %s, want %s: %v", got, want, err)
	}
}
