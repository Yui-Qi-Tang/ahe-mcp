package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func parseBriefRun(raw []byte) (briefRunReport, error) {
	var report briefRunReport
	bad := errors.New("invalid saved brief report")
	if len(raw) == 0 || len(raw) > 2<<20 || !utf8.Valid(raw) {
		return report, bad
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return report, bad
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return report, bad
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return report, bad
		}
		// The inner source report has its own bounded strict parser. Coordinates
		// use the smaller JSON validator so invalid Unicode is not repaired.
		if key != "result" && sourcemcp.ValidateRecordedJSON(`{"value":`+string(value)+`}`) != nil {
			return report, bad
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(fields) != 8 {
		return report, bad
	}
	if _, err := decoder.Token(); err != io.EOF {
		return report, bad
	}
	for _, key := range []string{"version", "model", "base_url", "started_at", "elapsed_ms", "status", "error_code", "result"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return report, bad
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || report.Version != briefRunVersion || !newsModelNameSafe(report.Model) || !briefEndpointSafe(report.BaseURL) || !newsPlain(report.StartedAt, 100) {
		return briefRunReport{}, bad
	}
	started, err := time.Parse(time.RFC3339Nano, report.StartedAt)
	// A suspended laptop can exceed a deadline in wall-clock duration. This
	// bounded recorded duration is not proof that the deadline was enforced.
	if err != nil || started.IsZero() || report.ElapsedMS < 0 || report.ElapsedMS > (24*time.Hour).Milliseconds() {
		return briefRunReport{}, bad
	}
	result, err := sourcepilot.ParseBriefReport(fields["result"])
	if err != nil {
		return briefRunReport{}, bad
	}
	switch report.Status {
	case "completed_unreviewed":
		if report.ErrorCode != "" || result.Stage != "complete" {
			return briefRunReport{}, bad
		}
	case "failed":
		switch report.ErrorCode {
		case "model_unavailable":
			if result.Stage != "inspected" {
				return briefRunReport{}, bad
			}
		case "extraction_failed":
			if result.Stage != "model_requested" {
				return briefRunReport{}, bad
			}
		case "cancelled", "timeout":
			// Cancellation can arrive immediately after extraction completes.
			// The outer status still records failure, not a completed run.
		default:
			return briefRunReport{}, bad
		}
	default:
		return briefRunReport{}, bad
	}
	report.Result = result
	return report, nil
}

func readBriefRun(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("detective brief read", flag.ContinueOnError)
	input := flags.String("input", "", "explicit private saved brief report")
	format := flags.String("format", "text", "text or json output")
	if parseReviewFlags(flags, args) != nil || *input == "" || (*format != "text" && *format != "json") || ctx == nil || ctx.Err() != nil {
		return errors.New("brief read requires explicit supported flags without duplicates")
	}
	raw, err := readSourcePrivateLimit(*input, 2<<20)
	if err != nil {
		return errors.New("brief read requires a private regular report at most 2 MiB; no model was called")
	}
	report, err := parseBriefRun(raw)
	if err != nil {
		return errors.New("brief saved report is inconsistent; no model was called")
	}
	if *format == "json" {
		return writeSourceJSON(stdout, report)
	}
	if err := writeNewsText(stdout, "離線重讀：只檢查檔案內部一致性，不證明來源、模型執行紀錄或語意真實；未取得人工核准。\n"); err != nil {
		return err
	}
	return writeBriefRunText(stdout, report, *input)
}
