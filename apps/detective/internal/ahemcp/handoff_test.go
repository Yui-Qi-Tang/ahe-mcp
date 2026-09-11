package ahemcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

func TestMain(m *testing.M) {
	if os.Getenv("AHEMCP_FAKE_SERVER") == "1" {
		os.Exit(runFakeServer())
	}
	os.Exit(m.Run())
}

func TestSubmitCreatesGroundedPendingProposal(t *testing.T) {
	for name, raw := range map[string]string{
		"LF":               "first evidence line\nsecond line\n\n",
		"CRLF":             "first evidence line\r\nsecond line\r\n\r\n",
		"UTF8":             "臺灣來源證據🙂\n第二行\n\n",
		"no_final_newline": "first evidence line\nsecond line",
	} {
		t.Run(name, func(t *testing.T) {
			document, records := handoffDocument(t, raw)
			launcher, trace := handoffLauncher(t, "valid")
			handoff, err := Submit(t.Context(), launcher, "lab-status", document, handoffExtractor(), records)
			if err != nil {
				t.Fatalf("Submit() error = %v", err)
			}
			want := Handoff{SchemaVersion: "ahe-mcp-pending-handoff/v0", SourceSnapshotID: "srcsnap:test", ExtractionViewID: "view:test", ExtractionAttemptID: "attempt:test", ProposalOccurrenceID: "occ:test", ProposalCount: 1, Status: "pending"}
			if handoff != want {
				t.Fatalf("handoff = %+v", handoff)
			}
			calls := readHandoffTrace(t, trace)
			if got := handoffCallNames(calls); !reflect.DeepEqual(got, []string{"initialize", "notifications/initialized", "tools/list", "submit_text_source", "get_extractor_input", "submit_extractor_output"}) {
				t.Fatalf("calls = %v", got)
			}
			var source sourceRequest
			if err := json.Unmarshal(calls[3].Arguments, &source); err != nil {
				t.Fatal(err)
			}
			if source.RawText != raw || source.SourceVersion != contentHash(raw) || source.OriginMetadata["snapshot_sha256"] != document.Source().SHA256 || source.OriginMetadata["capture_kind"] != "frozen_local_snapshot" {
				t.Fatal("source request lost exact bytes or frozen local snapshot coordinates")
			}
			var output extractorRequest
			if err := json.Unmarshal(calls[5].Arguments, &output); err != nil {
				t.Fatal(err)
			}
			if len(output.ExtractorOutput.Proposals) != 1 || output.ExtractorOutput.Proposals[0].StatementText != records[0].Statement || !reflect.DeepEqual(output.ExtractorOutput.Proposals[0].EvidenceRefs, []string{"span:S1"}) {
				t.Fatal("proposal lost its validated statement or exact first-line span")
			}
		})
	}
}

