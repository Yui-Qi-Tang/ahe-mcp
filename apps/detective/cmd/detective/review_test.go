package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func TestReviewDispatchRejectsUnknownMode(t *testing.T) {
	for _, args := range [][]string{{"review"}, {"review", "unknown-private-token"}, {"review", "-version"}} {
		var stdout, stderr bytes.Buffer
		err := run(args, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "review requires") || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("unexpected review dispatch result: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		if strings.Contains(err.Error(), "unknown-private-token") {
			t.Fatal("unknown mode was echoed")
		}
	}
}

func TestReviewClosedFlagSets(t *testing.T) {
	for _, mode := range []string{"prepare", "decide", "apply"} {
		for _, extra := range [][]string{
			{"-input", "private-source-marker"}, {"-model", "private-model-marker"},
			{"-ahe-submit-pending=true"}, {"-version=false"}, {"-ahe-inspect", "/private-source-marker"},
			{"extra-private-positional"}, {"--", "extra-private-positional"},
			{"-unknown-private-marker=value"},
		} {
			t.Run(mode+"/"+strings.Join(extra, " "), func(t *testing.T) {
				args := append(reviewModeArguments(mode), extra...)
				var stdout, stderr bytes.Buffer
				err := runReview(args, strings.NewReader(""), &stdout, &stderr)
				if err == nil || !strings.Contains(err.Error(), "review") || stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatalf("invalid flags were not rejected before I/O: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
				if strings.Contains(err.Error(), "private") {
					t.Fatal("argument error echoed private input")
				}
			})
		}
	}
	for _, tt := range []struct {
		mode  string
		extra []string
	}{
		{"prepare", []string{"-decision", "/other"}},
		{"prepare", []string{"-confirm-display", "confirmation"}},
		{"decide", []string{"-review-command", "/other"}},
		{"decide", []string{"-query-command", "/other"}},
		{"decide", []string{"-timeout", "1s"}},
		{"decide", []string{"-confirm-display", "confirmation"}},
		{"apply", []string{"-review", "/other"}},
		{"apply", []string{"-out", "/other"}},
	} {
		t.Run(tt.mode+"/mode-specific/"+strings.Join(tt.extra, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runReview(append(reviewModeArguments(tt.mode), tt.extra...), strings.NewReader(""), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "supported by the selected mode") || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("cross-mode flag reached I/O: %v", err)
			}
		})
	}
}

func TestReviewRejectsRepeatedFlags(t *testing.T) {
	for _, mode := range []string{"prepare", "decide", "apply"} {
		args := reviewModeArguments(mode)
		for _, duplicate := range [][]string{{args[1], args[2]}, {"-" + args[1] + "=" + args[2]}} {
			t.Run(mode+"/"+strings.Join(duplicate, " "), func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := runReview(append(append([]string(nil), args...), duplicate...), strings.NewReader(""), &stdout, &stderr)
				if err == nil || !strings.Contains(err.Error(), "must not repeat") || stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatalf("duplicate flag was accepted: %v", err)
				}
			})
		}
	}
}

func TestReviewRequiresExplicitPathsAndPositiveTimeout(t *testing.T) {
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/ambient-intake-must-not-be-used")
	t.Setenv("DETECTIVE_MODEL", "ambient-model-must-not-be-used")
	for _, mode := range []string{"prepare", "decide", "apply"} {
		args := reviewModeArguments(mode)
		for i := 1; i < len(args); i += 2 {
			if args[i] == "-confirm-display" {
				continue
			}
			for _, value := range []string{"", " ", "relative/private-marker"} {
				t.Run(mode+"/"+args[i]+"/"+value, func(t *testing.T) {
					invalid := append([]string(nil), args...)
					invalid[i+1] = value
					var stdout, stderr bytes.Buffer
					err := runReview(invalid, strings.NewReader(""), &stdout, &stderr)
					if err == nil || !strings.Contains(err.Error(), "explicit nonempty absolute") || stdout.Len() != 0 || stderr.Len() != 0 {
						t.Fatalf("invalid path was not rejected before I/O: %v", err)
					}
				})
			}
		}
	}
	for _, mode := range []string{"prepare", "apply"} {
		for _, timeout := range []string{"0", "-1s", "private-invalid-duration"} {
			t.Run(mode+"/timeout/"+timeout, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := runReview(append(reviewModeArguments(mode), "-timeout", timeout), strings.NewReader(""), &stdout, &stderr)
				if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "private-invalid") {
					t.Fatalf("invalid duration was not safely rejected: %v", err)
				}
			})
		}
	}
	for _, confirmation := range []string{"", " "} {
		args := reviewModeArguments("apply")
		args[len(args)-1] = confirmation
		var stdout, stderr bytes.Buffer
		err := runReview(args, strings.NewReader(""), &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "complete confirmed display ID") || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("blank confirmation reached file or launcher access: %v", err)
		}
	}
}

