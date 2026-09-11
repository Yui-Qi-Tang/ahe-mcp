package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsextract"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsfeed"
)

const newsSyntheticXML = `<rss version="2.0"><channel><title>Synthetic, not publisher content</title><item><title>Synthetic board plans a review</title><description>The board plans a review in Sample City next week.</description><link>https://example.invalid/synthetic</link><pubDate>Thu, 10 Sep 2026 01:00:00 GMT</pubDate></item><item><title>Unselected private marker</title><description>Must not be sent to the model.</description><link>https://example.invalid/unselected</link></item></channel></rss>`
const newsSyntheticAnswer = `{"outcome":"extracted","records":[{"statement":"合成來源報導董事會計畫進行審查。","reported_status":"planned","attribution":"The board","event_time":"next week","location":"Sample City","evidence_fields":["title","description"]}],"abstention_reason":"","limitations":["只有合成 RSS 片段。"]}`

type newsTestLLM struct {
	answer  string
	err     error
	calls   int
	request *model.LLMRequest
}

func (m *newsTestLLM) Name() string { return "synthetic-news" }
func (m *newsTestLLM) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.calls++
	m.request = request
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.err != nil {
			yield(nil, m.err)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(m.answer, genai.RoleModel), FinishReason: genai.FinishReasonStop}, nil)
	}
}

func newsTestCapture(t *testing.T) (string, newsfeed.Snapshot, *sourceCLIHTTP) {
	t.Helper()
	return newsTestCaptureXML(t, newsSyntheticXML)
}

func newsTestCaptureXML(t *testing.T, xml string) (string, newsfeed.Snapshot, *sourceCLIHTTP) {
	t.Helper()
	return newsTestCaptureFeed(t, xml, "nasa_news_releases")
}

