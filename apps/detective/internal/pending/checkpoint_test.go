//go:build darwin || linux

package pending

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

func TestCheckpointRoundTripRestoresSavedBytes(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[ending], func(t *testing.T) {
			document, batch := checkpointFixture(t, ending)
			checkpoint, err := New("mock:checkpoint", document, batch, "")
			if err != nil {
				t.Fatal(err)
			}
			if checkpoint.SchemaVersion != checkpointVersion || checkpoint.RequestIdentityVersion != requestVersion || len(checkpoint.Digest) != 71 || !strings.HasPrefix(checkpoint.Digest, "sha256:") {
				t.Fatal("checkpoint did not carry its exact version and digest contract")
			}
			path := filepath.Join(checkpointDirectory(t), "pending.json")
			writer, err := Reserve(path)
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
			loaded, err := Load(path)
			if err != nil || !reflect.DeepEqual(loaded, checkpoint) {
				t.Fatalf("Load() = %+v, %v", loaded, err)
			}
			restored, err := loaded.Document()
			if err != nil || restored.RawText() != document.RawText() || !reflect.DeepEqual(restored.Source(), document.Source()) {
				t.Fatal("checkpoint did not reconstruct the original observation without its source file")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("published checkpoint is not private")
			}
		})
	}
}

func TestCheckpointDetachesBatchAndDetectsMutation(t *testing.T) {
	document, batch := checkpointFixture(t, "\n")
	checkpoint, err := New("mock:checkpoint", document, batch, "")
	if err != nil {
		t.Fatal(err)
	}
	batch.Rows[0].Result.Records[0].Statement = "changed outside checkpoint"
	batch.Source.SelectedSections[0].Heading = "changed selection"
	if _, err := checkpoint.Document(); err != nil {
		t.Fatalf("caller mutation changed detached checkpoint: %v", err)
	}
	checkpoint.Batch.Rows[0].Result.Records[0].Statement = "changed inside checkpoint"
	if _, err := checkpoint.Document(); err == nil {
		t.Fatal("changed checkpoint retained valid digest")
	}
}

func TestCheckpointNewRejectsInvalidInputs(t *testing.T) {
	for _, name := range []string{"nil_document", "source_id", "two_rows", "two_candidates", "no_candidate", "source_hash", "source_path", "row_text", "row_status", "scope", "summary", "extractor_version", "invalid_UTF8", "status_clause"} {
		t.Run(name, func(t *testing.T) {
			document, batch := checkpointFixture(t, "\n")
			sourceID, clause := "mock:checkpoint", ""
			switch name {
			case "nil_document":
				document = nil
			case "source_id":
				sourceID = ""
			case "two_rows":
				batch.Rows = append(batch.Rows, batch.Rows[0])
			case "two_candidates":
				batch.Rows[0].Result.Records = append(batch.Rows[0].Result.Records, batch.Rows[0].Result.Records[0])
			case "no_candidate":
				batch.Rows[0].Result = nil
			case "source_hash":
				batch.Source.SHA256 = "wrong"
			case "source_path":
				batch.Source.Path = "/another/STATUS.md"
			case "row_text":
				batch.Rows[0].Row.Text = "changed row"
			case "row_status":
				batch.Rows[0].Status = "failed"
			case "scope":
				batch.Section.Heading = "another section"
			case "summary":
				batch.Summary.Attempted = 2
			case "extractor_version":
				batch.Extractor.Version = "future-version"
			case "invalid_UTF8":
				batch.Rows[0].Result.Limitations = []string{string([]byte{0xff})}
			case "status_clause":
				clause = "invented source clause"
			}
			if _, err := New(sourceID, document, batch, clause); err == nil {
				t.Fatal("invalid checkpoint inputs were accepted")
			}
		})
	}
}

