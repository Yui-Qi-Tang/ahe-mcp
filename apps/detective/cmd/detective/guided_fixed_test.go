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
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

const fixedCLIAnswer = `{"assessments":[{"claim":1,"relation":"supported","segments":[1],"explanation":"原文記載付款服務恢復。"},{"claim":2,"relation":"contradicted","segments":[2],"explanation":"原文仍記載退款服務延遲。"}]}`

func fixedCLIInput(t *testing.T) (string, sourcepilot.FixedClaims) {
	t.Helper()
	_, source := guidedCLIInput(t)
	input := sourcepilot.FixedClaims{Version: "detective-fixed-claims/v1", Source: source,
		Claims: []string{"Payment API recovered.", "Refund API recovered."}}
	return guidedPrivateJSON(t, input), input
}

func fixedCLIArgs(input, out string) []string {
	return []string{"check-fixed", "-input", input, "-model", "synthetic-guided", "-base-url", "http://127.0.0.1:11434/v1", "-out", out}
}

func TestGuidedFixedInspectOffline(t *testing.T) {
	path, input := fixedCLIInput(t)
	for _, format := range []string{"text", "json"} {
		var stdout, stderr bytes.Buffer
		if err := runGuidedWithContext(t.Context(), []string{"inspect-fixed", "-input", path, "-format", format}, &stdout, &stderr, nil); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 {
			t.Fatal("offline inspection emitted diagnostics")
		}
		if format == "json" {
			var report sourcepilot.FixedReport
			if json.Unmarshal(stdout.Bytes(), &report) != nil || !reflect.DeepEqual(report.Input, input) || report.Stage != "inspected" || report.ClaimsOrigin != "caller_supplied_unverified" || report.HumanReview != "not_reviewed" || report.AuthorityEffect != "none" || len(report.Attempts) != 0 {
				t.Fatal("fixed inspection changed caller input or implied approval")
			}
		} else if !strings.Contains(stdout.String(), "caller_supplied_unverified") || !strings.Contains(stdout.String(), input.Source.Body) || !strings.Contains(stdout.String(), input.Claims[1]) {
			t.Fatal("fixed inspection omitted original context or unverified boundary")
		}
	}
}

