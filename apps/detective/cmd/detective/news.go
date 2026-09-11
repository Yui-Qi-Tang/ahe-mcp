package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsextract"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsfeed"
)

const newsUsage = "detective news inspect -receipt /absolute/private/receipt.md [-item-id ID] [-format text|json]\n" +
	"detective news extract -receipt /absolute/private/receipt.md -item-id ID -model NAME -base-url http://127.0.0.1:11434/v1 -out /absolute/private/new-report.json [-timeout 2m] [-format text|json]\n" +
	"detective news --version\n" +
	"Inspect is offline. Extract uses one explicit frozen news item and one local model attempt.\n" +
	"No source refetch, retries, article crawl, cloud fallback, AHE or DB calls.\n" +
	"Output is an exclusive private file; interrupted/failed writes may leave a partial file. Do not overwrite or treat it as complete.\n" +
	"News candidates are not STATUS records, reviewed decisions or pending proposals.\n"

type newsFactory func(context.Context, string, string) (*newsextract.Extractor, error)

type newsSource struct {
	ReceiptPath    string          `json:"receipt_path"`
	ReceiptSHA256  string          `json:"receipt_sha256"`
	SnapshotSHA256 string          `json:"snapshot_sha256"`
	FeedID         string          `json:"feed_id"`
	FeedURL        string          `json:"feed_url"`
	CapturedAt     string          `json:"captured_at"`
	RawSHA256      string          `json:"raw_sha256"`
	Items          []newsfeed.Item `json:"items"`
}

type newsInspection struct {
	SchemaVersion   string     `json:"schema_version"`
	Source          newsSource `json:"source"`
	AuthorityEffect string     `json:"authority_effect"`
	Limitations     []string   `json:"limitations"`
}

// Source-use disclosures are owned by the controller, never by model JSON.
type newsSourceUse struct {
	PolicyURL         string `json:"policy_url"`
	InputDisclosure   string `json:"input_disclosure"`
	OutputAttribution string `json:"output_attribution"`
	Disclaimer        string `json:"disclaimer"`
	CheckScope        string `json:"check_scope"`
}

func nasaSourceUse() newsSourceUse {
	return newsSourceUse{
		PolicyURL:         "https://www.nasa.gov/nasa-brand-center/images-and-media/",
		InputDisclosure:   "輸入資料含 NASA 官方新聞標題與摘要欄位；來源列示不是 NASA 的使用核准或背書。",
		OutputAttribution: "Detective AI 產生的解讀，不是 NASA 輸出。",
		Disclaimer:        "NASA 未審閱或核准此模型報告，亦不負責模型生成內容的準確性。",
		CheckScope:        "只有有限字面措辭檢查，不是完整法律或語意保證；仍待人工核對。",
	}
}

// This report is local experiment output, deliberately not a pending schema.
type newsReport struct {
	SchemaVersion          string              `json:"schema_version"`
	Source                 newsSource          `json:"source"`
	SourceUse              newsSourceUse       `json:"source_use"`
	Model                  string              `json:"model"`
	BaseURL                string              `json:"base_url"`
	StartedAt              string              `json:"started_at"`
	ElapsedMS              int64               `json:"elapsed_ms"`
	Status                 string              `json:"status"`
	ErrorCode              string              `json:"error_code"`
	Result                 *newsextract.Result `json:"result"`
	AuthorityEffect        string              `json:"authority_effect"`
	AHEAttempted           bool                `json:"ahe_attempted"`
	HumanReview            string              `json:"human_review"`
	WorldMonitorComparison string              `json:"worldmonitor_comparison"`
}

func runNews(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runNewsWithContext(ctx, args, stdout, stderr, desktop.NewNewsExtractor)
}

