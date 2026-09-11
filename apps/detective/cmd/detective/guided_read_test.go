package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func guidedSavedRun(t *testing.T) guidedRunReport {
	t.Helper()
	_, source := guidedCLIInput(t)
	reviewer, err := sourcepilot.NewGuidedReviewer(&guidedTestLLM{answers: guidedCLIAnswers})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reviewer.Review(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	return guidedRunReport{Version: guidedRunVersion, Model: "synthetic-guided", BaseURL: "http://127.0.0.1:11434/v1",
		StartedAt: "2026-09-10T01:00:00Z", ElapsedMS: 10, Status: "structurally_validated", Result: result}
}

func guidedPrivateJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceCLIPrivateDir(t), "input.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGuidedReadSavedRunOffline(t *testing.T) {
	report := guidedSavedRun(t)
	path := guidedPrivateJSON(t, report)
	calls := 0
	factory := func(context.Context, string, string) (*sourcepilot.GuidedReviewer, error) {
		calls++
		return nil, errors.New("unexpected model factory")
	}
	for _, format := range []string{"text", "json"} {
		var stdout, stderr bytes.Buffer
		if err := runGuidedWithContext(t.Context(), []string{"read", "-input", path, "-format", format}, &stdout, &stderr, factory); err != nil {
			t.Fatal(err)
		}
		if calls != 0 || stderr.Len() != 0 {
			t.Fatal("offline read called model or emitted diagnostics")
		}
		if format == "text" {
			if !strings.Contains(stdout.String(), "離線重讀") || !strings.Contains(stdout.String(), "不證明來源") || !strings.Contains(stdout.String(), report.Result.Source.Body) {
				t.Fatal("read omitted authority warning or complete original body")
			}
		} else {
			var reread guidedRunReport
			if json.Unmarshal(stdout.Bytes(), &reread) != nil || !reflect.DeepEqual(report, reread) {
				t.Fatal("offline JSON read changed saved report")
			}
		}
	}
}

