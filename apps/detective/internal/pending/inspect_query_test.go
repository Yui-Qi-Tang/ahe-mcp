//go:build darwin || linux

package pending

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

func TestInspectQueryReadsOnlyExactPendingOccurrence(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("line_ending_%q", ending), func(t *testing.T) {
			for _, key := range []string{"DATABASE_DSN", "PGPASSWORD", "OPENAI_API_KEY", "GOOGLE_API_KEY", "DETECTIVE_BASE_URL", "DETECTIVE_AHE_INGEST_COMMAND"} {
				t.Setenv(key, "synthetic-secret-must-not-leave-parent")
			}
			document, batch := checkpointFixture(t, ending)
			checkpoint, err := New("mock:inspection", document, batch, "")
			if err != nil {
				t.Fatal(err)
			}
			checkpointPath, receiptPath, receipt := inspectQueryInputs(t, checkpoint)
			launcher, trace := inspectQueryLauncher(t, "valid", checkpoint, receipt.Handoff)
			assertUnchanged := inspectQueryUnchanged(t, checkpointPath, receiptPath)
			before := time.Now().UTC()
			result, err := Inspect(t.Context(), checkpointPath, receiptPath, launcher)
			after := time.Now().UTC()
			if err != nil {
				t.Fatal(err)
			}
			if result.Pending.State != "pending_verified" || result.AuthorityEffect != "none" || result.HumanReview != "not_recorded" ||
				result.NextAction != "inspect_and_handoff_only" || result.CheckpointDigest != checkpoint.Digest ||
				!reflect.DeepEqual(result.Pending.Receipt, &receipt.Handoff) || !reflect.DeepEqual(result.Candidate, batch.Rows[0].Result.Records[0]) {
				t.Fatalf("unexpected inspection: %+v", result)
			}
			completed, err := time.Parse(time.RFC3339Nano, result.Pending.CheckCompletedAt)
			if err != nil || completed.Before(before) || completed.After(after) || !strings.HasSuffix(result.Pending.CheckCompletedAt, "Z") {
				t.Fatalf("invalid client completion time %q: %v", result.Pending.CheckCompletedAt, err)
			}
			calls := inspectQueryCalls(t, trace)
			if len(calls) != 4 || calls[0].Method != "initialize" || calls[1].Method != "notifications/initialized" ||
				calls[2].Method != "tools/list" || calls[3].Method != "tools/call" || calls[3].Params.Name != "get_evidence_record" ||
				!reflect.DeepEqual(calls[3].Params.Arguments, map[string]string{"proposal_occurrence_id": receipt.Handoff.ProposalOccurrenceID}) {
				t.Fatalf("inspection performed calls outside exact readback: %+v", calls)
			}
			assertUnchanged()
		})
	}
}

func TestInspectQueryFailuresReturnNoInspection(t *testing.T) {
	for _, mode := range []string{"missing-record", "tool-error", "rpc-error", "nonpending", "admitted", "rejected", "changed-quote", "changed-statement", "wrong-endpoint", "nonzero-eof", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			checkpoint := validCheckpoint(t)
			checkpointPath, receiptPath, receipt := inspectQueryInputs(t, checkpoint)
			launcher, trace := inspectQueryLauncher(t, mode, checkpoint, receipt.Handoff)
			assertUnchanged := inspectQueryUnchanged(t, checkpointPath, receiptPath)
			ctx := t.Context()
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			result, err := Inspect(ctx, checkpointPath, receiptPath, launcher)
			if err == nil || !reflect.DeepEqual(result, Inspection{}) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("failed query leaked a result or diagnostic: result=%+v err=%v", result, err)
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout lost context cause: %v", err)
			}
			var calls []inspectQueryRequest
			info, traceErr := os.Stat(trace)
			// A deadline may expire before a loaded machine starts the helper,
			// or before its first trace write. That is still a correct bounded
			// failure, not evidence that a Query call necessarily took place.
			preTraceTimeout := mode == "timeout" && (errors.Is(traceErr, os.ErrNotExist) || (traceErr == nil && info.Size() == 0))
			if !preTraceTimeout {
				calls = inspectQueryCalls(t, trace)
			}
			for _, call := range calls {
				if call.Method == "tools/call" && (mode == "wrong-endpoint" || call.Params.Name != "get_evidence_record") {
					t.Fatalf("failed inspection invoked out-of-scope tool: %+v", call)
				}
			}
			assertUnchanged()
			// Failure is not an absence claim or a reason to replay intake. An
			// independent offline inspection remains available with no DB claim.
			offline, err := Inspect(t.Context(), checkpointPath, "", "")
			if err != nil || offline.Pending.State != "not_checked" || offline.Pending.Receipt != nil || offline.Pending.CheckCompletedAt != "" {
				t.Fatalf("offline fallback is not independent: %+v, %v", offline, err)
			}
			assertUnchanged()
		})
	}
}

