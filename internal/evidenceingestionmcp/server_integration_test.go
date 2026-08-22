//go:build integration

package evidenceingestionmcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationSubmitManualEvidenceRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationRequest(t, "mcp-round-trip")

	resp := callSubmitManualEvidence(t, ctx, server, req)
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
	if resp.SourceSnapshotID == "" || resp.ExtractionAttemptID == "" || resp.ProposalOccurrenceID == "" || resp.ProposalFingerprint == "" {
		t.Fatalf("response IDs not populated: %+v", resp)
	}
	got, err := evidenceingestion.TraceProposalProvenance(ctx, pool, resp.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.ProposalOccurrenceID != resp.ProposalOccurrenceID {
		t.Fatalf("trace occurrence ID = %q, want %q", got.ProposalOccurrenceID, resp.ProposalOccurrenceID)
	}
	if got.AdmissionOutcome != "pending" || got.CanonicalRef != "" {
		t.Fatalf("trace admission/canonical = %q/%q, want pending/empty", got.AdmissionOutcome, got.CanonicalRef)
	}
	if got.SourceSnapshotID != resp.SourceSnapshotID || got.ExtractionAttemptID != resp.ExtractionAttemptID {
		t.Fatalf("trace provenance mismatch: %+v vs %+v", got, resp)
	}
	if len(got.SourceRefs) != 1 || got.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("source refs = %+v, want span:S1", got.SourceRefs)
	}
}