func TestReviewIOErrorsDoNotEchoPrivateArguments(t *testing.T) {
	for _, mode := range []string{"prepare", "decide", "apply"} {
		t.Run(mode, func(t *testing.T) {
			args := reviewModeArguments(mode)
			args[2] = "/missing/private-argument-marker"
			var stdout, stderr bytes.Buffer
			err := runReview(args, strings.NewReader(""), &stdout, &stderr)
			if err == nil || strings.Contains(err.Error(), "private-argument-marker") || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("read failure leaked input or produced success: %v", err)
			}
			if mode == "apply" && (!strings.Contains(err.Error(), "可能已提交") || !strings.Contains(err.Error(), "相同決策")) {
				t.Fatal("apply failure omitted uncertain-outcome recovery instructions")
			}
		})
	}
}

func TestReviewDecisionInputHasNoDefaultAndPreservesReason(t *testing.T) {
	const displayID = "review-display-v1:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const reason = "引用只說合成資料測試通過；採納範圍不包含正式部署。"
	for _, decision := range []string{"admit", "audit_only", "reject", "pending"} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(decision+"/"+strings.TrimSpace(newline), func(t *testing.T) {
				var prompts bytes.Buffer
				input := strings.Join([]string{decision, reason, displayID, ""}, newline)
				gotDecision, gotReason, confirmed, err := readReviewDecision(strings.NewReader(input), &prompts, displayID)
				if err != nil || gotDecision != decision || gotReason != reason || confirmed != displayID {
					t.Fatalf("decision input changed: decision=%q reason=%q confirmed=%q err=%v", gotDecision, gotReason, confirmed, err)
				}
				for _, required := range []string{"無預設", "具體理由", "精確引用", "適用範圍", "完整 display ID", "不呼叫 MCP", "真人身分認證", "2000 UTF-8 bytes"} {
					if !strings.Contains(prompts.String(), required) {
						t.Errorf("prompt omitted %q", required)
					}
				}
				if strings.Contains(prompts.String(), reason) {
					t.Fatal("prompt echoed the supplied private reason")
				}
			})
		}
	}
}

func TestReviewDecisionStopsOnIncompleteOrInvalidInput(t *testing.T) {
	const displayID = "display:exact"
	for _, tt := range []struct {
		name  string
		input string
	}{
		{"empty decision", "\n"}, {"blank decision", " \n"}, {"decision eof", "admit"},
		{"unknown decision", "approved\n"}, {"decision case alias", "ADMIT\n"},
		{"decision whitespace", "admit \n"}, {"decision too long", strings.Repeat("a", 33) + "\n"},
		{"empty reason", "admit\n\n"}, {"blank reason", "admit\n \n"},
		{"reason eof", "admit\nprivate-reason"}, {"reason over bytes", "admit\n" + strings.Repeat("界", 667) + "\n"},
		{"reason leading space", "admit\n private-reason\n"}, {"reason trailing space", "admit\nprivate-reason \n"},
		{"reason invalid utf8", "admit\n\xff\n"}, {"reason nul", "admit\nprivate\x00reason\n"},
		{"unbounded reason", "admit\n" + strings.Repeat("x", 20000)},
		{"empty confirmation", "admit\nprivate-reason\n\n"},
		{"confirmation eof", "admit\nprivate-reason\n" + displayID},
		{"confirmation abbreviation", "admit\nprivate-reason\ndisplay\n"},
		{"confirmation mismatch", "admit\nprivate-reason\ndisplay:other\n"},
		{"confirmation too long", "admit\nprivate-reason\n" + strings.Repeat("x", 257) + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var prompts bytes.Buffer
			decision, reason, confirmation, err := readReviewDecision(strings.NewReader(tt.input), &prompts, displayID)
			if err == nil || decision != "" || reason != "" || confirmation != "" {
				t.Fatalf("invalid input produced a decision: %q %q %q %v", decision, reason, confirmation, err)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(prompts.String(), "private") {
				t.Fatal("invalid input was echoed")
			}
		})
	}
}