func runNewsWithContext(ctx context.Context, args []string, stdout, stderr io.Writer, factory newsFactory) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(stdout, newsUsage)
		return err
	}
	if len(args) == 1 && args[0] == "--version" {
		_, err := fmt.Fprintf(stdout, "detective-news-extractor %s (%s)\n", newsextract.ExtractorVersion, newsextract.SchemaVersion)
		return err
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "extract") {
		return errors.New("news requires inspect or extract; use news --help")
	}
	extract := args[0] == "extract"
	flags := flag.NewFlagSet("detective news", flag.ContinueOnError)
	receipt := flags.String("receipt", "", "explicit private saved source receipt")
	itemID := flags.String("item-id", "", "exact snapshot item ID from news inspect")
	format := flags.String("format", "text", "text or json output")
	var modelName, baseURL, out string
	timeout := 2 * time.Minute
	if extract {
		flags.StringVar(&modelName, "model", "", "explicit local model, no environment default")
		flags.StringVar(&baseURL, "base-url", "", "explicit credential-free loopback /v1 endpoint")
		flags.StringVar(&out, "out", "", "new private output file; never overwritten")
		flags.DurationVar(&timeout, "timeout", timeout, "maximum attempt duration, at most two minutes")
	}
	if parseReviewFlags(flags, args[1:]) != nil || (*format != "text" && *format != "json") || ctx == nil || ctx.Err() != nil || timeout <= 0 || timeout > 2*time.Minute {
		return errors.New("news flags require explicit supported values without duplicates")
	}
	if extract && (*itemID == "" || modelName == "" || baseURL == "" || out == "" || factory == nil) {
		return errors.New("news extract requires item-id, model, base-url and a new output path")
	}
	if extract && (!newsEndpointSafe(baseURL) || !newsModelNameSafe(modelName)) {
		return errors.New("news model settings require safe explicit values; no model was called")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	view, err := desktop.InspectSource(ctx, *receipt)
	if err != nil || view.Capture == nil {
		return errors.New("news source receipt could not be verified; no model or source was called")
	}
	snapshot, err := newsfeed.ParseSnapshot(view.RawText)
	if err != nil {
		return errors.New("source is not a valid frozen news feed; no model was called")
	}
	items := snapshot.Items
	if *itemID != "" {
		items = nil
		for _, item := range snapshot.Items {
			if item.ID == *itemID {
				items = []newsfeed.Item{item}
				break
			}
		}
		if len(items) != 1 {
			return errors.New("item-id does not identify this snapshot; no model was called")
		}
	}
	source := newsSource{ReceiptPath: *receipt, ReceiptSHA256: view.Capture.Receipt.SHA256,
		SnapshotSHA256: view.SHA256, FeedID: snapshot.FeedID, FeedURL: snapshot.FeedURL,
		CapturedAt: snapshot.CapturedAt, RawSHA256: snapshot.RawSHA256, Items: items}
	if !extract {
		inspection := newsInspection{SchemaVersion: "detective-news-inspection/v1", Source: source,
			AuthorityEffect: "none", Limitations: []string{"Source title/summary fields only, not article text or independent truth verification.", "Local receipt consistency is not publisher authenticity, currentness, or human approval.", "PublishedAt preserves the original RSS pubDate or NASA API date_gmt field, not an inferred event time. Item IDs belong to this snapshot only."}}
		if *format == "json" {
			return writeSourceJSON(stdout, inspection)
		}
		return writeNewsInspection(stdout, inspection)
	}
	if snapshot.FeedID != "nasa_news_releases" && snapshot.FeedID != "nasa_news_releases_api" {
		return errors.New("news extraction is enabled only for the selected NASA feed; no model was called")
	}
	if err := newsextract.ValidateItem(items[0]); err != nil {
		return errors.New("selected news item exceeds extraction limits; no model was called")
	}
	// Reserve output before model inventory; even failed attempts keep a bounded
	// report. An existing or unsafe path cannot cause an expensive model call.
	output, err := reserveNewsOutput(out)
	if err != nil {
		return errors.New("news output requires a new file in an existing private directory; no model was called")
	}
	defer output.close()
	started := time.Now()
	report := newsReport{SchemaVersion: "detective-news-report/v2", Source: source, SourceUse: nasaSourceUse(),
		Model: modelName, BaseURL: baseURL, StartedAt: started.UTC().Format(time.RFC3339Nano),
		Status: "failed", AuthorityEffect: "none", HumanReview: "not_reviewed", WorldMonitorComparison: "not_run"}
	extractor, attemptErr := factory(ctx, modelName, baseURL)
	if attemptErr != nil {
		report.ErrorCode = "model_unavailable"
	} else if extractor == nil {
		attemptErr = errors.New("model unavailable")
		report.ErrorCode = "model_unavailable"
	} else {
		result, err := extractor.Extract(ctx, items[0])
		attemptErr = err
		if err == nil {
			if usageErr := newsextract.ValidateNASAResult(result); usageErr != nil {
				attemptErr = usageErr
				report.ErrorCode = "source_usage_wording"
			}
		}
		if attemptErr == nil {
			report.Result = &result
			report.Status = "structurally_validated"
		} else if report.ErrorCode == "" {
			report.ErrorCode = "extraction_failed"
		}
	}
	if ctx.Err() != nil {
		attemptErr = ctx.Err()
		report.Status, report.Result = "failed", nil
		report.ErrorCode = "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			report.ErrorCode = "timeout"
		}
	}
	report.ElapsedMS = time.Since(started).Milliseconds()
	if err := output.write(report); err != nil {
		return errors.New("news report publication did not complete; retain output for inspection, do not retry or overwrite")
	}
	if *format == "json" {
		err = writeSourceJSON(stdout, report)
	} else {
		err = writeNewsReport(stdout, report, out)
	}
	if err != nil {
		return errors.New("news report is saved but terminal output failed; do not repeat extraction")
	}
	if attemptErr != nil {
		return fmt.Errorf("news attempt did not validate (%s); report saved, no retry or AHE call", report.ErrorCode)
	}
	return nil
}

