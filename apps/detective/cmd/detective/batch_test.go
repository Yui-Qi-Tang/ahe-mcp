//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func TestBatchCLIUsesSavedExtractionAndNeverModelFallback(t *testing.T) {
	t.Setenv("DETECTIVE_MODEL", "")
	t.Setenv("DETECTIVE_BASE_URL", "https://not-selected.invalid")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/unselected")
	cpPath, cp := inspectCheckpointFixture(t)
	directory := filepath.Dir(cpPath)
	sourcePath := filepath.Join(directory, "source.md")
	batchPath := filepath.Join(directory, "row.json")
	indexPath := filepath.Join(directory, "batch.json")
	if err := os.WriteFile(sourcePath, []byte(cp.RawText), 0600); err != nil {
		t.Fatal(err)
	}
	batch := cp.Batch
	batch.Source.Path = sourcePath
	second := batch.Rows[0].Result.Records[0]
	second.Subject = "another synthetic subject"
	second.Statement = "This second synthetic claim remains scoped to lab evidence."
	batch.Rows[0].Result.Records = append(batch.Rows[0].Result.Records, second)
	body, _ := json.Marshal(batch)
	if err := os.WriteFile(batchPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runBatch([]string{"prepare", "--input", sourcePath, "--row-batch", batchPath, "--index", indexPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var prepared pending.BatchResult
	if json.Unmarshal(stdout.Bytes(), &prepared) != nil || len(prepared.Members) != 2 || prepared.State != "prepared" {
		t.Fatal("prepare did not produce complete index")
	}
	for _, member := range prepared.Members {
		if !member.CheckpointAvailable || member.Locator != nil {
			t.Fatal("prepare wrote authority or omitted checkpoint")
		}
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(batchPath); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runBatch([]string{"inspect", "--index", indexPath}, &stdout, &stderr); err != nil {
		t.Fatal("inspection reopened source/model", err)
	}
	stdout.Reset()
	err := runBatch([]string{"resume", "--index", indexPath, "--ingest-command", "/unavailable-intake", "--query-command", "/unavailable-query"}, &stdout, &stderr)
	var partial pending.BatchResult
	if err == nil || json.Unmarshal(stdout.Bytes(), &partial) != nil || partial.Summary.Failed != 1 || partial.Summary.NotAttempted != 1 {
		t.Fatal("partial recovery did not emit explicit full index")
	}
}

func TestBatchCLIRejectsImplicitAuthorityAndModeDrift(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"resume", "--index", "/missing"}, {"inspect", "--index", "/missing", "--ingest-command", "/unselected"}, {"prepare", "--index", "/missing", "--model", "other"}, {"inspect", "--index", "/missing", "--timeout", "0"}} {
		var stdout, stderr bytes.Buffer
		if err := runBatch(args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
			t.Fatal("invalid batch mode reached I/O")
		}
	}
}

func TestBatchCLIRejectsDuplicateSelectionBeforeIO(t *testing.T) {
	for _, args := range [][]string{
		{"inspect", "--index", "/selected", "-index", "/replacement"},
		{"inspect", "--index=/selected", "--index=/replacement"},
		{"resume", "--index", "/selected", "--ingest-command", "/intake", "--ingest-command=/replacement", "--query-command", "/query"},
		{"resume", "--index", "/selected", "--ingest-command", "/intake", "--query-command", "/query", "-query-command", "/replacement"},
		{"inspect", "--index", "/selected", "--query-command", "/query", "--query-command=/replacement"},
	} {
		var stdout, stderr bytes.Buffer
		err := runBatch(args, &stdout, &stderr)
		if err == nil || err.Error() != "invalid batch arguments; select only flags for the explicit operation" || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatal("duplicate batch selection did not stop before I/O")
		}
	}
}
