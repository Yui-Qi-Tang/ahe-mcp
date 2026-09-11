package sourcepilot

import (
	"bytes"
	"context"
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

func guidedFixture() GuidedSource {
	return GuidedSource{Version: GuidedSourceVersion, SourceID: "synthetic-payments", SourceRevision: "fixture-v1",
		SourceURL: "https://example.invalid/incident", ObservedAt: "2026-09-10T09:00:00Z", SummaryOrigin: "synthetic-summary-not-publisher",
		Coverage: "full_document", Limitations: []string{},
		Summary: "Payment API and Refund API have both recovered.",
		Body:    "Payment API has recovered at 14:20 UTC.<br />Refund API still has delayed requests.\nThe cause remains unconfirmed."}
}

const guidedClaimJSON = `{"claims":[{"text":"Payment API 已恢復。","subject":"Payment API","predicate":"已恢復","object":""},{"text":"Refund API 已恢復。","subject":"Refund API","predicate":"已恢復","object":""}],"reason":""}`
const guidedCheckJSON = `{"assessments":[{"claim":2,"relation":"contradicted","segments":[2],"explanation":"原文仍指出 Refund API 有延遲。"},{"claim":1,"relation":"supported","segments":[1],"explanation":"原文明確記載 Payment API 已恢復。"}]}`
const guidedBodyJSON = `{"observations":[{"statement":{"text":"事故原因仍未確認。","subject":"事故原因","predicate":"仍未確認","object":""},"segments":[3]}],"reason":""}`

type guidedModel struct {
	responses []string
	requests  []*model.LLMRequest
	badKind   bool
	multiple  bool
	onCall    func(int)
}

func (m *guidedModel) Name() string { return "guided-test-only" }
func (m *guidedModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.requests = append(m.requests, req)
		index := len(m.requests) - 1
		if m.onCall != nil {
			m.onCall(index)
		}
		if stream || index >= len(m.responses) {
			yield(nil, errors.New("test model unavailable"))
			return
		}
		content := genai.NewContentFromText(m.responses[index], genai.RoleModel)
		if m.badKind {
			content.Parts = append(content.Parts, &genai.Part{Thought: true, Text: "private-reasoning-marker"})
		}
		response := &model.LLMResponse{Content: content, FinishReason: genai.FinishReasonStop}
		if yield(response, nil) && m.multiple {
			yield(response, nil)
		}
	}
}