func newsTestCaptureFeed(t *testing.T, xml, feed string) (string, newsfeed.Snapshot, *sourceCLIHTTP) {
	t.Helper()
	snapshot, err := newsfeed.NewSnapshot(feed, []byte(xml), 2, time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return newsTestCaptureSnapshot(t, snapshot)
}

func newsTestCaptureSnapshot(t *testing.T, snapshot newsfeed.Snapshot) (string, newsfeed.Snapshot, *sourceCLIHTTP) {
	t.Helper()
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f := sourceCLIFixture(t, "")
	result, err := json.Marshal(map[string]any{"content": []any{map[string]string{"type": "text", "text": string(body)}}, "isError": false})
	if err != nil {
		t.Fatal(err)
	}
	f.result = string(result)
	config, args, out := sourceCLIInputs(t, f.config, `{"key":"synthetic-news"}`)
	var input, stdout bytes.Buffer
	stderr := &sourcePromptAnswer{input: &input}
	if err := runSource(sourceCollectArgs(config, args, out), &input, &stdout, stderr); err != nil {
		t.Fatal(err)
	}
	var collected sourceCollection
	if err := json.Unmarshal(stdout.Bytes(), &collected); err != nil {
		t.Fatal(err)
	}
	return collected.Receipt.Path, snapshot, f
}

func newsTestArgs(receipt, item, out string) []string {
	return []string{"extract", "-receipt", receipt, "-item-id", item, "-model", "synthetic-news", "-base-url", "http://127.0.0.1:11434/v1", "-out", out}
}

func TestNewsInspectOfflineThenSingleExtraction(t *testing.T) {
	receipt, snapshot, source := newsTestCapture(t)
	requests := source.requests.Load()
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{"--version"}, {"--help"}, {"inspect", "-receipt", receipt, "-format", "json"}} {
		stdout.Reset()
		if err := runNewsWithContext(context.Background(), args, &stdout, &stderr, nil); err != nil {
			t.Fatal(err)
		}
	}
	var inspection newsInspection
	if err := json.Unmarshal(stdout.Bytes(), &inspection); err != nil || len(inspection.Source.Items) != 2 || inspection.AuthorityEffect != "none" {
		t.Fatal("invalid offline inspection", err)
	}
	llm := &newsTestLLM{answer: newsSyntheticAnswer}
	factoryCalls := 0
	factory := func(context.Context, string, string) (*newsextract.Extractor, error) {
		factoryCalls++
		return newsextract.NewExtractor(llm)
	}
	out := filepath.Join(sourceCLIPrivateDir(t), "new-report.json")
	stdout.Reset()
	if err := runNewsWithContext(context.Background(), newsTestArgs(receipt, snapshot.Items[0].ID, out), &stdout, &stderr, factory); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var report newsReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "structurally_validated" || report.Result == nil || len(report.Result.Records) != 1 || report.AHEAttempted || report.AuthorityEffect != "none" || report.HumanReview != "not_reviewed" || report.WorldMonitorComparison != "not_run" || len(report.Source.Items) != 1 {
		t.Fatalf("wrong report boundaries: %+v", report)
	}
	if report.Result.Records[0].Citations[1].ExactQuote != snapshot.Items[0].Description || report.Source.ReceiptSHA256 == "" || report.Source.RawSHA256 != snapshot.RawSHA256 {
		t.Fatal("lost exact source coordinate")
	}
	if report.SchemaVersion != "detective-news-report/v2" || report.SourceUse != nasaSourceUse() || !strings.Contains(stdout.String(), "Detective AI") || !strings.Contains(stdout.String(), "NASA 未審閱") {
		t.Fatal("missing controller-owned source and AI disclosure")
	}
	if source.requests.Load() != requests || source.calls.Load() != 1 || factoryCalls != 1 || llm.calls != 1 {
		t.Fatal("re-fetched source or repeated model attempt")
	}
	prompt, err := json.Marshal(llm.request)
	if err != nil || strings.Contains(string(prompt), "Unselected private marker") || strings.Contains(string(prompt), "example.invalid") || strings.Contains(string(prompt), "<rss") {
		t.Fatal("model received more than selected RSS projection")
	}
	if !strings.Contains(stdout.String(), "原文 [description]") || stderr.Len() != 0 {
		t.Fatal("missing readable exact citations")
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("report not private")
	}
	if err := runNewsWithContext(context.Background(), newsTestArgs(receipt, snapshot.Items[0].ID, out), io.Discard, io.Discard, factory); err == nil || factoryCalls != 1 {
		t.Fatal("existing output caused a new attempt")
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(body, after) {
		t.Fatal("existing report overwritten")
	}
}

func TestNewsAPIReceiptThenSingleExtractionPreservesRenderedFields(t *testing.T) {
	raw := `[{"id":73,"date_gmt":"2026-09-10T01:00:00","link":"https://example.invalid/synthetic-api","title":{"rendered":"Synthetic board plans a review"},"excerpt":{"rendered":"<p>The board plans a review in Sample City next week. A &amp; B.</p>","protected":false}}]`
	snapshot, err := newsfeed.NewAPISnapshot("nasa_news_releases_api", []byte(raw), 1, time.Date(2026, 9, 10, 1, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	receipt, snapshot, source := newsTestCaptureSnapshot(t, snapshot)
	requests := source.requests.Load()
	var stdout, stderr bytes.Buffer
	if err := runNewsWithContext(context.Background(), []string{"inspect", "-receipt", receipt}, &stdout, &stderr, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "HTML 不執行") || strings.Contains(stdout.String(), "RSS 描述") {
		t.Fatal("API source was mislabeled as RSS")
	}
	llm := &newsTestLLM{answer: newsSyntheticAnswer}
	factoryCalls := 0
	factory := func(context.Context, string, string) (*newsextract.Extractor, error) {
		factoryCalls++
		return newsextract.NewExtractor(llm)
	}
	out := filepath.Join(sourceCLIPrivateDir(t), "api-report.json")
	stdout.Reset()
	if err := runNewsWithContext(context.Background(), newsTestArgs(receipt, snapshot.Items[0].ID, out), &stdout, &stderr, factory); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var report newsReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if report.Source.FeedID != "nasa_news_releases_api" || report.Source.RawSHA256 != snapshot.RawSHA256 || report.Source.FeedURL != snapshot.FeedURL || report.Result == nil || report.Status != "structurally_validated" {
		t.Fatal("lost API source/report coordinates")
	}
	if report.Result.Records[0].Citations[1].ExactQuote != snapshot.Items[0].Description || report.Source.Items[0].PublishedAt != "2026-09-10T01:00:00" {
		t.Fatal("rewrote HTML or API timestamp")
	}
	input := llm.request.Contents[0].Parts[0].Text
	var projected map[string]string
	if json.Unmarshal([]byte(input), &projected) != nil || len(projected) != 3 || projected["description"] != snapshot.Items[0].Description || strings.Contains(input, "example.invalid") || strings.Contains(input, "protected") {
		t.Fatal("model did not receive only the exact selected field projection")
	}
	if source.requests.Load() != requests || source.calls.Load() != 1 || factoryCalls != 1 || llm.calls != 1 || report.AHEAttempted || report.HumanReview != "not_reviewed" {
		t.Fatal("API extraction crossed attempt or authority boundaries")
	}
}

func TestNewsFailedAttemptIsSavedWithoutRawDiagnostics(t *testing.T) {
	receipt, snapshot, _ := newsTestCapture(t)
	for _, kind := range []string{"factory", "nil", "model", "invalid", "usage", "cancel", "abstained", "stdout"} {
		t.Run(kind, func(t *testing.T) {
			llm := &newsTestLLM{answer: newsSyntheticAnswer}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			factoryCalls := 0
			factory := func(context.Context, string, string) (*newsextract.Extractor, error) {
				factoryCalls++
				switch kind {
				case "factory":
					return nil, errors.New("synthetic-private-diagnostic")
				case "nil":
					return nil, nil
				case "model":
					llm.err = errors.New("synthetic-private-diagnostic")
				case "invalid":
					llm.answer = "synthetic-private-diagnostic"
				case "usage":
					llm.answer = strings.Replace(newsSyntheticAnswer, "合成來源報導董事會計畫進行審查。", "According to NASA, the board plans a review.", 1)
				case "cancel":
					cancel()
				case "abstained":
					llm.answer = `{"outcome":"abstained","records":[],"abstention_reason":"合成片段不足。","limitations":[]}`
				}
				return newsextract.NewExtractor(llm)
			}
			out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			var stdout bytes.Buffer
			var writer io.Writer = &stdout
			if kind == "stdout" {
				writer = newsShortWriter{}
			}
			err := runNewsWithContext(ctx, newsTestArgs(receipt, snapshot.Items[0].ID, out), writer, io.Discard, factory)
			if (kind == "abstained") != (err == nil) {
				t.Fatalf("unexpected attempt result: %v", err)
			}
			body, readErr := os.ReadFile(out)
			var report newsReport
			if readErr != nil || json.Unmarshal(body, &report) != nil {
				t.Fatal("failed attempt was not saved")
			}
			if strings.Contains(string(body)+stdout.String()+fmtError(err), "synthetic-private-diagnostic") || factoryCalls != 1 || llm.calls > 1 {
				t.Fatal("raw diagnostic escaped or attempt repeated")
			}
			if kind == "abstained" {
				if report.Result == nil || report.Result.Outcome != "abstained" || len(report.Result.Records) != 0 {
					t.Fatal("abstention became a candidate")
				}
			} else if kind == "stdout" {
				if report.Status != "structurally_validated" || !strings.Contains(err.Error(), "saved but terminal") {
					t.Fatal("terminal failure hid saved report")
				}
			} else if report.Status != "failed" || report.Result != nil || report.ErrorCode == "" {
				t.Fatal("failed attempt called a success")
			}
			if kind == "usage" && (report.ErrorCode != "source_usage_wording" || strings.Contains(string(body)+stdout.String(), "According to NASA")) {
				t.Fatal("rejected wording escaped as a published model result")
			}
		})
	}
}

func TestNewsAbstentionDisplayDoesNotImplyCandidateSuccess(t *testing.T) {
	report := newsReport{Status: "structurally_validated", SourceUse: nasaSourceUse(),
		Result: &newsextract.Result{Outcome: "abstained", Records: []newsextract.Record{}, AbstentionReason: "只有導覽文字。"}}
	var output bytes.Buffer
	if err := writeNewsReport(&output, report, "/synthetic/not-written.json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "模型選擇不擷取，沒有候選；不是擷取成功") || !strings.Contains(output.String(), "處理狀態（非品質評分）：structurally_validated") || strings.Contains(output.String(), "AI 候選 1") {
		t.Fatal("abstention was displayed as a successful candidate extraction")
	}
}

func TestNewsBBCReceiptRemainsInspectableButCannotExtract(t *testing.T) {
	receipt, snapshot, source := newsTestCaptureFeed(t, newsSyntheticXML, "bbc_world")
	requests := source.requests.Load()
	if err := runNewsWithContext(context.Background(), []string{"inspect", "-receipt", receipt}, io.Discard, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
	factory := func(context.Context, string, string) (*newsextract.Extractor, error) {
		t.Fatal("disabled feed reached model inventory")
		return nil, nil
	}
	err := runNewsWithContext(context.Background(), newsTestArgs(receipt, snapshot.Items[0].ID, out), io.Discard, io.Discard, factory)
	if err == nil || !strings.Contains(err.Error(), "enabled only") {
		t.Fatal("disabled feed extracted")
	}
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) || source.requests.Load() != requests {
		t.Fatal("disabled feed wrote or fetched")
	}
}

func fmtError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type newsShortWriter struct{}

func (newsShortWriter) Write(body []byte) (int, error) { return len(body) / 2, nil }

func TestNewsPreflightRejectsBeforeModelAndOutput(t *testing.T) {
	receipt, snapshot, source := newsTestCapture(t)
	requests := source.requests.Load()
	factory := func(context.Context, string, string) (*newsextract.Extractor, error) {
		t.Fatal("preflight contacted model")
		return nil, nil
	}
	for _, kind := range []string{"item", "endpoint", "model", "duplicate", "timeout", "source"} {
		t.Run(kind, func(t *testing.T) {
			out := filepath.Join(sourceCLIPrivateDir(t), "new.json")
			args := newsTestArgs(receipt, snapshot.Items[0].ID, out)
			switch kind {
			case "item":
				args[4] = "invented"
			case "endpoint":
				args[8] = "http://secret:credential@example.com/v1"
			case "model":
				args[6] = "unsafe\u202e"
			case "duplicate":
				args = append(args, "-model", "other")
			case "timeout":
				args = append(args, "-timeout", "3m")
			case "source":
				args[2] = receipt + ".missing"
			}
			var stdout bytes.Buffer
			if err := runNewsWithContext(context.Background(), args, &stdout, io.Discard, factory); err == nil || strings.Contains(err.Error(), "credential") {
				t.Fatal("unsafe input accepted or leaked")
			}
			if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) || source.requests.Load() != requests {
				t.Fatal("preflight wrote or fetched")
			}
		})
	}
}

func TestNewsOutputRejectsUnsafeOrChangedPaths(t *testing.T) {
	dir := sourceCLIPrivateDir(t)
	unsafeDir := filepath.Join(dir, "public")
	if err := os.Mkdir(unsafeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := reserveNewsOutput(filepath.Join(unsafeDir, "report.json")); err == nil {
		output.close()
		t.Fatal("nonprivate parent accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if output, err := reserveNewsOutput(filepath.Join(link, "report.json")); err == nil {
		output.close()
		t.Fatal("symlink parent accepted")
	}
	path := filepath.Join(dir, "report.json")
	output, err := reserveNewsOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer output.close()
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("preserve replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := output.write(newsReport{}); err == nil {
		t.Fatal("replacement accepted")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "preserve replacement" {
		t.Fatal("replacement overwritten")
	}
	body, _ = os.ReadFile(path + ".original")
	if len(body) != 0 {
		t.Fatal("detached report written")
	}
}

func TestNewsItemLimitsBeforeInventoryOrReservation(t *testing.T) {
	for _, replacement := range []struct{ old, new string }{
		{"Synthetic board plans a review", strings.Repeat("x", 17<<10)},
		{"Thu, 10 Sep 2026 01:00:00 GMT", strings.Repeat("x", 257)},
	} {
		receipt, snapshot, source := newsTestCaptureXML(t, strings.Replace(newsSyntheticXML, replacement.old, replacement.new, 1))
		requests := source.requests.Load()
		out := filepath.Join(sourceCLIPrivateDir(t), "new.json")
		factory := func(context.Context, string, string) (*newsextract.Extractor, error) {
			t.Fatal("oversized item contacted inventory")
			return nil, nil
		}
		err := runNewsWithContext(context.Background(), newsTestArgs(receipt, snapshot.Items[0].ID, out), io.Discard, io.Discard, factory)
		if err == nil || !strings.Contains(err.Error(), "exceeds extraction limits") {
			t.Fatal("item bound was not checked in preflight")
		}
		if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) || source.requests.Load() != requests {
			t.Fatal("rejected item wrote or fetched")
		}
	}
}

func TestNewsEndpointAndDisplayBounds(t *testing.T) {
	for _, endpoint := range []string{"http://localhost:11434/v1", "http://127.0.0.1/v1/", "http://[::1]:11434/v1"} {
		if !newsEndpointSafe(endpoint) {
			t.Fatal("safe endpoint rejected")
		}
	}
	for _, endpoint := range []string{"https://127.0.0.1/v1", "http://127.0.0.1/v1?", "http://127.0.0.1/v1?a=b", "http://127.0.0.1/v1#x", "http://127.0.0.1:65536/v1", "http://example.com/v1", "http://127.0.0.1/%761"} {
		if newsEndpointSafe(endpoint) {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	if strings.ContainsAny(newsQuoted("\u202e\u009b\x1b"), "\u202e\u009b\x1b") {
		t.Fatal("terminal controls escaped quoting")
	}
	if !errors.Is(writeNewsInspection(newsShortWriter{}, newsInspection{}), io.ErrShortWrite) {
		t.Fatal("short terminal write accepted")
	}
}

func TestNewsOutputDoesNotOverwriteModifiedReservation(t *testing.T) {
	path := filepath.Join(sourceCLIPrivateDir(t), "report.json")
	output, err := reserveNewsOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer output.close()
	if err := os.WriteFile(path, []byte("preserve unexpected edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := output.write(newsReport{}); err == nil {
		t.Fatal("modified reservation accepted")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "preserve unexpected edit" {
		t.Fatal("modified reservation overwritten")
	}
}
