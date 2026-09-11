package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

const queryUsage = "detective query -query-command /absolute/query-launcher -question 'Was the earthquake measured on the Richter scale?' [-source-id ID] [-outcome pending|rejected|audit_only|admitted] [-lifecycle active|historical|all] [-limit 20] [-format text|json] [-timeout 2m]\n" +
	"Practical read-only material search; requires practical_multisurface_lexical_v1 / grounded-evidence-brief-v7.\n" +
	"No model, source fetch, pending submission, human decision, automatic retry or mode downgrade.\n" +
	"Output includes original source references and retrieval metadata. Keep redirected output private.\n"

type practicalSearcher func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error)

func runQuery(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runQueryContext(ctx, args, stdout, stderr, ahemcp.SearchPractical)
}

func runQueryContext(ctx context.Context, args []string, stdout, stderr io.Writer, search practicalSearcher) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return writeNewsText(stdout, queryUsage)
	}
	flags := flag.NewFlagSet("detective query", flag.ContinueOnError)
	command := flags.String("query-command", "", "explicit credential-free Query launcher executable")
	question := flags.String("question", "", "question, at most 256 characters and 16 whitespace terms")
	sourceID := flags.String("source-id", "", "optional exact source ID")
	outcome := flags.String("outcome", "", "optional exact persisted admission outcome")
	lifecycle := flags.String("lifecycle", "active", "active, historical or all; defaults to active")
	limit := flags.Int("limit", 20, "result bound from 1 to 100")
	format := flags.String("format", "text", "text or json")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum two minutes")
	if parseReviewFlags(flags, args) != nil || ctx == nil || ctx.Err() != nil || search == nil || *command == "" || *question == "" || *timeout <= 0 || *timeout > 2*time.Minute || (*format != "text" && *format != "json") {
		return errors.New("query requires explicit launcher and question, supported flags without duplicates, and an active bounded context")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	result, err := search(ctx, *command, ahemcp.PracticalQueryInput{Question: *question, SourceID: *sourceID, AdmissionOutcome: *outcome, LifecycleScope: *lifecycle, Limit: *limit})
	if err != nil {
		return err
	}
	if *format == "json" {
		var pretty bytes.Buffer
		if json.Indent(&pretty, result.Response, "", "  ") != nil {
			return errors.New("query returned invalid JSON; nothing was written")
		}
		return writeNewsText(stdout, sourceTerminalJSON(pretty.Bytes())+"\n")
	}
	summary := fmt.Sprintf("實務資料查詢：回傳 %d 筆既有記錄；這不是相關率或正確答案數。\n", result.ReturnedMatches)
	if result.FallbackAttempted {
		summary += "初查未命中，已進行一次較寬的詞彙補查；補查材料不是答案，也不代表來源支持問題中的前提。\n"
	}
	if result.ReturnedMatches == 0 {
		summary += "本次未找到符合結果，不代表資料不存在；可縮短關鍵詞、檢查篩選條件後由你決定是否再查。\n"
	}
	if result.Truncated {
		summary += "結果達到查詢上限，尚有材料未列出；不能視為已找齊。\n"
	}
	if policy := result.HanTermPolicy; policy != nil && len(policy.AuxiliaryTerms) > 0 {
		terms, _ := json.Marshal(policy.AuxiliaryTerms) // A string slice always has a JSON encoding.
		summary += "中文補充搜尋：輔助詞不可單獨作為中文補充命中（僅有輔助詞的短查詢例外）；本次輔助詞 " + sourceTerminalJSON(terms) + "。\n"
		if policy.AuxiliaryOnlyQueryPreserved {
			summary += "本次查詢只有輔助詞且沒有英文字詞，已保留廣查；可能帶回許多不同主題的材料，請加入更具體字詞縮小範圍。\n"
		}
		summary += "這項規則只限制中文補充搜尋，不攔截原本搜尋或英文命中；中文字對仍是字面線索，不代表理解主題或語意支持。\n"
	}
	summary += "唯讀：未新增待審、未修改 admit／audit_only／reject；與問題不相關的結果可略過，不需 reject。\n" +
		"以下是原引用與有界搜尋片段，不是完整來源；搜尋命中不是語意支持證明。\n"
	detail, err := practicalQueryText(result.Response)
	if err != nil {
		return err
	}
	return writeNewsText(stdout, summary+detail+"\n完整原生查詢回應（含所有引用、狀態與限制）可用 -format json 檢視。\n")
}

func practicalQueryText(raw json.RawMessage) (string, error) {
	type sourceRef struct {
		SpanID string `json:"span_id"`
		Quote  string `json:"quoted_text"`
	}
	var response struct {
		Matches []struct {
			Ref struct {
				ID string `json:"id"`
			} `json:"record_ref"`
			Statement string `json:"statement_text"`
			Outcome   string `json:"admission_outcome"`
			State     struct {
				Authority string `json:"authority_status"`
				Lifecycle string `json:"record_lifecycle"`
			} `json:"record_state"`
			Refs  []sourceRef `json:"source_refs"`
			Basis struct {
				Surfaces []struct {
					Surface string   `json:"surface"`
					Status  string   `json:"status"`
					Matched []string `json:"matched_terms"`
					Missing []string `json:"missing_terms"`
					Spans   []struct {
						Ref sourceRef `json:"source_ref"`
					} `json:"matched_spans"`
					Truncated bool `json:"spans_truncated"`
				} `json:"surfaces"`
			} `json:"retrieval_basis"`
		} `json:"matches"`
		Limitations []string `json:"limitations"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "", errors.New("query returned invalid display JSON; nothing was written")
	}
	var text bytes.Buffer
	for index, match := range response.Matches {
		fmt.Fprintf(&text, "\n%d. %s\n   記錄：%s；處置：%s；權威狀態：%s；生命週期：%s\n", index+1, newsQuoted(match.Statement), newsQuoted(match.Ref.ID), newsQuoted(match.Outcome), newsQuoted(match.State.Authority), newsQuoted(match.State.Lifecycle))
		for _, ref := range match.Refs {
			fmt.Fprintf(&text, "   原引用 %s：%s\n", newsQuoted(ref.SpanID), newsQuoted(ref.Quote))
		}
		for _, surface := range match.Basis.Surfaces {
			label := "候選文字"
			if surface.Surface == "extraction_views.rendered_content" {
				label = "來源內文"
			}
			matched, _ := json.Marshal(surface.Matched)
			missing, _ := json.Marshal(surface.Missing)
			fmt.Fprintf(&text, "   %s搜尋（%s）：命中字詞 %s；未命中字詞 %s\n", label, newsQuoted(surface.Status), sourceTerminalJSON(matched), sourceTerminalJSON(missing))
			for _, span := range surface.Spans {
				fmt.Fprintf(&text, "   搜尋命中片段（不是新增原引用）%s：%s\n", newsQuoted(span.Ref.SpanID), newsQuoted(span.Ref.Quote))
			}
			if surface.Truncated {
				text.WriteString("   搜尋片段達顯示上限；尚有片段未列出。\n")
			}
		}
	}
	for _, limitation := range response.Limitations {
		fmt.Fprintf(&text, "限制：%s\n", newsQuoted(limitation))
	}
	return text.String(), nil
}
