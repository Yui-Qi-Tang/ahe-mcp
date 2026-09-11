//go:build darwin || linux

package pending

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

func batchFixture(t *testing.T, count int) (*labstatus.Document, labstatus.RowBatch) {
	t.Helper()
	document, batch := checkpointFixture(t, "\r\n")
	first := batch.Rows[0].Result.Records[0]
	batch.Rows[0].Result.Records = []labstatus.Record{}
	for i := 0; i < count; i++ {
		record := first
		record.Subject = fmt.Sprintf("synthetic subject %d", i+1)
		record.Statement = fmt.Sprintf("Synthetic fixture claim %d is scoped to lab evidence.", i+1)
		batch.Rows[0].Result.Records = append(batch.Rows[0].Result.Records, record)
	}
	return document, batch
}

func savedBatch(t *testing.T, count int) (string, BatchIndex) {
	t.Helper()
	document, batch := batchFixture(t, count)
	index, err := NewBatchIndex("mock:batch", document, batch, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkpointDirectory(t), "batch.json")
	w, err := Reserve(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.WriteBatchIndex(index); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path, index
}

func TestBatchIndexPreservesAllCandidatesAndRecoverableChildren(t *testing.T) {
	path, index := savedBatch(t, 3)
	if len(index.Members) != 3 || len(index.Batch.Rows[0].Result.Records) != 3 {
		t.Fatal("full candidate set lost")
	}
	loaded, err := LoadBatchIndex(path)
	if err != nil || !reflect.DeepEqual(loaded, index) {
		t.Fatal("index did not round trip")
	}
	inspection, err := InspectBatch(t.Context(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range inspection.Members {
		if member.CheckpointAvailable || member.State != "not_checked" {
			t.Fatal("missing child invented")
		}
	}
	if err := MaterializeBatch(path); err != nil {
		t.Fatal(err)
	}
	for _, member := range index.Members {
		cpPath, _, _ := batchMemberPaths(path, member.Ordinal)
		checkpoint, err := Load(cpPath)
		if err != nil || checkpoint.Digest != member.CheckpointDigest || len(checkpoint.Batch.Rows[0].Result.Records) != 1 || checkpoint.Batch.Rows[0].Result.Records[0].Statement != index.Batch.Rows[0].Result.Records[member.Ordinal-1].Statement {
			t.Fatal("projection lost order or candidate")
		}
	}
	if err := MaterializeBatch(path); err != nil {
		t.Fatal("exact materialization retry failed", err)
	}
}

func TestBatchIndexBoundsAndDuplicateNativeIdentity(t *testing.T) {
	for _, count := range []int{0, 17} {
		document, batch := batchFixture(t, count)
		if _, err := NewBatchIndex("mock:batch", document, batch, ""); err == nil {
			t.Fatal("unbounded set accepted")
		}
	}
	document, batch := batchFixture(t, 2)
	// Legacy projection omits differing Record context and must still reject
	// this native identity collision. Version 0.1.2 carries that context.
	batch.Extractor.Version = "0.1.1"
	batch.Rows[0].Result.Records[1].Statement = batch.Rows[0].Result.Records[0].Statement
	if _, err := NewBatchIndex("mock:batch", document, batch, ""); err == nil {
		t.Fatal("same native request represented twice")
	}
	document, batch = batchFixture(t, 2)
	index, err := NewBatchIndex("mock:batch", document, batch, "")
	if err != nil {
		t.Fatal(err)
	}
	batch.Rows[0].Result.Records[0].Statement = "outside mutation"
	if _, err := index.Document(); err != nil {
		t.Fatal("input mutation changed detached index")
	}
	index.Members[0].Ordinal = 2
	if _, err := index.Document(); err == nil {
		t.Fatal("member drift accepted")
	}
}

func seedBatchLocators(t *testing.T, path string, index BatchIndex) (map[string]any, []ahemcp.CandidateLocator) {
	t.Helper()
	if err := MaterializeBatch(path); err != nil {
		t.Fatal(err)
	}
	doc, err := index.Document()
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]any{}
	locators := []ahemcp.CandidateLocator{}
	for _, member := range index.Members {
		cp, err := batchMemberCheckpoint(index, doc, member.Ordinal)
		if err != nil {
			t.Fatal(err)
		}
		_, locatorPath, _ := batchMemberPaths(path, member.Ordinal)
		id := fmt.Sprintf("%064x", member.Ordinal)
		locator := ahemcp.CandidateLocator{SourceSnapshotID: "srcsnap:" + strings.Repeat("a", 64), ExtractionViewID: "view:" + strings.Repeat("b", 64), ExtractionAttemptID: "attempt:" + id, ProposalOccurrenceID: "occ:" + id, SubmissionOutcome: "pending"}
		if err := saveBatchLocator(locatorPath, cp, locator); err != nil {
			t.Fatal(err)
		}
		h := ahemcp.Handoff{SourceSnapshotID: locator.SourceSnapshotID, ExtractionViewID: locator.ExtractionViewID, ExtractionAttemptID: locator.ExtractionAttemptID, ProposalOccurrenceID: locator.ProposalOccurrenceID}
		fixtures[locator.ProposalOccurrenceID] = inspectQueryRecord(cp, h)
		locators = append(locators, locator)
	}
	return fixtures, locators
}

func TestBatchResumeExistingLocatorsNeverSubmitAndReportTerminalMembers(t *testing.T) {
	path, index := savedBatch(t, 4)
	fixtures, locators := seedBatchLocators(t, path, index)
	states := []string{"pending", "admitted", "rejected", "audit_only"}
	for i, locator := range locators {
		record := fixtures[locator.ProposalOccurrenceID].(map[string]any)
		record["admission_outcome"] = states[i]
		if states[i] == "admitted" {
			record["canonical_ref"] = "canon-node:" + strings.Repeat("d", 16)
		}
	}
	launcher, _, _ := batchQueryLauncher(t, fixtures)
	result, err := ResumeBatch(t.Context(), path, "/does-not-exist-intake", launcher)
	if err != nil || !result.AllCandidatesChecked || result.Summary.SubmissionAttempts != 0 || result.Summary.Pending != 1 || result.Summary.Admitted != 1 || result.Summary.Rejected != 1 || result.Summary.AuditOnly != 1 {
		t.Fatalf("mixed outcomes: %+v %v", result.Summary, err)
	}
	for i, member := range result.Members {
		if member.State != states[i]+"_verified" || member.Locator == nil {
			t.Fatal("complete indexed outcome lost")
		}
		if member.PendingReceiptAvailable != (i == 0) {
			t.Fatal("terminal outcome acquired a pending receipt")
		}
	}
	// This exact v1 receipt can enter the existing per-candidate review prepare.
	cp, err := Load(result.Members[0].CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadInspectionReceipt(result.Members[0].ReceiptPath, cp); err != nil {
		t.Fatal(err)
	}
}

func TestBatchResumePartialFailurePreservesReceiptsAndResumesEveryMember(t *testing.T) {
	path, index := savedBatch(t, 3)
	fixtures, locators := seedBatchLocators(t, path, index)
	second := fixtures[locators[1].ProposalOccurrenceID]
	delete(fixtures, locators[1].ProposalOccurrenceID)
	launcher, fixturePath, _ := batchQueryLauncher(t, fixtures)
	result, err := ResumeBatch(t.Context(), path, "/does-not-exist-intake", launcher)
	if err == nil || result.Summary.Pending != 1 || result.Summary.Failed != 1 || result.Summary.NotAttempted != 1 || result.Members[1].FailureStage != "query_unverified" {
		t.Fatalf("partial result: %+v %v", result.Summary, err)
	}
	receiptBefore, err := os.ReadFile(result.Members[0].ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	fixtures[locators[1].ProposalOccurrenceID] = second
	inspectQueryWriteJSON(t, fixturePath, fixtures)
	recovered, err := ResumeBatch(t.Context(), path, "/does-not-exist-intake", launcher)
	if err != nil || recovered.Summary.Pending != 3 || recovered.Summary.SubmissionAttempts != 0 {
		t.Fatalf("recovery: %+v %v", recovered.Summary, err)
	}
	receiptAfter, _ := os.ReadFile(result.Members[0].ReceiptPath)
	if string(receiptBefore) != string(receiptAfter) {
		t.Fatal("saved receipt overwritten")
	}
	// The same saved locator is read again after a separate terminal decision.
	fixtures[locators[0].ProposalOccurrenceID].(map[string]any)["admission_outcome"] = "rejected"
	inspectQueryWriteJSON(t, fixturePath, fixtures)
	terminal, err := ResumeBatch(t.Context(), path, "/does-not-exist-intake", launcher)
	if err != nil || terminal.Summary.Rejected != 1 || terminal.Summary.Pending != 2 || !terminal.Members[0].PendingReceiptAvailable {
		t.Fatal("terminal lifecycle lost historical receipt", err)
	}
	receiptAfter, _ = os.ReadFile(result.Members[0].ReceiptPath)
	if string(receiptBefore) != string(receiptAfter) {
		t.Fatal("terminal read changed historical receipt")
	}
}

func TestBatchUnknownSubmissionStopsWithFullIndex(t *testing.T) {
	path, _ := savedBatch(t, 3)
	result, err := ResumeBatch(t.Context(), path, "/does-not-exist-intake", "/does-not-exist-query")
	if err == nil || len(result.Members) != 3 || result.Summary.Failed != 1 || result.Summary.NotAttempted != 2 || result.Members[0].FailureStage != "submission_outcome_uncertain" {
		t.Fatal("unknown write lost index or pretended completion")
	}
	if result.Members[0].Locator != nil {
		t.Fatal("unknown write invented locator")
	}
}

func TestBatchInspectionDoesNotPublishMissingFiles(t *testing.T) {
	path, index := savedBatch(t, 2)
	before, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	result, err := InspectBatch(context.Background(), path, "")
	if err != nil || result.Summary.NotChecked != len(index.Members) || result.AuthorityEffect != "none" {
		t.Fatal("offline inspection claimed authority", err)
	}
	after, _ := os.ReadDir(filepath.Dir(path))
	if len(before) != len(after) {
		t.Fatal("inspection wrote missing files")
	}
}

func batchQueryLauncher(t *testing.T, records map[string]any) (string, string, string) {
	t.Helper()
	directory := checkpointDirectory(t)
	fixturePath := filepath.Join(directory, "records.json")
	tracePath := filepath.Join(directory, "query.jsonl")
	launcher := filepath.Join(directory, "query-launcher")
	inspectQueryWriteJSON(t, fixturePath, records)
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nexport DETECTIVE_BATCH_QUERY_FIXTURE=" + quote(fixturePath) + "\nexport DETECTIVE_BATCH_QUERY_TRACE=" + quote(tracePath) + "\nexec " + quote(os.Args[0]) + " -test.run='^TestBatchQueryHelperProcess$'\n"
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return launcher, fixturePath, tracePath
}

func TestBatchQueryHelperProcess(t *testing.T) {
	path := os.Getenv("DETECTIVE_BATCH_QUERY_FIXTURE")
	if path == "" {
		return
	}
	os.Exit(serveBatchQuery(path, os.Getenv("DETECTIVE_BATCH_QUERY_TRACE")))
}

func serveBatchQuery(path, tracePath string) int {
	body, err := os.ReadFile(path)
	if err != nil {
		return 1
	}
	var records map[string]json.RawMessage
	if json.Unmarshal(body, &records) != nil {
		return 2
	}
	trace, err := os.OpenFile(tracePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 3
	}
	defer trace.Close()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request inspectQueryRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 4
		}
		fmt.Fprintln(trace, scanner.Text())
		if request.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "ahe-query-mcp", "version": "synthetic-batch"}}
		case "tools/list":
			names := []string{"get_evidence_record", "list_evidence_records", "search_evidence_records", "get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance", "get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path", "get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal", "get_canonical_supersession_head", "get_canonical_supersession_currentness"}
			tools := []map[string]any{}
			for _, name := range names {
				tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]string{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			if request.Params.Name != "get_evidence_record" || len(request.Params.Arguments) != 1 {
				return 5
			}
			record, ok := records[request.Params.Arguments["proposal_occurrence_id"]]
			if !ok {
				record = []byte(`{"message":"synthetic-secret unavailable record"}`)
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(record)}}, "structuredContent": record, "isError": !ok}
		default:
			return 6
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			return 7
		}
	}
	return 0
}
