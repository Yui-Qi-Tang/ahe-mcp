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
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

var guidedCLIAnswers = []string{
	`{"claims":[{"text":"付款 API 已恢復。","subject":"付款 API","predicate":"已恢復","object":""}],"reason":""}`,
	`{"assessments":[{"claim":1,"relation":"supported","segments":[1],"explanation":"原文明確記載付款 API 已恢復。"}]}`,
	`{"observations":[{"statement":{"text":"退款 API 仍有延遲。","subject":"退款 API","predicate":"仍有","object":"延遲"},"segments":[2]}],"reason":""}`,
}

type guidedTestLLM struct {
	answers []string
	err     error
	calls   int
	before  func(int)
}

func (m *guidedTestLLM) Name() string { return "synthetic-guided" }
func (m *guidedTestLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	index := m.calls
	m.calls++
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.before != nil {
			m.before(index)
		}
		if m.err != nil {
			yield(nil, m.err)
			return
		}
		if index >= len(m.answers) {
			yield(nil, errors.New("unexpected model call"))
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(m.answers[index], genai.RoleModel), FinishReason: genai.FinishReasonStop}, nil)
	}
}

func guidedCLIInput(t *testing.T) (string, sourcepilot.GuidedSource) {
	t.Helper()
	source := sourcepilot.GuidedSource{Version: "detective-summary-source/v1", SourceID: "synthetic-service", SourceRevision: "revision-1",
		SourceURL: "https://example.invalid/synthetic-service", ObservedAt: "2026-09-10T01:00:00Z", SummaryOrigin: "synthetic external summary",
		Coverage: "full_document", Limitations: []string{}, Summary: "Payment API recovered.",
		Body: "Payment API recovered at 14:20 UTC.<br />Refund API still had delays."}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceCLIPrivateDir(t), "bundle.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, source
}

func guidedCLIArgs(input, out string) []string {
	return []string{"run", "-input", input, "-model", "synthetic-guided", "-base-url", "http://127.0.0.1:11434/v1", "-out", out}
}

func TestGuidedInspectOfflinePreservesCompleteSource(t *testing.T) {
	input, source := guidedCLIInput(t)
	var stdout, stderr bytes.Buffer
	for _, format := range []string{"text", "json"} {
		stdout.Reset()
		if err := runGuidedWithContext(t.Context(), []string{"inspect", "-input", input, "-format", format}, &stdout, &stderr, nil); err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var report sourcepilot.GuidedReport
			if json.Unmarshal(stdout.Bytes(), &report) != nil || !reflect.DeepEqual(report.Source, source) || report.Stage != "inspected" || report.HumanReview != "not_reviewed" || report.AuthorityEffect != "none" || report.Readability != "requires_human_review" || len(report.Attempts) != 0 {
				t.Fatal("offline inspection lost original source or authority boundary")
			}
		} else if !strings.Contains(stdout.String(), "Payment API recovered at 14:20 UTC.") || !strings.Contains(stdout.String(), "Refund API still had delays.") {
			t.Fatal("readable inspection omitted complete source context")
		}
	}
	stdout.Reset()
	if err := run([]string{"claim-check", "--help"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "at most three") || stderr.Len() != 0 {
		t.Fatal("claim-check dispatch or offline help failed")
	}
}