func writeNewsInspection(w io.Writer, inspection newsInspection) error {
	var text strings.Builder
	text.WriteString("新聞來源檢視（離線；不是全文、最新狀態或真人核准）\n")
	fmt.Fprintf(&text, "來源：%s\n快照 SHA-256：%s\n", newsQuoted(inspection.Source.FeedID), inspection.Source.SnapshotSHA256)
	for _, item := range inspection.Source.Items {
		fmt.Fprintf(&text, "\n項目 ID：%s\n標題：%s\n來源摘要（原始文字，HTML 不執行）：%s\n連結：%s\n發布時間（原始欄位，非事件時間）：%s\n", item.ID,
			newsQuoted(item.Title), newsQuoted(item.Description), newsQuoted(item.URL), newsQuoted(item.PublishedAt))
	}
	return writeNewsText(w, text.String())
}

func writeNewsReport(w io.Writer, report newsReport, path string) error {
	var text strings.Builder
	if report.Status == "structurally_validated" && report.Result != nil {
		if report.Result.Outcome == "abstained" {
			text.WriteString("新聞抽取結果：模型選擇不擷取，沒有候選；不是擷取成功。\n")
		} else {
			fmt.Fprintf(&text, "新聞抽取結果：%d 筆待人工核對的候選。\n", len(report.Result.Records))
		}
	}
	fmt.Fprintf(&text, "處理狀態（非品質評分）：%s\n保存檔：%s\n耗時：%d ms\n", report.Status, newsQuoted(path), report.ElapsedMS)
	text.WriteString("只驗證格式／引用位置；尚未人工核對語意。不寫 DB、不採納。WorldMonitor 對照尚未執行。\n")
	fmt.Fprintf(&text, "AI 輸出歸屬：%s\n來源資料揭露：%s\n使用告知：%s\n校驗範圍：%s\n官方使用指引：%s\n", newsQuoted(report.SourceUse.OutputAttribution), newsQuoted(report.SourceUse.InputDisclosure), newsQuoted(report.SourceUse.Disclaimer), newsQuoted(report.SourceUse.CheckScope), newsQuoted(report.SourceUse.PolicyURL))
	fmt.Fprintf(&text, "來源座標：%s\n快照 SHA-256：%s\n", newsQuoted(report.Source.FeedID), newsQuoted(report.Source.SnapshotSHA256))
	for _, item := range report.Source.Items {
		fmt.Fprintf(&text, "項目 ID：%s\n來源標題：%s\n來源連結（未另行查詢）：%s\n發布時間（非事件時間）：%s\n", newsQuoted(item.ID), newsQuoted(item.Title), newsQuoted(item.URL), newsQuoted(item.PublishedAt))
	}
	if report.Result == nil {
		fmt.Fprintf(&text, "未完成：%s（未重試）\n", report.ErrorCode)
	} else {
		fmt.Fprintf(&text, "結果：%s\n不抽取理由：%s\n", report.Result.Outcome, newsQuoted(report.Result.AbstentionReason))
		for i, record := range report.Result.Records {
			fmt.Fprintf(&text, "\nAI 候選 %d：%s\n報導狀態：%s\n原文歸因／參與者（模型擷取，非輸出作者）：%s\n事件時間：%s\n地點：%s\n", i+1,
				newsQuoted(record.Statement), record.ReportedStatus, newsQuoted(record.Attribution), newsQuoted(record.EventTime), newsQuoted(record.Location))
			for _, cite := range record.Citations {
				fmt.Fprintf(&text, "原文 [%s]：%s\n", cite.Field, newsQuoted(cite.ExactQuote))
			}
		}
		for _, limitation := range report.Result.Limitations {
			fmt.Fprintf(&text, "限制：%s\n", newsQuoted(limitation))
		}
	}
	return writeNewsText(w, text.String())
}

func writeNewsText(w io.Writer, value string) error {
	n, err := io.WriteString(w, value)
	if err == nil && n != len(value) {
		return io.ErrShortWrite
	}
	return err
}

func newsQuoted(value string) string {
	raw, _ := json.Marshal(value) // A Go string always has a JSON encoding.
	return sourceTerminalJSON(raw)
}