func TestCheckpointLoadRejectsMalformedAndTamperedJSON(t *testing.T) {
	checkpoint := validCheckpoint(t)
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wrong_digest", "schema_version", "request_version", "semantic_mismatch", "unknown_field", "nested_unknown", "duplicate", "escaped_duplicate", "nested_duplicate", "case_alias", "missing_required", "trailing", "malformed", "invalid_UTF8", "oversized"} {
		t.Run(name, func(t *testing.T) {
			body := append([]byte(nil), encoded...)
			switch name {
			case "wrong_digest":
				body = bytes.Replace(body, []byte(checkpoint.Digest), []byte("sha256:"+strings.Repeat("0", 64)), 1)
			case "schema_version", "request_version", "semantic_mismatch":
				var changed Checkpoint
				if err := json.Unmarshal(body, &changed); err != nil {
					t.Fatal(err)
				}
				if name == "schema_version" {
					changed.SchemaVersion = "future-checkpoint"
				} else if name == "request_version" {
					changed.RequestIdentityVersion = "detective-v0"
				} else {
					changed.Batch.Rows[0].Result.Records[0].Citation.ExactQuote = "invented quote"
				}
				payload, err := changed.canonical()
				if err != nil {
					t.Fatal(err)
				}
				changed.Digest = checkpointHash(payload)
				body, err = json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
			case "unknown_field":
				body = append([]byte(`{"launcher":"forbidden",`), body[1:]...)
			case "nested_unknown":
				body = bytes.Replace(body, []byte(`"batch":{`), []byte(`"batch":{"approval":"forbidden",`), 1)
			case "duplicate":
				body = append([]byte(`{"source_id":"duplicate",`), body[1:]...)
			case "escaped_duplicate":
				body = append([]byte(`{"source_\u0069d":"duplicate",`), body[1:]...)
			case "nested_duplicate":
				body = bytes.Replace(body, []byte(`"batch":{`), []byte(`"batch":{"schema_version":"duplicate",`), 1)
			case "case_alias":
				body = bytes.Replace(body, []byte(`"source_id"`), []byte(`"SOURCE_ID"`), 1)
			case "missing_required":
				body = bytes.Replace(body, []byte(`,"status_clause":""`), nil, 1)
			case "trailing":
				body = append(body, []byte(" {}")...)
			case "malformed":
				body = []byte(`{"broken":`)
			case "invalid_UTF8":
				body = []byte{'{', '"', 0xff, '"', ':', '0', '}'}
			case "oversized":
				body = bytes.Repeat([]byte{' '}, maxCheckpointBytes+1)
			}
			path := filepath.Join(checkpointDirectory(t), "invalid.json")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid checkpoint JSON was accepted")
			}
			if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
				t.Fatal("Load mutated the checkpoint directory")
			}
		})
	}
}

func TestCheckpointReserveAndLoadRejectUnsafeFiles(t *testing.T) {
	for _, name := range []string{"parent_permissions", "file_permissions", "target_symlink", "target_hardlink", "ancestor_symlink", "target_FIFO", "lock_symlink", "lock_hardlink", "lock_FIFO", "lock_permissions"} {
		t.Run(name, func(t *testing.T) {
			directory := checkpointDirectory(t)
			path := filepath.Join(directory, "pending.json")
			body, err := json.Marshal(validCheckpoint(t))
			if err != nil {
				t.Fatal(err)
			}
			reserveOnly := strings.HasPrefix(name, "lock_")
			switch name {
			case "parent_permissions":
				err = os.Chmod(directory, 0o750)
			case "file_permissions":
				err = os.WriteFile(path, body, 0o640)
			case "target_symlink", "lock_symlink", "target_hardlink", "lock_hardlink":
				target := filepath.Join(directory, "other.json")
				if err = os.WriteFile(target, body, 0o600); err == nil {
					name := path
					if reserveOnly {
						name += ".lock"
					}
					if strings.Contains(t.Name(), "hardlink") {
						err = os.Link(target, name)
					} else {
						err = os.Symlink(target, name)
					}
				}
			case "ancestor_symlink":
				alias := filepath.Join(checkpointDirectory(t), "alias")
				err = os.Symlink(directory, alias)
				path = filepath.Join(alias, "pending.json")
			case "target_FIFO", "lock_FIFO":
				name := path
				if reserveOnly {
					name += ".lock"
				}
				err = syscall.Mkfifo(name, 0o600)
			case "lock_permissions":
				err = os.WriteFile(path+".lock", nil, 0o640)
			}
			if err != nil {
				t.Fatal(err)
			}
			if writer, err := Reserve(path); err == nil {
				_ = writer.Close()
				t.Fatal("unsafe checkpoint reservation succeeded")
			}
			if !reserveOnly {
				if _, err := Load(path); err == nil {
					t.Fatal("unsafe checkpoint load succeeded")
				}
			}
		})
	}
}

func TestCheckpointRejectsNoncanonicalPaths(t *testing.T) {
	directory := checkpointDirectory(t)
	for _, path := range []string{"", "pending.json", "/", directory + "/../pending.json", directory + "//pending.json", directory + "/pending\n.json"} {
		if writer, err := Reserve(path); err == nil {
			_ = writer.Close()
			t.Fatalf("Reserve(%q) succeeded", path)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("Load(%q) succeeded", path)
		}
	}
}