func TestGuidedPreflightDoesNotCreateModelOrReport(t *testing.T) {
	input, _ := guidedCLIInput(t)
	t.Setenv("DETECTIVE_MODEL", "must-not-default")
	t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:1/v1")
	for _, kind := range []string{"missing-input", "missing-model", "missing-url", "missing-out", "duplicate", "unsupported", "positional", "format", "timeout-zero", "timeout-large", "external-url", "credential-url", "model-control", "cancelled", "nil-context", "nil-factory", "relative-input", "input-permissions", "input-symlink", "input-json", "existing-output", "output-permissions", "output-symlink"} {
		t.Run(kind, func(t *testing.T) {
			out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			args := guidedCLIArgs(input, out)
			ctx := t.Context()
			calls := 0
			factory := guidedFactory(func(context.Context, string, string) (*sourcepilot.GuidedReviewer, error) {
				calls++
				return nil, errors.New("unexpected factory call")
			})
			switch kind {
			case "missing-input":
				args[2] = ""
			case "missing-model":
				args = append(args[:3], args[5:]...)
			case "missing-url":
				args = append(args[:5], args[7:]...)
			case "missing-out":
				args = args[:7]
			case "duplicate":
				args = append(args, "-model", "synthetic-guided")
			case "unsupported":
				args = append(args, "-ahe-submit-pending", "true")
			case "positional":
				args = append(args, "unexpected")
			case "format":
				args = append(args, "-format", "html")
			case "timeout-zero":
				args = append(args, "-timeout", "0")
			case "timeout-large":
				args = append(args, "-timeout", "7m")
			case "external-url":
				args[6] = "https://example.invalid/v1"
			case "credential-url":
				args[6] = "http://user:synthetic-secret@127.0.0.1:11434/v1"
			case "model-control":
				args[4] = "synthetic\u202emodel"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
			case "nil-context":
				ctx = nil
			case "nil-factory":
				factory = nil
			case "relative-input":
				args[2] = "bundle.json"
			case "input-permissions", "input-symlink", "input-json":
				path := filepath.Join(sourceCLIPrivateDir(t), "input.json")
				if kind == "input-symlink" {
					if err := os.Symlink(input, path); err != nil {
						t.Fatal(err)
					}
				} else {
					mode := os.FileMode(0o600)
					if kind == "input-permissions" {
						mode = 0o644
					}
					if err := os.WriteFile(path, []byte(`{"body":"synthetic-private-diagnostic"}`), mode); err != nil {
						t.Fatal(err)
					}
				}
				args[2] = path
			case "existing-output":
				if err := os.WriteFile(out, []byte("retain-original"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "output-permissions":
				if err := os.Chmod(filepath.Dir(out), 0o755); err != nil {
					t.Fatal(err)
				}
			case "output-symlink":
				if err := os.Symlink(input, out); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			err := runGuidedWithContext(ctx, args, &stdout, &stderr, factory)
			if err == nil || calls != 0 || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "synthetic-private-diagnostic") || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("preflight caused side effects or exposed input")
			}
			if kind == "existing-output" {
				raw, err := os.ReadFile(out)
				if err != nil || string(raw) != "retain-original" {
					t.Fatal("existing output changed")
				}
			} else if kind != "output-symlink" {
				if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("preflight reserved an output")
				}
			}
		})
	}
}

func TestGuidedFailedAttemptsRetainSourceWithoutRawErrors(t *testing.T) {
	input, source := guidedCLIInput(t)
	for _, kind := range []string{"factory", "nil", "model", "invalid-final", "cancel", "stdout"} {
		t.Run(kind, func(t *testing.T) {
			llm := &guidedTestLLM{err: errors.New("synthetic-private-upstream-error")}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			factory := func(ctx context.Context, modelName, baseURL string) (*sourcepilot.GuidedReviewer, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 6*time.Minute || modelName != "synthetic-guided" || baseURL != "http://127.0.0.1:11434/v1" {
					t.Fatal("explicit model or deadline was lost")
				}
				switch kind {
				case "factory":
					return nil, llm.err
				case "nil":
					return nil, nil
				case "invalid-final":
					llm.err = nil
					llm.answers = []string{`{"unexpected":"synthetic-untrusted-final"}`}
				case "cancel":
					cancel()
				}
				return sourcepilot.NewGuidedReviewer(llm)
			}
			out := filepath.Join(sourceCLIPrivateDir(t), "partial.json")
			var stdout, stderr bytes.Buffer
			var writer io.Writer = &stdout
			if kind == "stdout" {
				writer = newsShortWriter{}
			}
			err := runGuidedWithContext(ctx, guidedCLIArgs(input, out), writer, &stderr, factory)
			if err == nil || calls != 1 || llm.calls > 1 || stderr.Len() != 0 {
				t.Fatal("failed attempt was accepted or repeated")
			}
			raw, readErr := os.ReadFile(out)
			var report guidedRunReport
			if readErr != nil || json.Unmarshal(raw, &report) != nil || report.Status != "failed" || report.ErrorCode == "" || !reflect.DeepEqual(report.Result.Source, source) || report.Result.AuthorityEffect != "none" || report.Result.HumanReview != "not_reviewed" {
				t.Fatal("failed attempt lost original source or authority boundary")
			}
			if strings.Contains(string(raw)+stdout.String()+err.Error(), "synthetic-private-upstream-error") {
				t.Fatal("upstream error escaped controller-owned reporting")
			}
			if kind == "invalid-final" && (len(report.Result.Attempts) != 1 || report.Result.Attempts[0].RawText != llm.answers[0]) {
				t.Fatal("malformed final text was not retained for inspection")
			}
			if kind == "cancel" && report.ErrorCode != "cancelled" {
				t.Fatal("cancellation was mislabeled")
			}
			if kind == "stdout" && !strings.Contains(err.Error(), "saved but terminal") {
				t.Fatal("saved report was reported as lost after display failure")
			}
			info, statErr := os.Stat(out)
			if statErr != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("attempt report was not private")
			}
		})
	}
}

