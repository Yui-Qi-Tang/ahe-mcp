package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

// These fixtures contain no locators or launchers that could reach a database.
func queryBindingIndex(t *testing.T, sourceID string) pending.BatchIndex {
	t.Helper()
	document, err := labstatus.RestoreDocument("/synthetic-query-binding/STATUS.md", demoSource)
	if err != nil {
		t.Fatal(err)
	}
	document, err = document.SelectSections("Status at a Glance")
	if err != nil {
		t.Fatal(err)
	}
	var candidates labstatus.CandidateSet
	if err := json.Unmarshal([]byte(extractedCandidate), &candidates); err != nil {
		t.Fatal(err)
	}
	row := strings.Split(demoSource, "\n")[3]
	candidates.Records[0].Citation.ExactQuote = row
	batch := labstatus.RowBatch{
		SchemaVersion: labstatus.RowBatchSchemaVersion, Source: document.Source(),
		Extractor: labstatus.ExtractorInfo{Name: labstatus.ExtractorName, Version: labstatus.ExtractorVersion, Model: "synthetic-model"},
		Section:   document.Source().SelectedSections[0], Summary: labstatus.RowSummary{Attempted: 1, Validated: 1},
		Rows: []labstatus.RowOutcome{{Row: labstatus.SourceRow{StartLine: 4, EndLine: 4, Text: row}, Status: "validated", Result: &candidates}},
	}
	index, err := pending.NewBatchIndex(sourceID, document, batch, "")
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func writeQueryBindingIndex(t *testing.T, path string, index pending.BatchIndex) {
	t.Helper()
	body, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	// Only a synthetic temporary test file is replaced to reproduce stale UI state.
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQueryPendingBindsDisplayedBatchDigest(t *testing.T) {
	for _, scenario := range []string{"same_index", "replaced_index", "missing_displayed_digest"} {
		t.Run(scenario, func(t *testing.T) {
			s := newTestService(t)
			path := filepath.Join(s.dataDir, "batch.json")
			original := queryBindingIndex(t, "mock:original")
			writeQueryBindingIndex(t, path, original)
			if _, err := s.OpenBatch(path); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			s.state.Settings.Mode = "local"
			s.state.Settings.QueryLauncher = filepath.Join(s.dataDir, "nonexistent-query")
			if scenario == "missing_displayed_digest" {
				s.state.BatchDigest = ""
			}
			s.mu.Unlock()
			before := s.Snapshot()
			if scenario == "replaced_index" {
				replacement := queryBindingIndex(t, "mock:replacement")
				if replacement.Digest == original.Digest {
					t.Fatal("replacement must have a different valid digest")
				}
				writeQueryBindingIndex(t, path, replacement)
				if _, err := pending.LoadBatchIndex(path); err != nil {
					t.Fatal("replacement must be internally valid", err)
				}
			}
			state, err := s.QueryPending(context.Background())
			if scenario == "same_index" {
				if err != nil || state.BatchResult == nil || state.BatchResult.IndexDigest != original.Digest || state.Candidates[0].State != "not_checked" {
					t.Fatalf("unchanged offline batch should remain inspectable: %v", err)
				}
				return
			}
			if err == nil || state.BatchResult != nil {
				t.Fatalf("unbound batch inspection reached displayed candidates: err=%v result=%+v", err, state.BatchResult)
			}
			if state.Busy || state.BatchDigest != before.BatchDigest || !reflect.DeepEqual(state.Source, before.Source) || !reflect.DeepEqual(state.Candidates[0].Record, before.Candidates[0].Record) || state.Candidates[0].State != "not_checked" {
				t.Fatal("rejection lost the displayed source/candidate or retained stale observation")
			}
		})
	}
}

func TestApplyBatchResultRejectsDifferentDigest(t *testing.T) {
	for _, digest := range []string{"sha256:displayed", "sha256:different", ""} {
		t.Run(digest, func(t *testing.T) {
			s := newTestService(t)
			s.mu.Lock()
			s.state.BatchDigest = "sha256:displayed"
			s.state.Candidates = []CandidateView{{Ordinal: 1, State: "pending_verified", CheckpointPath: "original-checkpoint"}}
			s.state.BatchResult = &pending.BatchResult{SchemaVersion: "old"}
			result := pending.BatchResult{SchemaVersion: "detective-candidate-batch-result/v1", IndexDigest: digest,
				Members: []pending.BatchMemberResult{{Ordinal: 1, State: "admitted_verified", CheckpointPath: "new-checkpoint"}}}
			err := s.applyBatchResultLocked(result)
			s.mu.Unlock()
			state := s.Snapshot()
			if digest == state.BatchDigest {
				if err != nil || state.BatchResult == nil || state.Candidates[0].State != "admitted_verified" || state.Candidates[0].CheckpointPath != "new-checkpoint" {
					t.Fatal("matching result was not applied")
				}
				return
			}
			if !errors.Is(err, errBatchChanged) || state.BatchResult != nil || state.Candidates[0].State != "not_checked" || state.Candidates[0].CheckpointPath != "original-checkpoint" {
				t.Fatal("foreign result changed candidate identity or retained stale observation")
			}
		})
	}
}

func TestApplyEmptyBatchResultClearsEarlierObservation(t *testing.T) {
	s := newTestService(t)
	s.mu.Lock()
	s.state.BatchDigest = "sha256:displayed"
	s.state.Candidates = []CandidateView{{Ordinal: 1, State: "pending_verified"}}
	s.state.BatchResult = &pending.BatchResult{SchemaVersion: "old"}
	err := s.applyBatchResultLocked(pending.BatchResult{})
	s.mu.Unlock()
	state := s.Snapshot()
	if err != nil || state.BatchResult != nil || state.Candidates[0].State != "not_checked" {
		t.Fatal("empty inspection retained an earlier batch observation")
	}
}
