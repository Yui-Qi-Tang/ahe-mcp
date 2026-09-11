package labstatus

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRestoreDocumentUsesCapturedBytesOnly(t *testing.T) {
	t.Parallel()
	original := loadTestDocument(t, "## Status at a Glance\r\nexact bytes\r\n")
	restored, err := RestoreDocument(original.Source().Path, original.RawText())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Source(), original.Source()) || restored.RawText() != original.RawText() || restored.NumberedText() != original.NumberedText() {
		t.Fatal("captured source coordinates changed")
	}
	missing := filepath.Join(t.TempDir(), "absent.md")
	if _, err := RestoreDocument(missing, "captured text"); err != nil {
		t.Fatalf("restoring bytes opened the original path: %v", err)
	}
	for name, raw := range map[string]string{
		"invalid utf8": string([]byte{0xff}), "nul": "a\x00b", "oversize": strings.Repeat("x", maxDocumentBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RestoreDocument(missing, raw); err == nil {
				t.Fatal("invalid captured bytes accepted")
			}
		})
	}
	if _, err := RestoreDocument("relative.md", "captured text"); err == nil {
		t.Fatal("relative audit path accepted")
	}
}

func TestValidateRowBatchRejectsAlteredCoordinates(t *testing.T) {
	document := loadRowFixture(t)
	extractor, err := NewExtractor(&rowLLM{responses: map[string]string{"Canonical relation": candidateJSON(t, 8)}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := extractor.ExtractTableRow(context.Background(), document, "Status at a Glance", 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRowBatch(document, batch, ""); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.1.0", "0.1.1", ExtractorVersion} {
		historical := batch
		historical.Extractor.Version = version
		if err := ValidateRowBatch(document, historical, ""); err != nil {
			t.Fatalf("saved supported extractor %s became unrecoverable: %v", version, err)
		}
	}
	if err := ValidateRowBatch(document, batch, "UNRELEASED LAB PROVEN"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRowBatch(document, batch, "invented clause"); err == nil {
		t.Fatal("invented clause accepted")
	}
	mutations := map[string]func(*RowBatch){
		"schema":            func(b *RowBatch) { b.SchemaVersion = "future" },
		"source hash":       func(b *RowBatch) { b.Source.SHA256 = strings.Repeat("0", 64) },
		"source bytes":      func(b *RowBatch) { b.Source.Bytes++ },
		"source selection":  func(b *RowBatch) { b.Source.SelectedSections[0].EndLine++ },
		"section":           func(b *RowBatch) { b.Section.EndLine++ },
		"row text":          func(b *RowBatch) { b.Rows[0].Row.Text += " changed" },
		"summary":           func(b *RowBatch) { b.Summary.Failed = 1 },
		"extractor version": func(b *RowBatch) { b.Extractor.Version = "future" },
		"failed row":        func(b *RowBatch) { b.Rows[0].Status = "failed" },
		"row error":         func(b *RowBatch) { b.Rows[0].Error = "unexpected" },
		"nil result":        func(b *RowBatch) { b.Rows[0].Result = nil },
		"citation":          func(b *RowBatch) { b.Rows[0].Result.Records[0].Citation.StartLine = 7 },
		"quote":             func(b *RowBatch) { b.Rows[0].Result.Records[0].Citation.ExactQuote = "changed" },
		"status":            func(b *RowBatch) { b.Rows[0].Result.Records[0].Status = "implemented" },
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var changed RowBatch
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if err := ValidateRowBatch(document, changed, ""); err == nil {
				t.Fatal("altered batch accepted")
			}
		})
	}
}