func TestCheckpointNeverOverwritesExistingTarget(t *testing.T) {
	for _, timing := range []string{"before_reserve", "after_reserve", "after_write"} {
		t.Run(timing, func(t *testing.T) {
			directory := checkpointDirectory(t)
			path := filepath.Join(directory, "pending.json")
			original := []byte("existing synthetic data")
			if timing == "before_reserve" {
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := Reserve(path); !errors.Is(err, ErrExists) {
					t.Fatalf("Reserve() = %v, want ErrExists", err)
				}
			} else {
				writer, err := Reserve(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = writer.Close() })
				if timing == "after_write" {
					if err := writer.Write(validCheckpoint(t)); err != nil {
						t.Fatal(err)
					}
					original, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := writer.Write(validCheckpoint(t)); !errors.Is(err, ErrExists) {
					t.Fatalf("Write() = %v, want ErrExists", err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("existing target was changed")
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".tmp") {
					t.Fatal("publication left a private temporary file behind")
				}
			}
		})
	}
}

func TestCheckpointParallelReserveAndRelease(t *testing.T) {
	path := filepath.Join(checkpointDirectory(t), "pending.json")
	type result struct {
		writer *Writer
		err    error
	}
	results := make(chan result, 12)
	start := make(chan struct{})
	for range cap(results) {
		go func() {
			<-start
			writer, err := Reserve(path)
			results <- result{writer, err}
		}()
	}
	close(start)
	var owner *Writer
	var acquisitionErrors []error
	owners := 0
	for range cap(results) {
		got := <-results
		if got.err == nil {
			owners++
			t.Cleanup(func() { _ = got.writer.Close() })
			owner = got.writer
		} else if !errors.Is(got.err, ErrBusy) {
			acquisitionErrors = append(acquisitionErrors, got.err)
		}
	}
	if owners != 1 || len(acquisitionErrors) != 0 {
		t.Fatalf("parallel reservation: owners=%d errors=%v", owners, acquisitionErrors)
	}
	before, err := os.Stat(path + ".lock")
	if err != nil || before.Mode().Perm() != 0o600 {
		t.Fatal("reservation sidecar is not a retained private file")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Write(validCheckpoint(t)); err == nil {
		t.Fatal("closed reservation published a checkpoint")
	}
	next, err := Reserve(path)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	after, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("reservation release replaced the sidecar inode")
	}
}

func TestCheckpointKernelLockReleasesOnProcessExit(t *testing.T) {
	path := filepath.Join(checkpointDirectory(t), "pending.json")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckpointLockProcessHelper$")
	command.Env = []string{"DETECTIVE_CHECKPOINT_TEST_PATH=" + path, "GORACE=atexit_sleep_ms=0 halt_on_error=1"}
	if err := command.Run(); err != nil {
		t.Fatal("isolated checkpoint lock process failed")
	}
	writer, err := Reserve(path)
	if err != nil {
		t.Fatalf("kernel lock survived process exit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointLockProcessHelper(t *testing.T) {
	path := os.Getenv("DETECTIVE_CHECKPOINT_TEST_PATH")
	if path == "" {
		return
	}
	if _, err := Reserve(path); err != nil {
		os.Exit(1)
	}
	// Intentionally omit Close to prove kernel cleanup, not a stale-PID policy.
	os.Exit(0)
}

func TestCheckpointRejectsChangedReservation(t *testing.T) {
	for _, change := range []string{"directory_permissions", "replaced_lock"} {
		t.Run(change, func(t *testing.T) {
			directory := checkpointDirectory(t)
			path := filepath.Join(directory, "pending.json")
			writer, err := Reserve(path)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if change == "directory_permissions" {
				err = os.Chmod(directory, 0o750)
			} else if err = os.Remove(path + ".lock"); err == nil {
				err = os.WriteFile(path+".lock", nil, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Write(validCheckpoint(t)); err == nil {
				t.Fatal("changed reservation published checkpoint")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("rejected reservation created checkpoint target")
			}
		})
	}
}

func checkpointDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func validCheckpoint(t *testing.T) Checkpoint {
	t.Helper()
	document, batch := checkpointFixture(t, "\n")
	checkpoint, err := New("mock:checkpoint", document, batch, "")
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func checkpointFixture(t *testing.T, ending string) (*labstatus.Document, labstatus.RowBatch) {
	t.Helper()
	const row = "| 臺灣證據🙂 | **LAB PROVEN** | synthetic fixture only |"
	raw := strings.ReplaceAll("# Fixture\n\n## Status at a Glance\n\n| Capability | Status | Boundary |\n| --- | --- | --- |\n"+row+"\n", "\n", ending)
	document, err := labstatus.RestoreDocument("/synthetic-observation-not-on-disk/STATUS.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := document.SelectSections("Status at a Glance")
	if err != nil {
		t.Fatal(err)
	}
	batch := labstatus.RowBatch{SchemaVersion: labstatus.RowBatchSchemaVersion, Source: selected.Source(),
		Extractor: labstatus.ExtractorInfo{Name: labstatus.ExtractorName, Version: labstatus.ExtractorVersion, Model: "synthetic-model"},
		Section:   selected.Source().SelectedSections[0], Summary: labstatus.RowSummary{Attempted: 1, Validated: 1},
		Rows: []labstatus.RowOutcome{{Row: labstatus.SourceRow{StartLine: 7, EndLine: 7, Text: row}, Status: "validated",
			Result: &labstatus.CandidateSet{Outcome: "extracted", Abstentions: []labstatus.Abstention{}, Limitations: []string{}, Records: []labstatus.Record{{
				RecordType: "capability_state", Subject: "checkpoint", Statement: "The synthetic source reports lab evidence.", EpistemicClass: "claim", Status: "lab_proven", Scope: "lab_contract", SelectionState: "unspecified",
				Citation: labstatus.Citation{StartLine: 7, EndLine: 7, ExactQuote: row}, BlockedBy: []string{}, DoesNotEstablish: []string{}, Qualifiers: []string{},
			}}}}}}
	return document, batch
}
