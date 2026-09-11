//go:build darwin || linux

package pending

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const thinkingPrivateMarker = "private-thinking-only-marker: 不可當成引用或核准"

func thinkingJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func thinkingReadFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestReasonThinkingOnPreservesAdvisoryAndDecisionBoundaries(t *testing.T) {
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/must-not-start")
	t.Setenv("DETECTIVE_AHE_QUERY_COMMAND", "/must-not-start")
	for _, intent := range []string{"admit", "audit_only", "reject", "pending"} {
		t.Run(intent, func(t *testing.T) {
			path, decision := reasonDecisionFixture(t, intent, "僅記錄明確範圍；模型不得代替人工決定。")
			before := thinkingReadFile(t, path)
			envelope := reasonEnvelopeFixture(string(thinkingJSON(t, reasonReplyFixture())))
			envelope["message"].(map[string]any)["thinking"] = thinkingPrivateMarker
			captured := make(chan []byte, 1)
			server, calls := reasonServer(t, reasonInventoryFixture(), envelope, captured)
			out := filepath.Join(filepath.Dir(path), "assessment.json")
			assessment, err := AssessReviewReasonWithOptions(t.Context(), path, server.URL, reasonTestModel, out, ReasonAssessmentOptions{Think: true})
			if err != nil {
				t.Fatal(err)
			}
			wantThinking := &ReasonThinking{Mode: "on", Present: true, Bytes: len(thinkingPrivateMarker), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(thinkingPrivateMarker)))}
			if assessment.SchemaVersion != "detective-reason-assessment/v2" || assessment.PromptVersion != "detective-reason-adviser/v6" || !reflect.DeepEqual(assessment.Thinking, wantThinking) {
				t.Fatal("thinking mode was not bound to the versioned assessment metadata")
			}
			if calls.Load() != 1 || assessment.AuthorityEffect != "none" || assessment.DecisionDigest != decision.Digest || !reflect.DeepEqual(assessment.ReviewDecision, decision) {
				t.Fatal("thinking changed the original intent, authority, or single-request boundary")
			}
			var request map[string]json.RawMessage
			if err := json.Unmarshal(<-captured, &request); err != nil {
				t.Fatal(err)
			}
			if string(request["think"]) != "true" || request["tools"] != nil || string(request["stream"]) != "false" {
				t.Fatal("explicit thinking changed tool or streaming authority")
			}
			for _, concern := range assessment.Concerns {
				for _, anchor := range concern.Anchors {
					if anchor.Field != "claim" && anchor.Field != "source" && anchor.Field != "reason" {
						t.Fatal("thinking became an evidence anchor")
					}
				}
			}
			loaded, err := LoadReasonAssessment(out)
			if err != nil || !reflect.DeepEqual(loaded, assessment) {
				t.Fatalf("thinking assessment did not round trip exactly: %v", err)
			}
			var display bytes.Buffer
			if err := WriteReasonAssessmentText(&display, loaded); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(thinkingReadFile(t, out), []byte("private-thinking-only-marker")) || strings.Contains(display.String(), "private-thinking-only-marker") {
				t.Fatal("raw thinking was persisted or displayed")
			}
			if !bytes.Equal(before, thinkingReadFile(t, path)) {
				t.Fatal("advice rewrote the saved decision")
			}
			if _, err := LoadReviewDecision(out); err == nil {
				t.Fatal("thinking advice became an executable decision")
			}
			if _, err := AssessReviewReasonWithOptions(t.Context(), path, server.URL, reasonTestModel, out, ReasonAssessmentOptions{Think: true}); !errors.Is(err, ErrExists) || calls.Load() != 1 {
				t.Fatalf("existing output triggered another model request: %v", err)
			}
			info, err := os.Stat(out)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("thinking metadata was not saved in a private file")
			}
		})
	}
}

