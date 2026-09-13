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

const briefCLIAnswer = "付款 API 已恢復，退款 API 仍有延遲。"

func briefCLIInput(t *testing.T) (string, sourcepilot.BriefSource) {
	t.Helper()
	source := sourcepilot.BriefSource{Version: sourcepilot.BriefSourceVersion, SourceKind: "public_event", SourceID: "synthetic-service", SourceRevision: "revision-1",
		SourceURL: "https://example.invalid/synthetic-service", ObservedAt: "2026-09-10T01:00:00Z",
		Coverage: "full_document", Limitations: []string{}, Body: "Payment API recovered at 14:20 UTC.<br />Refund API still had delays."}
	return briefPrivateJSON(t, source), source
}

func briefPrivateJSON(t *testing.T, value any) string {
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

func briefCLIArgs(input, out string) []string {
	return []string{"run", "-input", input, "-model", "synthetic-brief", "-base-url", "http://127.0.0.1:11434/v1", "-out", out}
}

func TestBriefInspectOfflineAndHelp(t *testing.T) {
	input, source := briefCLIInput(t)
	for _, format := range []string{"text", "json"} {
		var stdout, stderr bytes.Buffer
		if err := runBriefWithContext(t.Context(), []string{"inspect", "-input", input, "-format", format}, &stdout, &stderr, nil); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 {
			t.Fatal("inspect exposed diagnostics")
		}
		if format == "json" {
			var report sourcepilot.BriefReport
			if json.Unmarshal(stdout.Bytes(), &report) != nil || !reflect.DeepEqual(report.Source, source) || report.Stage != "inspected" || report.HumanReview != "not_reviewed" || report.AuthorityEffect != "none" || report.ReferenceScope != "complete_input_not_claim_support" || report.RawText != "" || report.Text != "" {
				t.Fatal("inspection changed source or represented model or human work")
			}
		} else if !strings.Contains(stdout.String(), source.Body) {
			t.Fatal("inspection omitted complete original source")
		}
	}
	for _, arg := range []string{"--help", "-h", "--version"} {
		var stdout, stderr bytes.Buffer
		if err := runBrief([]string{arg}, &stdout, &stderr); err != nil || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatal("offline help or version failed", err)
		}
	}
}

func TestBriefCLIRejectsEngineeringAndUnclassifiedSourcesBeforeModelFactory(t *testing.T) {
	for _, kind := range []string{"", "unknown", "engineering_document", "repository_code", "git_commit"} {
		t.Run(kind, func(t *testing.T) {
			_, source := briefCLIInput(t)
			source.SourceKind = kind
			if kind == "" {
				source.Version = "detective-brief-source/v1"
			}
			input := briefPrivateJSON(t, source)
			out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			calls := 0
			factory := func(context.Context, string, string) (*sourcepilot.BriefExtractor, error) {
				calls++
				return nil, errors.New("unexpected model factory")
			}
			var stdout, stderr bytes.Buffer
			if err := runBriefWithContext(t.Context(), briefCLIArgs(input, out), &stdout, &stderr, factory); err == nil || calls != 0 {
				t.Fatal("unsupported source reached model factory")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("rejected source created an output file")
			}
		})
	}
}

