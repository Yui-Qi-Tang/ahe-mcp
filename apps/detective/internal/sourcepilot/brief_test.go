package sourcepilot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func briefFixture() BriefSource {
	return BriefSource{Version: BriefSourceVersion, SourceKind: "news", SourceID: "synthetic-atlas", SourceRevision: "fixture-v1",
		SourceURL: "https://example.invalid/incident", ObservedAt: "2026-09-10T09:00:00Z",
		Coverage: "full_document", Limitations: []string{},
		Body: "Atlas API reported increased errors. The team reverted a configuration change, and requests outside the north region returned to normal.\nAtlas API requests in the north region remain delayed. The cause remains unconfirmed."}
}

type briefModel struct {
	responses []*model.LLMResponse
	err       error
	requests  []*model.LLMRequest
	stream    bool
	after     func()
}

func (m *briefModel) Name() string { return "brief-test-only" }
func (m *briefModel) GenerateContent(_ context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.requests = append(m.requests, req)
		m.stream = stream
		if m.err != nil {
			yield(nil, m.err)
			return
		}
		for _, response := range m.responses {
			if !yield(response, nil) {
				return
			}
		}
		if m.after != nil {
			m.after()
		}
	}
}

func briefResponse(text string) *model.LLMResponse {
	return &model.LLMResponse{Content: genai.NewContentFromText(text, genai.RoleModel), FinishReason: genai.FinishReasonStop}
}

func TestBriefSinglePlainTextCallKeepsCompleteInput(t *testing.T) {
	source := briefFixture()
	source.Body = strings.Repeat("Original context. ", 80) + "\r\n" + source.Body + "<br/>Final marker: the cause remains unconfirmed."
	wantText := " Atlas API 曾出現較多錯誤，團隊已還原設定變更，北區以外的請求已恢復正常。北區仍有延遲，原因尚未確認。\n"
	llm := &briefModel{responses: []*model.LLMResponse{briefResponse(wantText)}}
	extractor, err := NewBriefExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}
	report, err := extractor.Extract(context.Background(), source)
	if err != nil || len(llm.requests) != 1 || llm.stream {
		t.Fatalf("one-shot extraction failed: %v", err)
	}
	if report.Stage != "complete" || report.RawText != wantText || report.Text != wantText || !reflect.DeepEqual(report.Source, source) || report.HumanReview != "not_reviewed" || report.AuthorityEffect != "none" || report.ReferenceScope != "complete_input_not_claim_support" {
		t.Fatalf("changed text, source or authority: %+v", report)
	}
	if ValidateSegments(source.Body, report.Projection) != nil || len(report.Projection.Segments) != 4 {
		t.Fatal("original paragraph inventory was not retained")
	}
	req := llm.requests[0]
	if len(req.Contents) != 1 || req.Contents[0].Role != genai.RoleUser || len(req.Contents[0].Parts) != 1 || len(req.Tools) != 0 {
		t.Fatal("request acquired history or tools")
	}
	cfg := req.Config
	if cfg == nil || cfg.Temperature == nil || *cfg.Temperature != float32(0.3) || cfg.TopP == nil || *cfg.TopP != float32(0.9) || cfg.MaxOutputTokens != 768 || cfg.ResponseMIMEType != "text/plain" || cfg.ResponseJsonSchema != nil || cfg.ResponseSchema != nil || len(cfg.Tools) != 0 || cfg.ThinkingConfig != nil || cfg.SystemInstruction.Parts[0].Text != BriefInstruction {
		t.Fatal("brief acquired structured reasoning, tools or different parameters")
	}
	input := req.Contents[0].Parts[0].Text
	var decoded map[string]string
	if json.Unmarshal([]byte(input), &decoded) != nil || len(decoded) != 1 || decoded["body"] != source.Body {
		t.Fatal("body was truncated, summarized, title-prefixed or numbered")
	}
	digest := sha256.Sum256([]byte(input))
	if report.InputSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("input digest is not bound to the actual serialized request")
	}
	for _, marker := range []string{source.SourceID, source.SourceRevision, source.SourceURL, source.ObservedAt, "coverage", "segments", "body_sha256", "confidence"} {
		if strings.Contains(input, marker) {
			t.Fatalf("controller metadata leaked into model input: %q", marker)
		}
	}
	assertBriefRoundTrip(t, report)
}

