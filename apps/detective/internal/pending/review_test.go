//go:build darwin || linux

package pending

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func reviewBundleFixture(t *testing.T) ReviewBundle {
	t.Helper()
	body, err := os.ReadFile("testdata/source_review.json")
	if err != nil {
		t.Fatal(err)
	}
	var bundle ReviewBundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		t.Fatal(err)
	}
	if err := bundle.validate(); err != nil {
		t.Fatalf("synthetic native review fixture: %v", err)
	}
	return bundle
}

func saveReviewTestJSON(t *testing.T, path string, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestReviewDecisionLocalOnlyRoundTripAndNoReplace(t *testing.T) {
	for _, outcome := range []string{"admit", "audit_only", "reject", "pending"} {
		t.Run(outcome, func(t *testing.T) {
			bundle := reviewBundleFixture(t)
			directory := checkpointDirectory(t)
			reviewPath := filepath.Join(directory, "review.json")
			original := saveReviewTestJSON(t, reviewPath, bundle)
			loaded, err := LoadReview(reviewPath)
			if err != nil || !reflect.DeepEqual(loaded, bundle) {
				t.Fatalf("load exact review: %v", err)
			}
			path := filepath.Join(directory, "decision.json")
			reason := "候選保留引用所述的合成測試範圍，沒有擴張成正式部署。"
			decision, err := RecordReviewDecision(loaded, outcome, reason, loaded.Review.Display.ID, path)
			if err != nil || decision.Decision != outcome || decision.Reason != reason || decision.Digest == "" {
				t.Fatalf("save explicit local intent: %v", err)
			}
			var saved ReviewDecision
			if err := readReviewFile(path, &saved); err != nil || !reflect.DeepEqual(saved, decision) || saved.validate() != nil {
				t.Fatalf("decision did not preserve its complete exact inputs: %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RecordReviewDecision(bundle, outcome, reason, bundle.Review.Display.ID, path); !errors.Is(err, ErrExists) {
				t.Fatalf("existing decision replaced: %v", err)
			}
			after, _ := os.ReadFile(path)
			reviewAfter, _ := os.ReadFile(reviewPath)
			if !bytes.Equal(before, after) || !bytes.Equal(original, reviewAfter) {
				t.Fatal("recording or retry changed an existing decision/review file")
			}
			if outcome == "pending" {
				result, err := ApplyReviewDecision(t.Context(), path, "/must-not-start/reviewer", "/must-not-start/query", bundle.Review.Display.ID)
				if !errors.Is(err, ErrReviewDecisionNotExecutable) || !reflect.DeepEqual(result, ReviewExecution{}) {
					t.Fatalf("local intent reached MCP or invented execution: %v", err)
				}
			}
			for _, name := range []string{path, path + ".lock"} {
				info, err := os.Stat(name)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatal("saved decision/lock not private")
				}
			}
		})
	}
}

func TestReviewDecisionRejectsImplicitOrInvalidInputBeforeSave(t *testing.T) {
	for _, name := range []string{"empty_decision", "continue", "approved", "whitespace_decision", "empty_reason", "leading_reason", "trailing_reason", "nul_reason", "invalid_utf8", "oversize_reason", "wrong_confirmation", "empty_confirmation", "changed_review"} {
		t.Run(name, func(t *testing.T) {
			bundle := reviewBundleFixture(t)
			decision, reason, confirmation := "admit", "引用與候選的範圍一致。", bundle.Review.Display.ID
			switch name {
			case "empty_decision":
				decision = ""
			case "continue":
				decision = "continue"
			case "approved":
				decision = "approved"
			case "whitespace_decision":
				decision = " admit"
			case "empty_reason":
				reason = ""
			case "leading_reason":
				reason = " " + reason
			case "trailing_reason":
				reason += "\n"
			case "nul_reason":
				reason += "\x00"
			case "invalid_utf8":
				reason += string([]byte{0xff})
			case "oversize_reason":
				reason = strings.Repeat("臺", 667)
			case "wrong_confirmation":
				confirmation += "x"
			case "empty_confirmation":
				confirmation = ""
			case "changed_review":
				bundle.Review.Display.PayloadUTF8 += " "
			}
			directory := checkpointDirectory(t)
			result, err := RecordReviewDecision(bundle, decision, reason, confirmation, filepath.Join(directory, "decision.json"))
			entries, readErr := os.ReadDir(directory)
			if err == nil || !reflect.DeepEqual(result, ReviewDecision{}) || readErr != nil || len(entries) != 0 {
				t.Fatal("invalid or implicit decision saved data or produced a success result")
			}
		})
	}
}

func TestReviewArtifactClosedSchemaAndChangedDecision(t *testing.T) {
	bundle := reviewBundleFixture(t)
	original, _ := json.Marshal(bundle)
	for _, name := range []string{"duplicate", "nested_duplicate", "unknown", "missing", "alias", "trailing", "invalid_utf8", "oversized", "digest", "payload", "checkpoint", "malformed", "deep"} {
		t.Run(name, func(t *testing.T) {
			body := append([]byte(nil), original...)
			switch name {
			case "duplicate":
				body = append([]byte(`{"schema_version":"duplicate",`), body[1:]...)
			case "nested_duplicate":
				body = bytes.Replace(body, []byte(`"review":{`), []byte(`"review":{"contract_version":"duplicate",`), 1)
			case "unknown":
				body = append([]byte(`{"approved":true,`), body[1:]...)
			case "missing":
				body = bytes.Replace(body, []byte(`"schema_version":"`+reviewBundleVersion+`",`), nil, 1)
			case "alias":
				body = bytes.Replace(body, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
			case "trailing":
				body = append(body, []byte(" {}")...)
			case "invalid_utf8":
				body = append(body, 0xff)
			case "oversized":
				body = bytes.Repeat([]byte{' '}, maxReviewFileBytes+1)
			case "digest":
				body = bytes.Replace(body, []byte(bundle.Digest), []byte("sha256:"+strings.Repeat("0", 64)), 1)
			case "payload":
				body = bytes.Replace(body, []byte(`"payload_utf8":"`), []byte(`"payload_utf8":"changed `), 1)
			case "checkpoint":
				body = bytes.Replace(body, []byte(`"raw_text":"`), []byte(`"raw_text":"changed `), 1)
			case "malformed":
				body = []byte(`{"review":`)
			case "deep":
				body = []byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66))
			}
			path := filepath.Join(checkpointDirectory(t), "review.json")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := LoadReview(path)
			if err == nil || !reflect.DeepEqual(result, ReviewBundle{}) {
				t.Fatal("malformed review accepted")
			}
		})
	}
	path := filepath.Join(checkpointDirectory(t), "decision.json")
	decision, err := RecordReviewDecision(bundle, "admit", "引用支持限定範圍。", bundle.Review.Display.ID, path)
	if err != nil {
		t.Fatal(err)
	}
	decision.Reason = "偷偷改過的理由"
	saveReviewTestJSON(t, path, decision)
	result, err := ApplyReviewDecision(t.Context(), path, "/must-not-start/reviewer", "/must-not-start/query", bundle.Review.Display.ID)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") || !reflect.DeepEqual(result, ReviewExecution{}) {
		t.Fatal("changed decision reached writer or returned execution")
	}
}