func TestReasonThinkingAbsentEmptyAndByteLimit(t *testing.T) {
	for _, tt := range []struct {
		name    string
		on      bool
		present bool
		value   string
	}{
		{name: "off_absent"},
		{name: "off_empty", present: true},
		{name: "on_absent", on: true},
		{name: "on_empty", on: true, present: true},
		{name: "on_exact_byte_limit", on: true, present: true, value: strings.Repeat("界", (64<<10)/3) + "a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _ := reasonDecisionFixture(t, "pending", "先暫停，待確認適用範圍。")
			envelope := reasonEnvelopeFixture(string(thinkingJSON(t, reasonReplyFixture())))
			if tt.present {
				envelope["message"].(map[string]any)["thinking"] = tt.value
			}
			captured := make(chan []byte, 1)
			server, calls := reasonServer(t, reasonInventoryFixture(), envelope, captured)
			out := filepath.Join(filepath.Dir(path), "assessment.json")
			assessment, err := AssessReviewReasonWithOptions(t.Context(), path, server.URL, reasonTestModel, out, ReasonAssessmentOptions{Think: tt.on})
			if err != nil {
				t.Fatal(err)
			}
			want := ReasonThinking{Mode: "off", Present: tt.present, Bytes: len(tt.value)}
			if tt.on {
				want.Mode = "on"
			}
			if tt.present {
				want.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(tt.value)))
			}
			if assessment.Thinking == nil || *assessment.Thinking != want || calls.Load() != 1 {
				t.Fatal("absent, empty, or bounded thinking metadata changed")
			}
			var request struct {
				Think bool `json:"think"`
			}
			if json.Unmarshal(<-captured, &request) != nil || request.Think != tt.on {
				t.Fatal("wire thinking mode differs from the explicit option")
			}
			if loaded, err := LoadReasonAssessment(out); err != nil || !reflect.DeepEqual(loaded, assessment) {
				t.Fatalf("empty or absent thinking failed offline readback: %v", err)
			}
		})
	}
}

func TestReasonThinkingLegacyAPIDefaultIsOff(t *testing.T) {
	for _, nonempty := range []bool{false, true} {
		t.Run(fmt.Sprint(nonempty), func(t *testing.T) {
			path, _ := reasonDecisionFixture(t, "admit", "只接受來源所述的測試範圍。")
			envelope := reasonEnvelopeFixture(string(thinkingJSON(t, reasonReplyFixture())))
			if nonempty {
				envelope["message"].(map[string]any)["thinking"] = thinkingPrivateMarker
			}
			captured := make(chan []byte, 1)
			server, calls := reasonServer(t, reasonInventoryFixture(), envelope, captured)
			out := filepath.Join(filepath.Dir(path), "assessment.json")
			assessment, err := AssessReviewReason(t.Context(), path, server.URL, reasonTestModel, out)
			var request map[string]json.RawMessage
			if json.Unmarshal(<-captured, &request) != nil || string(request["think"]) != "false" || calls.Load() != 1 {
				t.Fatal("the legacy API silently enabled thinking or retried")
			}
			if nonempty {
				if err == nil || ReasonAssessmentFailureStage(err) != "completion_envelope" || !reflect.DeepEqual(assessment, ReasonAssessment{}) {
					t.Fatal("default-off API accepted nonempty thinking")
				}
				if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rejected thinking published an assessment")
				}
			} else if err != nil || assessment.Thinking == nil || assessment.Thinking.Mode != "off" || assessment.SchemaVersion != "detective-reason-assessment/v2" {
				t.Fatalf("legacy API lost explicit default-off metadata: %v", err)
			}
		})
	}
}

