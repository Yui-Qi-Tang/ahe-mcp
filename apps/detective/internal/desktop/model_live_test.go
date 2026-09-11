package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// Explicit opt-in only: this performs one real local model extraction, never
// source MCP, pending submission, Query, or admission. Normal tests skip it.
func TestDesktopLocalModelSyntheticRow(t *testing.T) {
	if os.Getenv("DETECTIVE_DESKTOP_MODEL_SMOKE") != "1" {
		t.Skip("set DETECTIVE_DESKTOP_MODEL_SMOKE=1 only for an approved local-model trial")
	}
	const source = "# Synthetic Detective source\n\n## Status at a Glance\n\n| Area | State | Limits |\n| --- | --- | --- |\n| Preview search | **IMPLEMENTED, EXPOSED** | Synthetic rehearsal only; company deployment has not been verified |\n| Desktop review | **PLANNED** | No canonical writer is exposed by the desktop preview |\n"
	const sourceHash = "d56a3390d3d6767748243fd40559fbac6e2114e9ea6aa584529e21f358d1e1e3"
	if fmt.Sprintf("%x", sha256.Sum256([]byte(source))) != sourceHash {
		t.Fatal("frozen synthetic source changed")
	}
	s := newTestService(t)
	path := filepath.Join(serviceDirectory(t), "STATUS.md")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportSource(path); err != nil {
		t.Fatal(err)
	}
	settings := s.Snapshot().Settings
	settings.Mode = "local"
	settings.BaseURL = "http://127.0.0.1:11434/v1"
	settings.Model = "gemma4:e4b-it-qat"
	settings.IntakeLauncher, settings.QueryLauncher = "", ""
	settings.Connections = nil
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()
	state, err := s.Extract(ctx, 8)
	// Keep only the synthetic candidate and bounded diagnostics in test output.
	metadata, marshalErr := json.Marshal(struct {
		Version      string                  `json:"desktop_version"`
		SourceSHA256 string                  `json:"source_sha256"`
		Model        string                  `json:"model"`
		Line         int                     `json:"line"`
		Candidates   []CandidateView         `json:"candidates"`
		Error        string                  `json:"error"`
		Result       *labstatus.CandidateSet `json:"result,omitempty"`
	}{Version: Version, SourceSHA256: sourceHash, Model: settings.Model, Line: 8, Candidates: state.Candidates, Error: state.Error, Result: smokeResult(state)})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	t.Log(string(metadata))
	if err != nil {
		t.Fatalf("single approved extraction failed: %v", err)
	}
	result := smokeResult(state)
	if result == nil {
		t.Fatal("no validated extraction or abstention")
	}
	if result.Outcome == "abstained" {
		if len(state.Candidates) != 0 || state.BatchPath != "" || state.BatchResult != nil {
			t.Fatal("abstention acquired a batch")
		}
		t.Log("valid abstention, not a successful candidate extraction or model-quality qualification")
		return
	}
	if state.BatchPath == "" || len(state.Candidates) != 1 || state.Candidates[0].Record.Status != "planned" || state.Candidates[0].Record.Citation.StartLine != 8 || state.Candidates[0].Record.Citation.EndLine != 8 || state.BatchResult == nil || state.BatchResult.Summary.SubmissionAttempts != 0 {
		t.Fatal("expected one source-grounded local planned candidate, with no submission")
	}
}

func smokeResult(state State) *labstatus.CandidateSet {
	if state.Extraction == nil || len(state.Extraction.Rows) != 1 || state.Extraction.Rows[0].Status != "validated" {
		return nil
	}
	return state.Extraction.Rows[0].Result
}