func TestBriefPreflightNeverCreatesModel(t *testing.T) {
	input, _ := briefCLIInput(t)
	t.Setenv("DETECTIVE_MODEL", "must-not-default")
	t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:1/v1")
	for _, kind := range []string{"missing-input", "missing-model", "missing-url", "missing-out", "duplicate", "unsupported", "positional", "format", "timeout-zero", "timeout-large", "external-url", "credential-url", "localhost", "query-url", "model-control", "cancelled", "nil-context", "nil-factory", "relative-input", "input-permissions", "input-symlink", "input-json", "input-oversized", "existing-output", "output-permissions", "output-symlink"} {
		t.Run(kind, func(t *testing.T) {
			out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			args := briefCLIArgs(input, out)
			ctx := t.Context()
			calls := 0
			factory := briefFactory(func(context.Context, string, string) (*sourcepilot.BriefExtractor, error) {
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
				args = append(args, "-model", "synthetic-brief")
			case "unsupported":
				args = append(args, "-admit", "true")
			case "positional":
				args = append(args, "unexpected")
			case "format":
				args = append(args, "-format", "html")
			case "timeout-zero":
				args = append(args, "-timeout", "0")
			case "timeout-large":
				args = append(args, "-timeout", "121s")
			case "external-url":
				args[6] = "https://example.invalid/v1"
			case "credential-url":
				args[6] = "http://user:private-secret@127.0.0.1:11434/v1"
			case "localhost":
				args[6] = "http://localhost:11434/v1"
			case "query-url":
				args[6] = "http://127.0.0.1:11434/v1?token=private-secret"
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
				args[2] = "source.json"
			case "input-permissions", "input-symlink", "input-json", "input-oversized":
				path := filepath.Join(sourceCLIPrivateDir(t), "source.json")
				if kind == "input-symlink" {
					if err := os.Symlink(input, path); err != nil {
						t.Fatal(err)
					}
				} else {
					mode := os.FileMode(0o600)
					if kind == "input-permissions" {
						mode = 0o644
					}
					value := []byte(`{"body":"private-diagnostic"}`)
					if kind == "input-oversized" {
						value = bytes.Repeat([]byte(" "), (64<<10)+1)
					}
					if err := os.WriteFile(path, value, mode); err != nil {
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
			if err := runBriefWithContext(ctx, args, &stdout, &stderr, factory); err == nil || calls != 0 || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "private-diagnostic") {
				t.Fatal("preflight ran model, accepted invalid input or exposed content", err)
			}
			if kind == "existing-output" {
				body, err := os.ReadFile(out)
				if err != nil || string(body) != "retain-original" {
					t.Fatal("existing output changed")
				}
			} else if kind != "output-symlink" {
				if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed preflight reserved an output")
				}
			}
		})
	}
}

func TestBriefRunSavesOnceAndNeverOverwrites(t *testing.T) {
	input, source := briefCLIInput(t)
	out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
	llm := &guidedTestLLM{answers: []string{briefCLIAnswer}}
	calls := 0
	factory := func(ctx context.Context, modelName, baseURL string) (*sourcepilot.BriefExtractor, error) {
		calls++
		deadline, ok := ctx.Deadline()
		info, err := os.Stat(out)
		if !ok || time.Until(deadline) > 2*time.Minute || modelName != "synthetic-brief" || baseURL != "http://127.0.0.1:11434/v1" || err != nil || info.Size() != 0 || info.Mode().Perm() != 0o600 {
			t.Fatal("factory ran without exact settings, deadline and reserved private output")
		}
		return sourcepilot.NewBriefExtractor(llm)
	}
	var stdout, stderr bytes.Buffer
	args := append(briefCLIArgs(input, out), "-format", "json")
	if err := runBriefWithContext(t.Context(), args, &stdout, &stderr, factory); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	report, err := parseBriefRun(saved)
	var shown briefRunReport
	if err != nil || json.Unmarshal(stdout.Bytes(), &shown) != nil || !reflect.DeepEqual(shown, report) || report.Status != "completed_unreviewed" || report.Result.Text != briefCLIAnswer || !reflect.DeepEqual(report.Result.Source, source) || calls != 1 || llm.calls != 1 || stderr.Len() != 0 {
		t.Fatal("successful run lost source, result or one-attempt boundary", err)
	}
	if err := runBriefWithContext(t.Context(), args, io.Discard, io.Discard, factory); err == nil || calls != 1 || llm.calls != 1 {
		t.Fatal("rerun overwrote saved result or called model")
	}
	after, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(saved, after) {
		t.Fatal("saved report changed")
	}
}

type briefBrokenWriter struct{}

func (briefBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBriefFailureStillSavesOriginalSource(t *testing.T) {
	for _, kind := range []string{"factory-error", "factory-nil", "model-error", "invalid-final", "cancelled", "terminal-output"} {
		t.Run(kind, func(t *testing.T) {
			input, source := briefCLIInput(t)
			out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			llm := &guidedTestLLM{answers: []string{briefCLIAnswer}}
			if kind == "model-error" {
				llm.err = errors.New("private-upstream-diagnostic")
			}
			if kind == "invalid-final" {
				llm.answers = []string{"\x1b[2J"}
			}
			if kind == "cancelled" {
				llm.before = func(int) { cancel() }
			}
			factory := func(context.Context, string, string) (*sourcepilot.BriefExtractor, error) {
				if kind == "factory-error" {
					return nil, errors.New("private-upstream-diagnostic")
				}
				if kind == "factory-nil" {
					return nil, nil
				}
				return sourcepilot.NewBriefExtractor(llm)
			}
			var stdout, stderr bytes.Buffer
			var writer io.Writer = &stdout
			if kind == "terminal-output" {
				writer = briefBrokenWriter{}
			}
			err := runBriefWithContext(ctx, append(briefCLIArgs(input, out), "-format", "json"), writer, &stderr, factory)
			if err == nil || strings.Contains(err.Error(), "private-upstream") || strings.Contains(stdout.String(), "private-upstream") || stderr.Len() != 0 || llm.calls > 1 {
				t.Fatal("failure exposed diagnostics, retried or returned success", err)
			}
			raw, readErr := os.ReadFile(out)
			if readErr != nil || bytes.Contains(raw, []byte("private-upstream")) {
				t.Fatal("failure report missing or contains upstream diagnostics")
			}
			report, parseErr := parseBriefRun(raw)
			if parseErr != nil || !reflect.DeepEqual(report.Result.Source, source) || report.Result.HumanReview != "not_reviewed" || report.Result.AuthorityEffect != "none" {
				t.Fatal("failure report lost original source or cannot be reread", parseErr)
			}
			if kind == "terminal-output" {
				if report.Status != "completed_unreviewed" || !strings.Contains(err.Error(), "is saved") {
					t.Fatal("terminal failure obscured safely saved completed report")
				}
			} else if report.Status != "failed" || report.ErrorCode == "" {
				t.Fatal("failed extraction presented as completed")
			}
			if kind == "invalid-final" && (report.Result.RawText != "\x1b[2J" || report.Result.Text != "") {
				t.Fatal("invalid final was repaired, discarded or used as summary")
			}
		})
	}
}