func TestIntegrationRunLocalOllamaExtractorRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	source := callSubmitTextSource(t, ctx, server, integrationTextSourceRequest(t, "mcp-ollama-source"))
	fixtureData := integrationFixtureData(t)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("ollama path = %q, want /api/generate", r.URL.Path)
		}
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Format any    `json:"format"`
			Stream bool   `json:"stream"`
			Think  bool   `json:"think"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode ollama request: %v", err)
		}
		if req.Model != "fixture-model" || req.Stream || req.Think {
			t.Fatalf("ollama request = %+v", req)
		}
		if !strings.Contains(req.Prompt, "span:S1") || strings.Contains(req.Prompt, "canonical_id") {
			t.Fatalf("unexpected prompt boundary: %s", req.Prompt)
		}
		resp, err := json.Marshal(map[string]string{"response": string(fixtureData)})
		if err != nil {
			t.Fatalf("marshal ollama response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(ollama.Close)

	resp := callRunLocalOllamaExtractor(t, ctx, server, RunLocalOllamaExtractorRequest{
		RequestID:        "mcp-ollama-run",
		ExtractionViewID: source.ExtractionViewID,
		Model:            "fixture-model",
		BaseURL:          ollama.URL,
		NumPredict:       256,
	})
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
	if resp.SourceSnapshotID != source.SourceSnapshotID || resp.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("source = %s/%s, want %s/%s", resp.SourceSnapshotID, resp.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if resp.ExtractionRunID == "" || resp.ExtractionAttemptID == "" || resp.ProposalOccurrenceID == "" || resp.ProposalFingerprint == "" {
		t.Fatalf("response IDs not populated: %+v", resp)
	}
	got, err := evidenceingestion.TraceProposalProvenance(ctx, pool, resp.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.ExtractorName != evidenceingestion.ExtractorOllamaLocal || got.ExtractorVersion != evidenceingestion.ExtractorOllamaLocalVersion {
		t.Fatalf("extractor provenance = %s/%s, want ollama-local/v1", got.ExtractorName, got.ExtractorVersion)
	}
	if got.ExtractorConfigHash == "" || len(got.SourceRefs) != 1 || got.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("trace provenance = %+v", got)
	}
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationRunGoParserExtractorRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	raw := strings.Join([]string{
		"package refund",
		"",
		"type RefundService struct{}",
		"",
		"func (s *RefundService) Create(orderID string) error {",
		"\treturn nil",
		"}",
		"",
	}, "\n")
	source := callSubmitTextSource(t, ctx, server, SubmitTextSourceRequest{
		RequestID:     "mcp-go-parser-source",
		SourceSystem:  evidenceingestion.SourceSystemCodeFile,
		SourceID:      "repo:ahe-wrap:internal/refund/service.go",
		SourceVersion: "abc123",
		RawText:       raw,
		OriginMetadata: map[string]string{
			"commit_sha":  "abc123",
			"path":        "internal/refund/service.go",
			"repo_id":     "ahe-wrap",
			"source_kind": "go_file",
		},
	})
	if source.SourceSystem != evidenceingestion.SourceSystemCodeFile || source.SpanCatalogVersion != evidenceingestion.SpanCatalogCodeLineV1 {
		t.Fatalf("code source contract = %s/%s", source.SourceSystem, source.SpanCatalogVersion)
	}

	resp := callRunGoParserExtractor(t, ctx, server, RunGoParserExtractorRequest{
		RequestID:        "mcp-go-parser-run",
		ExtractionViewID: source.ExtractionViewID,
		RepoID:           "ahe-wrap",
		CommitSHA:        "abc123",
		Path:             "internal/refund/service.go",
	})
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
	if resp.SourceSnapshotID != source.SourceSnapshotID || resp.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("source = %s/%s, want %s/%s", resp.SourceSnapshotID, resp.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if resp.ExtractionRunID == "" || resp.ExtractionAttemptID == "" || resp.ProposalOccurrenceID == "" || resp.ProposalFingerprint == "" {
		t.Fatalf("response IDs not populated: %+v", resp)
	}

	got, err := evidenceingestion.TraceProposalProvenance(ctx, pool, resp.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.ExtractorName != evidenceingestion.ExtractorGoParserCodeFact || got.ExtractorVersion != evidenceingestion.ExtractorGoParserCodeFactVersion {
		t.Fatalf("extractor provenance = %s/%s, want go-parser-code-fact/v1", got.ExtractorName, got.ExtractorVersion)
	}
	if got.StatementText != "Go type refund.RefundService is declared in internal/refund/service.go:3." {
		t.Fatalf("statement text = %q", got.StatementText)
	}
	if got.ProposalFingerprintVersion != evidenceingestion.ProposalFingerprintCodeFactV1 {
		t.Fatalf("fingerprint version = %q, want %q", got.ProposalFingerprintVersion, evidenceingestion.ProposalFingerprintCodeFactV1)
	}
	if got.CodeFact == nil || got.CodeFact.SymbolRef != "symbol:ahe-wrap:abc123:internal/refund/service.go:refund.RefundService" || got.CodeFact.QuotedText != "RefundService" {
		t.Fatalf("resolved code fact = %+v", got.CodeFact)
	}
	if len(got.SourceRefs) != 1 || got.SourceRefs[0].SpanID != "span:S2" {
		t.Fatalf("source refs = %+v, want span:S2", got.SourceRefs)
	}

	records, err := evidenceingestion.ListProposalRecords(ctx, pool, evidenceingestion.ProposalListInput{
		SourceSnapshotID: source.SourceSnapshotID,
		Limit:            10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2: %+v", len(records), records)
	}
	if records[0].AdmissionOutcome != "pending" || records[1].AdmissionOutcome != "pending" {
		t.Fatalf("admission outcomes = %q/%q, want pending/pending", records[0].AdmissionOutcome, records[1].AdmissionOutcome)
	}
	for _, record := range records {
		if record.CodeFact == nil {
			t.Fatalf("listed code proposal lost code fact: %+v", record)
		}
	}

	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 5)
	assertTableCount(t, ctx, pool, "extractor_definitions", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 2)
}

func TestIntegrationRunGoplsExtractorRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not on PATH")
	}

	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	workspaceRoot := t.TempDir()
	writeIntegrationFile(t, filepath.Join(workspaceRoot, "go.mod"), []byte("module example.com/gopls-mcp-smoke\n\ngo 1.22\n"))
	raw := strings.Join([]string{
		"package refund",
		"",
		"type RefundService struct{}",
		"",
		"func NewRefundService() *RefundService {",
		"\treturn &RefundService{}",
		"}",
		"",
	}, "\n")
	writeIntegrationFile(t, filepath.Join(workspaceRoot, "service.go"), []byte(raw))
	inventoryPayload, err := json.Marshal(InspectGoplsWorkspaceRequest{
		WorkspaceRoot: workspaceRoot,
		RepoID:        "ahe-wrap",
		CommitSHA:     "abc123",
	})
	if err != nil {
		t.Fatalf("json.Marshal(inventory request) error = %v", err)
	}
	inventoryData, err := server.CallTool(ctx, ToolInspectGoplsWorkspace, inventoryPayload)
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", ToolInspectGoplsWorkspace, err)
	}
	var inventory InspectGoplsWorkspaceResponse
	if err := json.Unmarshal(inventoryData, &inventory); err != nil {
		t.Fatalf("json.Unmarshal(inventory response) error = %v", err)
	}
	if len(inventory.Modules) != 1 || len(inventory.Packages) != 1 || len(inventory.Files) != 1 {
		t.Fatalf("gopls inventory = %+v", inventory)
	}
	if inventory.Files[0].Path != "service.go" || inventory.Files[0].ContentHash == "" {
		t.Fatalf("gopls inventory file = %+v", inventory.Files[0])
	}
	assertTableCount(t, ctx, pool, "source_snapshots", 0)
	assertTableCount(t, ctx, pool, "extraction_attempts", 0)

	source := callSubmitTextSource(t, ctx, server, SubmitTextSourceRequest{
		RequestID:     "mcp-gopls-source",
		SourceSystem:  evidenceingestion.SourceSystemCodeFile,
		SourceID:      "repo:ahe-wrap:service.go",
		SourceVersion: "abc123",
		RawText:       raw,
		OriginMetadata: map[string]string{
			"commit_sha":  "abc123",
			"path":        "service.go",
			"repo_id":     "ahe-wrap",
			"source_kind": "go_file",
		},
	})
	resp := callRunGoplsExtractor(t, ctx, server, RunGoplsExtractorRequest{
		RequestID:        "mcp-gopls-run",
		ExtractionViewID: source.ExtractionViewID,
		WorkspaceRoot:    workspaceRoot,
		RepoID:           "ahe-wrap",
		CommitSHA:        "abc123",
		Path:             "service.go",
	})
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
	if resp.SourceSnapshotID != source.SourceSnapshotID || resp.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("source = %s/%s, want %s/%s", resp.SourceSnapshotID, resp.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if resp.ExtractionRunID == "" || resp.ExtractionAttemptID == "" || resp.ProposalOccurrenceID == "" || resp.ProposalFingerprint == "" {
		t.Fatalf("response IDs not populated: %+v", resp)
	}

	got, err := evidenceingestion.TraceProposalProvenance(ctx, pool, resp.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.ExtractorName != evidenceingestion.ExtractorGoplsCodeFact || got.ExtractorVersion != evidenceingestion.ExtractorGoplsCodeFactVersion {
		t.Fatalf("extractor provenance = %s/%s, want gopls-code-fact/v1", got.ExtractorName, got.ExtractorVersion)
	}
	if got.StatementText != "Go type refund.RefundService is declared in service.go:3." {
		t.Fatalf("statement text = %q", got.StatementText)
	}
	if got.CodeFact == nil || got.CodeFact.SymbolRef != "symbol:ahe-wrap:abc123:service.go:refund.RefundService" || got.CodeFact.QuotedText != "RefundService" {
		t.Fatalf("resolved code fact = %+v", got.CodeFact)
	}

	records, err := evidenceingestion.ListProposalRecords(ctx, pool, evidenceingestion.ProposalListInput{
		SourceSnapshotID: source.SourceSnapshotID,
		Limit:            10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2: %+v", len(records), records)
	}
	for _, record := range records {
		if record.AdmissionOutcome != "pending" || record.CodeFact == nil {
			t.Fatalf("gopls proposal lost lifecycle or code fact: %+v", record)
		}
	}

	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extractor_definitions", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 2)
}

func TestIntegrationAdmitPendingProposalRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	submit := callSubmitManualEvidence(t, ctx, server, integrationRequest(t, "mcp-admission"))

	admission := callAdmitPendingProposal(t, ctx, server, AdmitPendingProposalRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "fixture statement accepted",
	})
	if admission.AdmissionOutcome != "admitted" {
		t.Fatalf("admission outcome = %q, want admitted", admission.AdmissionOutcome)
	}
	if admission.ProposalOccurrenceID != submit.ProposalOccurrenceID || admission.AdmissionDecisionID == "" || admission.CanonicalRef == "" {
		t.Fatalf("admission IDs not populated: %+v", admission)
	}
	if len(admission.RawEvidenceNodeIDs) != 1 || len(admission.CanonicalEdgeIDs) != 1 {
		t.Fatalf("admission graph IDs = raw %v edges %v, want one raw node and one edge", admission.RawEvidenceNodeIDs, admission.CanonicalEdgeIDs)
	}

	got, err := evidenceingestion.TraceProposalProvenance(ctx, pool, submit.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.AdmissionOutcome != "admitted" || got.CanonicalRef != admission.CanonicalRef {
		t.Fatalf("trace admission/canonical = %q/%q, want admitted/%s", got.AdmissionOutcome, got.CanonicalRef, admission.CanonicalRef)
	}

	replay := callAdmitPendingProposal(t, ctx, server, AdmitPendingProposalRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
	})
	if !replay.Replayed || replay.AdmissionDecisionID != admission.AdmissionDecisionID || replay.CanonicalRef != admission.CanonicalRef {
		t.Fatalf("replay admission = %+v, want replay of %+v", replay, admission)
	}

	assertTableCount(t, ctx, pool, "admission_decisions", 1)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 2)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 1)
}

func TestIntegrationRecordPendingProposalDispositionRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	submit := callSubmitManualEvidence(t, ctx, server, integrationRequest(t, "mcp-disposition"))

	disposition := callRecordPendingProposalDisposition(t, ctx, server, RecordPendingProposalDispositionRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
		Outcome:              evidenceingestion.ProposalDispositionRejected,
		DecisionBy:           "integration-test",
		DecisionReason:       "fixture is not eligible for canonical publication",
	})
	if disposition.ProposalOccurrenceID != submit.ProposalOccurrenceID ||
		disposition.AdmissionDecisionID == "" ||
		disposition.AdmissionOutcome != evidenceingestion.ProposalDispositionRejected ||
		disposition.DecisionBy != "integration-test" ||
		disposition.DecisionReason != "fixture is not eligible for canonical publication" ||
		disposition.Replayed {
		t.Fatalf("disposition result = %+v", disposition)
	}

	got, err := evidenceingestion.TraceProposalProvenance(ctx, pool, submit.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.AdmissionOutcome != evidenceingestion.ProposalDispositionRejected || got.CanonicalRef != "" {
		t.Fatalf("trace admission/canonical = %q/%q, want rejected/empty", got.AdmissionOutcome, got.CanonicalRef)
	}

	replay := callRecordPendingProposalDisposition(t, ctx, server, RecordPendingProposalDispositionRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
		Outcome:              evidenceingestion.ProposalDispositionRejected,
		DecisionBy:           "integration-test",
		DecisionReason:       "fixture is not eligible for canonical publication",
	})
	if !replay.Replayed ||
		replay.AdmissionDecisionID != disposition.AdmissionDecisionID ||
		replay.DecisionBy != disposition.DecisionBy ||
		replay.DecisionReason != disposition.DecisionReason {
		t.Fatalf("replay disposition = %+v, want replay of %+v", replay, disposition)
	}
	payload, err := json.Marshal(RecordPendingProposalDispositionRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
		Outcome:              evidenceingestion.ProposalDispositionRejected,
		DecisionBy:           "different-reviewer",
		DecisionReason:       "must not replace the first decision",
	})
	if err != nil {
		t.Fatalf("Marshal conflicting disposition: %v", err)
	}
	_, err = server.CallTool(ctx, ToolRecordPendingProposalDisposition, payload)
	assertToolError(t, err, string(evidenceingestion.ErrorAdmissionStateConflict))

	assertTableCount(t, ctx, pool, "admission_decisions", 1)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 0)
}

func TestIntegrationSubmitManualEvidenceReplayAndDistinctAttempt(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationRequest(t, "mcp-replay")

	first := callSubmitManualEvidence(t, ctx, server, req)
	second := callSubmitManualEvidence(t, ctx, server, req)
	if !second.Replayed {
		t.Fatalf("second Replayed = false, want true")
	}
	if second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("replay occurrence ID = %q, want %q", second.ProposalOccurrenceID, first.ProposalOccurrenceID)
	}
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)

	req.RequestID = "mcp-comparison-run"
	third := callSubmitManualEvidence(t, ctx, server, req)
	if third.Replayed {
		t.Fatalf("comparison Replayed = true, want false")
	}
	if third.ProposalOccurrenceID == first.ProposalOccurrenceID {
		t.Fatalf("comparison reused occurrence ID %q", third.ProposalOccurrenceID)
	}
	if third.ProposalFingerprint != first.ProposalFingerprint {
		t.Fatalf("comparison fingerprint = %q, want %q", third.ProposalFingerprint, first.ProposalFingerprint)
	}
	assertFingerprintCount(t, ctx, pool, first.ProposalFingerprint, 2)
}

func TestIntegrationSubmitManualEvidenceUnknownSpanPersistsFailure(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationRequest(t, "mcp-unknown-span")
	req.ExtractorOutput.Proposals[0].EvidenceRefs = []string{"span:S404"}

	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	_, err = server.CallTool(ctx, ToolSubmitManualEvidence, payload)
	assertToolError(t, err, string(evidenceingestion.ErrorUnknownSpan))
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
	assertFailureCount(t, ctx, pool, string(evidenceingestion.ErrorUnknownSpan), 1)
}

func TestIntegrationSubmitManualEvidenceInvalidUTF8(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationRequest(t, "mcp-invalid-utf8")
	req.RawText = string([]byte{0xff})

	_, err = server.SubmitManualEvidence(ctx, req)
	assertToolError(t, err, string(evidenceingestion.ErrorInvalidUTF8))
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationSubmitTextSourceRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationTextSourceRequest(t, "mcp-source-intake")

	resp := callSubmitTextSource(t, ctx, server, req)
	if resp.Replayed {
		t.Fatalf("first submit_text_source Replayed = true, want false")
	}
	if resp.SourceSnapshotID == "" || resp.ExtractionViewID == "" || resp.RawContentHash == "" || resp.RenderedContentHash == "" {
		t.Fatalf("response IDs not populated: %+v", resp)
	}
	if resp.SpanCatalogVersion != evidenceingestion.SpanCatalogManualLineV1 {
		t.Fatalf("span catalog version = %q, want %q", resp.SpanCatalogVersion, evidenceingestion.SpanCatalogManualLineV1)
	}
	if len(resp.Spans) != 2 || resp.Spans[0].SpanID != "span:S1" {
		t.Fatalf("response spans = %+v, want deterministic line spans", resp.Spans)
	}

	var raw []byte
	if err := pool.QueryRow(ctx, `
		SELECT raw_content
		FROM source_blobs
		WHERE raw_content_hash = $1
	`, resp.RawContentHash).Scan(&raw); err != nil {
		t.Fatalf("read source blob: %v", err)
	}
	if string(raw) != req.RawText {
		t.Fatalf("raw bytes = %q, want %q", string(raw), req.RawText)
	}

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "extractor_definitions", 0)
	assertTableCount(t, ctx, pool, "extraction_runs", 0)
	assertTableCount(t, ctx, pool, "extraction_attempts", 0)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationSubmitTextSourceReplayAndStrictErrors(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationTextSourceRequest(t, "mcp-source-replay")

	first := callSubmitTextSource(t, ctx, server, req)
	second := callSubmitTextSource(t, ctx, server, req)
	if !second.Replayed {
		t.Fatalf("second submit_text_source Replayed = false, want true")
	}
	if second.SourceSnapshotID != first.SourceSnapshotID || second.ExtractionViewID != first.ExtractionViewID {
		t.Fatalf("replay IDs = %+v, want %+v", second, first)
	}
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)

	_, err = server.CallTool(ctx, ToolSubmitTextSource, []byte(`{"source_id":"fixture-refund-policy","source_version":"v1","raw_text":"x","projection":"raw"}`))
	assertToolError(t, err, toolErrorInvalidRequest)

	badUTF8 := req
	badUTF8.RawText = string([]byte{0xff})
	_, err = server.SubmitTextSource(ctx, badUTF8)
	assertToolError(t, err, string(evidenceingestion.ErrorInvalidUTF8))
}

func TestIntegrationSubmitTextSourceRequestConflict(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationTextSourceRequest(t, "mcp-source-conflict")
	callSubmitTextSource(t, ctx, server, req)

	req.RawText = "Refunds must be completed within 8 days.\n"
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	_, err = server.CallTool(ctx, ToolSubmitTextSource, payload)
	assertToolError(t, err, string(evidenceingestion.ErrorIdempotencyKeyReused))

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationSubmitExternalSourceAuthorityReplayAndConflict(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := integrationExternalSourceRequest("mcp-external-source")

	first := callSubmitExternalSource(t, ctx, server, req)
	if first.Replayed {
		t.Fatal("first submit_external_source Replayed = true")
	}
	if first.AuthoritySourceSystem != evidenceingestion.SourceSystemExternalDocument ||
		first.SourceSystem != req.SourceSystem ||
		first.SourceNamespace != req.SourceNamespace ||
		first.ObjectID != req.ObjectID {
		t.Fatalf("external source response identity = %+v", first)
	}
	if first.ReceivedAt.IsZero() || first.ObservedAt.IsZero() {
		t.Fatalf("external source times not populated: %+v", first)
	}
	if first.SpanCatalogVersion != evidenceingestion.SpanCatalogExternalDocumentLineV1 || len(first.Spans) != 2 {
		t.Fatalf("external source spans = %+v", first)
	}

	var originJSON []byte
	var receivedAt time.Time
	var connectorID string
	var observedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT ss.origin_metadata, receipt.connector_id, receipt.observed_at, receipt.created_at
		FROM source_snapshots ss
		JOIN external_source_intake_receipts receipt ON receipt.source_snapshot_id = ss.source_snapshot_id
		WHERE ss.source_snapshot_id = $1 AND receipt.request_id = $2
	`, first.SourceSnapshotID, req.RequestID).Scan(&originJSON, &connectorID, &observedAt, &receivedAt); err != nil {
		t.Fatalf("read external source authority: %v", err)
	}
	var origin map[string]string
	if err := json.Unmarshal(originJSON, &origin); err != nil {
		t.Fatalf("decode external source origin: %v", err)
	}
	if origin["external_source_system"] != "jira" ||
		origin["external_content_fidelity"] != evidenceingestion.ExternalSourceContentFidelityVerbatim {
		t.Fatalf("external source origin = %#v", origin)
	}
	if connectorID != req.ConnectorID || !observedAt.Equal(first.ObservedAt) {
		t.Fatalf("external receipt connector/observed = %q/%v", connectorID, observedAt)
	}
	if !first.ReceivedAt.Equal(receivedAt.UTC()) {
		t.Fatalf("received_at = %v, persisted = %v", first.ReceivedAt, receivedAt)
	}

	second := callSubmitExternalSource(t, ctx, server, req)
	if !second.Replayed || second.SourceSnapshotID != first.SourceSnapshotID || !second.ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("external source replay = %+v, want same receipt as %+v", second, first)
	}

	conflict := req
	conflict.RequestID = "mcp-external-source-conflict"
	conflict.Content = "# AHE-42\nDifferent bytes under the same Jira revision.\n"
	payload, err := json.Marshal(conflict)
	if err != nil {
		t.Fatalf("Marshal conflict: %v", err)
	}
	_, err = server.CallTool(ctx, ToolSubmitExternalSource, payload)
	assertToolError(t, err, string(evidenceingestion.ErrorOccurrenceConflict))

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "external_source_intake_receipts", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationGetExtractorInputRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	sourceReq := integrationTextSourceRequest(t, "mcp-extractor-input")
	source := callSubmitTextSource(t, ctx, server, sourceReq)

	input := callGetExtractorInput(t, ctx, server, GetExtractorInputRequest{
		ExtractionViewID: source.ExtractionViewID,
	})
	if input.SourceSnapshotID != source.SourceSnapshotID || input.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("extractor input source = %s/%s, want %s/%s", input.SourceSnapshotID, input.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if input.RenderedText != sourceReq.RawText {
		t.Fatalf("rendered text = %q, want submitted raw text", input.RenderedText)
	}
	if input.Renderer.Name != evidenceingestion.RendererManualTextIdentity || input.Renderer.Version != evidenceingestion.RendererManualTextIdentityVersion {
		t.Fatalf("renderer = %+v, want manual identity", input.Renderer)
	}
	if len(input.Spans) != 2 || input.Spans[0].SpanID != "span:S1" || input.Spans[0].Text != "Refunds must be completed within 7 days." {
		t.Fatalf("spans = %+v, want grounded span:S1", input.Spans)
	}

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "extractor_definitions", 0)
	assertTableCount(t, ctx, pool, "extraction_runs", 0)
	assertTableCount(t, ctx, pool, "extraction_attempts", 0)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)

	output := callSubmitExtractorOutput(t, ctx, server, SubmitExtractorOutputRequest{
		RequestID:           "mcp-extractor-input-output",
		SourceSnapshotID:    input.SourceSnapshotID,
		ExtractionViewID:    input.ExtractionViewID,
		ExtractorDefinition: integrationAgentExtractorDefinition(),
		ExtractorOutput:     integrationRequest(t, "unused").ExtractorOutput,
	})
	if output.Status != "pending" {
		t.Fatalf("status = %q, want pending", output.Status)
	}
}