// Each mutation changes one contract field of an otherwise valid response.
// Trace assertions prove a rejected source/input never reaches proposal intake.
var handoffMutations = map[string]struct {
	stage, path string
	value       any
}{
	"source_snapshot":        {"submit_text_source", "source_snapshot_id", ""},
	"source_view":            {"submit_text_source", "extraction_view_id", ""},
	"source_system":          {"submit_text_source", "source_system", "external_document"},
	"source_raw_hash":        {"submit_text_source", "raw_content_hash", "sha256:wrong"},
	"source_rendered_hash":   {"submit_text_source", "rendered_content_hash", "sha256:wrong"},
	"source_catalog":         {"submit_text_source", "span_catalog_version", "another-catalog"},
	"input_snapshot":         {"get_extractor_input", "source_snapshot_id", "srcsnap:wrong"},
	"input_view":             {"get_extractor_input", "extraction_view_id", "view:wrong"},
	"input_system":           {"get_extractor_input", "source_system", "external_document"},
	"input_source_id":        {"get_extractor_input", "source_id", "another-source"},
	"input_version":          {"get_extractor_input", "source_version", "another-version"},
	"input_raw_hash":         {"get_extractor_input", "raw_content_hash", "sha256:wrong"},
	"input_rendered_hash":    {"get_extractor_input", "rendered_content_hash", "sha256:wrong"},
	"input_text":             {"get_extractor_input", "rendered_text", "changed text"},
	"input_renderer":         {"get_extractor_input", "renderer.name", "another-renderer"},
	"input_renderer_version": {"get_extractor_input", "renderer.version", "v2"},
	"input_catalog":          {"get_extractor_input", "span_catalog_version", "another-catalog"},
	"span_empty_id":          {"get_extractor_input", "spans.0.span_id", ""},
	"span_duplicate_id":      {"get_extractor_input", "spans.1.span_id", "span:S1"},
	"span_duplicate_line":    {"get_extractor_input", "spans.1.display_line", 1},
	"span_missing":           {"get_extractor_input", "spans", []any{}},
	"span_text":              {"get_extractor_input", "spans.0.text", "changed quote"},
	"span_hash":              {"get_extractor_input", "spans.0.quoted_text_hash", "sha256:wrong"},
	"span_start":             {"get_extractor_input", "spans.0.start_byte", 1},
	"span_end":               {"get_extractor_input", "spans.0.end_byte", 20},
	"span_invalid_line":      {"get_extractor_input", "spans.0.display_line", 0},
	"span_extra_blank":       {"get_extractor_input", "append_span", span{SpanID: "span:blank", DisplayLine: 3, StartByte: 32, EndByte: 32, QuotedTextHash: contentHash("")}},
	"span_extra_line":        {"get_extractor_input", "append_span", span{SpanID: "span:extra", DisplayLine: 100, Text: "invented", StartByte: 33, EndByte: 41, QuotedTextHash: contentHash("invented")}},
	"receipt_snapshot":       {"submit_extractor_output", "source_snapshot_id", "srcsnap:wrong"},
	"receipt_view":           {"submit_extractor_output", "extraction_view_id", "view:wrong"},
	"receipt_count_zero":     {"submit_extractor_output", "proposal_count", 0},
	"receipt_count_extra":    {"submit_extractor_output", "proposal_count", 2},
	"receipt_attempt":        {"submit_extractor_output", "extraction_attempt_id", ""},
	"receipt_occurrence":     {"submit_extractor_output", "proposal_occurrence_id", ""},
	"receipt_status":         {"submit_extractor_output", "status", "admitted"},
	"nonzero_eof":            {"submit_extractor_output", "", nil},
}

