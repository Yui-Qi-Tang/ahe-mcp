//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func TestInspectRejectsOtherExplicitFlags(t *testing.T) {
	t.Setenv("DETECTIVE_MODEL", "")
	t.Setenv("DETECTIVE_BASE_URL", "")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/environment-only-intake")
	for _, args := range [][]string{
		{"-input", ""}, {"-model", ""}, {"-base-url", ""}, {"-sections", ""},
		{"-row-chunks=false"}, {"-row-chunks=true"}, {"-row-line", "0"}, {"-status-clause", ""},
		{"-ahe-submit-pending=false"}, {"-ahe-submit-pending=true"}, {"-ahe-ingest-command", ""},
		{"-ahe-source-id", ""}, {"-ahe-checkpoint", ""},
		{"-ahe-prepare-only=false"}, {"-ahe-prepare-only=true"}, {"-ahe-resume", ""},
		{"-version=false"}, {"-version=true"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(append([]string{"-ahe-inspect", "/missing/checkpoint.json"}, args...), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "cannot use extraction or write mode flags") || stdout.Len() != 0 {
				t.Fatalf("conflicting flag was not rejected before I/O: err=%v stdout=%q", err, stdout.String())
			}
		})
	}
}

func TestInspectValidatesArgumentsBeforeIO(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"empty checkpoint", []string{"-ahe-inspect", ""}, "nonempty checkpoint path"},
		{"blank checkpoint", []string{"-ahe-inspect", " "}, "nonempty checkpoint path"},
		{"receipt only", []string{"-ahe-receipt", "/missing/receipt.json"}, "requires both"},
		{"query only", []string{"-ahe-query-command", "/missing/query"}, "requires both"},
		{"empty receipt only", []string{"-ahe-receipt", ""}, "requires both"},
		{"empty query only", []string{"-ahe-query-command", ""}, "requires both"},
		{"empty pair", []string{"-ahe-receipt", "", "-ahe-query-command", ""}, "requires both"},
		{"empty receipt with query", []string{"-ahe-receipt", "", "-ahe-query-command", "/missing/query"}, "requires both"},
		{"empty query with receipt", []string{"-ahe-receipt", "/missing/receipt.json", "-ahe-query-command", ""}, "requires both"},
		{"blank receipt", []string{"-ahe-receipt", " ", "-ahe-query-command", "/missing/query"}, "requires both"},
		{"blank query", []string{"-ahe-receipt", "/missing/receipt.json", "-ahe-query-command", " "}, "requires both"},
		{"empty format", []string{"-format", ""}, "format must be text or json"},
		{"unknown format", []string{"-format", "yaml"}, "format must be text or json"},
		{"zero timeout", []string{"-timeout", "0"}, "timeout must be positive"},
		{"negative timeout", []string{"-timeout", "-1s"}, "timeout must be positive"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"-ahe-inspect", "/missing/checkpoint.json"}, tt.args...)
			err := run(args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.want) || stdout.Len() != 0 {
				t.Fatalf("invalid inspection arguments reached I/O: err=%v stdout=%q", err, stdout.String())
			}
		})
	}
}

func TestInspectOnlyFlagsCannotChangeOtherModes(t *testing.T) {
	for _, mode := range [][]string{
		nil,
		{"-version"},
		{"-ahe-resume", "/missing/checkpoint.json"},
		{"-ahe-checkpoint", "/missing/checkpoint.json", "-ahe-prepare-only"},
		{"-ahe-submit-pending"},
	} {
		for _, extra := range [][]string{
			{"-format", "text"}, {"-format", ""}, {"-ahe-receipt", "/missing/receipt.json"}, {"-ahe-receipt", ""},
		} {
			args := append(append([]string(nil), mode...), extra...)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := run(args, &stdout, &stderr)
				if err == nil || !strings.Contains(err.Error(), "require ahe inspect mode") || stdout.Len() != 0 {
					t.Fatalf("inspect-only flags were not rejected: err=%v stdout=%q", err, stdout.String())
				}
			})
		}
	}
}

func TestInspectOfflineTextAndJSONWithoutModelOrLauncher(t *testing.T) {
	t.Setenv("DETECTIVE_MODEL", "")
	t.Setenv("DETECTIVE_BASE_URL", "")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/nonexistent/environment-intake")
	path, checkpoint := inspectCheckpointFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"default", "text", "json"} {
		t.Run(format, func(t *testing.T) {
			args := []string{"-ahe-inspect", path}
			if format != "default" {
				args = append(args, "-format", format)
			}
			var stdout, stderr bytes.Buffer
			if err := run(args, &stdout, &stderr); err != nil {
				t.Fatalf("offline inspection failed: %v; stderr=%s", err, stderr.String())
			}
			for _, want := range []string{checkpoint.Digest, checkpoint.SourceID, checkpoint.Batch.Rows[0].Result.Records[0].Statement} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("inspection omitted %q: %s", want, stdout.String())
				}
			}
			if format == "json" {
				var output map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || len(output) == 0 {
					t.Fatalf("inspection JSON is invalid: %v", err)
				}
			}
		})
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("inspection changed checkpoint bytes: %v", err)
	}
	afterEntries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || !reflect.DeepEqual(entries, afterEntries) {
		t.Fatalf("inspection changed checkpoint directory: %v", err)
	}
}