func TestGuidedReviewKeepsReadableContextAndIndependentCalls(t *testing.T) {
	llm := &guidedModel{responses: []string{guidedClaimJSON, guidedCheckJSON, guidedBodyJSON}}
	reviewer, err := NewGuidedReviewer(llm)
	if err != nil {
		t.Fatal(err)
	}
	source := guidedFixture()
	report, err := reviewer.Review(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stage != "complete" || len(llm.requests) != 3 || len(report.Claims) != 2 || len(report.Assessments) != 2 || len(report.Observations) != 1 {
		t.Fatalf("incomplete report: %+v", report)
	}
	if report.HumanReview != "not_reviewed" || report.Readability != "requires_human_review" || report.AuthorityEffect != "none" || !reflect.DeepEqual(source, report.Source) {
		t.Fatal("a model result changed source or authority")
	}
	for i, req := range llm.requests {
		if len(req.Contents) != 1 || req.Config == nil || len(req.Config.Tools) != 0 || req.Config.MaxOutputTokens != 1536 || req.Config.Temperature == nil || *req.Config.Temperature != 0 {
			t.Fatal("request acquired history, tools or unfrozen parameters")
		}
		var input map[string]json.RawMessage
		raw := req.Contents[0].Parts[0].Text
		if json.Unmarshal([]byte(raw), &input) != nil {
			t.Fatal("non-JSON input")
		}
		for _, marker := range []string{source.SourceID, source.SourceRevision, source.SourceURL, source.SummaryOrigin, "source_id", "start_byte", "body_sha256"} {
			if strings.Contains(raw, marker) {
				t.Fatalf("metadata leaked into stage %d", i)
			}
		}
		if i == 0 {
			if len(input) != 1 || input["external_summary"] == nil || strings.Contains(raw, "unconfirmed") {
				t.Fatal("decomposition used the answer body")
			}
			continue
		}
		var parts []Line
		if json.Unmarshal(input["source_segments"], &parts) != nil || len(parts) != 3 || parts[1].Text != "Refund API still has delayed requests." || parts[2].Text != "The cause remains unconfirmed." {
			t.Fatal("source check removed context or limitations")
		}
		if i == 2 && (len(input) != 1 || strings.Contains(raw, "claims") || strings.Contains(raw, "contradicted") || strings.Contains(raw, "both recovered")) {
			t.Fatal("body-only reading was anchored to summary or previous verdict")
		}
	}
	if report.Assessments[0].Claim != 1 || report.Assessments[1].Claim != 2 {
		t.Fatal("controller did not preserve fixed claim ordering")
	}
	for _, assessment := range report.Assessments {
		for _, quote := range assessment.Citations {
			if quote.Text != source.Body[quote.StartByte:quote.EndByte] {
				t.Fatal("citation does not match unchanged body")
			}
		}
	}
	var display bytes.Buffer
	if err := WriteGuidedText(&display, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{source.Body, "Refund API", "可讀性與語意都仍待人審", "不是 admit/reject/audit_only"} {
		if !strings.Contains(display.String(), want) {
			t.Fatalf("reading view lost %q", want)
		}
	}
}

func TestGuidedInputsAndReadingAidsFailClosed(t *testing.T) {
	source := guidedFixture()
	raw, _ := json.Marshal(source)
	if parsed, err := ParseGuidedSource(raw); err != nil || !reflect.DeepEqual(parsed, source) {
		t.Fatal("source did not round trip")
	}
	for _, mutate := range []func(*GuidedSource){
		func(s *GuidedSource) { s.Body = "" },
		func(s *GuidedSource) { s.Body = strings.Repeat("x", (32<<10)+1) },
		func(s *GuidedSource) { s.Body = "\x1b[2Jsource" },
		func(s *GuidedSource) { s.Summary = "bad\u202etext" },
		func(s *GuidedSource) { s.SourceRevision = "" },
		func(s *GuidedSource) { s.ObservedAt = "not-a-time" },
		func(s *GuidedSource) { s.SourceURL = "https://user:pass@example.invalid" },
		func(s *GuidedSource) { s.SourceURL += "?token=private" },
		func(s *GuidedSource) { s.Coverage = "unknown" },
		func(s *GuidedSource) { s.Coverage = "exact_excerpt"; s.Limitations = []string{} },
		func(s *GuidedSource) { s.Limitations = []string{"incomplete"} },
		func(s *GuidedSource) { s.Limitations = nil },
	} {
		copy := source
		mutate(&copy)
		llm := &guidedModel{}
		reviewer, _ := NewGuidedReviewer(llm)
		if _, err := reviewer.Review(context.Background(), copy); err == nil || len(llm.requests) != 0 {
			t.Fatal("invalid source reached a model")
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"source_id":`, `"source_id":"duplicate","source_id":`, 1),
		strings.Replace(string(raw), `"limitations":[]`, `"limitations":null`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"admit":true}`,
		string(raw) + `{}`,
	} {
		if _, err := ParseGuidedSource([]byte(bad)); err == nil {
			t.Fatal("open source JSON shape accepted")
		}
	}
	for _, raw := range []string{
		`{"claims":[{"text":"API recovered.","subject":"API","predicate":"recovered"}],"reason":""}`,
		`{"claims":[{"text":"API recovered.","subject":"API","predicate":"recovered","object":null}],"reason":""}`,
		`{"claims":[{"text":"API recovered.","subject":"API","predicate":"failed","object":""}],"reason":""}`,
		`{"claims":[{"text":"API ... recovered.","subject":"API","predicate":"recovered","object":""}],"reason":""}`,
		`{"claims":[{"text":"API recovered","subject":"API","predicate":"recovered","object":""}],"reason":""}`,
		`{"claims":[{"text":"API recovered?","subject":"API","predicate":"recovered","object":""}],"reason":""}`,
		`{"claims":null,"reason":"No facts."}`,
		`{"claims":[],"reason":""}`,
		strings.Replace(guidedClaimJSON, `"reason":""`, `"reason":"No facts."`, 1),
		strings.Replace(guidedClaimJSON, `"subject":"Payment API"`, `"subject":"other actor"`, 1),
	} {
		report, _ := NewGuidedReport(source)
		if guidedApply(&report, "summary_claims", raw) == nil || len(report.Claims) != 0 {
			t.Fatalf("malformed reading aid accepted: %s", raw)
		}
	}
}

type guidedShortWriter struct{}

func (guidedShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestGuidedAbstentionIsVisibleAndCharacterLimitsMatchSchema(t *testing.T) {
	llm := &guidedModel{responses: []string{`{"claims":[],"reason":"摘要只有導覽文字。"}`, `{"observations":[],"reason":"原文只有通知設定。"}`}}
	reviewer, _ := NewGuidedReviewer(llm)
	report, err := reviewer.Review(context.Background(), guidedFixture())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if WriteGuidedText(&out, report) != nil || !strings.Contains(out.String(), "摘要只有導覽文字。") || !strings.Contains(out.String(), "原文只有通知設定。") {
		t.Fatal("abstention reasons are hidden")
	}
	if !errors.Is(WriteGuidedText(guidedShortWriter{}, report), io.ErrShortWrite) {
		t.Fatal("short write treated as complete")
	}
	if guidedSentence(ReadableStatement{Text: "Alice " + strings.Repeat("x", 600) + " works.", Subject: "Alice", Predicate: "works"}) || guidedReason(0, strings.Repeat("x", 513)) {
		t.Fatal("local parser exceeded schema character bound")
	}
}

func TestGuidedAssessmentsNeedEveryFixedClaimAndExactContext(t *testing.T) {
	for _, raw := range []string{
		`{"assessments":[]}`,
		strings.Replace(guidedCheckJSON, `"claim":2`, `"claim":1`, 1),
		strings.Replace(guidedCheckJSON, `"segments":[2]`, `"segments":[99]`, 1),
		strings.Replace(guidedCheckJSON, `"segments":[2]`, `"segments":[2,2]`, 1),
		strings.Replace(guidedCheckJSON, `"segments":[2]`, `"segments":[]`, 1),
		strings.Replace(guidedCheckJSON, `"segments":[2]`, `"segments":null`, 1),
		strings.Replace(guidedCheckJSON, `"relation":"supported"`, `"relation":"admit"`, 1),
		strings.Replace(guidedCheckJSON, `"claim":2`, `"claim":2,"confidence":1`, 1),
		strings.Replace(guidedCheckJSON, `"claim":2`, `"claim":2,"citations":[]`, 1),
	} {
		report, _ := NewGuidedReport(guidedFixture())
		if err := guidedApply(&report, "summary_claims", guidedClaimJSON); err != nil {
			t.Fatal(err)
		}
		if guidedApply(&report, "source_check", raw) == nil || len(report.Assessments) != 0 {
			t.Fatal("invalid or partially parsed assessment accepted")
		}
	}
	report, _ := NewGuidedReport(guidedFixture())
	_ = guidedApply(&report, "summary_claims", guidedClaimJSON)
	insufficient := strings.ReplaceAll(strings.ReplaceAll(guidedCheckJSON, "supported", "insufficient"), "contradicted", "insufficient")
	insufficient = strings.ReplaceAll(strings.ReplaceAll(insufficient, `"segments":[1]`, `"segments":[]`), `"segments":[2]`, `"segments":[]`)
	if err := guidedApply(&report, "source_check", insufficient); err != nil {
		t.Fatal("unknown is not false and need not invent a quote", err)
	}
	quotes, ok := guidedRefs([]int{3, 1}, report.Projection, false)
	if !ok || quotes[0].Number != 1 || quotes[1].Number != 3 {
		t.Fatal("citations lost original order")
	}
}

func TestGuidedFailureDoesNotRetryOrClaimHumanAcceptance(t *testing.T) {
	for _, llm := range []*guidedModel{
		{responses: []string{guidedClaimJSON, `{"assessments":[]}`}},
		{responses: []string{guidedClaimJSON}, badKind: true},
		{responses: []string{guidedClaimJSON}, multiple: true},
	} {
		reviewer, _ := NewGuidedReviewer(llm)
		report, err := reviewer.Review(context.Background(), guidedFixture())
		if err == nil || report.Stage == "complete" || len(llm.requests) > 2 || report.HumanReview != "not_reviewed" || report.Source.Body != guidedFixture().Body {
			t.Fatal("failed run lost context or gained authority")
		}
		if len(report.Attempts) > 0 && strings.Contains(report.Attempts[0].RawText, "private-reasoning-marker") {
			t.Fatal("private reasoning entered report")
		}
	}
	llm := &guidedModel{responses: []string{`{"claims":[],"reason":"摘要沒有可核對主張。"}`, guidedBodyJSON}}
	reviewer, _ := NewGuidedReviewer(llm)
	report, err := reviewer.Review(context.Background(), guidedFixture())
	if err != nil || report.Stage != "complete" || len(llm.requests) != 2 || len(report.Observations) != 1 {
		t.Fatal("empty summary must still permit independent source reading", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	llm = &guidedModel{responses: []string{guidedClaimJSON}, onCall: func(int) { cancel() }}
	reviewer, _ = NewGuidedReviewer(llm)
	if _, err := reviewer.Review(ctx, guidedFixture()); !errors.Is(err, context.Canceled) || len(llm.requests) != 1 {
		t.Fatal("cancelled review continued")
	}
}

func TestGuidedReadingAidsDoNotPretendToProveGrammar(t *testing.T) {
	// A lexical schema cannot prove grammar. Even this deliberately bad noun
	// fragment must remain unreviewed; never promote it from string matching.
	fragment := `{"claims":[{"text":"API recovery.","subject":"API","predicate":"recovery","object":""}],"reason":""}`
	llm := &guidedModel{responses: []string{fragment, `{"assessments":[{"claim":1,"relation":"supported","segments":[1],"explanation":"Model claim only."}]}`, guidedBodyJSON}}
	reviewer, _ := NewGuidedReviewer(llm)
	report, err := reviewer.Review(context.Background(), guidedFixture())
	if err != nil || report.Readability != "requires_human_review" || report.HumanReview != "not_reviewed" || report.AuthorityEffect != "none" {
		t.Fatal("lexical aids were treated as grammatical or human proof")
	}
}