func TestBriefAcceptsNavigationAndDoesNotClaimSemanticValidation(t *testing.T) {
	for _, text := range []string{"原文只有導覽連結，沒有事件資訊。", "設定變更已確定造成全球所有服務中斷。", "A state.", strings.Repeat("界", 1536), strings.Repeat("🌍", 1536)} {
		llm := &briefModel{responses: []*model.LLMResponse{briefResponse(text)}}
		extractor, _ := NewBriefExtractor(llm)
		source := briefFixture()
		source.Body = "View history<br/>Notification preferences<br />Support contact"
		report, err := extractor.Extract(context.Background(), source)
		if err != nil || report.Text != text || report.Stage != "complete" {
			t.Fatalf("plain text should require neither reason token nor SVO fields: %v", err)
		}
		assertBriefRoundTrip(t, report)
	}
}

func TestBriefPromptDoesNotImposeRegionalVocabulary(t *testing.T) {
	if BriefPromptVersion != "detective-brief-prompt/v2" {
		t.Fatal("neutral wording must have its own prompt version")
	}
	for _, constraint := range []string{"台灣", "繁體", "簡體", "用語", "用詞"} {
		if strings.Contains(BriefInstruction, constraint) {
			t.Fatalf("brief prompt imposes vocabulary constraint: %s", constraint)
		}
	}
	for _, text := range []string{"The team reverted a configuration change.", "團隊已回滾配置變更。", "团队已回滚配置变更。"} {
		llm := &briefModel{responses: []*model.LLMResponse{briefResponse(text)}}
		extractor, _ := NewBriefExtractor(llm)
		report, err := extractor.Extract(t.Context(), briefFixture())
		if err != nil || report.Text != text || report.RawText != text || report.PromptVersion != BriefPromptVersion {
			t.Fatalf("brief rewrote or rejected source wording: %v", err)
		}
		assertBriefRoundTrip(t, report)
	}
}

func TestBriefReadPreservesKnownHistoricalPromptVersions(t *testing.T) {
	llm := &briefModel{responses: []*model.LLMResponse{briefResponse("Atlas API reported increased errors.")}}
	extractor, _ := NewBriefExtractor(llm)
	complete, err := extractor.Extract(t.Context(), briefFixture())
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := NewBriefReport(briefFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"detective-brief-prompt/v1", BriefPromptVersion} {
		for _, report := range []BriefReport{inspected, complete} {
			report.PromptVersion = version
			assertBriefRoundTrip(t, report)
			report.AuthorityEffect = "admit"
			raw, _ := json.Marshal(report)
			if _, err := ParseBriefReport(raw); err == nil {
				t.Fatal("historical prompt bypassed authority validation")
			}
		}
	}
}

func TestBriefRequiresExplicitNewsOrPublicEventBeforeModel(t *testing.T) {
	for _, kind := range []string{"news", "public_event"} {
		t.Run(kind, func(t *testing.T) {
			source := briefFixture()
			source.SourceKind = kind
			raw, _ := json.Marshal(source)
			if got, err := ParseBriefSource(raw); err != nil || !reflect.DeepEqual(got, source) {
				t.Fatalf("explicit source selection rejected: %v", err)
			}
			llm := &briefModel{responses: []*model.LLMResponse{briefResponse("A public event was reported.")}}
			extractor, _ := NewBriefExtractor(llm)
			report, err := extractor.Extract(t.Context(), source)
			if err != nil || len(llm.requests) != 1 || report.Source.SourceKind != kind {
				t.Fatalf("selected source was changed or not extracted once: %v", err)
			}
			assertBriefRoundTrip(t, report)
		})
	}
	for _, kind := range []string{"", "unknown", "engineering", "engineering_evidence", "jira", "confluence", "code", "git", "manual_text", "news_event", "News", " news"} {
		t.Run("reject_"+kind, func(t *testing.T) {
			source := briefFixture()
			source.SourceKind = kind
			assertBriefSourceCannotExecute(t, source)
		})
	}
	for _, kind := range []string{"", "news", "public_event"} {
		source := briefFixture()
		source.Version, source.SourceKind = "detective-brief-source/v1", kind
		assertBriefSourceCannotExecute(t, source)
	}
}

