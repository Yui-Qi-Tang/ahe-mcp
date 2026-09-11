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
	"strings"
	"syscall"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

func TestInspectOfflinePreservesInputsAndEvidenceBoundary(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[ending], func(t *testing.T) {
			document, batch := checkpointFixture(t, ending)
			checkpoint, err := New("mock:checkpoint", document, batch, "")
			if err != nil {
				t.Fatal(err)
			}
			directory := checkpointDirectory(t)
			path := filepath.Join(directory, "checkpoint.json")
			body, _ := json.Marshal(checkpoint)
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/must-not-run/intake")
			t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:1")
			inspection, err := Inspect(t.Context(), path, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if inspection.SchemaVersion != "detective-pending-inspection/v1" || inspection.CheckpointDigest != checkpoint.Digest ||
				inspection.Pending.State != "not_checked" || inspection.Pending.Receipt != nil || inspection.Pending.CheckCompletedAt != "" ||
				inspection.AuthorityEffect != "none" || inspection.HumanReview != "not_recorded" || inspection.NextAction != "inspect_and_handoff_only" ||
				!reflect.DeepEqual(inspection.Candidate, batch.Rows[0].Result.Records[0]) || !reflect.DeepEqual(inspection.Source, batch.Source) {
				t.Fatal("inspection changed source/candidate or invented authority")
			}
			encoded, err := json.Marshal(inspection)
			if err != nil || bytes.Contains(encoded, []byte(`"raw_text"`)) {
				t.Fatal("inspection exposed a full raw-text payload")
			}
			var decoded Inspection
			if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Candidate.Citation.ExactQuote != batch.Rows[0].Row.Text {
				t.Fatal("inspection JSON lost exact source quote")
			}
			var display bytes.Buffer
			if err := WriteInspectionText(&display, inspection); err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{"候選 1", "臺灣證據🙂", "not_checked", "尚未查詢 AHE", "人工作業", "不記錄人工決定", "未重開檔案"} {
				if !strings.Contains(display.String(), expected) {
					t.Errorf("missing display item %q", expected)
				}
			}
			after, err := os.ReadFile(path)
			entries, directoryErr := os.ReadDir(directory)
			if err != nil || directoryErr != nil || !bytes.Equal(after, body) || len(entries) != 1 {
				t.Fatal("read-only inspection changed a file or created a lock")
			}
		})
	}
}

func TestInspectRejectsIncompleteQuerySelectionAndCanceledContext(t *testing.T) {
	for _, pair := range [][2]string{{"/receipt.json", ""}, {"", "/query"}} {
		result, err := Inspect(t.Context(), "/must-not-read/checkpoint.json", pair[0], pair[1])
		if err == nil || !strings.Contains(err.Error(), "requires both") || !reflect.DeepEqual(result, Inspection{}) {
			t.Fatal("incomplete query selection proceeded to checkpoint access")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := Inspect(ctx, "/must-not-read/checkpoint.json", "", "")
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, Inspection{}) {
		t.Fatal("canceled inspection produced a result")
	}
}

func TestInspectionReceiptRejectsMalformedAndChangedInputs(t *testing.T) {
	checkpoint := validCheckpoint(t)
	original := inspectionReceiptFixture(checkpoint)
	encoded, _ := json.Marshal(original)
	for _, mode := range []string{"valid", "schema", "state", "unverified", "digest", "batch", "unknown", "nested_unknown", "missing", "nested_missing", "duplicate", "escaped_duplicate", "nested_duplicate", "case_alias", "nested_alias", "trailing", "invalid_utf8", "oversized", "malformed", "deep"} {
		t.Run(mode, func(t *testing.T) {
			body := append([]byte(nil), encoded...)
			changed := original
			switch mode {
			case "schema":
				changed.SchemaVersion = "detective-pending-inspection/v1"
			case "state":
				changed.State = "prepared"
			case "unverified":
				changed.ReadbackVerified = false
			case "digest":
				changed.CheckpointDigest = "sha256:" + strings.Repeat("0", 64)
			case "batch":
				changed.Batch.Extractor.Model = "different-model"
			case "unknown":
				body = append([]byte(`{"approved":true,`), body[1:]...)
			case "nested_unknown":
				body = bytes.Replace(body, []byte(`"handoff":{`), []byte(`"handoff":{"decision":"approved",`), 1)
			case "missing":
				body = bytes.Replace(body, []byte(`,"readback_verified":true`), nil, 1)
			case "nested_missing":
				body = bytes.Replace(body, []byte(`,"replayed":false`), nil, 1)
			case "duplicate":
				body = append([]byte(`{"state":"pending_verified",`), body[1:]...)
			case "escaped_duplicate":
				body = append([]byte(`{"st\u0061te":"pending_verified",`), body[1:]...)
			case "nested_duplicate":
				body = bytes.Replace(body, []byte(`"handoff":{`), []byte(`"handoff":{"status":"pending",`), 1)
			case "case_alias":
				body = bytes.Replace(body, []byte(`"state"`), []byte(`"State"`), 1)
			case "nested_alias":
				body = bytes.Replace(body, []byte(`"source_snapshot_id"`), []byte(`"SOURCE_SNAPSHOT_ID"`), 1)
			case "trailing":
				body = append(body, []byte(" {}")...)
			case "invalid_utf8":
				body = append(body, 0xff)
			case "oversized":
				body = bytes.Repeat([]byte{' '}, maxCheckpointBytes+1)
			case "malformed":
				body = []byte(`{"schema_version":`)
			case "deep":
				body = []byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66))
			}
			if !reflect.DeepEqual(changed, original) {
				body, _ = json.Marshal(changed)
			}
			path := filepath.Join(checkpointDirectory(t), "resume.json")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := loadInspectionReceipt(path, checkpoint)
			if mode == "valid" {
				if err != nil || !reflect.DeepEqual(result, original) {
					t.Fatalf("valid saved receipt refused: %v", err)
				}
			} else if err == nil {
				t.Fatal("malformed or changed receipt accepted")
			}
		})
	}
}