func TestGuidedFixedSingleCallAndOfflineRead(t *testing.T) {
	path, input := fixedCLIInput(t)
	out := filepath.Join(sourceCLIPrivateDir(t), "fixed-report.json")
	llm := &guidedTestLLM{answers: []string{fixedCLIAnswer}}
	calls := 0
	factory := func(ctx context.Context, modelName, baseURL string) (*sourcepilot.GuidedReviewer, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Minute || modelName != "synthetic-guided" || baseURL != "http://127.0.0.1:11434/v1" {
			t.Fatal("fixed run lost explicit coordinates or deadline")
		}
		// Input identity is the already-frozen content, not a later file edit.
		if err := os.WriteFile(path, []byte(`{"changed":"after validation"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return sourcepilot.NewGuidedReviewer(llm)
	}
	var stdout, stderr bytes.Buffer
	if err := runGuidedWithContext(t.Context(), append(fixedCLIArgs(path, out), "-format", "json"), &stdout, &stderr, factory); err != nil {
		t.Fatal(err)
	}
	var report fixedRunReport
	if json.Unmarshal(stdout.Bytes(), &report) != nil || report.Version != fixedRunVersion || report.Status != "structurally_validated" || report.ErrorCode != "" || report.Result.Stage != "complete" || !reflect.DeepEqual(report.Result.Input, input) || len(report.Result.Attempts) != 1 || len(report.Result.Assessments) != 2 || calls != 1 || llm.calls != 1 || stderr.Len() != 0 {
		t.Fatal("fixed check did not preserve input and single-call execution")
	}
	if report.Result.HumanReview != "not_reviewed" || report.Result.ClaimsOrigin != "caller_supplied_unverified" || report.Result.AuthorityEffect != "none" {
		t.Fatal("fixed input became approval")
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		stdout.Reset()
		if err := runGuidedWithContext(t.Context(), []string{"read", "-input", out, "-format", format}, &stdout, &stderr, factory); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || llm.calls != 1 {
			t.Fatal("saved fixed read called model")
		}
		if format == "text" && (!strings.Contains(stdout.String(), "離線重讀") || !strings.Contains(stdout.String(), input.Source.Body)) {
			t.Fatal("fixed read omitted boundary or source")
		}
	}
	bundle, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGuidedWithContext(t.Context(), fixedCLIArgs(path, out), io.Discard, io.Discard, factory); err == nil || calls != 1 || llm.calls != 1 {
		t.Fatal("existing fixed report caused another call")
	}
	after, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("fixed report was overwritten")
	}
}

func TestGuidedFixedPreflight(t *testing.T) {
	path, _ := fixedCLIInput(t)
	for _, kind := range []string{"missing-model", "missing-url", "missing-out", "duplicate", "timeout", "external-url", "auth-url", "unknown-flag", "input-json", "approved-input", "existing-output", "nil-factory", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			out := filepath.Join(sourceCLIPrivateDir(t), "fixed-report.json")
			args := fixedCLIArgs(path, out)
			ctx := t.Context()
			calls := 0
			factory := guidedFactory(func(context.Context, string, string) (*sourcepilot.GuidedReviewer, error) {
				calls++
				return nil, errors.New("unexpected factory")
			})
			switch kind {
			case "missing-model":
				args[4] = ""
			case "missing-url":
				args[6] = ""
			case "missing-out":
				args[8] = ""
			case "duplicate":
				args = append(args, "-model", "synthetic-guided")
			case "timeout":
				args = append(args, "-timeout", "3m")
			case "external-url":
				args[6] = "https://example.invalid/v1"
			case "auth-url":
				args[6] = "http://user:private-secret@127.0.0.1:11434/v1"
			case "unknown-flag":
				args = append(args, "-admit", "true")
			case "input-json":
				args[2] = guidedPrivateJSON(t, map[string]string{"unknown": "private-secret"})
			case "approved-input":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if json.Unmarshal(raw, &fields) != nil {
					t.Fatal("invalid test input")
				}
				fields["human_review"] = "approved"
				args[2] = guidedPrivateJSON(t, fields)
			case "existing-output":
				if err := os.WriteFile(out, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "nil-factory":
				factory = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var stdout, stderr bytes.Buffer
			err := runGuidedWithContext(ctx, args, &stdout, &stderr, factory)
			if err == nil || calls != 0 || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "private-secret") {
				t.Fatal("preflight caused an effect or leaked input")
			}
			if kind != "existing-output" {
				if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("preflight reserved output")
				}
			}
		})
	}
}

func TestGuidedFixedFailuresRetainReadableReport(t *testing.T) {
	path, input := fixedCLIInput(t)
	for _, kind := range []string{"factory", "nil", "transport", "invalid-final", "cancelled", "stdout"} {
		t.Run(kind, func(t *testing.T) {
			llm := &guidedTestLLM{answers: []string{fixedCLIAnswer}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			factory := func(context.Context, string, string) (*sourcepilot.GuidedReviewer, error) {
				switch kind {
				case "factory":
					return nil, errors.New("private-upstream-error")
				case "nil":
					return nil, nil
				case "transport":
					llm.err = errors.New("private-upstream-error")
				case "invalid-final":
					llm.answers = []string{`{"bad":"preserve invalid final"}`}
				case "cancelled":
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
			err := runGuidedWithContext(ctx, fixedCLIArgs(path, out), writer, &stderr, factory)
			if err == nil || llm.calls > 1 || strings.Contains(err.Error(), "private-upstream-error") || stderr.Len() != 0 {
				t.Fatal("failed fixed attempt repeated or leaked")
			}
			raw, readErr := os.ReadFile(out)
			var report fixedRunReport
			if readErr != nil || json.Unmarshal(raw, &report) != nil || !reflect.DeepEqual(report.Result.Input, input) || report.Result.AuthorityEffect != "none" || strings.Contains(string(raw), "private-upstream-error") {
				t.Fatal("failure did not preserve original fixed input")
			}
			if kind == "stdout" {
				if report.Status != "structurally_validated" || !strings.Contains(err.Error(), "saved but terminal") {
					t.Fatal("display error mislabeled completed execution")
				}
			} else if report.Status != "failed" || report.ErrorCode == "" {
				t.Fatal("failed attempt accepted")
			}
			if kind == "invalid-final" && (len(report.Result.Attempts) != 1 || report.Result.Attempts[0].RawText != llm.answers[0]) {
				t.Fatal("invalid final was discarded")
			}
			if err := runGuidedWithContext(t.Context(), []string{"read", "-input", out}, io.Discard, io.Discard, nil); err != nil {
				t.Fatal("saved failed fixed report cannot be inspected:", err)
			}
		})
	}
}