func assertBriefSourceCannotExecute(t *testing.T, source BriefSource) {
	t.Helper()
	if err := ValidateBriefSourceForExtraction(source); err == nil {
		t.Fatal("ineligible source passed extraction preflight")
	}
	raw, _ := json.Marshal(source)
	if _, err := ParseBriefSource(raw); err == nil {
		t.Fatal("ineligible source accepted for new import")
	}
	if _, err := NewBriefReport(source); err == nil {
		t.Fatal("ineligible source accepted for a new report")
	}
	llm := &briefModel{}
	extractor, _ := NewBriefExtractor(llm)
	if _, err := extractor.Extract(t.Context(), source); err == nil || len(llm.requests) != 0 {
		t.Fatal("ineligible source reached the model")
	}
}

func TestBriefHistoricalV1SourceReadPreservesBytesWithoutExecution(t *testing.T) {
	for _, prompt := range []string{"detective-brief-prompt/v1", BriefPromptVersion} {
		for _, stage := range []string{"inspected", "model_requested", "complete"} {
			report, err := NewBriefReport(briefFixture())
			if err != nil {
				t.Fatal(err)
			}
			report.PromptVersion, report.Stage = prompt, stage
			if stage != "inspected" {
				report.RawText, report.Text = "Historical reading.", "Historical reading."
			}
			raw, _ := json.Marshal(report)
			// Recreate the v1 wire format: the source kind did not exist. These
			// bytes must survive readback without reclassifying the old source.
			raw = bytes.Replace(raw, []byte(BriefSourceVersion), []byte("detective-brief-source/v1"), 1)
			raw = bytes.Replace(raw, []byte(`,"source_kind":"news"`), nil, 1)
			got, err := ParseBriefReport(raw)
			if err != nil || got.Source.Version != "detective-brief-source/v1" || got.Source.SourceKind != "" {
				t.Fatalf("historical source rejected or relabeled: %v", err)
			}
			preserved, _ := json.Marshal(got)
			if !bytes.Equal(preserved, raw) {
				t.Fatal("historical source/report bytes or identity changed")
			}
			var out bytes.Buffer
			if err := WriteBriefText(&out, got); err != nil || !strings.Contains(out.String(), "未宣告（歷史 v1") || !strings.Contains(out.String(), got.Source.Body) {
				t.Fatalf("historical source context lost during display: %v", err)
			}
			assertBriefSourceCannotExecute(t, got.Source)
		}
	}
}

func TestBriefSourceKindDoesNotLoosenRecordedJSONShape(t *testing.T) {
	report, err := NewBriefReport(briefFixture())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	for _, bad := range []string{
		strings.Replace(string(raw), `,"source_kind":"news"`, "", 1),
		strings.Replace(string(raw), `"source_kind":"news"`, `"source_kind":null`, 1),
		strings.Replace(string(raw), `"source_kind":"news"`, `"source_kind":""`, 1),
		strings.Replace(string(raw), `"source_kind":"news"`, `"source_kind":"unknown"`, 1),
		strings.Replace(string(raw), `"source_kind":"news"`, `"source_kind":"engineering"`, 1),
		strings.Replace(string(raw), `"source_kind":"news"`, `"source_kind":"news","source_kind":"public_event"`, 1),
		strings.Replace(string(raw), `"source_kind":"news"`, `"SourceKind":"news"`, 1),
		strings.Replace(string(raw), `"source_kind":"news"`, `"source_kind":"news","extra":"unknown"`, 1),
		strings.Replace(string(raw), `,"source_revision":"fixture-v1"`, "", 1),
		strings.Replace(string(raw), `,"source_revision":"fixture-v1"`, `,"extra":"unknown"`, 1),
		strings.Replace(string(raw), BriefSourceVersion, "detective-brief-source/v1", 1),
	} {
		if _, err := ParseBriefReport([]byte(bad)); err == nil {
			t.Fatal("optional source kind loosened source/report shape checks")
		}
	}
}

