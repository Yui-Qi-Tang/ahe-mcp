package ahemcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

func TestVerifyPendingReadback(t *testing.T) {
	for _, raw := range []string{"first line\nsecond line\n", "前文\r\n\r\n臺灣來源🙂\r\n第二行\r\n", "first line\nsecond line"} {
		t.Run(fmt.Sprintf("bytes_%d", len(raw)), func(t *testing.T) {
			document, records := handoffDocument(t, raw)
			if strings.Contains(raw, "\r") {
				records[0].Citation.StartLine, records[0].Citation.EndLine = 3, 4
				records[0].Citation.ExactQuote, _ = document.ExactQuote(3, 4)
			}
			handoff := queryHandoff()
			result := queryFixture(document, records[0], handoff)
			launcher, trace := queryLauncher(t, "valid", result)
			if err := VerifyPending(t.Context(), launcher, "lab-status", document, handoffExtractor(), records, handoff); err != nil {
				t.Fatal(err)
			}
			calls := readQueryTrace(t, trace)
			if len(calls) != 4 || calls[0].Method != "initialize" || calls[1].Method != "notifications/initialized" || calls[2].Method != "tools/list" ||
				calls[3].Method != "tools/call" || calls[3].Params.Name != "get_evidence_record" ||
				!reflect.DeepEqual(calls[3].Params.Arguments, map[string]string{"proposal_occurrence_id": handoff.ProposalOccurrenceID}) {
				t.Fatalf("unexpected readback calls: %+v", calls)
			}
		})
	}
}

func TestQueryClientKeepsAuthoritiesSeparate(t *testing.T) {
	for _, tool := range []string{"submit_text_source", "get_extractor_input", "submit_extractor_output", "admit_reviewed_source_claim", "admit_pending_proposal", "list_evidence_records", "open_canonical_read_view", "get_grounded_evidence_brief"} {
		t.Run(tool, func(t *testing.T) {
			launcher, trace := queryLauncher(t, "valid", pendingRecord{})
			c, err := startQuery(t.Context(), launcher)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if err := c.initialize(); err != nil {
				t.Fatal(err)
			}
			if err := c.call(tool, map[string]string{}, &struct{}{}); err == nil {
				t.Fatal("out-of-scope query call accepted")
			}
			for _, call := range readQueryTrace(t, trace) {
				if call.Method == "tools/call" {
					t.Fatal("denied call reached child")
				}
			}
			assertTransportJoined(t, c)
		})
	}
}

func TestVerifyPendingRejectsWrongEndpointAndAbnormalExit(t *testing.T) {
	for _, mode := range []string{"intake", "reviewer", "extra-tool", "duplicate-tool", "missing-tool", "wrong-protocol", "pagination", "missing-record", "tool-error", "nonzero-eof"} {
		t.Run(mode, func(t *testing.T) {
			document, records := handoffDocument(t, "first line\n")
			handoff := queryHandoff()
			launcher, trace := queryLauncher(t, mode, queryFixture(document, records[0], handoff))
			err := VerifyPending(t.Context(), launcher, "lab-status", document, handoffExtractor(), records, handoff)
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("VerifyPending() = %v", err)
			}
			if !slices.Contains([]string{"missing-record", "tool-error", "nonzero-eof"}, mode) {
				for _, call := range readQueryTrace(t, trace) {
					if call.Method == "tools/call" {
						t.Fatal("query ran before endpoint acceptance")
					}
				}
			}
		})
	}
}