func TestInspectQueryReceiptMismatchStopsBeforeLauncher(t *testing.T) {
	for _, mode := range []string{"version", "digest", "state", "readback", "batch-source", "batch-model", "batch-quote", "handoff-version", "handoff-state", "handoff-count", "handoff-occurrence", "handoff-snapshot", "handoff-view", "handoff-attempt"} {
		t.Run(mode, func(t *testing.T) {
			checkpoint := validCheckpoint(t)
			checkpointPath, receiptPath, receipt := inspectQueryInputs(t, checkpoint)
			launcher, trace := inspectQueryLauncher(t, "valid", checkpoint, receipt.Handoff)
			// Detach the receipt's slices before mutating nested batch values.
			body, err := os.ReadFile(receiptPath)
			receipt = Result{}
			if err != nil || json.Unmarshal(body, &receipt) != nil {
				t.Fatal("cannot detach fixture receipt")
			}
			switch mode {
			case "version":
				receipt.SchemaVersion = "future-resume"
			case "digest":
				receipt.CheckpointDigest = "sha256:" + strings.Repeat("0", 64)
			case "state":
				receipt.State = "approved"
			case "readback":
				receipt.ReadbackVerified = false
			case "batch-source":
				receipt.Batch.Source.Path = "/synthetic-other-source.md"
			case "batch-model":
				receipt.Batch.Extractor.Model = "different-model"
			case "batch-quote":
				receipt.Batch.Rows[0].Result.Records[0].Citation.ExactQuote = "different quote"
			case "handoff-version":
				receipt.Handoff.SchemaVersion = "future-handoff"
			case "handoff-state":
				receipt.Handoff.Status = "admitted"
			case "handoff-count":
				receipt.Handoff.ProposalCount = 2
			case "handoff-occurrence":
				receipt.Handoff.ProposalOccurrenceID = ""
			case "handoff-snapshot":
				receipt.Handoff.SourceSnapshotID = ""
			case "handoff-view":
				receipt.Handoff.ExtractionViewID = ""
			case "handoff-attempt":
				receipt.Handoff.ExtractionAttemptID = ""
			}
			inspectQueryWriteJSON(t, receiptPath, receipt)
			assertUnchanged := inspectQueryUnchanged(t, checkpointPath, receiptPath)
			result, err := Inspect(t.Context(), checkpointPath, receiptPath, launcher)
			if err == nil || !reflect.DeepEqual(result, Inspection{}) {
				t.Fatalf("receipt mismatch returned an inspection: %+v, %v", result, err)
			}
			if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("launcher ran before receipt acceptance: %v", err)
			}
			assertUnchanged()
		})
	}
}

func TestInspectQueryDoesNotExposeBackendDiagnostics(t *testing.T) {
	for _, mode := range []string{"valid", "tool-error", "rpc-error", "changed-quote", "nonzero-eof"} {
		t.Run(mode, func(t *testing.T) {
			checkpoint := validCheckpoint(t)
			checkpointPath, receiptPath, receipt := inspectQueryInputs(t, checkpoint)
			launcher, _ := inspectQueryLauncher(t, mode, checkpoint, receipt.Handoff)
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInspectQueryHelperProcess$")
			command.Env = append(os.Environ(), "DETECTIVE_INSPECT_QUERY_HELPER=inspection-driver",
				"DETECTIVE_INSPECT_CHECKPOINT="+checkpointPath, "DETECTIVE_INSPECT_RECEIPT="+receiptPath,
				"DETECTIVE_INSPECT_LAUNCHER="+launcher)
			output, err := command.CombinedOutput()
			if bytes.Contains(output, []byte("synthetic-secret")) {
				t.Fatalf("inspection exposed raw Query diagnostics: %q", output)
			}
			if mode == "valid" {
				var inspection Inspection
				if err != nil || json.Unmarshal(output, &inspection) != nil || inspection.Pending.State != "pending_verified" {
					t.Fatalf("successful inspection output was not clean JSON: %q, %v", output, err)
				}
			} else if err == nil || bytes.Contains(output, []byte("detective-pending-inspection/v1")) {
				t.Fatalf("failed query emitted a successful inspection: %q, %v", output, err)
			}
		})
	}
}