func TestReviewFilesRejectUnsafeCoordinates(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "shared_parent", "shared_file", "relative", "missing"} {
		t.Run(kind, func(t *testing.T) {
			directory := checkpointDirectory(t)
			path := filepath.Join(directory, "review.json")
			body, _ := json.Marshal(reviewBundleFixture(t))
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(directory, "target.json")
				if err := os.WriteFile(target, body, 0o600); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				}
				if err := link(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "relative":
				path = "review.json"
			case "missing":
			default:
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
				if kind == "shared_parent" {
					if err := os.Chmod(directory, 0o750); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Chmod(path, 0o640); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := LoadReview(path); err == nil {
				t.Fatal("unsafe review file accepted")
			}
		})
	}
}

func TestReviewDisplayCompleteAndNoImplicitApproval(t *testing.T) {
	bundle := reviewBundleFixture(t)
	var output bytes.Buffer
	if err := WriteReviewText(&output, bundle); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{strconv.Quote(bundle.Review.Display.PayloadUTF8), bundle.Review.Display.ID, "有來源", "沒有預設核准", "明確 apply 可執行 admit、audit_only 或 reject", "不驗證真人身分"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("incomplete review display: missing %q", expected)
		}
	}
	if err := WriteReviewText(inspectFailWriter{}, bundle); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("display failure suppressed")
	}
	bundle.Review.Display.PayloadUTF8 += "\x1b[2J"
	output.Reset()
	if err := WriteReviewText(&output, bundle); err == nil || output.Len() != 0 {
		t.Fatal("altered review emitted partial display")
	}
}

func TestReviewPreparationExistingOutputAndCanceledOperations(t *testing.T) {
	bundle := reviewBundleFixture(t)
	directory := checkpointDirectory(t)
	cp, receipt, out := filepath.Join(directory, "checkpoint.json"), filepath.Join(directory, "receipt.json"), filepath.Join(directory, "review.json")
	saveReviewTestJSON(t, cp, bundle.Checkpoint)
	saveReviewTestJSON(t, receipt, bundle.Receipt)
	original := saveReviewTestJSON(t, out, bundle)
	result, err := PrepareReview(t.Context(), cp, receipt, "/must-not-start/review", "/must-not-start/query", out)
	if !errors.Is(err, ErrExists) || !reflect.DeepEqual(result, ReviewBundle{}) {
		t.Fatalf("existing review reached MCP: %v", err)
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(original, after) {
		t.Fatal("existing review overwritten")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := PrepareReview(ctx, "/missing", "/missing", "/missing", "/missing", "/missing"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled prepare performed IO")
	}
	if _, err := ApplyReviewDecision(ctx, "/missing", "/missing", "/missing", "missing"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled apply performed IO")
	}
}