func TestBriefSourceBoundaryAndStrictJSON(t *testing.T) {
	source := briefFixture()
	raw, _ := json.Marshal(source)
	got, err := ParseBriefSource(raw)
	if err != nil || !reflect.DeepEqual(got, source) {
		t.Fatalf("source round trip: %v", err)
	}
	for name, mutate := range map[string]func(*BriefSource){
		"version":        func(s *BriefSource) { s.Version = GuidedSourceVersion },
		"id":             func(s *BriefSource) { s.SourceID = "" },
		"revision":       func(s *BriefSource) { s.SourceRevision = "\u202efalse" },
		"url auth":       func(s *BriefSource) { s.SourceURL = "https://a:b@example.invalid" },
		"url query":      func(s *BriefSource) { s.SourceURL += "?token=private" },
		"url fragment":   func(s *BriefSource) { s.SourceURL += "#fragment" },
		"url scheme":     func(s *BriefSource) { s.SourceURL = "http://example.invalid" },
		"time":           func(s *BriefSource) { s.ObservedAt = "unknown" },
		"coverage":       func(s *BriefSource) { s.Coverage = "unknown" },
		"missing limits": func(s *BriefSource) { s.Coverage = "exact_excerpt" },
		"extra limits":   func(s *BriefSource) { s.Limitations = []string{"partial"} },
		"null limits":    func(s *BriefSource) { s.Limitations = nil },
		"control":        func(s *BriefSource) { s.Body = "bad\x1b[2J" },
		"empty":          func(s *BriefSource) { s.Body = " \n " },
		"empty segments": func(s *BriefSource) { s.Body = "<br/><br>" },
		"body size":      func(s *BriefSource) { s.Body = strings.Repeat("x", 32<<10+1) },
		"segment count":  func(s *BriefSource) { s.Body = strings.Repeat("line\n", 65) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := source
			mutate(&bad)
			llm := &briefModel{}
			extractor, _ := NewBriefExtractor(llm)
			if _, err := extractor.Extract(context.Background(), bad); err == nil || len(llm.requests) != 0 {
				t.Fatal("invalid source reached model")
			}
		})
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"source_id":`, `"source_id":"duplicate","source_id":`, 1),
		strings.Replace(string(raw), `"limitations":[]`, `"limitations":null`, 1),
		strings.Replace(string(raw), `"limitations":[]`, `"limitations":[null]`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"summary":"external answer"}`,
		strings.Replace(string(raw), `"version":`, `"Version":`, 1),
		string(raw) + `{}`,
	} {
		if _, err := ParseBriefSource([]byte(bad)); err == nil {
			t.Fatal("invalid source JSON accepted")
		}
	}
	source.Body = strings.Repeat("x", 32<<10)
	if _, err := NewBriefReport(source); err != nil {
		t.Fatalf("full 32 KiB body should be accepted: %v", err)
	}
	source.Body = strings.Repeat("line\n", 64)
	source.Coverage, source.Limitations = "exact_excerpt", []string{"Only the incident excerpt is supplied."}
	if _, err := NewBriefReport(source); err != nil {
		t.Fatalf("declared excerpt or 64-segment bound rejected: %v", err)
	}
}