func inspectQueryInputs(t *testing.T, checkpoint Checkpoint) (string, string, Result) {
	t.Helper()
	directory := checkpointDirectory(t)
	checkpointPath, receiptPath := filepath.Join(directory, "checkpoint.json"), filepath.Join(directory, "resume.json")
	receipt := Result{SchemaVersion: "detective-pending-resume/v1", CheckpointDigest: checkpoint.Digest,
		State: "pending_verified", Batch: checkpoint.Batch, ReadbackVerified: true,
		Handoff: ahemcp.Handoff{SchemaVersion: "ahe-mcp-pending-handoff/v0", Status: "pending", ProposalCount: 1,
			SourceSnapshotID: "srcsnap:inspection", ExtractionViewID: "view:inspection", ExtractionAttemptID: "attempt:inspection", ProposalOccurrenceID: "occ:inspection"}}
	inspectQueryWriteJSON(t, checkpointPath, checkpoint)
	inspectQueryWriteJSON(t, receiptPath, receipt)
	return checkpointPath, receiptPath, receipt
}

func inspectQueryWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func inspectQueryUnchanged(t *testing.T, checkpointPath, receiptPath string) func() {
	t.Helper()
	read := func(path string) []byte {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	checkpoint, receipt := read(checkpointPath), read(receiptPath)
	return func() {
		t.Helper()
		if !bytes.Equal(checkpoint, read(checkpointPath)) || !bytes.Equal(receipt, read(receiptPath)) {
			t.Fatal("inspection modified saved inputs")
		}
		entries, err := os.ReadDir(filepath.Dir(checkpointPath))
		if err != nil || len(entries) != 2 || entries[0].Name() != "checkpoint.json" || entries[1].Name() != "resume.json" {
			t.Fatalf("inspection changed private directory or created a lock: %v, %v", entries, err)
		}
	}
}

type inspectQueryRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	} `json:"params"`
}