func TestReasonThinkingDoesNotRelaxResponseValidation(t *testing.T) {
	for _, kind := range []string{"off_nonempty", "null", "number", "boolean", "array", "object", "nul", "oversize", "unfinished", "length", "malformed_final", "unknown_final_field", "thinking_anchor", "tool_call", "remote_origin"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := reasonDecisionFixture(t, "admit", "採納範圍只限來源所述。")
			before := thinkingReadFile(t, path)
			reply := reasonReplyFixture()
			if kind == "thinking_anchor" {
				reply.Concerns[0].AnchorIDs = []string{"a0001", "thinking"}
			}
			content := string(thinkingJSON(t, reply))
			if kind == "malformed_final" {
				content = strings.TrimSuffix(content, "}")
			}
			if kind == "unknown_final_field" {
				content = strings.TrimSuffix(content, "}") + `,"execute_writer":true}`
			}
			envelope := reasonEnvelopeFixture(content)
			message := envelope["message"].(map[string]any)
			message["thinking"] = thinkingPrivateMarker
			switch kind {
			case "null":
				message["thinking"] = nil
			case "number":
				message["thinking"] = 1
			case "boolean":
				message["thinking"] = true
			case "array":
				message["thinking"] = []string{}
			case "object":
				message["thinking"] = map[string]any{}
			case "nul":
				message["thinking"] = "private-thinking-only-marker\x00"
			case "oversize":
				message["thinking"] = strings.Repeat("x", (64<<10)+1)
			case "unfinished":
				envelope["done"] = false
			case "length":
				envelope["done_reason"] = "length"
			case "tool_call":
				message["tool_calls"] = []any{map[string]any{"function": "admit_reviewed_source_claim"}}
			case "remote_origin":
				envelope["remote_host"] = "https://cloud.invalid"
			}
			server, calls := reasonServer(t, reasonInventoryFixture(), envelope, nil)
			out := filepath.Join(filepath.Dir(path), "assessment.json")
			assessment, err := AssessReviewReasonWithOptions(t.Context(), path, server.URL, reasonTestModel, out, ReasonAssessmentOptions{Think: kind != "off_nonempty"})
			if err == nil || !reflect.DeepEqual(assessment, ReasonAssessment{}) || calls.Load() != 1 {
				t.Fatal("thinking bypassed validation, gained authority, or triggered a retry")
			}
			if strings.Contains(err.Error(), "private-thinking-only-marker") {
				t.Fatal("rejection disclosed raw thinking")
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid response published an assessment")
			}
			if !bytes.Equal(before, thinkingReadFile(t, path)) {
				t.Fatal("failed advice modified the saved decision")
			}
		})
	}
}