func TestVerifyPendingSanitizesInheritedCredentials(t *testing.T) {
	for _, name := range []string{"DATABASE_DSN", "PGPASSWORD", "AHE_RUNTIME_PROFILE", "AHE_DATABASE_ROLE", "OPENAI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(name, "synthetic-secret")
	}
	document, records := handoffDocument(t, "first line\n")
	handoff := queryHandoff()
	launcher, _ := queryLauncher(t, "environment", queryFixture(document, records[0], handoff))
	if err := VerifyPending(t.Context(), launcher, "lab-status", document, handoffExtractor(), records, handoff); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPendingRejectsLocalMismatchBeforeLaunch(t *testing.T) {
	for _, mode := range []string{"no-record", "multiple-records", "wrong-quote", "receipt-version", "receipt-state", "receipt-count", "receipt-occurrence", "receipt-attempt", "receipt-snapshot", "receipt-view"} {
		t.Run(mode, func(t *testing.T) {
			document, records := handoffDocument(t, "first line\n")
			handoff := queryHandoff()
			launcher, trace := queryLauncher(t, "valid", queryFixture(document, records[0], handoff))
			switch mode {
			case "no-record":
				records = nil
			case "multiple-records":
				records = append(records, records[0])
			case "wrong-quote":
				records[0].Citation.ExactQuote = "wrong"
			case "receipt-version":
				handoff.SchemaVersion = "v-next"
			case "receipt-state":
				handoff.Status = "admitted"
			case "receipt-count":
				handoff.ProposalCount = 2
			case "receipt-occurrence":
				handoff.ProposalOccurrenceID = ""
			case "receipt-attempt":
				handoff.ExtractionAttemptID = ""
			case "receipt-snapshot":
				handoff.SourceSnapshotID = ""
			case "receipt-view":
				handoff.ExtractionViewID = ""
			}
			if err := VerifyPending(t.Context(), launcher, "lab-status", document, handoffExtractor(), records, handoff); err == nil {
				t.Fatal("invalid local readback accepted")
			}
			if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("launcher ran: %v", err)
			}
		})
	}
}