func TestReviewReasonByteLimitDoesNotJudgeMeaning(t *testing.T) {
	for _, reason := range []string{strings.Repeat("界", 666) + "ok", "很厲害"} {
		decision, gotReason, confirmation, err := readReviewDecision(strings.NewReader("admit\n"+reason+"\ndisplay:exact\n"), io.Discard, "display:exact")
		if err != nil || decision != "admit" || gotReason != reason || confirmation != "display:exact" {
			t.Fatalf("valid reason syntax was rejected: %v", err)
		}
	}
}

func TestReviewPromptFailureStopsInput(t *testing.T) {
	input := strings.NewReader("admit\nprivate-reason\ndisplay:exact\n")
	decision, reason, confirmation, err := readReviewDecision(input, failingReviewWriter{}, "display:exact")
	if err == nil || decision != "" || reason != "" || confirmation != "" || input.Len() != len("admit\nprivate-reason\ndisplay:exact\n") {
		t.Fatalf("failed display consumed input or produced decision: %v", err)
	}
	if strings.Contains(err.Error(), "private-writer-marker") {
		t.Fatal("writer diagnostic was echoed")
	}
}

func TestReviewDecideRecordsOnlyLocalSummary(t *testing.T) {
	t.Setenv("DETECTIVE_MODEL", "")
	t.Setenv("DETECTIVE_BASE_URL", "")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/must-not-start-an-intake")
	for _, decision := range []string{"admit", "audit_only", "reject", "pending"} {
		t.Run(decision, func(t *testing.T) {
			path, bundle := reviewCLIFileFixture(t)
			outPath := filepath.Join(filepath.Dir(path), "decision.json")
			const reason = "CLI-private-reason：引用與候選範圍已對照；本測試不判斷語意正確。"
			input := decision + "\n" + reason + "\n" + bundle.Review.Display.ID + "\n"
			var stdout, stderr, display bytes.Buffer
			if err := pending.WriteReviewText(&display, bundle); err != nil {
				t.Fatal(err)
			}
			if err := runReview([]string{"decide", "-review", path, "-out", outPath}, strings.NewReader(input), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(stderr.Bytes(), display.Bytes()) || display.Len() == 0 {
				t.Fatal("decide omitted or replaced part of the full review display")
			}
			if strings.Contains(stderr.String(), reason) || strings.Contains(stdout.String(), reason) {
				t.Fatal("private reason escaped into command output")
			}
			var summary reviewDecisionRecorded
			if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
				t.Fatalf("stdout is not one valid summary: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil || len(fields) != 7 {
				t.Fatalf("stdout is not the closed seven-field summary: %v", err)
			}
			wantNext := "not_executable_by_current_reviewer"
			if decision != "pending" {
				wantNext = "explicit_review_apply"
			}
			if summary.SchemaVersion != "detective-review-decision-recorded/v1" || summary.State != "decision_recorded_locally" || summary.Decision != decision || summary.AuthorityEffect != "none" || summary.ReviewDisplayID != bundle.Review.Display.ID || summary.NextAction != wantNext {
				t.Fatalf("incorrect local-only summary: %+v", summary)
			}
			saved, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			var recorded pending.ReviewDecision
			if err := json.Unmarshal(saved, &recorded); err != nil {
				t.Fatal(err)
			}
			if recorded.Decision != decision || recorded.Reason != reason || recorded.Digest == "" || summary.Digest != recorded.Digest || recorded.ConfirmedDisplayID != bundle.Review.Display.ID || !reflect.DeepEqual(recorded.ReviewBundle, bundle) {
				t.Fatal("private decision omitted or changed the approved local inputs")
			}
			info, err := os.Stat(outPath)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("decision file is not private: %v", err)
			}
			if _, err := pending.LoadReview(path); err != nil {
				t.Fatalf("decide damaged the saved review: %v", err)
			}
		})
	}
}