func TestBriefFailuresDoNotRetryOrKeepUnsupportedContent(t *testing.T) {
	for name, mutate := range map[string]func(*model.LLMResponse){
		"partial":       func(r *model.LLMResponse) { r.Partial = true },
		"interrupted":   func(r *model.LLMResponse) { r.Interrupted = true },
		"error code":    func(r *model.LLMResponse) { r.ErrorCode = "failed" },
		"error message": func(r *model.LLMResponse) { r.ErrorMessage = "failed" },
		"token limit":   func(r *model.LLMResponse) { r.FinishReason = genai.FinishReasonMaxTokens },
		"no content":    func(r *model.LLMResponse) { r.Content = nil },
		"wrong role":    func(r *model.LLMResponse) { r.Content.Role = genai.RoleUser },
		"nil part":      func(r *model.LLMResponse) { r.Content.Parts = append(r.Content.Parts, nil) },
		"thought":       func(r *model.LLMResponse) { r.Content.Parts[0].Thought = true },
		"tool":          func(r *model.LLMResponse) { r.Content.Parts[0].FunctionCall = &genai.FunctionCall{Name: "forbidden"} },
		"image":         func(r *model.LLMResponse) { r.Content.Parts[0].InlineData = &genai.Blob{MIMEType: "image/png"} },
		"signature":     func(r *model.LLMResponse) { r.Content.Parts[0].ThoughtSignature = []byte("private") },
		"metadata":      func(r *model.LLMResponse) { r.Content.Parts[0].PartMetadata = map[string]any{"hidden": true} },
		"grounding":     func(r *model.LLMResponse) { r.GroundingMetadata = &genai.GroundingMetadata{} },
		"citation":      func(r *model.LLMResponse) { r.CitationMetadata = &genai.CitationMetadata{} },
		"transcription": func(r *model.LLMResponse) { r.OutputTranscription = &genai.Transcription{Text: "audio"} },
		"custom key":    func(r *model.LLMResponse) { r.CustomMetadata = map[string]any{"admit": true} },
		"custom type":   func(r *model.LLMResponse) { r.CustomMetadata = map[string]any{"openai_model": true} },
		"raw overflow":  func(r *model.LLMResponse) { r.Content.Parts[0].Text = strings.Repeat("x", 32<<10+1) },
		"invalid UTF8":  func(r *model.LLMResponse) { r.Content.Parts[0].Text = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			response := briefResponse("private or unsupported content")
			mutate(response)
			llm := &briefModel{responses: []*model.LLMResponse{response}}
			extractor, _ := NewBriefExtractor(llm)
			report, err := extractor.Extract(context.Background(), briefFixture())
			if err == nil || len(llm.requests) != 1 || report.Stage != "model_requested" || report.RawText != "" || report.Text != "" {
				t.Fatalf("unsupported response retained or retried: %v", err)
			}
			assertBriefRoundTrip(t, report)
		})
	}
	transportErr := errors.New("transport failure")
	for _, llm := range []*briefModel{
		{err: transportErr}, {}, {responses: []*model.LLMResponse{nil}},
		{responses: []*model.LLMResponse{briefResponse("first"), briefResponse("second")}},
	} {
		extractor, _ := NewBriefExtractor(llm)
		report, err := extractor.Extract(context.Background(), briefFixture())
		if err == nil || len(llm.requests) != 1 || report.RawText != "" || report.Text != "" || report.Stage != "model_requested" || (llm.err != nil && !errors.Is(err, transportErr)) {
			t.Fatal("invalid stream accepted, retried or original error lost")
		}
		assertBriefRoundTrip(t, report)
	}
}

func TestBriefAllowsOnlyKnownAdapterStringMetadata(t *testing.T) {
	response := briefResponse("Atlas API is degraded.")
	response.CustomMetadata = map[string]any{"openai_response_id": "response-test", "openai_model": "model-test"}
	llm := &briefModel{responses: []*model.LLMResponse{response}}
	extractor, _ := NewBriefExtractor(llm)
	report, err := extractor.Extract(context.Background(), briefFixture())
	if err != nil || report.Text != "Atlas API is degraded." {
		t.Fatalf("known adapter metadata rejected: %v", err)
	}
	raw, _ := json.Marshal(report)
	if bytes.Contains(raw, []byte("response-test")) || bytes.Contains(raw, []byte("model-test")) {
		t.Fatal("transport metadata was promoted into source or output")
	}
}