func inspectQueryCalls(t *testing.T, path string) []inspectQueryRequest {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []inspectQueryRequest
	for line := range bytes.SplitSeq(bytes.TrimSpace(body), []byte("\n")) {
		var call inspectQueryRequest
		if err := json.Unmarshal(line, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func inspectQueryLauncher(t *testing.T, mode string, checkpoint Checkpoint, handoff ahemcp.Handoff) (string, string) {
	t.Helper()
	directory := checkpointDirectory(t)
	launcher, trace := filepath.Join(directory, "query-launcher"), filepath.Join(directory, "query-trace.jsonl")
	fixture := filepath.Join(directory, "query-record.json")
	inspectQueryWriteJSON(t, fixture, inspectQueryRecord(checkpoint, handoff))
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nexport DETECTIVE_INSPECT_QUERY_HELPER=" + quote(mode) +
		"\nexport DETECTIVE_INSPECT_QUERY_TRACE=" + quote(trace) + "\nexport DETECTIVE_INSPECT_QUERY_RECORD=" + quote(fixture) +
		"\nexec " + quote(os.Args[0]) + " -test.run='^TestInspectQueryHelperProcess$'\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return launcher, trace
}

func inspectQueryRecord(checkpoint Checkpoint, handoff ahemcp.Handoff) map[string]any {
	hash := func(value string) string {
		digest := sha256.Sum256([]byte(value))
		return "sha256:" + hex.EncodeToString(digest[:])
	}
	record, extractor := checkpoint.Batch.Rows[0].Result.Records[0], checkpoint.Batch.Extractor
	statement, err := ahemcp.ProposalStatement(extractor, record)
	if err != nil {
		panic("invalid synthetic Query statement fixture")
	}
	config, _ := json.Marshal(map[string]string{"model": extractor.Model, "source_mode": "frozen_local_snapshot"}) // String maps always encode.
	refs := []map[string]any{}
	start, span := 0, 0
	for line, raw := range strings.Split(checkpoint.RawText, "\n") {
		text := strings.TrimSuffix(raw, "\r")
		if text != "" {
			span++
			if line+1 >= record.Citation.StartLine && line+1 <= record.Citation.EndLine {
				refs = append(refs, map[string]any{"extraction_view_id": handoff.ExtractionViewID,
					"span_id": fmt.Sprintf("span:S%d", span), "start_byte": start, "end_byte": start + len(text), "quoted_text": text, "quoted_text_hash": hash(text)})
			}
		}
		start += len(raw) + 1
	}
	return map[string]any{
		"record_ref":        map[string]string{"kind": "proposal", "id": handoff.ProposalOccurrenceID},
		"proposal_local_id": "record-1", "proposal_fingerprint": "fp:inspection", "proposal_fingerprint_version": "statement-v1",
		"proposal_kind": "statement", "statement_text": statement, "admission_outcome": "pending", "canonical_ref": nil,
		"source": map[string]any{"binding_kind": "source_snapshot", "source_snapshot_id": handoff.SourceSnapshotID,
			"source_system": "manual_text", "source_id": checkpoint.SourceID, "source_version": hash(checkpoint.RawText), "raw_content_hash": hash(checkpoint.RawText)},
		"extractor": map[string]string{"extractor_definition_id": "extractor:inspection", "extraction_run_id": "run:inspection",
			"extraction_attempt_id": handoff.ExtractionAttemptID, "producer_session_ref": "", "name": extractor.Name,
			"version": extractor.Version, "config_hash": hash(string(config)), "attempt_status": "succeeded"},
		"source_refs": refs, "extraction_view_id": handoff.ExtractionViewID, "renderer_name": "manual-text-identity",
		"renderer_version": "v1", "rendered_content_hash": hash(checkpoint.RawText),
	}
}

// TestInspectQueryHelperProcess is reached only through the synthetic launcher
// after the production Query transport has filtered its inherited environment.
func TestInspectQueryHelperProcess(t *testing.T) {
	mode := os.Getenv("DETECTIVE_INSPECT_QUERY_HELPER")
	if mode == "" {
		return
	}
	if mode == "inspection-driver" {
		inspection, err := Inspect(t.Context(), os.Getenv("DETECTIVE_INSPECT_CHECKPOINT"),
			os.Getenv("DETECTIVE_INSPECT_RECEIPT"), os.Getenv("DETECTIVE_INSPECT_LAUNCHER"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if json.NewEncoder(os.Stdout).Encode(inspection) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(serveInspectQuery(mode))
}

func serveInspectQuery(mode string) int {
	for _, entry := range os.Environ() {
		if strings.Contains(entry, "synthetic-secret") {
			return 20
		}
	}
	body, err := os.ReadFile(os.Getenv("DETECTIVE_INSPECT_QUERY_RECORD"))
	if err != nil {
		return 21
	}
	var record map[string]any
	if json.Unmarshal(body, &record) != nil {
		return 22
	}
	trace, err := os.OpenFile(os.Getenv("DETECTIVE_INSPECT_QUERY_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 23
	}
	defer trace.Close()
	// This diagnostic must be discarded by the transport, including success.
	fmt.Fprintln(os.Stderr, "synthetic-secret from untrusted Query diagnostics")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request inspectQueryRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 24
		}
		if _, err := fmt.Fprintln(trace, scanner.Text()); err != nil {
			return 25
		}
		if request.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			name := "ahe-query-mcp"
			if mode == "wrong-endpoint" {
				name = "ahe-ingest-mcp"
			}
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]string{"name": name, "version": "synthetic-test"}}
		case "tools/list":
			names := []string{"get_evidence_record", "list_evidence_records", "search_evidence_records", "get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance", "get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path", "get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal", "get_canonical_supersession_head", "get_canonical_supersession_currentness"}
			tools := make([]map[string]any, 0, len(names))
			for _, name := range names {
				tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]string{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			if request.Params.Name != "get_evidence_record" || !reflect.DeepEqual(request.Params.Arguments, map[string]string{"proposal_occurrence_id": "occ:inspection"}) {
				return 26
			}
			switch mode {
			case "timeout":
				_, _ = io.Copy(io.Discard, os.Stdin) // Wait for cancellation, never a timer or external service.
				return 0
			case "rpc-error":
				if json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32000, "message": "synthetic-secret backend detail"}}) != nil {
					return 27
				}
				continue
			case "missing-record":
				record = map[string]any{}
			case "tool-error":
				record = map[string]any{"message": "synthetic-secret backend detail"}
			case "nonpending", "admitted", "rejected":
				record["admission_outcome"] = mode
			case "changed-statement":
				record["statement_text"] = "synthetic-secret unexpected statement"
			case "changed-quote":
				record["source_refs"].([]any)[0].(map[string]any)["quoted_text"] = "synthetic-secret unexpected quote"
			}
			data, err := json.Marshal(record)
			if err != nil {
				return 28
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}},
				"structuredContent": json.RawMessage(data), "isError": mode == "tool-error"}
		default:
			return 29
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			return 30
		}
	}
	if scanner.Err() != nil || mode == "nonzero-eof" {
		return 31
	}
	return 0
}