func TestReviewDecideStopsWithoutCreatingDecision(t *testing.T) {
	for _, input := range []string{"", "\n", "admit\n\n", "admit\nprivate-reason\nwrong-display\n"} {
		t.Run(strings.ReplaceAll(input, "\n", "/"), func(t *testing.T) {
			path, _ := reviewCLIFileFixture(t)
			outPath := filepath.Join(filepath.Dir(path), "decision.json")
			before, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if err := runReview([]string{"decide", "-review", path, "-out", outPath}, strings.NewReader(input), &stdout, &stderr); err == nil || stdout.Len() != 0 {
				t.Fatal("incomplete decision produced a success result")
			}
			after, err := os.ReadDir(filepath.Dir(path))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("incomplete input changed local files: %v", err)
			}
		})
	}
}

func TestReviewDecideDisplayFailureDoesNotReadOrSave(t *testing.T) {
	path, _ := reviewCLIFileFixture(t)
	outPath := filepath.Join(filepath.Dir(path), "decision.json")
	input := strings.NewReader("admit\nprivate-reason\nnot-read\n")
	originalLength := input.Len()
	var stdout bytes.Buffer
	err := runReview([]string{"decide", "-review", path, "-out", outPath}, input, &stdout, failingReviewWriter{})
	if err == nil || input.Len() != originalLength || stdout.Len() != 0 {
		t.Fatal("failed complete display permitted decision input")
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("display failure created a decision file: %v", err)
	}
}

func TestReviewApplyPendingIsExplicitlyLocalOnly(t *testing.T) {
	for _, decision := range []string{"pending"} {
		t.Run(decision, func(t *testing.T) {
			path, bundle := reviewCLIFileFixture(t)
			decisionPath := filepath.Join(filepath.Dir(path), "decision.json")
			if _, err := pending.RecordReviewDecision(bundle, decision, "合成案例只記錄本機意圖。", bundle.Review.Display.ID, decisionPath); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			err := runReview([]string{"apply", "-decision", decisionPath, "-review-command", "/must-not-start-reviewer", "-query-command", "/must-not-start-query", "-confirm-display", bundle.Review.Display.ID}, strings.NewReader(""), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "本機暫緩") || !strings.Contains(err.Error(), "未啟動任何 MCP") || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("unsupported remote intent was not explicit: %v", err)
			}
		})
	}
}

func reviewCLIFileFixture(t *testing.T) (string, pending.ReviewBundle) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("private review persistence is supported only on Darwin and Linux")
	}
	body, err := os.ReadFile("../../internal/pending/testdata/source_review.json")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "review.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := pending.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, bundle
}

type failingReviewWriter struct{}

func (failingReviewWriter) Write([]byte) (int, error) {
	return 0, errors.New("private-writer-marker")
}

func reviewModeArguments(mode string) []string {
	switch mode {
	case "prepare":
		return []string{mode, "-checkpoint", "/missing/checkpoint", "-receipt", "/missing/receipt", "-review-command", "/missing/reviewer", "-query-command", "/missing/query", "-out", "/missing/review"}
	case "decide":
		return []string{mode, "-review", "/missing/review", "-out", "/missing/decision"}
	default:
		return []string{mode, "-decision", "/missing/decision", "-review-command", "/missing/reviewer", "-query-command", "/missing/query", "-confirm-display", "display:exact"}
	}
}