func TestBriefInvalidTextRemainsDiagnosticAndCancellationDoesNotComplete(t *testing.T) {
	for _, text := range []string{"", " \n\t", "bad\x1b[2J", "bad\u202etext", strings.Repeat("x", 1537), strings.Repeat("界", 1537), strings.Repeat("x", 32<<10)} {
		llm := &briefModel{responses: []*model.LLMResponse{briefResponse(text)}}
		extractor, _ := NewBriefExtractor(llm)
		report, err := extractor.Extract(context.Background(), briefFixture())
		if err == nil || report.RawText != text || report.Text != "" || report.Stage != "model_requested" || len(llm.requests) != 1 {
			t.Fatal("invalid text promoted, repaired or retried")
		}
		assertBriefRoundTrip(t, report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	llm := &briefModel{responses: []*model.LLMResponse{briefResponse("Atlas API has recovered.")}, after: cancel}
	extractor, _ := NewBriefExtractor(llm)
	report, err := extractor.Extract(ctx, briefFixture())
	if !errors.Is(err, context.Canceled) || report.Stage != "model_requested" || report.Text == "" || report.Text != report.RawText {
		t.Fatal("cancellation after final text lost context or claimed completion")
	}
	assertBriefRoundTrip(t, report)
	report, err = extractor.Extract(ctx, briefFixture())
	if !errors.Is(err, context.Canceled) || report.Stage != "inspected" || len(llm.requests) != 1 {
		t.Fatal("cancelled context reached model")
	}
	assertBriefRoundTrip(t, report)
	if _, err := NewBriefExtractor(nil); err == nil {
		t.Fatal("nil model accepted")
	}
	if report, err := extractor.Extract(nil, briefFixture()); err == nil || report.Stage != "inspected" {
		t.Fatal("nil context discarded source or reached model")
	}
}

func assertBriefRoundTrip(t *testing.T, report BriefReport) {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseBriefReport(raw)
	if err != nil || !reflect.DeepEqual(got, report) {
		t.Fatalf("brief report did not round trip: %v", err)
	}
}

func TestBriefReadbackRejectsForgedDerivedFieldsAndAuthority(t *testing.T) {
	llm := &briefModel{responses: []*model.LLMResponse{briefResponse("Atlas API requests remain delayed in the north region.")}}
	extractor, _ := NewBriefExtractor(llm)
	report, _ := extractor.Extract(context.Background(), briefFixture())
	raw, _ := json.Marshal(report)
	for name, mutate := range map[string]func(*BriefReport){
		"version":      func(r *BriefReport) { r.Version = "other" },
		"prompt":       func(r *BriefReport) { r.PromptVersion = "other" },
		"source":       func(r *BriefReport) { r.Source.Body += " Extra context." },
		"body digest":  func(r *BriefReport) { r.BodySHA256 = strings.Repeat("0", 64) },
		"input digest": func(r *BriefReport) { r.InputSHA256 = strings.Repeat("0", 64) },
		"text":         func(r *BriefReport) { r.Text = "Another statement." },
		"missing raw":  func(r *BriefReport) { r.RawText = "" },
		"projection":   func(r *BriefReport) { r.Projection.Segments = r.Projection.Segments[:1] },
		"authority":    func(r *BriefReport) { r.AuthorityEffect = "admit" },
		"review":       func(r *BriefReport) { r.HumanReview = "reviewed" },
		"reference":    func(r *BriefReport) { r.ReferenceScope = "claim_support" },
		"stage":        func(r *BriefReport) { r.Stage = "inspected" },
	} {
		t.Run(name, func(t *testing.T) {
			bad, err := ParseBriefReport(raw)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&bad)
			changed, _ := json.Marshal(bad)
			if _, err := ParseBriefReport(changed); err == nil {
				t.Fatal("forged report accepted")
			}
			if err := WriteBriefText(io.Discard, bad); err == nil {
				t.Fatal("forged report displayed as valid")
			}
		})
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"stage":`, `"stage":"complete","stage":`, 1),
		strings.Replace(string(raw), `"raw_text":`, `"RawText":`, 1),
		strings.Replace(string(raw), `"text":"Atlas API requests remain delayed in the north region."`, `"text":null`, 1),
		strings.Replace(string(raw), `"segments":[`, `"segments":[null,`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"admit":true}`,
		string(raw) + `{}`,
	} {
		if _, err := ParseBriefReport([]byte(bad)); err == nil {
			t.Fatal("report shape is not strict")
		}
	}
}

func TestBriefDisplayKeepsSourceAndReadingBoundary(t *testing.T) {
	report, _ := NewBriefReport(briefFixture())
	var out bytes.Buffer
	if err := WriteBriefText(&out, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{report.Source.Body, "原文段 1", "原文段 2", "不是模型選取或逐句支持證明", "不記錄人工核准", "尚無可用的模型短摘要"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("reading view lost %q", want)
		}
	}
	if err := WriteBriefText(guidedShortWriter{}, report); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write accepted")
	}
	report.Stage, report.RawText = "model_requested", "bad\x1b[2J"
	out.Reset()
	if err := WriteBriefText(&out, report); err != nil || strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), `\u001b`) || !strings.Contains(out.String(), "未通過輸出檢查") {
		t.Fatal("diagnostic output executes terminal control or loses failure label")
	}
}
