package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWorkflowExtractionFailureIsClassifiedAndPrivate(t *testing.T) {
	for _, code := range []string{"model_http", "model_incomplete", "model_no_final_text", "model_output_kind", "model_json", "citation", "candidate_validation"} {
		t.Run(code, func(t *testing.T) {
			s := newTestService(t)
			importTestSource(t, s)
			var calls atomic.Int64
			server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch code {
				case "model_http":
					http.Error(w, reasoningMarker, 500)
				case "model_incomplete":
					_, _ = w.Write([]byte(`{"model":"gemma4:e4b-it-qat","status":"incomplete","output":[]}`))
				case "model_no_final_text":
					writeModelItems(w, modelReasoning())
				case "model_output_kind":
					writeModelItems(w, modelReasoning(), modelMessage(extractedCandidate), map[string]any{"type": "function_call", "name": "writer"})
				case "model_json":
					writeModelText(w, reasoningMarker)
				case "citation":
					writeModelText(w, strings.Replace(extractedCandidate, `"end_line":4`, `"end_line":99`, 1))
				case "candidate_validation":
					writeModelText(w, strings.Replace(extractedCandidate, `"status":"implemented"`, `"status":"invalid"`, 1))
				}
			})
			localSettings(t, s, server)
			state, err := s.Extract(context.Background(), 4)
			if err == nil || calls.Load() != 1 || state.BatchPath != "" || state.Extraction == nil || !strings.Contains(state.Error, "["+code+"]") {
				t.Fatalf("failure code missing or side effect: calls=%d error=%v", calls.Load(), err)
			}
			if state.Extraction.Rows[0].Cause != nil || s.state.Extraction.Rows[0].Cause != nil {
				t.Fatal("raw cause retained")
			}
			body, _ := json.Marshal(state)
			if strings.Contains(string(body), reasoningMarker) || strings.Contains(err.Error(), reasoningMarker) {
				t.Fatal("raw failure escaped")
			}
			files, err := filepath.Glob(filepath.Join(s.dataDir, "extraction-*.json"))
			if err != nil || len(files) != 1 {
				t.Fatal("missing saved diagnostic")
			}
			body, err = os.ReadFile(files[0])
			if err != nil || !strings.Contains(string(body), "["+code+"]") || strings.Contains(string(body), reasoningMarker) {
				t.Fatal("saved diagnostic not classified/private")
			}
		})
	}
}

func TestExtractionFailureFallbackAndCancellation(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("private: %w", context.Canceled), "cancelled"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New(reasoningMarker), "extraction_failed"},
		{modelFailure(reasoningMarker), "extraction_failed"},
	} {
		text := extractionFailureText(test.err)
		if !strings.Contains(text, "["+test.code+"]") || strings.Contains(text, reasoningMarker) {
			t.Fatal("unsafe or incorrect classification")
		}
	}
}