func TestGuidedReadBoundExceedsSourceBound(t *testing.T) {
	report := guidedSavedRun(t)
	path := guidedPrivateJSON(t, report)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(bytes.Repeat([]byte(" "), 70<<10), raw...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSourcePrivate(path); err == nil {
		t.Fatal("original source input limit was expanded")
	}
	if err := runGuidedWithContext(t.Context(), []string{"read", "-input", path}, io.Discard, io.Discard, nil); err != nil {
		t.Fatal("bounded saved report over 64 KiB could not be read:", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), (2<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGuidedWithContext(t.Context(), []string{"read", "-input", path}, io.Discard, io.Discard, nil); err == nil {
		t.Fatal("oversized saved report accepted")
	}
}

func TestGuidedReadLargeValidReport(t *testing.T) {
	_, source := guidedCLIInput(t)
	// A synthetic JSON-escaping stress case, not a readable-evidence example.
	// Seven exact citations repeat a legal 24 KiB source paragraph. The actual
	// resulting JSON data exceeds 1 MiB without padding or altered fields.
	source.Body = "Synthetic size-bound payload " + strings.Repeat("&", 24<<10) + "."
	var claims []sourcepilot.ReadableStatement
	var assessments []map[string]any
	var observations []map[string]any
	for i := 1; i <= 4; i++ {
		statement := sourcepilot.ReadableStatement{Text: fmt.Sprintf("Service %d recovered.", i), Subject: fmt.Sprintf("Service %d", i), Predicate: "recovered", Object: ""}
		if i <= 3 {
			claims = append(claims, statement)
			assessments = append(assessments, map[string]any{"claim": i, "relation": "insufficient", "segments": []int{1}, "explanation": "The synthetic payload does not establish recovery."})
		}
		observations = append(observations, map[string]any{"statement": statement, "segments": []int{1}})
	}
	var answers []string
	for _, value := range []any{map[string]any{"claims": claims, "reason": ""}, map[string]any{"assessments": assessments}, map[string]any{"observations": observations, "reason": ""}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, string(raw))
	}
	reviewer, err := sourcepilot.NewGuidedReviewer(&guidedTestLLM{answers: answers})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reviewer.Review(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	report := guidedSavedRun(t)
	report.Result = result
	path := guidedPrivateJSON(t, report)
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 1<<20 || info.Size() > 2<<20 {
		t.Fatal("test report does not exercise 1–2 MiB actual data")
	}
	var stdout bytes.Buffer
	if err := runGuidedWithContext(t.Context(), []string{"read", "-input", path, "-format", "json"}, &stdout, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	var reread guidedRunReport
	if json.Unmarshal(stdout.Bytes(), &reread) != nil || !reflect.DeepEqual(report, reread) {
		t.Fatal("large offline report lost exact content")
	}
}

func TestGuidedReadRejectsEnvelopeAndInnerTampering(t *testing.T) {
	report := guidedSavedRun(t)
	baseline, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"unknown", "missing", "null", "duplicate", "nested-duplicate", "trailing", "array", "version", "model", "endpoint", "time", "duration-negative", "duration-large", "status", "success-error", "failure-empty", "failure-unknown", "wrong-version-error", "failure-complete", "model-unavailable-complete", "inner-version", "inner-body", "inner-authority", "inner-stage"} {
		t.Run(kind, func(t *testing.T) {
			var fields map[string]any
			if json.Unmarshal(baseline, &fields) != nil {
				t.Fatal("invalid baseline")
			}
			inner := fields["result"].(map[string]any)
			switch kind {
			case "unknown":
				fields["unknown"] = "private-diagnostic"
			case "missing":
				delete(fields, "error_code")
			case "null":
				fields["elapsed_ms"] = nil
			case "version":
				fields["version"] = "future/v1"
			case "model":
				fields["model"] = "synthetic\u202emodel"
			case "endpoint":
				fields["base_url"] = "https://example.invalid/?token=private-diagnostic"
			case "time":
				fields["started_at"] = "yesterday"
			case "duration-negative":
				fields["elapsed_ms"] = -1
			case "duration-large":
				fields["elapsed_ms"] = 86400001
			case "status":
				fields["status"] = "admit"
			case "success-error":
				fields["error_code"] = "timeout"
			case "failure-empty":
				fields["status"] = "failed"
			case "failure-unknown":
				fields["status"], fields["error_code"] = "failed", "private-diagnostic"
			case "wrong-version-error":
				fields["status"], fields["error_code"] = "failed", "check_failed"
			case "failure-complete":
				fields["status"], fields["error_code"] = "failed", "review_failed"
			case "model-unavailable-complete":
				fields["status"], fields["error_code"] = "failed", "model_unavailable"
			case "inner-version":
				inner["version"] = "future/v1"
			case "inner-body":
				inner["source"].(map[string]any)["body"] = "Tampered body."
			case "inner-authority":
				inner["authority_effect"] = "admit"
			case "inner-stage":
				inner["stage"] = "inspected"
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "duplicate":
				raw = bytes.Replace(raw, []byte("{"), []byte(`{"version":"duplicate",`), 1)
			case "nested-duplicate":
				raw = bytes.Replace(raw, []byte(`"source":{`), []byte(`"source":{"body":"duplicate",`), 1)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			case "array":
				raw = []byte(`[]`)
			}
			path := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			err = runGuidedWithContext(t.Context(), []string{"read", "-input", path}, &stdout, &stderr, nil)
			if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "private-diagnostic") {
				t.Fatal("invalid saved report accepted, partially printed or leaked")
			}
		})
	}
}

func TestGuidedReadFailedAndCancelledReports(t *testing.T) {
	_, source := guidedCLIInput(t)
	for _, kind := range []string{"factory", "invalid-final", "cancelled-complete", "timeout-complete"} {
		t.Run(kind, func(t *testing.T) {
			report := guidedSavedRun(t)
			report.Status = "failed"
			switch kind {
			case "factory":
				report.Result, _ = sourcepilot.NewGuidedReport(source)
				report.ErrorCode = "model_unavailable"
			case "invalid-final":
				reviewer, err := sourcepilot.NewGuidedReviewer(&guidedTestLLM{answers: []string{`{"unknown":"retained invalid model final"}`}})
				if err != nil {
					t.Fatal(err)
				}
				report.Result, err = reviewer.Review(t.Context(), source)
				if err == nil {
					t.Fatal("invalid final accepted")
				}
				report.ErrorCode = "review_failed"
			case "cancelled-complete":
				report.ErrorCode = "cancelled"
			case "timeout-complete":
				report.ErrorCode = "timeout"
			}
			path := guidedPrivateJSON(t, report)
			var stdout bytes.Buffer
			if err := runGuidedWithContext(t.Context(), []string{"read", "-input", path, "-format", "json"}, &stdout, io.Discard, nil); err != nil {
				t.Fatal(err)
			}
			var reread guidedRunReport
			if json.Unmarshal(stdout.Bytes(), &reread) != nil || !reflect.DeepEqual(report, reread) {
				t.Fatal("offline inspection lost the original failed state")
			}
		})
	}
}

func TestGuidedReadPrivatePathAndFlags(t *testing.T) {
	path := guidedPrivateJSON(t, guidedSavedRun(t))
	for _, kind := range []string{"missing", "duplicate", "model-flag", "out-flag", "format", "relative", "cancelled", "nil-context", "permissions", "parent-permissions", "symlink", "fifo", "short-writer"} {
		t.Run(kind, func(t *testing.T) {
			args := []string{"read", "-input", path}
			ctx := t.Context()
			var stdout, stderr bytes.Buffer
			var writer io.Writer = &stdout
			switch kind {
			case "missing":
				args = []string{"read"}
			case "duplicate":
				args = append(args, "-input", path)
			case "model-flag":
				args = append(args, "-model", "unused")
			case "out-flag":
				args = append(args, "-out", "unused")
			case "format":
				args = append(args, "-format", "html")
			case "relative":
				args[2] = "relative.json"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			case "permissions", "parent-permissions", "symlink", "fifo":
				args[2] = filepath.Join(sourceCLIPrivateDir(t), "input.json")
				switch kind {
				case "symlink":
					if err := os.Symlink(path, args[2]); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := unix.Mkfifo(args[2], 0o600); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(args[2], []byte(`{}`), 0o644); err != nil {
						t.Fatal(err)
					}
					if kind == "parent-permissions" {
						if err := os.Chmod(filepath.Dir(args[2]), 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "short-writer":
				writer = newsShortWriter{}
			}
			if err := runGuidedWithContext(ctx, args, writer, &stderr, nil); err == nil || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("unsafe read or failed terminal output was accepted")
			}
		})
	}
}