func TestIntegrationGetExtractorInputStrictErrors(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	_, err = server.CallTool(ctx, ToolGetExtractorInput, []byte(`{"extraction_view_id":"bad-id"}`))
	assertToolError(t, err, string(evidenceingestion.ErrorInvalidRecordID))

	_, err = server.CallTool(ctx, ToolGetExtractorInput, []byte(`{"extraction_view_id":"view:missing"}`))
	assertToolError(t, err, string(evidenceingestion.ErrorMissingSourceViewAttempt))

	_, err = server.CallTool(ctx, ToolGetExtractorInput, []byte(`{"extraction_view_id":"view:missing","raw_text":"x"}`))
	assertToolError(t, err, toolErrorInvalidRequest)
}

func TestIntegrationSubmitExtractorOutputRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	sourceReq := integrationTextSourceRequest(t, "mcp-source-for-output")
	source := callSubmitTextSource(t, ctx, server, sourceReq)
	outputReq := SubmitExtractorOutputRequest{
		RequestID:           "mcp-extractor-output-round-trip",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: integrationAgentExtractorDefinition(),
		ExtractorOutput:     integrationRequest(t, "unused").ExtractorOutput,
	}

	resp := callSubmitExtractorOutput(t, ctx, server, outputReq)
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
	if resp.SourceSnapshotID != source.SourceSnapshotID || resp.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("response source = %s/%s, want %s/%s", resp.SourceSnapshotID, resp.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if resp.ExtractionAttemptID == "" || resp.ProposalOccurrenceID == "" || resp.ProposalFingerprint == "" {
		t.Fatalf("response proposal IDs not populated: %+v", resp)
	}
	got, err := evidenceingestion.TraceProposalProvenance(ctx, pool, resp.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.SourceSnapshotID != source.SourceSnapshotID || got.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("trace source = %s/%s, want %s/%s", got.SourceSnapshotID, got.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if len(got.SourceRefs) != 1 || got.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("source refs = %+v, want span:S1", got.SourceRefs)
	}

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationSubmitExtractorOutputReplayConflictAndStrictErrors(t *testing.T) {
	ctx, pool := integrationPool(t)
	server, err := NewServer(pool)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	source := callSubmitTextSource(t, ctx, server, integrationTextSourceRequest(t, "mcp-source-for-output-replay"))
	outputReq := SubmitExtractorOutputRequest{
		RequestID:           "mcp-extractor-output-replay",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: integrationAgentExtractorDefinition(),
		ExtractorOutput:     integrationRequest(t, "unused").ExtractorOutput,
	}

	first := callSubmitExtractorOutput(t, ctx, server, outputReq)
	second := callSubmitExtractorOutput(t, ctx, server, outputReq)
	if !second.Replayed {
		t.Fatalf("second submit_extractor_output Replayed = false, want true")
	}
	if second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("replay occurrence = %q, want %q", second.ProposalOccurrenceID, first.ProposalOccurrenceID)
	}

	outputReq.ExtractorOutput.Proposals[0].StatementText = "Refunds must be completed within 8 days."
	payload, err := json.Marshal(outputReq)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	_, err = server.CallTool(ctx, ToolSubmitExtractorOutput, payload)
	assertToolError(t, err, string(evidenceingestion.ErrorIdempotencyKeyReused))

	_, err = server.CallTool(ctx, ToolSubmitExtractorOutput, []byte(`{"request_id":"r","source_snapshot_id":"srcsnap:missing","extraction_view_id":"view:missing","raw_text":"x","extractor_output":{"proposals":[]}}`))
	assertToolError(t, err, toolErrorInvalidRequest)

	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func callSubmitManualEvidence(t *testing.T, ctx context.Context, server *Server, req SubmitManualEvidenceRequest) SubmitManualEvidenceResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolSubmitManualEvidence, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp SubmitManualEvidenceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callSubmitTextSource(t *testing.T, ctx context.Context, server *Server, req SubmitTextSourceRequest) SubmitTextSourceResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolSubmitTextSource, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp SubmitTextSourceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callSubmitExternalSource(t *testing.T, ctx context.Context, server *Server, req SubmitExternalSourceRequest) SubmitExternalSourceResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolSubmitExternalSource, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp SubmitExternalSourceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callSubmitExtractorOutput(t *testing.T, ctx context.Context, server *Server, req SubmitExtractorOutputRequest) SubmitExtractorOutputResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolSubmitExtractorOutput, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp SubmitExtractorOutputResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callGetExtractorInput(t *testing.T, ctx context.Context, server *Server, req GetExtractorInputRequest) GetExtractorInputResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolGetExtractorInput, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp GetExtractorInputResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callRunLocalOllamaExtractor(t *testing.T, ctx context.Context, server *Server, req RunLocalOllamaExtractorRequest) RunLocalOllamaExtractorResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolRunLocalOllamaExtractor, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RunLocalOllamaExtractorResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callRunGoParserExtractor(t *testing.T, ctx context.Context, server *Server, req RunGoParserExtractorRequest) RunGoParserExtractorResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolRunGoParserExtractor, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RunGoParserExtractorResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callRunGoplsExtractor(t *testing.T, ctx context.Context, server *Server, req RunGoplsExtractorRequest) RunGoplsExtractorResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolRunGoplsExtractor, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RunGoplsExtractorResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func writeIntegrationFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}

func callAdmitPendingProposal(t *testing.T, ctx context.Context, server *Server, req AdmitPendingProposalRequest) AdmitPendingProposalResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolAdmitPendingProposal, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp AdmitPendingProposalResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func callRecordPendingProposalDisposition(
	t *testing.T,
	ctx context.Context,
	server *Server,
	req RecordPendingProposalDispositionRequest,
) RecordPendingProposalDispositionResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolRecordPendingProposalDisposition, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RecordPendingProposalDispositionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func integrationRequest(t *testing.T, requestID string) SubmitManualEvidenceRequest {
	t.Helper()
	raw, fixtureData := integrationRawAndFixtureData(t)
	var fixture evidenceingestion.FrozenExtractorOutput
	if err := json.Unmarshal(fixtureData, &fixture); err != nil {
		t.Fatalf("parse extractor fixture: %v", err)
	}
	return SubmitManualEvidenceRequest{
		SourceID:        "fixture-refund-policy",
		SourceVersion:   "v1",
		RawText:         string(raw),
		OriginMetadata:  map[string]string{"fixture": "manual_refund_policy"},
		RequestID:       requestID,
		ExtractorOutput: fixture,
	}
}

func integrationFixtureData(t *testing.T) []byte {
	t.Helper()
	_, fixtureData := integrationRawAndFixtureData(t)
	return fixtureData
}

func integrationRawAndFixtureData(t *testing.T) ([]byte, []byte) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	raw, err := os.ReadFile(filepath.Join(dir, "..", "evidenceingestion", "testdata", "manual_refund_policy.txt"))
	if err != nil {
		t.Fatalf("read manual fixture: %v", err)
	}
	fixtureData, err := os.ReadFile(filepath.Join(dir, "..", "evidenceingestion", "testdata", "frozen_fixture.json"))
	if err != nil {
		t.Fatalf("read extractor fixture: %v", err)
	}
	return raw, fixtureData
}