func TestQueryRecordRejectsMissingOrMismatchedProvenance(t *testing.T) {
	document, records := handoffDocument(t, "first line\r\nsecond line\r\n")
	records[0].Citation.EndLine = 2
	records[0].Citation.ExactQuote, _ = document.ExactQuote(1, 2)
	handoff := queryHandoff()
	original := queryFixture(document, records[0], handoff)
	mutations := map[string]any{
		"record_ref.kind": "canonical", "record_ref.id": "occ:other", "proposal_local_id": "record-2", "proposal_kind": "code_fact",
		"proposal_fingerprint": "", "proposal_fingerprint_version": "another", "statement_text": "different", "admission_outcome": "admitted",
		"canonical_ref": "canon-node:other", "canonical": map[string]any{}, "proposal_origin_ref": map[string]any{}, "code_fact": map[string]any{},
		"code_relation": map[string]any{}, "source_generation": map[string]any{}, "repository_lifecycle": map[string]any{},
		"source.binding_kind": "repository_snapshot", "source.source_snapshot_id": "srcsnap:other", "source.source_system": "external_document",
		"source.source_id": "other", "source.source_version": "other", "source.raw_content_hash": "sha256:other",
		"source.repository_snapshot": map[string]any{}, "source.mcp_read": map[string]any{}, "source.external_source": map[string]any{},
		"extraction_view_id": "view:other", "renderer_name": "another", "renderer_version": "v2", "rendered_content_hash": "sha256:other",
		"extractor.extraction_attempt_id": "attempt:other", "extractor.extraction_run_id": "", "extractor.extractor_definition_id": "",
		"extractor.name": "other", "extractor.version": "other", "extractor.config_hash": "sha256:other", "extractor.attempt_status": "failed",
		"extractor.producer_session_ref": "unexpected", "source_refs": []any{},
	}
	for path, value := range mutations {
		t.Run(path, func(t *testing.T) {
			changed := mutateQueryFixture(t, original, path, value, false)
			if err := verifyPendingRecord(changed, "lab-status", document, handoffExtractor(), records[0], handoff); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
	for _, path := range []string{"record_ref", "statement_text", "canonical_ref", "source", "source_refs", "extractor", "renderer_name", "extraction_view_id"} {
		t.Run("missing_"+path, func(t *testing.T) {
			changed := mutateQueryFixture(t, original, path, nil, true)
			if err := verifyPendingRecord(changed, "lab-status", document, handoffExtractor(), records[0], handoff); err == nil {
				t.Fatal("missing field accepted")
			}
		})
	}
	for _, mode := range []string{"order", "duplicate", "start", "missing-start", "end", "span", "hash", "text", "view", "repository"} {
		t.Run("ref_"+mode, func(t *testing.T) {
			changed := original
			changed.SourceRefs = slices.Clone(original.SourceRefs)
			switch mode {
			case "order":
				slices.Reverse(changed.SourceRefs)
			case "duplicate":
				changed.SourceRefs[1] = changed.SourceRefs[0]
			case "start":
				value := 1
				changed.SourceRefs[0].StartByte = &value
			case "missing-start":
				changed.SourceRefs[0].StartByte = nil
			case "end":
				value := 1
				changed.SourceRefs[0].EndByte = &value
			case "span":
				changed.SourceRefs[0].SpanID = "span:S999"
			case "hash":
				changed.SourceRefs[0].QuotedTextHash = "sha256:wrong"
			case "text":
				changed.SourceRefs[0].QuotedText = "wrong"
			case "view":
				changed.SourceRefs[0].ExtractionViewID = "view:other"
			case "repository":
				changed.SourceRefs[0].RepositorySnapshotID = "repo:other"
			}
			if err := verifyPendingRecord(changed, "lab-status", document, handoffExtractor(), records[0], handoff); err == nil {
				t.Fatal("invalid source ref accepted")
			}
		})
	}
	// Exercise the public caller too: a well-formed response with changed content
	// must never become a successful readback receipt.
	changed := original
	changed.StatementText = "changed"
	launcher, _ := queryLauncher(t, "valid", changed)
	if err := VerifyPending(t.Context(), launcher, "lab-status", document, handoffExtractor(), records, handoff); err == nil {
		t.Fatal("public readback accepted changed statement")
	}
}

func queryHandoff() Handoff {
	return Handoff{SchemaVersion: "ahe-mcp-pending-handoff/v0", SourceSnapshotID: "srcsnap:test", ExtractionViewID: "view:test", ExtractionAttemptID: "attempt:test", ProposalOccurrenceID: "occ:test", ProposalCount: 1, Status: "pending"}
}

func queryFixture(document *labstatus.Document, record labstatus.Record, handoff Handoff) pendingRecord {
	var result pendingRecord
	result.RecordRef.Kind, result.RecordRef.ID = "proposal", handoff.ProposalOccurrenceID
	result.ProposalLocalID, result.ProposalKind, result.ProposalFingerprint, result.ProposalFingerprintVersion = "record-1", "statement", "fp:test", "statement-v1"
	result.StatementText, result.AdmissionOutcome, result.CanonicalRef = record.Statement, "pending", json.RawMessage("null")
	result.Source.BindingKind, result.Source.SourceSnapshotID, result.Source.SourceSystem, result.Source.SourceID = "source_snapshot", handoff.SourceSnapshotID, "manual_text", "lab-status"
	result.Source.SourceVersion, result.Source.RawContentHash = contentHash(document.RawText()), contentHash(document.RawText())
	result.ExtractionViewID, result.RendererName, result.RendererVersion, result.RenderedContentHash = handoff.ExtractionViewID, "manual-text-identity", "v1", contentHash(document.RawText())
	result.Extractor.ExtractorDefinitionID, result.Extractor.ExtractionRunID, result.Extractor.ExtractionAttemptID = "extractor:test", "run:test", handoff.ExtractionAttemptID
	result.Extractor.Name, result.Extractor.Version, result.Extractor.AttemptStatus = handoffExtractor().Name, handoffExtractor().Version, "succeeded"
	result.Extractor.ConfigHash = "sha256:" + digest(map[string]string{"model": handoffExtractor().Model, "source_mode": "frozen_local_snapshot"})
	input := handoffFakeInput(sourceRequest{SourceID: "lab-status", SourceVersion: contentHash(document.RawText()), RawText: document.RawText()})
	for _, current := range input.Spans {
		if current.DisplayLine < record.Citation.StartLine || current.DisplayLine > record.Citation.EndLine {
			continue
		}
		start, end := current.StartByte, current.EndByte
		result.SourceRefs = append(result.SourceRefs, pendingSourceRef{ExtractionViewID: handoff.ExtractionViewID, SpanID: current.SpanID, StartByte: &start, EndByte: &end, QuotedText: current.Text, QuotedTextHash: current.QuotedTextHash})
	}
	return result
}

func mutateQueryFixture(t *testing.T, original pendingRecord, path string, value any, remove bool) pendingRecord {
	t.Helper()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(path, ".")
	parent := object
	for _, key := range parts[:len(parts)-1] {
		parent = parent[key].(map[string]any)
	}
	if remove {
		delete(parent, parts[len(parts)-1])
	} else {
		parent[parts[len(parts)-1]] = value
	}
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var result pendingRecord
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func queryLauncher(t *testing.T, mode string, result pendingRecord) (string, string) {
	t.Helper()
	launcher, trace := transportLauncher(t, "query-readback-"+mode)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(trace), "query-record.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return launcher, trace
}

type queryTestRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	} `json:"params"`
}

func readQueryTrace(t *testing.T, path string) []queryTestRequest {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var result []queryTestRequest
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var request queryTestRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		result = append(result, request)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func serveQueryHelper(mode string) int {
	mode = strings.TrimPrefix(mode, "query-readback-")
	tracePath := os.Getenv("DETECTIVE_TRANSPORT_MARKER")
	trace, err := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 2
	}
	defer trace.Close()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(tracePath), "query-record.json"))
	if err != nil {
		return 3
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request queryTestRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 4
		}
		if _, err := fmt.Fprintln(trace, string(scanner.Bytes())); err != nil {
			return 5
		}
		if request.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = transportInitializeResult("query-server")
			if mode == "intake" {
				result = transportInitializeResult("valid")
			}
			if mode == "wrong-protocol" {
				result.(map[string]any)["protocolVersion"] = "1900-01-01"
			}
		case "tools/list":
			result = queryToolsFixture(mode)
		case "tools/call":
			if request.Params.Name != "get_evidence_record" || !reflect.DeepEqual(request.Params.Arguments, map[string]string{"proposal_occurrence_id": "occ:test"}) {
				return 6
			}
			if mode == "environment" {
				for _, value := range os.Environ() {
					if strings.Contains(value, "synthetic-secret") {
						return 7
					}
				}
			}
			if mode == "missing-record" {
				data = []byte(`{}`)
			}
			if mode == "tool-error" {
				data = []byte(`{"message":"synthetic-secret"}`)
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}, "structuredContent": json.RawMessage(data), "isError": mode == "tool-error"}
		default:
			return 8
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			return 9
		}
	}
	if mode == "nonzero-eof" {
		return 10
	}
	return 0
}

func queryToolsFixture(mode string) map[string]any {
	names := []string{"get_evidence_record", "list_evidence_records", "search_evidence_records", "get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance", "get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path", "get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal", "get_canonical_supersession_head", "get_canonical_supersession_currentness"}
	switch mode {
	case "reviewer":
		names = []string{"get_source_claim_review", "admit_reviewed_source_claim"}
	case "extra-tool":
		names = append(names, "submit_text_source")
	case "duplicate-tool":
		names[12] = names[0]
	case "missing-tool":
		names = names[:12]
	}
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]string{"type": "object"}})
	}
	result := map[string]any{"tools": tools}
	if mode == "pagination" {
		result["nextCursor"] = "more"
	}
	return result
}
