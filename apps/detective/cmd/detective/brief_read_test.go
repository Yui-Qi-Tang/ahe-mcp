package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func briefSavedRun(t *testing.T) briefRunReport {
	t.Helper()
	_, source := briefCLIInput(t)
	extractor, err := sourcepilot.NewBriefExtractor(&guidedTestLLM{answers: []string{briefCLIAnswer}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := extractor.Extract(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	return briefRunReport{Version: briefRunVersion, Model: "synthetic-brief", BaseURL: "http://127.0.0.1:11434/v1",
		StartedAt: "2026-09-10T01:00:00Z", ElapsedMS: 10, Status: "completed_unreviewed", Result: result}
}

func TestBriefReadIsOfflineAndPreservesReport(t *testing.T) {
	report := briefSavedRun(t)
	path := briefPrivateJSON(t, report)
	calls := 0
	factory := func(context.Context, string, string) (*sourcepilot.BriefExtractor, error) {
		calls++
		return nil, errors.New("unexpected factory")
	}
	for _, format := range []string{"text", "json"} {
		var stdout, stderr bytes.Buffer
		if err := runBriefWithContext(t.Context(), []string{"read", "-input", path, "-format", format}, &stdout, &stderr, factory); err != nil || calls != 0 || stderr.Len() != 0 {
			t.Fatal("offline read called factory or failed", err)
		}
		if format == "json" {
			var shown briefRunReport
			if json.Unmarshal(stdout.Bytes(), &shown) != nil || !reflect.DeepEqual(shown, report) {
				t.Fatal("read changed report")
			}
		} else if !strings.Contains(stdout.String(), "離線重讀") || !strings.Contains(stdout.String(), "不證明來源") || !strings.Contains(stdout.String(), report.Result.Source.Body) || !strings.Contains(stdout.String(), briefCLIAnswer) {
			t.Fatal("read omitted source, summary or authority warning")
		}
	}
}

func TestBriefReadRejectsEnvelopeAndResultTampering(t *testing.T) {
	baseline, err := json.Marshal(briefSavedRun(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"unknown", "missing", "null", "case", "duplicate", "nested-duplicate", "trailing", "array", "unicode", "version", "model", "endpoint", "localhost", "time", "duration-negative", "duration-large", "status", "success-error", "failure-empty", "failure-unknown", "failure-complete", "unavailable-complete", "inner-version", "inner-body", "inner-authority", "inner-human", "inner-scope", "inner-stage", "inner-text"} {
		t.Run(kind, func(t *testing.T) {
			var fields map[string]any
			if json.Unmarshal(baseline, &fields) != nil {
				t.Fatal("invalid baseline")
			}
			inner := fields["result"].(map[string]any)
			switch kind {
			case "unknown":
				fields["extra"] = "private-marker"
			case "missing":
				delete(fields, "error_code")
			case "null":
				fields["elapsed_ms"] = nil
			case "case":
				fields["Model"] = fields["model"]
				delete(fields, "model")
			case "version":
				fields["version"] = "future/v1"
			case "model":
				fields["model"] = "synthetic\u202emodel"
			case "endpoint":
				fields["base_url"] = "https://example.invalid/?token=private-marker"
			case "localhost":
				fields["base_url"] = "http://localhost:11434/v1"
			case "time":
				fields["started_at"] = "yesterday"
			case "duration-negative":
				fields["elapsed_ms"] = -1
			case "duration-large":
				fields["elapsed_ms"] = 86_400_001
			case "status":
				fields["status"] = "admitted"
			case "success-error":
				fields["error_code"] = "timeout"
			case "failure-empty":
				fields["status"] = "failed"
			case "failure-unknown":
				fields["status"], fields["error_code"] = "failed", "private-marker"
			case "failure-complete":
				fields["status"], fields["error_code"] = "failed", "extraction_failed"
			case "unavailable-complete":
				fields["status"], fields["error_code"] = "failed", "model_unavailable"
			case "inner-version":
				inner["version"] = "future/v1"
			case "inner-body":
				inner["source"].(map[string]any)["body"] = "changed private-marker"
			case "inner-authority":
				inner["authority_effect"] = "admit"
			case "inner-human":
				inner["human_review"] = "approved"
			case "inner-scope":
				inner["reference_scope"] = "verified_claim_support"
			case "inner-stage":
				inner["stage"] = "inspected"
			case "inner-text":
				inner["text"] = "changed private-marker"
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "duplicate":
				raw = append([]byte(`{"version":"wrong",`), raw[1:]...)
			case "nested-duplicate":
				raw = bytes.Replace(raw, []byte(`"source":{`), []byte(`"source":{"body":"private-marker",`), 1)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			case "array":
				raw = []byte(`[]`)
			case "unicode":
				raw = bytes.Replace(raw, []byte(`"model":"synthetic-brief"`), []byte(`"model":"\ud800"`), 1)
			}
			path := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			err = runBriefWithContext(t.Context(), []string{"read", "-input", path}, &stdout, &stderr, nil)
			if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "private-marker") {
				t.Fatal("read accepted tampering or exposed raw content", err)
			}
		})
	}
}

func TestBriefReadReportsHaveIndependentSizeBound(t *testing.T) {
	path := briefPrivateJSON(t, briefSavedRun(t))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(bytes.Repeat([]byte(" "), 70<<10), raw...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSourcePrivate(path); err == nil {
		t.Fatal("source limit unexpectedly expanded")
	}
	if err := readBriefRun(t.Context(), []string{"-input", path}, io.Discard); err != nil {
		t.Fatal("report above source input bound could not be read", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), (2<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readBriefRun(t.Context(), []string{"-input", path}, io.Discard); err == nil {
		t.Fatal("oversized report accepted")
	}
}

func TestBriefReadPrivateFilesAndFlags(t *testing.T) {
	path := briefPrivateJSON(t, briefSavedRun(t))
	for _, kind := range []string{"relative", "missing", "permissions", "symlink", "duplicate", "model-flag", "format", "cancelled", "nil-context", "writer"} {
		t.Run(kind, func(t *testing.T) {
			args := []string{"read", "-input", path}
			ctx := t.Context()
			var stdout, stderr bytes.Buffer
			var writer io.Writer = &stdout
			switch kind {
			case "relative":
				args[2] = "report.json"
			case "missing":
				args[2] = filepath.Join(sourceCLIPrivateDir(t), "missing.json")
			case "permissions", "symlink":
				newPath := filepath.Join(sourceCLIPrivateDir(t), "report.json")
				if kind == "symlink" {
					if err := os.Symlink(path, newPath); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(newPath, []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
				args[2] = newPath
			case "duplicate":
				args = append(args, "-input", path)
			case "model-flag":
				args = append(args, "-model", "unexpected")
			case "format":
				args = append(args, "-format", "html")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
			case "nil-context":
				ctx = nil
			case "writer":
				writer = briefBrokenWriter{}
			}
			if err := runBriefWithContext(ctx, args, writer, &stderr, nil); err == nil || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("invalid read was accepted or leaked content", err)
			}
		})
	}
}

func TestBriefReadFailedCancellationCannotBecomeSuccess(t *testing.T) {
	for _, stage := range []string{"inspected", "complete"} {
		report := briefSavedRun(t)
		if stage == "inspected" {
			var err error
			report.Result, err = sourcepilot.NewBriefReport(report.Result.Source)
			if err != nil {
				t.Fatal(err)
			}
		}
		report.Status, report.ErrorCode = "failed", "cancelled"
		path := briefPrivateJSON(t, report)
		var stdout bytes.Buffer
		if err := readBriefRun(t.Context(), []string{"-input", path, "-format", "json"}, &stdout); err != nil {
			t.Fatal(err)
		}
		var shown briefRunReport
		if json.Unmarshal(stdout.Bytes(), &shown) != nil || !reflect.DeepEqual(shown, report) || shown.Status != "failed" {
			t.Fatal("cancellation was lost during offline read")
		}
	}
}
