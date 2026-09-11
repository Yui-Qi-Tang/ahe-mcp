//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

// The public wire must return a domain conflict, not a leaked unique violation
// or a second pending run, using the unchanged restricted intake privileges.
func TestIntegrationRuntimeAuthoritySubprocessRequestIdentity(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	fixture := newAuthorityProcessFixture(t, ctx, databaseURL)
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", fixture.intake, fixture.schema, "intake")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", fixture.query, fixture.schema, "")
	const statement = "Mock refunds must be completed within 7 days."
	var sources []evidenceingestionmcp.SubmitTextSourceResponse
	for _, identity := range []string{"first", "second"} {
		source := authorityProcessTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", map[string]any{
			"request_id": "identity-source-" + identity, "source_id": "mock:" + identity,
			"source_version": "1", "raw_text": statement,
		})
		if source.Replayed || source.SourceSnapshotID == "" || source.ExtractionViewID == "" || len(source.Spans) != 1 {
			t.Fatal("source did not create one exact view and span")
		}
		sources = append(sources, source)
	}
	output := map[string]any{"proposals": []any{map[string]any{
		"proposal_local_id": "claim-1", "statement_text": statement, "evidence_refs": []string{sources[0].Spans[0].SpanID},
	}}}
	request := map[string]any{
		"request_id": "identity-output", "source_snapshot_id": sources[0].SourceSnapshotID,
		"extraction_view_id": sources[0].ExtractionViewID, "producer_session_ref": "session:first-created",
		"extractor_definition": map[string]any{"name": "identity-extractor", "version": "1"},
		"extractor_output":     output,
	}
	first := authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", request)
	if first.Status != "pending" || first.Replayed || first.ProposalCount != 1 {
		t.Fatal("first extraction did not create one pending proposal")
	}
	for _, change := range []string{"source", "extractor", "output"} {
		changed := maps.Clone(request)
		switch change {
		case "source":
			changed["source_snapshot_id"] = sources[1].SourceSnapshotID
			changed["extraction_view_id"] = sources[1].ExtractionViewID
			changed["extractor_output"] = map[string]any{"proposals": []any{map[string]any{
				"proposal_local_id": "claim-1", "statement_text": statement, "evidence_refs": []string{sources[1].Spans[0].SpanID},
			}}}
		case "extractor":
			changed["extractor_definition"] = map[string]any{"name": "identity-extractor", "version": "2"}
		case "output":
			changed["extractor_output"] = map[string]any{"proposals": []any{}}
		}
		response := intake.request(t, "tools/call", map[string]any{"name": "submit_extractor_output", "arguments": changed})
		var result struct {
			IsError bool `json:"isError"`
			Error   struct {
				Code string `json:"code"`
			} `json:"structuredContent"`
		}
		if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || !result.IsError || result.Error.Code != "idempotency_key_reused" {
			t.Fatalf("changed %s did not return the typed idempotency conflict", change)
		}
	}
	// Session is an audit annotation, not part of semantic run identity.
	retry := maps.Clone(request)
	retry["producer_session_ref"] = "session:later-retry"
	replayed := authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", retry)
	if !replayed.Replayed {
		t.Fatal("different-session exact retry was not replayed")
	}
	replayed.Replayed = false
	if first != replayed {
		t.Fatal("different-session retry changed the pending result")
	}
	record := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{
		"proposal_occurrence_id": first.ProposalOccurrenceID,
	})
	if record.AdmissionOutcome != "pending" || record.Extractor.AttemptStatus != "succeeded" ||
		record.Extractor.ProducerSessionRef != "session:first-created" || record.Source.SourceSnapshotID != sources[0].SourceSnapshotID ||
		len(record.SourceRefs) != 1 || record.SourceRefs[0].QuotedText != statement {
		t.Fatal("Query lost the original pending source or first-created session")
	}
	stdioAssertTableCount(t, ctx, fixture.pool, "source_snapshots", 2)
	for _, table := range []string{"extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences", "extractor_definitions"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 1)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_supersession_admission_events", "repository_generation_activation_requests"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
	query.finish(t)
	intake.finish(t)
}