func integrationTextSourceRequest(t *testing.T, requestID string) SubmitTextSourceRequest {
	t.Helper()
	req := integrationRequest(t, requestID)
	return SubmitTextSourceRequest{
		SourceID:       req.SourceID,
		SourceVersion:  req.SourceVersion,
		RawText:        req.RawText,
		OriginMetadata: req.OriginMetadata,
		RequestID:      req.RequestID,
	}
}

func integrationExternalSourceRequest(requestID string) SubmitExternalSourceRequest {
	return SubmitExternalSourceRequest{
		SchemaVersion:   evidenceingestion.ExternalSourceEnvelopeSchemaV1,
		RequestID:       requestID,
		SourceSystem:    "jira",
		SourceNamespace: "acme/eng",
		ObjectType:      "issue",
		ObjectID:        "AHE-42",
		Revision:        "2026-08-23T02:00:00Z",
		SourceLocation:  "https://acme.example/jira/AHE-42",
		Title:           "External intake boundary",
		ContentFormat:   evidenceingestion.ExternalSourceContentFormatMarkdown,
		ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
		Content:         "# AHE-42\nExternal connector owns collection.\n",
		Coverage:        evidenceingestion.ExternalSourceCoverageFullDocument,
		CollectorID:     "claude-code",
		ConnectorID:     "atlassian-rovo",
		ObservedAt:      "2026-08-23T02:03:04Z",
		SourceCreatedAt: "2026-08-22T01:00:00Z",
		SourceUpdatedAt: "2026-08-23T02:00:00Z",
	}
}

func integrationAgentExtractorDefinition() evidenceingestion.ExtractorDefinitionInput {
	return evidenceingestion.ExtractorDefinitionInput{
		Name:    "external-cooperating-agent",
		Version: "v1",
		Config: map[string]string{
			"producer_class": "external_agent",
		},
	}
}

func integrationPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	schema := "ahe_ingestion_mcp_test_" + randomHex(t, 8)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, readMigrations(t)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return ctx, pool
}

func readMigrations(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(paths)
	var combined strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		combined.Write(data)
		combined.WriteString("\n")
	}
	return combined.String()
}

func assertTableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertFingerprintCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fingerprint string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM proposal_occurrences
		WHERE proposal_fingerprint = $1
	`, fingerprint).Scan(&got); err != nil {
		t.Fatalf("count fingerprint occurrences: %v", err)
	}
	if got != want {
		t.Fatalf("fingerprint count = %d, want %d", got, want)
	}
}

func assertFailureCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, failureClass string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM extraction_attempts
		WHERE status = 'failed' AND failure_class = $1
	`, failureClass).Scan(&got); err != nil {
		t.Fatalf("count failed attempts: %v", err)
	}
	if got != want {
		t.Fatalf("failure count = %d, want %d", got, want)
	}
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}
