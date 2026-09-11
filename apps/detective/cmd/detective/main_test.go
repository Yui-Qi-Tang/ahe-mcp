package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func TestRunRequestsResponsesThinkingOff(t *testing.T) {
	var sawThinkingOff bool
	var requests atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	})
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		defer r.Body.Close()
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		reasoning, _ := request["reasoning"].(map[string]any)
		_, nativeThink := request["think"]
		sawThinkingOff = reasoning["effort"] == "none" && !nativeThink
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response-1","model":"test-model","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"synthetic-private-reasoning"}]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"outcome\":\"extracted\",\"records\":[{\"record_type\":\"capability_state\",\"subject\":\"lab_status\",\"statement\":\"The source reports lab evidence.\",\"epistemic_class\":\"claim\",\"status\":\"lab_proven\",\"scope\":\"lab_contract\",\"selection_state\":\"unspecified\",\"citation\":{\"start_line\":2,\"end_line\":2},\"blocked_by\":[],\"does_not_establish\":[],\"qualifiers\":[]}],\"abstentions\":[],\"limitations\":[],\"abstention_reason\":\"\"}"}]}]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	input := filepath.Join(t.TempDir(), "STATUS.md")
	if err := os.WriteFile(input, []byte("## Status at a Glance\nLAB PROVEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-input", input, "-base-url", server.URL + "/v1", "-model", "test-model"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v; stderr = %s", err, stderr.String())
	}
	if !sawThinkingOff {
		t.Fatal("Responses request must set reasoning.effort=none and omit native think")
	}
	if requests.Load() != 1 || strings.Contains(stdout.String()+stderr.String(), "synthetic-private-reasoning") {
		t.Fatal("unexpected model call count or reasoning reached CLI output")
	}
	var output labstatus.Envelope
	if json.Unmarshal(stdout.Bytes(), &output) != nil || output.Result.Outcome != "extracted" || len(output.Result.Records) != 1 || output.Result.Records[0].Status != "lab_proven" || output.Result.Records[0].Citation.ExactQuote != "LAB PROVEN" {
		t.Fatal("final candidate lost validation or exact source citation")
	}
}

func TestCheckpointModesRejectBeforeModelOrLauncher(t *testing.T) {
	t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/environment-only-launcher")
	for name, args := range map[string][]string{
		"prepare without checkpoint":      {"-ahe-prepare-only"},
		"checkpoint without mode":         {"-ahe-checkpoint", "/missing/checkpoint.json", "-row-chunks", "-row-line", "4"},
		"prepare and submit":              {"-ahe-checkpoint", "/missing/checkpoint.json", "-ahe-prepare-only", "-ahe-submit-pending", "-row-chunks", "-row-line", "4"},
		"resume source override":          {"-ahe-resume", "/missing/checkpoint.json", "-input", "/missing/source.md"},
		"resume model override":           {"-ahe-resume", "/missing/checkpoint.json", "-model", "different-model"},
		"resume requires explicit intake": {"-ahe-resume", "/missing/checkpoint.json", "-ahe-query-command", "/missing/query"},
		"resume requires query":           {"-ahe-resume", "/missing/checkpoint.json", "-ahe-ingest-command", "/missing/intake"},
		"query without checkpoint":        {"-ahe-query-command", "/missing/query"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(args, &stdout, &stderr)
			if err == nil || stdout.Len() != 0 || strings.Contains(err.Error(), "model endpoint") || strings.Contains(err.Error(), "start") {
				t.Fatalf("invalid mode reached external operation: err=%v stdout=%q", err, stdout.String())
			}
		})
	}
}

func TestResumeRejectsMalformedCheckpointWithoutModel(t *testing.T) {
	t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:1/v1")
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "checkpoint.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{"-ahe-resume", path, "-ahe-ingest-command", "/nonexistent/intake", "-ahe-query-command", "/nonexistent/query"}, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || strings.Contains(err.Error(), "model endpoint") || strings.Contains(err.Error(), "start") {
		t.Fatalf("malformed checkpoint reached external operation: err=%v stdout=%q", err, stdout.String())
	}
}

func TestEmptyCheckpointPathCannotFallBackToLegacyMode(t *testing.T) {
	for _, name := range []string{"ahe-resume", "ahe-checkpoint"} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run([]string{"-" + name, ""}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "require a nonempty path") || stdout.Len() != 0 {
				t.Fatalf("empty checkpoint path did not fail before legacy input processing: %v", err)
			}
		})
	}
}

func TestCheckpointSubmissionSavesBeforeLauncherFailure(t *testing.T) {
	var requests atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	})
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		candidate := `{"outcome":"extracted","records":[{"record_type":"capability_state","subject":"pending_intake","statement":"The source reports lab evidence.","epistemic_class":"claim","status":"lab_proven","scope":"lab_contract","selection_state":"unspecified","citation":{"start_line":4,"end_line":4},"blocked_by":[],"does_not_establish":[],"qualifiers":[]}],"abstentions":[],"limitations":[],"abstention_reason":""}`
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "response-checkpoint", "model": "test-model",
			"output": []any{map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": candidate},
			}}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(directory, "STATUS.md")
	const raw = "## Status at a Glance\n| Capability | Status |\n| --- | --- |\n| Pending intake | LAB PROVEN |\n"
	if err := os.WriteFile(input, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "pending.json")
	args := []string{"-input", input, "-base-url", server.URL + "/v1", "-model", "test-model",
		"-row-chunks", "-row-line", "4", "-ahe-checkpoint", path, "-ahe-submit-pending",
		"-ahe-ingest-command", filepath.Join(directory, "missing-intake"),
		"-ahe-query-command", filepath.Join(directory, "missing-query")}
	var stdout, stderr bytes.Buffer
	if err := run(args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
		t.Fatalf("failed launcher reported success: err=%v", err)
	}
	checkpoint, err := pending.Load(path)
	if err != nil || checkpoint.RawText != raw || requests.Load() != 1 {
		t.Fatalf("checkpoint was not durable before launcher failure: err=%v requests=%d", err, requests.Load())
	}
	if err := run(args, &stdout, &stderr); err != pending.ErrExists || stdout.Len() != 0 || requests.Load() != 1 {
		t.Fatalf("fresh retry did not stop before model: err=%v requests=%d", err, requests.Load())
	}
}