func TestInspectionReceiptRejectsUnsafeFiles(t *testing.T) {
	for _, mode := range []string{"parent_permissions", "file_permissions", "symlink", "hardlink", "fifo", "missing", "relative"} {
		t.Run(mode, func(t *testing.T) {
			directory := checkpointDirectory(t)
			path := filepath.Join(directory, "receipt.json")
			body, _ := json.Marshal(inspectionReceiptFixture(validCheckpoint(t)))
			switch mode {
			case "symlink", "hardlink":
				target := filepath.Join(directory, "other.json")
				if err := os.WriteFile(target, body, 0o600); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if mode == "hardlink" {
					link = os.Link
				}
				if err := link(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing", "relative":
				if mode == "relative" {
					path = "receipt.json"
				}
			default:
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "parent_permissions" {
					if err := os.Chmod(directory, 0o750); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Chmod(path, 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadInspectionReceipt(path, validCheckpoint(t)); err == nil {
				t.Fatal("unsafe receipt file accepted")
			}
		})
	}
}

func TestInspectionTextEscapesEveryUntrustedSurface(t *testing.T) {
	const unsafe = "臺灣\x1b[2J\x1b]8;;https://invalid.test\x07\r\t\u202e\u2066\u200b\nFAKE APPROVAL"
	checkpoint := validCheckpoint(t)
	record := checkpoint.Batch.Rows[0].Result.Records[0]
	record.Subject, record.Statement, record.Citation.ExactQuote = unsafe, unsafe, unsafe
	record.RecordType, record.EpistemicClass, record.Status, record.Scope, record.SelectionState = unsafe, unsafe, unsafe, unsafe, unsafe
	record.BlockedBy, record.DoesNotEstablish, record.Qualifiers = []string{unsafe}, []string{unsafe}, []string{unsafe}
	inspection := Inspection{CheckpointDigest: unsafe, RequestIdentityVersion: unsafe, SourceID: unsafe,
		Source: checkpoint.Batch.Source, Section: checkpoint.Batch.Section, Extractor: checkpoint.Batch.Extractor,
		Row: checkpoint.Batch.Rows[0].Row, Candidate: record, StatusClause: unsafe, Limitations: []string{unsafe},
		Pending: PendingInspection{State: unsafe, CheckCompletedAt: unsafe, Receipt: &ahemcp.Handoff{
			SourceSnapshotID: unsafe, ExtractionViewID: unsafe, ExtractionAttemptID: unsafe, ProposalOccurrenceID: unsafe}}}
	inspection.Source.Path, inspection.Source.SHA256, inspection.Section.Heading, inspection.Row.Text = unsafe, unsafe, unsafe, unsafe
	inspection.Extractor.Name, inspection.Extractor.Version, inspection.Extractor.Model = unsafe, unsafe, unsafe
	var out bytes.Buffer
	if err := WriteInspectionText(&out, inspection); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\x1b", "\x07", "\r", "\t", "\u202e", "\u2066", "\u200b", "\nFAKE APPROVAL"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("display contains unescaped control %q", forbidden)
		}
	}
	if !strings.Contains(out.String(), "臺灣") || !strings.Contains(out.String(), `\u202e`) {
		t.Fatal("display did not preserve readable text with explicit escapes")
	}
	if err := WriteInspectionText(inspectFailWriter{}, inspection); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("display suppressed output failure")
	}
}

type inspectFailWriter struct{}

func (inspectFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func inspectionReceiptFixture(checkpoint Checkpoint) Result {
	return Result{SchemaVersion: "detective-pending-resume/v1", CheckpointDigest: checkpoint.Digest,
		State: "pending_verified", Batch: checkpoint.Batch, ReadbackVerified: true,
		Handoff: ahemcp.Handoff{SchemaVersion: "ahe-mcp-pending-handoff/v0", SourceSnapshotID: "srcsnap:inspect",
			ExtractionViewID: "view:inspect", ExtractionAttemptID: "attempt:inspect",
			ProposalOccurrenceID: "occ:inspect", ProposalCount: 1, Status: "pending"}}
}