func TestInspectInvalidCheckpointProducesNoSuccessOutput(t *testing.T) {
	path, _ := inspectCheckpointFixture(t)
	invalidPath := filepath.Join(filepath.Dir(path), "invalid.json")
	if err := os.WriteFile(invalidPath, []byte(`{"schema_version":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{invalidPath, "relative/checkpoint.json"} {
		for _, format := range []string{"text", "json"} {
			var stdout, stderr bytes.Buffer
			err := run([]string{"-ahe-inspect", path, "-format", format}, &stdout, &stderr)
			if err == nil || stdout.Len() != 0 {
				t.Fatalf("invalid checkpoint produced success output: err=%v stdout=%q", err, stdout.String())
			}
		}
	}
}

func TestInspectQueryMismatchProducesNoSuccessOutput(t *testing.T) {
	path, checkpoint := inspectCheckpointFixture(t)
	receipt := pending.Result{
		SchemaVersion: "detective-pending-resume/v1", CheckpointDigest: checkpoint.Digest,
		State: "pending_verified", Batch: checkpoint.Batch, ReadbackVerified: true,
		Handoff: ahemcp.Handoff{
			SchemaVersion: "ahe-mcp-pending-handoff/v0", SourceSnapshotID: "srcsnap:fixture",
			ExtractionViewID: "view:fixture", ExtractionAttemptID: "attempt:fixture",
			ProposalOccurrenceID: "occ:fixture", ProposalCount: 1, Status: "pending",
		},
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(filepath.Dir(path), "receipt.json")
	if err := os.WriteFile(receiptPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(filepath.Dir(path), "wrong-query.sh")
	const script = `#!/bin/sh
read -r request || exit 1
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"wrong-protocol","capabilities":{"tools":{}},"serverInfo":{"name":"wrong-endpoint","version":"fixture"}}}'
`
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run([]string{"-ahe-inspect", path, "-ahe-receipt", receiptPath, "-ahe-query-command", launcher, "-format", format}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "endpoint identity or protocol mismatch") || stdout.Len() != 0 {
				t.Fatalf("query mismatch produced success output: err=%v stdout=%q", err, stdout.String())
			}
		})
	}
}

func inspectCheckpointFixture(t *testing.T) (string, pending.Checkpoint) {
	t.Helper()
	const row = "| Pending intake | LAB PROVEN |"
	document, err := labstatus.RestoreDocument("/synthetic-source-not-on-disk/STATUS.md", "## Status at a Glance\n| Capability | Status |\n| --- | --- |\n"+row+"\n")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := document.SelectSections("Status at a Glance")
	if err != nil {
		t.Fatal(err)
	}
	batch := labstatus.RowBatch{
		SchemaVersion: labstatus.RowBatchSchemaVersion, Source: selected.Source(),
		Extractor: labstatus.ExtractorInfo{Name: labstatus.ExtractorName, Version: labstatus.ExtractorVersion, Model: "synthetic-model"},
		Section:   selected.Source().SelectedSections[0], Summary: labstatus.RowSummary{Attempted: 1, Validated: 1},
		Rows: []labstatus.RowOutcome{{Row: labstatus.SourceRow{StartLine: 4, EndLine: 4, Text: row}, Status: "validated",
			Result: &labstatus.CandidateSet{Outcome: "extracted", Abstentions: []labstatus.Abstention{}, Limitations: []string{}, Records: []labstatus.Record{{
				RecordType: "capability_state", Subject: "pending_intake", Statement: "The synthetic source reports lab evidence.",
				EpistemicClass: "claim", Status: "lab_proven", Scope: "lab_contract", SelectionState: "unspecified",
				Citation: labstatus.Citation{StartLine: 4, EndLine: 4, ExactQuote: row}, BlockedBy: []string{}, DoesNotEstablish: []string{}, Qualifiers: []string{},
			}}}}},
	}
	checkpoint, err := pending.New("mock:inspection", document, batch, "")
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
	path := filepath.Join(directory, "pending.json")
	writer, err := pending.Reserve(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if err := writer.Write(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path, checkpoint
}