func TestGuidedRunSavesThreeStageReportAndNeverOverwrites(t *testing.T) {
	input, source := guidedCLIInput(t)
	llm := &guidedTestLLM{answers: guidedCLIAnswers}
	calls := 0
	factory := func(context.Context, string, string) (*sourcepilot.GuidedReviewer, error) {
		calls++
		// A later file edit cannot replace the already validated input snapshot.
		if err := os.WriteFile(input, []byte(`{"changed":"after validation"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return sourcepilot.NewGuidedReviewer(llm)
	}
	out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
	var stdout, stderr bytes.Buffer
	if err := runGuidedWithContext(t.Context(), guidedCLIArgs(input, out), &stdout, &stderr, factory); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var report guidedRunReport
	if json.Unmarshal(raw, &report) != nil || report.Version != "detective-claim-check-run/v1" || report.Status != "structurally_validated" || report.ErrorCode != "" || report.Result.Stage != "complete" || !reflect.DeepEqual(report.Result.Source, source) {
		t.Fatal("successful execution lost frozen input or stage status")
	}
	if len(report.Result.Claims) != 1 || len(report.Result.Assessments) != 1 || len(report.Result.Observations) != 1 || len(report.Result.Attempts) != 3 || report.Result.AuthorityEffect != "none" || report.Result.HumanReview != "not_reviewed" || report.Result.Readability != "requires_human_review" || llm.calls != 3 || calls != 1 {
		t.Fatal("three-stage run lost context or became approval")
	}
	if len(report.Result.Observations[0].Citations) != 1 || report.Result.Observations[0].Citations[0].Text != "Refund API still had delays." || !strings.Contains(stdout.String(), "退款 API") || !strings.Contains(stdout.String(), "Refund API still had delays.") || stderr.Len() != 0 {
		t.Fatal("readable report omitted the independently observed limitation")
	}
	// Restore the test input so this retry reaches the existing-output gate.
	bundle, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGuidedWithContext(t.Context(), guidedCLIArgs(input, out), io.Discard, io.Discard, factory); err == nil || calls != 1 || llm.calls != 3 {
		t.Fatal("existing report caused another model call")
	}
	after, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("existing report was overwritten")
	}
}

func TestGuidedLaterFailureRetainsEarlierStages(t *testing.T) {
	input, _ := guidedCLIInput(t)
	for _, failedStage := range []int{1, 2} {
		t.Run([]string{"", "source-check", "body-observations"}[failedStage], func(t *testing.T) {
			answers := append([]string{}, guidedCLIAnswers...)
			answers[failedStage] = `{"unexpected":"retain invalid final for inspection"}`
			llm := &guidedTestLLM{answers: answers}
			factory := func(context.Context, string, string) (*sourcepilot.GuidedReviewer, error) {
				return sourcepilot.NewGuidedReviewer(llm)
			}
			out := filepath.Join(sourceCLIPrivateDir(t), "partial.json")
			var stdout bytes.Buffer
			err := runGuidedWithContext(t.Context(), append(guidedCLIArgs(input, out), "-format", "json"), &stdout, io.Discard, factory)
			if err == nil {
				t.Fatal("invalid later stage was accepted")
			}
			var report guidedRunReport
			if json.Unmarshal(stdout.Bytes(), &report) != nil || report.Status != "failed" || report.ErrorCode != "review_failed" || len(report.Result.Claims) != 1 || len(report.Result.Assessments) != failedStage-1 || len(report.Result.Attempts) != failedStage+1 || llm.calls != failedStage+1 {
				t.Fatal("later failure discarded earlier results or retried")
			}
			if report.Result.Attempts[failedStage].RawText != answers[failedStage] || report.Result.HumanReview != "not_reviewed" {
				t.Fatal("partial final text or authority boundary was lost")
			}
		})
	}
}
