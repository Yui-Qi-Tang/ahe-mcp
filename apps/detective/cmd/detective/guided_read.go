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

const guidedRunVersion = "detective-claim-check-run/v1"
const fixedRunVersion = "detective-fixed-check-run/v1"

// The saved run coordinates are assertions in a local file, not signatures or
// a record of human approval. Inner validation only reconstructs consistency.
type guidedRunEnvelope struct {
	Version   string          `json:"version"`
	Model     string          `json:"model"`
	BaseURL   string          `json:"base_url"`
	StartedAt string          `json:"started_at"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Status    string          `json:"status"`
	ErrorCode string          `json:"error_code"`
	Result    json.RawMessage `json:"result"`
}

func parseGuidedEnvelope(raw []byte) (guidedRunEnvelope, error) {
	var envelope guidedRunEnvelope
	bad := errors.New("invalid saved claim-check report")
	if len(raw) == 0 || len(raw) > 2<<20 || !utf8.Valid(raw) {
		return envelope, bad
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return envelope, bad
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return envelope, bad
		}
		key, ok := token.(string)
		if !ok || fields[key] != nil {
			return envelope, bad
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return envelope, bad
		}
		// The result has its own 2 MiB strict parser. Scalar coordinates use
		// the existing smaller JSON validator, including Unicode validation.
		if key != "result" && sourcemcp.ValidateRecordedJSON(`{"value":`+string(value)+`}`) != nil {
			return envelope, bad
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(fields) != 8 {
		return envelope, bad
	}
	if _, err := decoder.Token(); err != io.EOF {
		return envelope, bad
	}
	for _, name := range []string{"version", "model", "base_url", "started_at", "elapsed_ms", "status", "error_code", "result"} {
		value, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return envelope, bad
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || !newsModelNameSafe(envelope.Model) || !newsEndpointSafe(envelope.BaseURL) || !newsPlain(envelope.StartedAt, 100) {
		return envelope, bad
	}
	started, err := time.Parse(time.RFC3339Nano, envelope.StartedAt)
	// A suspended laptop can exceed the model deadline in wall-clock time. This
	// is a bounded recorded duration, not proof that the timeout was enforced.
	if err != nil || started.IsZero() || envelope.ElapsedMS < 0 || envelope.ElapsedMS > (24*time.Hour).Milliseconds() {
		return envelope, bad
	}
	if envelope.Version != guidedRunVersion && envelope.Version != fixedRunVersion {
		return envelope, bad
	}
	switch envelope.Status {
	case "structurally_validated":
		if envelope.ErrorCode != "" {
			return envelope, bad
		}
	case "failed":
		switch envelope.ErrorCode {
		case "model_unavailable", "cancelled", "timeout":
		case "review_failed":
			if envelope.Version != guidedRunVersion {
				return envelope, bad
			}
		case "check_failed":
			if envelope.Version != fixedRunVersion {
				return envelope, bad
			}
		default:
			return envelope, bad
		}
	default:
		return envelope, bad
	}
	return envelope, nil
}

func readGuidedRun(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("detective claim-check read", flag.ContinueOnError)
	input := flags.String("input", "", "explicit private saved run report")
	format := flags.String("format", "text", "text or json output")
	if parseReviewFlags(flags, args) != nil || *input == "" || (*format != "text" && *format != "json") || ctx == nil || ctx.Err() != nil {
		return errors.New("claim-check read requires explicit supported flags without duplicates")
	}
	raw, err := readSourcePrivateLimit(*input, 2<<20)
	if err != nil {
		return errors.New("claim-check read requires a private regular report at most 2 MiB; no model was called")
	}
	envelope, err := parseGuidedEnvelope(raw)
	if err != nil {
		return errors.New("claim-check saved run envelope is invalid; no model was called")
	}
	const boundary = "離線重讀：只檢查檔案內部一致性，不證明來源、模型執行紀錄或語意真實；未取得人工核准。\n"
	if envelope.Version == guidedRunVersion {
		result, err := sourcepilot.ParseGuidedReport(envelope.Result)
		if err != nil || !guidedEnvelopeStage(envelope, result.Stage) {
			return errors.New("claim-check saved report is inconsistent; no model was called")
		}
		report := guidedRunReport{Version: envelope.Version, Model: envelope.Model, BaseURL: envelope.BaseURL,
			StartedAt: envelope.StartedAt, ElapsedMS: envelope.ElapsedMS, Status: envelope.Status, ErrorCode: envelope.ErrorCode, Result: result}
		if *format == "json" {
			return writeSourceJSON(stdout, report)
		}
		if err := writeNewsText(stdout, boundary); err != nil {
			return err
		}
		return writeGuidedRunText(stdout, report, *input)
	}
	result, err := sourcepilot.ParseFixedReport(envelope.Result)
	if err != nil || !guidedEnvelopeStage(envelope, result.Stage) {
		return errors.New("claim-check saved fixed report is inconsistent; no model was called")
	}
	report := fixedRunReport{Version: envelope.Version, Model: envelope.Model, BaseURL: envelope.BaseURL,
		StartedAt: envelope.StartedAt, ElapsedMS: envelope.ElapsedMS, Status: envelope.Status, ErrorCode: envelope.ErrorCode, Result: result}
	if *format == "json" {
		return writeSourceJSON(stdout, report)
	}
	if err := writeNewsText(stdout, boundary); err != nil {
		return err
	}
	return writeFixedRunText(stdout, report, *input)
}

func guidedEnvelopeStage(envelope guidedRunEnvelope, stage string) bool {
	if envelope.Status == "structurally_validated" {
		return stage == "complete"
	}
	switch envelope.ErrorCode {
	case "cancelled", "timeout":
		// Cancellation may arrive just after the last model response.
		return true
	case "model_unavailable":
		return stage == "inspected"
	default:
		return stage != "inspected" && stage != "complete"
	}
}