func TestSubmitRejectsMismatchedSourceInputAndReceipt(t *testing.T) {
	names := make([]string, 0, len(handoffMutations))
	for name := range handoffMutations {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, mode := range names {
		t.Run(mode, func(t *testing.T) {
			document, records := handoffDocument(t, "first evidence line\nsecond line\n\n")
			launcher, trace := handoffLauncher(t, mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			handoff, err := Submit(ctx, launcher, "lab-status", document, handoffExtractor(), records)
			if err == nil || handoff != (Handoff{}) {
				t.Fatalf("Submit() = %+v, %v; want no success receipt", handoff, err)
			}
			calls := handoffCallNames(readHandoffTrace(t, trace))
			stage := handoffMutations[mode].stage
			if calls[len(calls)-1] != stage {
				t.Fatalf("last call = %s, want rejected stage %s", calls[len(calls)-1], stage)
			}
			if stage == "submit_extractor_output" && !strings.Contains(err.Error(), "uncertain") && !strings.Contains(err.Error(), "retry identical inputs") {
				t.Fatalf("post-write error lost reconciliation guidance: %v", err)
			}
		})
	}
}

func TestSubmitRejectsInvalidInputBeforeStartingLauncher(t *testing.T) {
	for _, name := range []string{"lone_CR", "too_many_spans", "escaped_response_size", "nil_document", "empty_records", "zero_citation", "reversed_citation", "outside_citation", "blank_line_citation", "wrong_quote", "invalid_status", "missing_statement", "statement_NUL", "statement_invalid_UTF8", "missing_list", "missing_extractor_name", "missing_extractor_version", "missing_extractor_model", "oversized_extractor_model", "invalid_extractor_UTF8", "extractor_NUL", "empty_source_id", "oversized_source_id"} {
		t.Run(name, func(t *testing.T) {
			raw := "first evidence line\nsecond line\n\n"
			if name == "lone_CR" {
				raw = "first evidence line\rsecond line\r"
			} else if name == "too_many_spans" {
				raw = strings.Repeat("x\n", 10_000)
			} else if name == "escaped_response_size" {
				raw = strings.Repeat("\x01", 100_000)
			}
			document, records := handoffDocument(t, raw)
			extractor, sourceID := handoffExtractor(), "lab-status"
			switch name {
			case "nil_document":
				document = nil
			case "empty_records":
				records = nil
			case "zero_citation":
				records[0].Citation.StartLine = 0
			case "reversed_citation":
				records[0].Citation.EndLine = 0
			case "outside_citation":
				records[0].Citation.EndLine = 100
			case "blank_line_citation":
				records[0].Citation.EndLine = 3
				records[0].Citation.ExactQuote, _ = document.ExactQuote(1, 3)
			case "wrong_quote":
				records[0].Citation.ExactQuote = "invented"
			case "invalid_status":
				records[0].Status = "invented"
			case "missing_statement":
				records[0].Statement = ""
			case "statement_NUL":
				records[0].Statement = "text\x00"
			case "statement_invalid_UTF8":
				records[0].Statement = string([]byte{0xff})
			case "missing_list":
				records[0].BlockedBy = nil
			case "missing_extractor_name":
				extractor.Name = ""
			case "missing_extractor_version":
				extractor.Version = ""
			case "missing_extractor_model":
				extractor.Model = ""
			case "oversized_extractor_model":
				extractor.Model = strings.Repeat("x", 4097)
			case "invalid_extractor_UTF8":
				extractor.Name = string([]byte{0xff})
			case "extractor_NUL":
				extractor.Version = "1\x00"
			case "empty_source_id":
				sourceID = " "
			case "oversized_source_id":
				sourceID = strings.Repeat("x", 501)
			}
			launcher, trace := handoffLauncher(t, "valid")
			if handoff, err := Submit(t.Context(), launcher, sourceID, document, extractor, records); err == nil || handoff != (Handoff{}) {
				t.Fatalf("Submit() = %+v, %v", handoff, err)
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatalf("invalid candidate started the launcher: %v", err)
			}
		})
	}
}

func TestHandoffResponseBudget(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want bool
	}{
		{"LF", "first\nsecond\n", true},
		{"CRLF", "first\r\nsecond\r\n", true},
		{"UTF8", "臺灣證據🙂\n", true},
		{"representative_112KiB_1400_lines", strings.Repeat(strings.Repeat("x", 79)+"\n", 1400), true},
		{"10000_short_lines", strings.Repeat("x\n", 10_000), false},
		{"escaped_control_bytes", strings.Repeat("\x01", 100_000), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := withinResponseBudget(tt.raw); got != tt.want {
				t.Fatalf("withinResponseBudget() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestProposalsForRejectsUnmappedCitation(t *testing.T) {
	_, err := proposalsFor(handoffExtractor(), []labstatus.Record{{Statement: "unsupported", Citation: labstatus.Citation{StartLine: 2, EndLine: 2}}}, []span{{SpanID: "span:S1", DisplayLine: 1}})
	if err == nil {
		t.Fatal("proposalsFor() error = nil, want unmapped citation error")
	}
}

func TestProposalsForRejectsInvalidCitationRanges(t *testing.T) {
	for _, citation := range []labstatus.Citation{{StartLine: 0, EndLine: 1}, {StartLine: 2, EndLine: 1}, {StartLine: 1, EndLine: 13}, {StartLine: 1, EndLine: int(^uint(0) >> 1)}} {
		if _, err := proposalsFor(handoffExtractor(), []labstatus.Record{{Statement: "fixture", Citation: citation}}, []span{{SpanID: "span:S1", DisplayLine: 1}}); err == nil {
			t.Fatalf("citation %+v unexpectedly accepted", citation)
		}
	}
}

func TestRequestIDUsesBoundedFramedV1Identity(t *testing.T) {
	for _, parts := range [][]string{nil, {"source", "lab", "digest"}, {strings.Repeat("x", 10000)}, {"來源", "🙂"}} {
		id := requestID(parts...)
		if !strings.HasPrefix(id, "detective-v1-") || len(id) != len("detective-v1-")+64 || id != requestID(parts...) {
			t.Fatalf("request ID is not deterministic bounded v1: %q", id)
		}
	}
	if requestID("a-b", "c") == requestID("a", "b-c") || requestID("ab", "c") == requestID("a", "bc") || requestID("source", "x") == requestID("proposal", "x") {
		t.Fatal("request IDs collide across field boundaries or operation kind")
	}
}

func TestSubmitRequestIdentityIncludesExtractorDefinition(t *testing.T) {
	document, records := handoffDocument(t, "first evidence line\nsecond line\n")
	var firstSource, firstProposal string
	for _, mode := range []string{"baseline", "exact_retry", "name", "version", "model"} {
		t.Run(mode, func(t *testing.T) {
			extractor := handoffExtractor()
			switch mode {
			case "name":
				extractor.Name = "another-extractor"
			case "version":
				extractor.Version = "0.1.1"
			case "model":
				extractor.Model = "another-model"
			}
			launcher, trace := handoffLauncher(t, "valid")
			if _, err := Submit(t.Context(), launcher, "lab-status", document, extractor, records); err != nil {
				t.Fatal(err)
			}
			calls := readHandoffTrace(t, trace)
			var source sourceRequest
			var output extractorRequest
			if json.Unmarshal(calls[3].Arguments, &source) != nil || json.Unmarshal(calls[5].Arguments, &output) != nil {
				t.Fatal("cannot decode request identity trace")
			}
			if mode == "baseline" {
				firstSource, firstProposal = source.RequestID, output.RequestID
				return
			}
			if source.RequestID != firstSource || (output.RequestID == firstProposal) != (mode == "exact_retry") {
				t.Fatal("extractor changes must preserve source identity but change proposal retry identity")
			}
			if output.ExtractorDefinition.Name != extractor.Name || output.ExtractorDefinition.Version != extractor.Version || output.ExtractorDefinition.Config["model"] != extractor.Model || output.ExtractorDefinition.Config["source_mode"] != "frozen_local_snapshot" {
				t.Fatal("producer identity was not included in the extractor request")
			}
		})
	}
}

func handoffDocument(t *testing.T, raw string) (*labstatus.Document, []labstatus.Record) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "STATUS.md")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := labstatus.LoadDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	quote, ok := document.ExactQuote(1, 1)
	if !ok {
		t.Fatal("fixture has no first source line")
	}
	return document, []labstatus.Record{{RecordType: "capability_state", Subject: "lab_status", Statement: "The fixture reports lab evidence.", EpistemicClass: "claim", Status: "lab_proven", Scope: "lab_contract", SelectionState: "unspecified", Citation: labstatus.Citation{StartLine: 1, EndLine: 1, ExactQuote: quote}, BlockedBy: []string{}, DoesNotEstablish: []string{}, Qualifiers: []string{}}}
}

func handoffExtractor() labstatus.ExtractorInfo {
	return labstatus.ExtractorInfo{Name: "lab-status-extractor", Version: "0.1.0", Model: "test-model"}
}

func handoffLauncher(t *testing.T, mode string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	launcher, trace := filepath.Join(directory, "launcher"), filepath.Join(directory, "trace.jsonl")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	body := "#!/bin/sh\nAHEMCP_FAKE_SERVER=1 AHEMCP_FAKE_MODE=" + quote(mode) + " AHEMCP_FAKE_TRACE=" + quote(trace) + " GORACE='atexit_sleep_ms=0 halt_on_error=1' exec " + quote(os.Args[0]) + "\n"
	if err := os.WriteFile(launcher, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return launcher, trace
}

type handoffTrace struct {
	Method    string          `json:"method"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func readHandoffTrace(t *testing.T, path string) []handoffTrace {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []handoffTrace
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var call handoffTrace
		if json.Unmarshal([]byte(line), &call) != nil {
			t.Fatal("invalid fake server trace")
		}
		calls = append(calls, call)
	}
	return calls
}

func handoffCallNames(calls []handoffTrace) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		name := call.Method
		if call.Tool != "" {
			name = call.Tool
		}
		names = append(names, name)
	}
	return names
}

func runFakeServer() int {
	mode := os.Getenv("AHEMCP_FAKE_MODE")
	trace, err := os.OpenFile(os.Getenv("AHEMCP_FAKE_TRACE"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	defer trace.Close()
	var source sourceRequest
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 3
		}
		if json.NewEncoder(trace).Encode(handoffTrace{Method: request.Method, Tool: request.Params.Name, Arguments: request.Params.Arguments}) != nil {
			return 4
		}
		if request.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]bool{"listChanged": false}}, "serverInfo": map[string]string{"name": "ahe-ingest-mcp", "version": "test"}}
		case "tools/list":
			var tools []map[string]any
			for _, name := range []string{"submit_manual_evidence", "submit_text_source", "submit_external_source", "submit_extractor_output", "get_extractor_input"} {
				tools = append(tools, map[string]any{"name": name, "description": "synthetic fixture", "inputSchema": map[string]string{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			var domain any
			switch request.Params.Name {
			case "submit_text_source":
				if json.Unmarshal(request.Params.Arguments, &source) != nil || source.RawText == "" || source.SourceVersion != contentHash(source.RawText) {
					return 5
				}
				domain = sourceResult{SourceSnapshotID: "srcsnap:test", ExtractionViewID: "view:test", SourceSystem: "manual_text", RawContentHash: source.SourceVersion, RenderedContentHash: source.SourceVersion, SpanCatalogVersion: "manual-line-v1"}
			case "get_extractor_input":
				domain = handoffFakeInput(source)
			case "submit_extractor_output":
				var output extractorRequest
				if json.Unmarshal(request.Params.Arguments, &output) != nil || len(output.ExtractorOutput.Proposals) != 1 {
					return 6
				}
				domain = Handoff{SourceSnapshotID: "srcsnap:test", ExtractionViewID: "view:test", ExtractionAttemptID: "attempt:test", ProposalOccurrenceID: "occ:test", ProposalCount: 1, Status: "pending"}
			default:
				return 7
			}
			result = textResult(mutateHandoffResponse(domain, mode, request.Params.Name))
		default:
			return 8
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			return 9
		}
	}
	if scanner.Err() != nil || mode == "nonzero_eof" {
		return 10
	}
	return 0
}

func handoffFakeInput(source sourceRequest) extractorInput {
	input := extractorInput{SourceSnapshotID: "srcsnap:test", ExtractionViewID: "view:test", SourceSystem: "manual_text", SourceID: source.SourceID, SourceVersion: source.SourceVersion, RawContentHash: source.SourceVersion, RenderedContentHash: source.SourceVersion, RenderedText: source.RawText, SpanCatalogVersion: "manual-line-v1"}
	input.Renderer.Name, input.Renderer.Version = "manual-text-identity", "v1"
	start := 0
	for i, line := range strings.Split(source.RawText, "\n") {
		text := strings.TrimSuffix(line, "\r")
		if text != "" {
			input.Spans = append(input.Spans, span{SpanID: fmt.Sprintf("span:S%d", len(input.Spans)+1), DisplayLine: i + 1, Text: text, StartByte: start, EndByte: start + len(text), QuotedTextHash: contentHash(text)})
		}
		start += len(line) + 1
	}
	return input
}

func mutateHandoffResponse(value any, mode, stage string) any {
	if strings.HasPrefix(mode, "batch_") {
		return batchHandoffResponse(value, mode, stage)
	}
	mutation, exists := handoffMutations[mode]
	if !exists || mutation.stage != stage || mutation.path == "" {
		return value
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		panic(err)
	}
	if mutation.path == "append_span" {
		fields["spans"] = append(fields["spans"].([]any), mutation.value)
		return fields
	}
	path := strings.Split(mutation.path, ".")
	var current any = fields
	for _, part := range path[:len(path)-1] {
		switch node := current.(type) {
		case map[string]any:
			current = node[part]
		case []any:
			if part == "0" {
				current = node[0]
			} else {
				current = node[1]
			}
		default:
			panic("invalid fixed fixture mutation path")
		}
	}
	current.(map[string]any)[path[len(path)-1]] = mutation.value
	return fields
}

func textResult(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(encoded)}}}
}