func TestReasonThinkingMetadataTamperingAndImpossibleCombinations(t *testing.T) {
	path, _ := reasonDecisionFixture(t, "pending", "暫停，尚待確認範圍。")
	envelope := reasonEnvelopeFixture(string(thinkingJSON(t, reasonReplyFixture())))
	envelope["message"].(map[string]any)["thinking"] = thinkingPrivateMarker
	server, calls := reasonServer(t, reasonInventoryFixture(), envelope, nil)
	out := filepath.Join(filepath.Dir(path), "assessment.json")
	assessment, err := AssessReviewReasonWithOptions(t.Context(), path, server.URL, reasonTestModel, out, ReasonAssessmentOptions{Think: true})
	if err != nil {
		t.Fatal(err)
	}
	emptyHash := fmt.Sprintf("%x", sha256.Sum256(nil))
	for _, kind := range []string{"digest_tamper", "missing", "mode", "off_nonempty", "absent_bytes", "absent_hash", "empty_missing_hash", "empty_wrong_hash", "negative_bytes", "oversize_bytes", "nonempty_missing_hash", "nonempty_empty_hash", "uppercase_hash", "bad_hash", "v1_with_metadata", "v2_old_prompt", "v1_new_prompt", "future_schema"} {
		t.Run(kind, func(t *testing.T) {
			var changed ReasonAssessment
			if err := json.Unmarshal(thinkingJSON(t, assessment), &changed); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "digest_tamper":
				changed.Thinking.Bytes++
			case "missing":
				changed.Thinking = nil
			case "mode":
				changed.Thinking.Mode = "auto"
			case "off_nonempty":
				changed.Thinking.Mode = "off"
			case "absent_bytes":
				changed.Thinking.Present, changed.Thinking.SHA256 = false, ""
			case "absent_hash":
				changed.Thinking.Present, changed.Thinking.Bytes = false, 0
			case "empty_missing_hash":
				changed.Thinking.Bytes, changed.Thinking.SHA256 = 0, ""
			case "empty_wrong_hash":
				changed.Thinking.Bytes, changed.Thinking.SHA256 = 0, reasonTestDigest
			case "negative_bytes":
				changed.Thinking.Bytes = -1
			case "oversize_bytes":
				changed.Thinking.Bytes = (64 << 10) + 1
			case "nonempty_missing_hash":
				changed.Thinking.SHA256 = ""
			case "nonempty_empty_hash":
				changed.Thinking.SHA256 = emptyHash
			case "uppercase_hash":
				changed.Thinking.SHA256 = strings.ToUpper(emptyHash)
			case "bad_hash":
				changed.Thinking.SHA256 = "sha256:invalid"
			case "v1_with_metadata":
				changed.SchemaVersion, changed.PromptVersion = "detective-reason-assessment/v1", "detective-reason-adviser/v5"
			case "v2_old_prompt":
				changed.PromptVersion = "detective-reason-adviser/v5"
			case "v1_new_prompt":
				changed.SchemaVersion, changed.Thinking = "detective-reason-assessment/v1", nil
			case "future_schema":
				changed.SchemaVersion = "detective-reason-assessment/v3"
			}
			if kind != "digest_tamper" {
				digest, err := reasonAssessmentHash(changed)
				if err != nil {
					t.Fatal(err)
				}
				changed.Digest = digest
			}
			file := filepath.Join(checkpointDirectory(t), "changed.json")
			if err := os.WriteFile(file, thinkingJSON(t, changed), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadReasonAssessment(file); err == nil {
				t.Fatal("digest recomputation accepted impossible thinking metadata")
			}
			var display bytes.Buffer
			if err := WriteReasonAssessmentText(&display, changed); err == nil || display.Len() != 0 {
				t.Fatal("invalid thinking metadata produced a review display")
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatal("offline metadata validation contacted the model")
	}
}

func TestReasonThinkingRejectsInvalidUTF8WithoutDecoderReplacement(t *testing.T) {
	path, _ := reasonDecisionFixture(t, "pending", "先暫停以確認範圍。")
	before := thinkingReadFile(t, path)
	envelope := reasonEnvelopeFixture(string(thinkingJSON(t, reasonReplyFixture())))
	envelope["message"].(map[string]any)["thinking"] = "invalid-utf8-placeholder"
	// Write actual invalid wire bytes; json.Marshal on a Go string replaces them.
	raw := bytes.Replace(thinkingJSON(t, envelope), []byte(`"invalid-utf8-placeholder"`), []byte{'"', 0xff, '"'}, 1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			if err := json.NewEncoder(w).Encode(reasonInventoryFixture()); err != nil {
				t.Error(err)
			}
		case "/api/chat":
			calls.Add(1)
			if _, err := w.Write(raw); err != nil {
				t.Error(err)
			}
		default:
			t.Error("thinking assessment used an unexpected endpoint")
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	out := filepath.Join(filepath.Dir(path), "assessment.json")
	assessment, err := AssessReviewReasonWithOptions(t.Context(), path, server.URL, reasonTestModel, out, ReasonAssessmentOptions{Think: true})
	if err == nil || !reflect.DeepEqual(assessment, ReasonAssessment{}) || calls.Load() != 1 {
		t.Fatal("invalid thinking bytes were normalized into an accepted response or retried")
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid UTF-8 thinking published an assessment")
	}
	if !bytes.Equal(before, thinkingReadFile(t, path)) {
		t.Fatal("invalid UTF-8 response changed the saved decision")
	}
}

func TestReasonThinkingJSONSurrogatesPreserveDecodedBytes(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "paired", raw: `"\ud83d\ude00"`, want: "😀", ok: true},
		{name: "uppercase_hex_pair", raw: `"\uD83D\uDE00"`, want: "😀", ok: true},
		{name: "replacement_literal", raw: `"�"`, want: "�", ok: true},
		{name: "replacement_escape", raw: `"\ufffd"`, want: "�", ok: true},
		{name: "escaped_literal_surrogate", raw: `"\\ud800"`, want: `\ud800`, ok: true},
		{name: "backslash_then_pair", raw: `"\\\ud83d\ude00"`, want: "\\😀", ok: true},
		{name: "isolated_high", raw: `"\ud800"`},
		{name: "isolated_low", raw: `"\udc00"`},
		{name: "reversed_pair", raw: `"\udc00\ud800"`},
		{name: "two_high", raw: `"\ud800\ud801"`},
		{name: "high_then_ascii", raw: `"\ud800x"`},
		{name: "high_then_escape", raw: `"\ud800\u0041"`},
		{name: "high_then_literal_low", raw: `"\ud800\\udc00"`},
		{name: "valid_pair_then_high", raw: `"\ud83d\ude00\ud800"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _ := reasonDecisionFixture(t, "pending", "先暫停以確認範圍。")
			before := thinkingReadFile(t, path)
			envelope := reasonEnvelopeFixture(string(thinkingJSON(t, reasonReplyFixture())))
			// RawMessage preserves the escaped wire form instead of normalizing it.
			envelope["message"].(map[string]any)["thinking"] = json.RawMessage(tt.raw)
			server, calls := reasonServer(t, reasonInventoryFixture(), envelope, nil)
			out := filepath.Join(filepath.Dir(path), "assessment.json")
			assessment, err := AssessReviewReasonWithOptions(t.Context(), path, server.URL, reasonTestModel, out, ReasonAssessmentOptions{Think: true})
			if calls.Load() != 1 || !bytes.Equal(before, thinkingReadFile(t, path)) {
				t.Fatal("surrogate validation retried or changed the saved decision")
			}
			if !tt.ok {
				if err == nil || ReasonAssessmentFailureStage(err) != "completion_envelope" || !reflect.DeepEqual(assessment, ReasonAssessment{}) {
					t.Fatal("unpaired surrogate was replaced and accepted")
				}
				if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unpaired surrogate published an assessment")
				}
				return
			}
			want := &ReasonThinking{Mode: "on", Present: true, Bytes: len(tt.want), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(tt.want)))}
			if err != nil || !reflect.DeepEqual(assessment.Thinking, want) {
				t.Fatalf("legitimate escaped thinking changed decoded bytes: %v", err)
			}
			if loaded, err := LoadReasonAssessment(out); err != nil || !reflect.DeepEqual(loaded, assessment) {
				t.Fatalf("legitimate escaped thinking metadata did not round trip: %v", err)
			}
		})
	}
}

func TestReasonThinkingLoadsFrozenV1V5AssessmentsWithoutUpgrade(t *testing.T) {
	for _, name := range []string{"bounded-support", "citation-only", "praise-paraphrase", "audit-bounded", "reject-overclaim", "pending-scope"} {
		t.Run(name, func(t *testing.T) {
			frozen := filepath.Join("testdata", "frozen_reason_v1_v5", name+".assessment.json")
			original := thinkingReadFile(t, frozen)
			path := filepath.Join(checkpointDirectory(t), "historical-assessment.json")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadReasonAssessment(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.SchemaVersion != "detective-reason-assessment/v1" || loaded.PromptVersion != "detective-reason-adviser/v5" || loaded.Thinking != nil {
				t.Fatal("historical assessment gained inferred thinking metadata or a new version")
			}
			var stored map[string]json.RawMessage
			if json.Unmarshal(original, &stored) != nil || stored["thinking"] != nil {
				t.Fatal("historical fixture is not the original metadata-free v1 contract")
			}
			if digest, err := reasonAssessmentHash(loaded); err != nil || digest != loaded.Digest {
				t.Fatalf("historical digest changed after adding optional metadata: %v", err)
			}
			var display bytes.Buffer
			if err := WriteReasonAssessmentText(&display, loaded); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, thinkingReadFile(t, path)) || !bytes.Equal(original, thinkingReadFile(t, frozen)) {
				t.Fatal("loading or displaying upgraded historical assessment bytes")
			}
		})
	}
}
