//go:build integration

package detective

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationMCPReadSourceCycleResumesAndPreservesEvidenceBoundary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	ctx, pool := detectiveIntegrationPool(t)
	workspace := integrationMCPReadWorkspace(t, ctx, pool, "runtime", "fixture:AHE-42")
	config := integrationMCPReadSourceConfig(t, "complete", "AHE-42")

	binding, replayed, err := RegisterMCPReadSourceBinding(ctx, pool, MCPReadSourceBindingInput{
		WorkspaceID:     workspace.ID,
		SourceBindingID: workspace.Sources[0].ID,
		Config:          config,
	})
	if err != nil {
		t.Fatalf("RegisterMCPReadSourceBinding() error = %v", err)
	}
	if replayed || binding.SourceID != "fixture:AHE-42" ||
		binding.BindingHash == "" || binding.CommandHash == "" {
		t.Fatalf("MCP read binding = %+v, replayed=%t", binding, replayed)
	}
	prepared, err := prepareMCPReadSource(workspace.ID, workspace.Sources[0].ID, config)
	if err != nil {
		t.Fatalf("prepareMCPReadSource() error = %v", err)
	}
	_, reserved, resumed, err := reserveMCPReadCollectionCycle(
		ctx,
		pool,
		prepared,
		workspace.Sources[0].SourceID,
	)
	if err != nil {
		t.Fatalf("reserveMCPReadCollectionCycle() error = %v", err)
	}
	if resumed || reserved.Status != MCPReadCycleStatusRunning {
		t.Fatalf("reserved MCP read cycle = %+v, resumed=%t", reserved, resumed)
	}

	first, err := RunMCPReadSourceTick(ctx, pool, MCPReadSourceTickInput{
		WorkspaceID:     workspace.ID,
		SourceBindingID: workspace.Sources[0].ID,
		Config:          config,
	})
	if err != nil {
		t.Fatalf("RunMCPReadSourceTick() error = %v", err)
	}
	if !first.Resumed || first.Deferred ||
		first.Cycle.ID != reserved.ID ||
		first.Cycle.Status != MCPReadCycleStatusCompleted ||
		first.Cycle.ProviderRevision != "revision-42" ||
		first.Cycle.SourceSnapshotID == "" ||
		first.Collection == nil || first.Processing == nil {
		t.Fatalf("first MCP read tick = %+v", first)
	}
	second, err := RunMCPReadSourceTick(ctx, pool, MCPReadSourceTickInput{
		WorkspaceID:     workspace.ID,
		SourceBindingID: workspace.Sources[0].ID,
		Config:          config,
	})
	if err != nil {
		t.Fatalf("second RunMCPReadSourceTick() error = %v", err)
	}
	if second.Resumed || second.Deferred ||
		second.Cycle.Number != 2 ||
		second.Cycle.ID == first.Cycle.ID ||
		second.Cycle.SourceSnapshotID != first.Cycle.SourceSnapshotID {
		t.Fatalf("second MCP read tick = %+v", second)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_mcp_read_source_bindings", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_mcp_read_collection_cycles", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_work", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
	assertDetectiveTableCount(t, ctx, pool, "canonical_graph_nodes", 0)

	replayedBinding, replayed, err := RegisterMCPReadSourceBinding(ctx, pool, MCPReadSourceBindingInput{
		WorkspaceID:     workspace.ID,
		SourceBindingID: workspace.Sources[0].ID,
		Config:          config,
	})
	if err != nil {
		t.Fatalf("replay RegisterMCPReadSourceBinding() error = %v", err)
	}
	if !replayed || replayedBinding.BindingHash != binding.BindingHash {
		t.Fatalf("replayed MCP binding = %+v, replayed=%t", replayedBinding, replayed)
	}
	changed := config
	changed.AdapterVersion = "v2"
	_, _, err = RegisterMCPReadSourceBinding(ctx, pool, MCPReadSourceBindingInput{
		WorkspaceID:     workspace.ID,
		SourceBindingID: workspace.Sources[0].ID,
		Config:          changed,
	})
	assertDetectiveKind(t, err, ErrorWorkspaceConflict)
}

func TestIntegrationMCPReadSourceCycleUsesDatabaseExecutionLock(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	ctx, pool := detectiveIntegrationPool(t)
	workspace := integrationMCPReadWorkspace(t, ctx, pool, "parallel", "fixture:AHE-48")
	config := integrationMCPReadSourceConfig(t, "slow", "AHE-48")
	if _, _, err := RegisterMCPReadSourceBinding(ctx, pool, MCPReadSourceBindingInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, Config: config,
	}); err != nil {
		t.Fatalf("RegisterMCPReadSourceBinding() error = %v", err)
	}

	start := make(chan struct{})
	results := make(chan MCPReadSourceTickResult, 2)
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			result, err := RunMCPReadSourceTick(ctx, pool, MCPReadSourceTickInput{
				WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, Config: config,
			})
			results <- result
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	deferred := 0
	completed := 0
	for range 2 {
		result := <-results
		if err := <-errs; err != nil {
			t.Fatalf("concurrent RunMCPReadSourceTick() error = %v", err)
		}
		if result.Deferred {
			deferred++
		}
		if result.Cycle.Status == MCPReadCycleStatusCompleted {
			completed++
		}
	}
	if deferred != 1 || completed != 1 {
		t.Fatalf("concurrent MCP read outcomes deferred/completed = %d/%d, want 1/1", deferred, completed)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_mcp_read_collection_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
}

func TestIntegrationMCPReadSourceCycleRecordsTerminalProviderFailure(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	ctx, pool := detectiveIntegrationPool(t)
	workspace := integrationMCPReadWorkspace(t, ctx, pool, "failure", "fixture:AHE-49")
	config := integrationMCPReadSourceConfig(t, "tool-error", "AHE-49")
	if _, _, err := RegisterMCPReadSourceBinding(ctx, pool, MCPReadSourceBindingInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, Config: config,
	}); err != nil {
		t.Fatalf("RegisterMCPReadSourceBinding() error = %v", err)
	}
	result, err := RunMCPReadSourceTick(ctx, pool, MCPReadSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, Config: config,
	})
	assertDetectiveKind(t, err, ErrorMCPTransportFailed)
	if result.Cycle.Status != MCPReadCycleStatusFailed ||
		result.Cycle.FailureKind != string(ErrorMCPTransportFailed) ||
		result.Cycle.SourceSnapshotID != "" {
		t.Fatalf("failed MCP read cycle = %+v", result.Cycle)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_mcp_read_collection_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 0)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationMCPReadProposalConversionRoundTripReplay(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	collected, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "complete", "collect-mcp-convert", "AHE-52"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, collected.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}

	first, err := ConvertMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalConversionInput{
		SourceSnapshotID: processed.Source.SourceSnapshotID,
	})
	if err != nil {
		t.Fatalf("ConvertMCPReadDocumentSnapshot() error = %v", err)
	}
	if first.Status != MCPReadProposalConversionStatusConverted ||
		first.ProposalCount != 1 ||
		first.ProposalOccurrenceID == "" ||
		first.ExtractionRunID == "" ||
		first.ExtractionAttemptID == "" ||
		first.Replayed ||
		first.GlobalAbsenceInference {
		t.Fatalf("first MCP proposal conversion = %+v", first)
	}
	second, err := ConvertMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalConversionInput{
		SourceSnapshotID: processed.Source.SourceSnapshotID,
	})
	if err != nil {
		t.Fatalf("replay ConvertMCPReadDocumentSnapshot() error = %v", err)
	}
	if !second.Replayed ||
		second.ProposalOccurrenceID != first.ProposalOccurrenceID ||
		second.ConversionRequestID != first.ConversionRequestID {
		t.Fatalf("replayed MCP proposal conversion = %+v, want %+v", second, first)
	}

	records, err := evidenceingestion.ListProposalRecords(ctx, pool, evidenceingestion.ProposalListInput{
		SourceSnapshotID: processed.Source.SourceSnapshotID,
		Limit:            10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords() error = %v", err)
	}
	if len(records) != 1 ||
		records[0].ProposalLocalID != "document-line-1" ||
		records[0].StatementText != "Refunds must be completed within 7 days." ||
		records[0].AdmissionOutcome != "pending" ||
		records[0].SourceSystem != evidenceingestion.SourceSystemMCPReadDocument ||
		records[0].OriginMetadata["mcp_provider"] != "fixture" ||
		records[0].OriginMetadata["mcp_object_id"] != "AHE-52" ||
		records[0].OriginMetadata["global_absence_inference_allowed"] != "false" ||
		len(records[0].SourceRefs) != 1 ||
		records[0].SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("converted MCP proposal records = %+v", records)
	}
	brief, err := evidenceingestion.GetGroundedEvidenceBrief(ctx, pool, evidenceingestion.GroundedEvidenceBriefInput{
		Query:                "refunds completed",
		IncludeSourceContext: true,
		ProposalListInput: evidenceingestion.ProposalListInput{
			SourceSnapshotID: processed.Source.SourceSnapshotID,
			Limit:            10,
		},
	})
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief() error = %v", err)
	}
	if len(brief.Matches) != 1 ||
		len(brief.SourceContexts) != 1 ||
		brief.SourceContexts[0].Status != evidenceingestion.GroundedEvidenceSourceContextStatusNotApplicable ||
		brief.SourceContexts[0].AtomicContainer != nil ||
		len(brief.SourceContexts[0].SearchCore) != 1 ||
		brief.SourceContexts[0].SearchCore[0].SpanID != "span:S1" {
		t.Fatalf("converted MCP grounded brief = %+v", brief)
	}
	assertDetectiveTableCount(t, ctx, pool, "extraction_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_batches", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
	assertDetectiveTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertDetectiveTableCount(t, ctx, pool, "canonical_graph_edges", 0)
}

func TestIntegrationMCPReadProposalExtractionRoundTripAndReplay(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	collected, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "complete", "collect-mcp-extract", "AHE-56"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, collected.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	definition := evidenceingestion.ExtractorDefinitionInput{
		Name:    "test-bounded-model",
		Version: "v1",
		Config:  map[string]string{"model": "fixture"},
	}
	calls := 0
	first, err := ExtractMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalExtractionInput{
		SourceSnapshotID:    processed.Source.SourceSnapshotID,
		MaxProposals:        4,
		ExtractorDefinition: definition,
		Runner: func(_ context.Context, input evidenceingestion.ExtractorInput) ([]byte, error) {
			calls++
			if input.RenderedText != "" ||
				len(input.Spans) != 1 ||
				input.Spans[0].Text != "Refunds must be completed within 7 days." {
				t.Fatalf("bounded extractor input = %+v", input)
			}
			return []byte(`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"]}]}`), nil
		},
	})
	if err != nil {
		t.Fatalf("ExtractMCPReadDocumentSnapshot() error = %v", err)
	}
	if first.Status != MCPReadProposalExtractionStatusExtracted ||
		first.ProposalCount != 1 ||
		!first.ModelInvoked ||
		first.Replayed ||
		first.ProposalOccurrenceID == "" {
		t.Fatalf("first MCP proposal extraction = %+v", first)
	}
	second, err := ExtractMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalExtractionInput{
		SourceSnapshotID:    processed.Source.SourceSnapshotID,
		MaxProposals:        4,
		ExtractorDefinition: definition,
		Runner: func(context.Context, evidenceingestion.ExtractorInput) ([]byte, error) {
			calls++
			return nil, errors.New("runner should not be called on replay")
		},
	})
	if err != nil {
		t.Fatalf("replay ExtractMCPReadDocumentSnapshot() error = %v", err)
	}
	if calls != 1 ||
		second.Status != MCPReadProposalExtractionStatusExtracted ||
		second.ModelInvoked ||
		!second.Replayed ||
		second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("replayed MCP proposal extraction = %+v, calls=%d", second, calls)
	}
	assertDetectiveTableCount(t, ctx, pool, "extraction_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_batches", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
	assertDetectiveTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
}

func TestIntegrationMCPReadSectionProposalExtractionPersistsCoverageAndReplays(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	collected, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "sectioned", "collect-mcp-section-extract", "AHE-60"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, collected.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	definition := evidenceingestion.ExtractorDefinitionInput{
		Name:    "test-section-bounded-model",
		Version: "v1",
	}
	calls := 0
	input := MCPReadProposalExtractionInput{
		SourceSnapshotID:    processed.Source.SourceSnapshotID,
		MaxProposals:        1,
		SectionMode:         MCPReadProposalSectionModeHeadingV1,
		MaxSections:         3,
		ExtractorDefinition: definition,
		Runner: func(_ context.Context, input evidenceingestion.ExtractorInput) ([]byte, error) {
			calls++
			if input.RenderedText != "" || len(input.Spans) != 1 {
				t.Fatalf("section extractor input = %+v", input)
			}
			statement := ""
			switch {
			case strings.Contains(input.Spans[0].Text, "Purpose fact."):
				statement = "Purpose fact."
			case strings.Contains(input.Spans[0].Text, "It is not autonomous."):
				statement = "It is not autonomous."
			case strings.Contains(input.Spans[0].Text, "Rollback restores artifacts."):
				statement = "Rollback restores artifacts."
			default:
				t.Fatalf("unexpected section input %q", input.Spans[0].Text)
			}
			return json.Marshal(evidenceingestion.FrozenExtractorOutput{
				Proposals: []evidenceingestion.ExtractorProposalOutput{{
					ProposalLocalID: "model-local-id",
					StatementText:   statement,
					EvidenceRefs:    []string{input.Spans[0].SpanID},
				}},
			})
		},
	}
	first, err := ExtractMCPReadDocumentSnapshot(ctx, pool, input)
	if err != nil {
		t.Fatalf("ExtractMCPReadDocumentSnapshot() error = %v", err)
	}
	if first.Contract != MCPReadProposalSectionExtractionContract ||
		first.Status != MCPReadProposalExtractionStatusExtracted ||
		first.ProposalCount != 3 ||
		first.SectionCount != 3 ||
		first.ModelCallCount != 3 ||
		!first.ModelInvoked ||
		!first.SectionCoverageComplete ||
		first.Replayed {
		t.Fatalf("first section extraction = %+v", first)
	}

	var outputData []byte
	if err := pool.QueryRow(ctx, `
		SELECT fixture_output
		FROM extraction_attempts
		WHERE extraction_attempt_id = $1
	`, first.ExtractionAttemptID).Scan(&outputData); err != nil {
		t.Fatalf("read section extraction output: %v", err)
	}
	var output evidenceingestion.FrozenExtractorOutput
	if err := json.Unmarshal(outputData, &output); err != nil {
		t.Fatalf("decode section extraction output: %v", err)
	}
	coverage := output.DocumentSectionCoverage
	if coverage == nil ||
		!coverage.CoverageComplete ||
		coverage.NegativeInferenceAllowed ||
		coverage.SectionCount != 3 ||
		coverage.ModelCallCount != 3 ||
		len(coverage.Sections) != 3 {
		t.Fatalf("persisted section coverage = %+v", coverage)
	}
	for index, section := range coverage.Sections {
		wantID := fmt.Sprintf("section-%03d-proposal-01", index+1)
		if section.Abstained ||
			len(section.ProposalLocalIDs) != 1 ||
			section.ProposalLocalIDs[0] != wantID ||
			output.Proposals[index].ProposalLocalID != wantID {
			t.Fatalf("persisted section %d = %+v proposal=%+v", index, section, output.Proposals[index])
		}
	}

	input.Runner = func(context.Context, evidenceingestion.ExtractorInput) ([]byte, error) {
		calls++
		return nil, errors.New("runner should not be called on section replay")
	}
	second, err := ExtractMCPReadDocumentSnapshot(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay ExtractMCPReadDocumentSnapshot() error = %v", err)
	}
	if calls != 3 ||
		!second.Replayed ||
		second.ModelInvoked ||
		second.ModelCallCount != 0 ||
		second.SectionCount != 3 ||
		!second.SectionCoverageComplete {
		t.Fatalf("replayed section extraction = %+v, calls=%d", second, calls)
	}
}

func TestIntegrationMCPReadProposalExtractionPersistsAbstention(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	collected, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "complete", "collect-mcp-abstain", "AHE-57"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, collected.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	definition := evidenceingestion.ExtractorDefinitionInput{Name: "test-abstaining-model", Version: "v1"}
	calls := 0
	input := MCPReadProposalExtractionInput{
		SourceSnapshotID:    processed.Source.SourceSnapshotID,
		MaxProposals:        4,
		ExtractorDefinition: definition,
		Runner: func(context.Context, evidenceingestion.ExtractorInput) ([]byte, error) {
			calls++
			return []byte(`{"proposals":[]}`), nil
		},
	}
	first, err := ExtractMCPReadDocumentSnapshot(ctx, pool, input)
	if err != nil {
		t.Fatalf("ExtractMCPReadDocumentSnapshot() error = %v", err)
	}
	input.Runner = func(context.Context, evidenceingestion.ExtractorInput) ([]byte, error) {
		calls++
		return nil, errors.New("runner should not be called on abstention replay")
	}
	second, err := ExtractMCPReadDocumentSnapshot(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay ExtractMCPReadDocumentSnapshot() error = %v", err)
	}
	if calls != 1 ||
		first.Status != MCPReadProposalExtractionStatusAbstained ||
		first.ProposalCount != 0 ||
		!first.ModelInvoked ||
		second.Status != MCPReadProposalExtractionStatusAbstained ||
		!second.Replayed ||
		second.ModelInvoked {
		t.Fatalf("abstention results first=%+v second=%+v calls=%d", first, second, calls)
	}
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_batches", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationMCPReadProposalExtractionRejectsParaphraseAndReplaysFailure(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	collected, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "complete", "collect-mcp-paraphrase", "AHE-58"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, collected.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	calls := 0
	input := MCPReadProposalExtractionInput{
		SourceSnapshotID: processed.Source.SourceSnapshotID,
		MaxProposals:     4,
		ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{
			Name: "test-paraphrasing-model", Version: "v1",
		},
		Runner: func(context.Context, evidenceingestion.ExtractorInput) ([]byte, error) {
			calls++
			return []byte(`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds take one week.","evidence_refs":["span:S1"]}]}`), nil
		},
	}
	_, err = ExtractMCPReadDocumentSnapshot(ctx, pool, input)
	if kind, ok := evidenceingestion.KindOf(err); !ok || kind != evidenceingestion.ErrorInvalidExtractorOutput {
		t.Fatalf("paraphrase error = %v, kind=%q", err, kind)
	}
	_, err = ExtractMCPReadDocumentSnapshot(ctx, pool, input)
	if kind, ok := evidenceingestion.KindOf(err); !ok || kind != evidenceingestion.ErrorPersistedAttemptFailed {
		t.Fatalf("failed replay error = %v, kind=%q", err, kind)
	}
	if calls != 1 {
		t.Fatalf("paraphrase runner calls = %d, want 1", calls)
	}
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_batches", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationMCPReadProposalConversionRejectsIncompleteAndContradictoryAuthority(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	partial, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "partial", "collect-mcp-partial-convert", "AHE-53"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery(partial) error = %v", err)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, partial.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery(partial) error = %v", err)
	}
	rejected, err := ConvertMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalConversionInput{
		SourceSnapshotID: processed.Source.SourceSnapshotID,
	})
	if err != nil {
		t.Fatalf("ConvertMCPReadDocumentSnapshot(partial) error = %v", err)
	}
	if rejected.Status != MCPReadProposalConversionStatusRejected ||
		rejected.Reason != MCPReadProposalConversionReasonIncompleteCoverage ||
		rejected.ProposalCount != 0 {
		t.Fatalf("partial MCP proposal conversion = %+v", rejected)
	}
	assertDetectiveTableCount(t, ctx, pool, "extraction_runs", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)

	if _, err := pool.Exec(ctx, `
		UPDATE source_snapshots
		SET origin_metadata = jsonb_set(
			origin_metadata,
			'{global_absence_inference_allowed}',
			'"true"'::jsonb
		)
		WHERE source_snapshot_id = $1
	`, processed.Source.SourceSnapshotID); err != nil {
		t.Fatalf("tampering source authority fixture: %v", err)
	}
	_, err = ConvertMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalConversionInput{
		SourceSnapshotID: processed.Source.SourceSnapshotID,
	})
	assertDetectiveKind(t, err, ErrorMCPProposalConversionRejected)
	assertDetectiveTableCount(t, ctx, pool, "extraction_runs", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationMCPReadProposalConversionRejectsAbsentCandidates(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	collected, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "no-candidates", "collect-mcp-no-candidates", "AHE-55"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, collected.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	rejected, err := ConvertMCPReadDocumentSnapshot(ctx, pool, MCPReadProposalConversionInput{
		SourceSnapshotID: processed.Source.SourceSnapshotID,
	})
	if err != nil {
		t.Fatalf("ConvertMCPReadDocumentSnapshot() error = %v", err)
	}
	if rejected.Status != MCPReadProposalConversionStatusRejected ||
		rejected.Reason != MCPReadProposalConversionReasonNoProposalCandidates ||
		rejected.ProposalCount != 0 {
		t.Fatalf("candidate-free MCP proposal conversion = %+v", rejected)
	}
	assertDetectiveTableCount(t, ctx, pool, "extraction_runs", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationLatestMCPReadSourceProposalConversionUsesCompletedCycle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	ctx, pool := detectiveIntegrationPool(t)
	workspace := integrationMCPReadWorkspace(t, ctx, pool, "latest-conversion", "fixture:AHE-54")
	config := integrationMCPReadSourceConfig(t, "complete", "AHE-54")
	if _, _, err := RegisterMCPReadSourceBinding(ctx, pool, MCPReadSourceBindingInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, Config: config,
	}); err != nil {
		t.Fatalf("RegisterMCPReadSourceBinding() error = %v", err)
	}
	if _, found, err := ConvertLatestMCPReadSourceSnapshot(
		ctx,
		pool,
		workspace.ID,
		workspace.Sources[0].ID,
	); err != nil || found {
		t.Fatalf("pre-collection latest conversion found=%t, err=%v", found, err)
	}
	tick, err := RunMCPReadSourceTick(ctx, pool, MCPReadSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, Config: config,
	})
	if err != nil {
		t.Fatalf("RunMCPReadSourceTick() error = %v", err)
	}
	converted, found, err := ConvertLatestMCPReadSourceSnapshot(
		ctx,
		pool,
		workspace.ID,
		workspace.Sources[0].ID,
	)
	if err != nil {
		t.Fatalf("ConvertLatestMCPReadSourceSnapshot() error = %v", err)
	}
	if !found ||
		converted.Status != MCPReadProposalConversionStatusConverted ||
		converted.SourceSnapshotID != tick.Cycle.SourceSnapshotID {
		t.Fatalf("latest completed conversion found=%t, result=%+v", found, converted)
	}
}

func TestIntegrationMCPReadDeliveryRoundTripAndSourceReplay(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	input := integrationMCPReadInput(t, "complete", "collect-mcp-ahe-42", "AHE-42")

	first, err := CollectMCPReadDelivery(ctx, pool, input)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	if first.Receipt.ContentType != ConnectorDeliveryContentTypeJSON ||
		!first.Receipt.DeliveryCreated || first.Receipt.Replayed ||
		first.ObjectID != "AHE-42" || first.Revision != "revision-42" ||
		first.DocumentID != "AHE-42-description" || !first.Coverage.Complete ||
		first.Coverage.Truncated || first.NextPageCursor != "" {
		t.Fatalf("first MCP collection = %+v", first)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_work", 0)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)

	processed, err := ProcessMCPReadDelivery(ctx, pool, first.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	wantText := []byte("Refunds must be completed within 7 days.\n")
	if processed.Provider != "fixture" ||
		processed.ObjectID != "AHE-42" ||
		processed.DocumentID != "AHE-42-description" ||
		processed.Source.SourceSystem != evidenceingestion.SourceSystemMCPReadDocument ||
		processed.Source.RawContentHash != contentHash(wantText) ||
		processed.Source.RendererName != evidenceingestion.RendererMCPReadDocumentIdentity ||
		processed.Source.SpanCatalogVersion != evidenceingestion.SpanCatalogMCPReadDocumentLineV1 ||
		processed.Source.Replayed {
		t.Fatalf("processed MCP source = %+v", processed)
	}

	var sourceSystem, sourceVersion string
	var raw, originData []byte
	if err := pool.QueryRow(ctx, `
		SELECT snapshot.source_system, snapshot.source_version, blob.raw_content,
		       snapshot.origin_metadata
		FROM source_snapshots AS snapshot
		JOIN source_blobs AS blob
		  ON blob.raw_content_hash = snapshot.raw_content_hash
		WHERE snapshot.source_snapshot_id = $1
	`, processed.Source.SourceSnapshotID).Scan(
		&sourceSystem,
		&sourceVersion,
		&raw,
		&originData,
	); err != nil {
		t.Fatalf("read MCP source authority: %v", err)
	}
	if sourceSystem != evidenceingestion.SourceSystemMCPReadDocument ||
		sourceVersion != "revision-42" ||
		!bytes.Equal(raw, wantText) {
		t.Fatalf("MCP source authority = %s/%s/%q", sourceSystem, sourceVersion, raw)
	}
	var origin map[string]string
	if err := json.Unmarshal(originData, &origin); err != nil {
		t.Fatalf("decode MCP origin: %v", err)
	}
	for key, value := range map[string]string{
		"connector_delivery_id":            first.Receipt.ConnectorDeliveryID,
		"mcp_provider":                     "fixture",
		"mcp_logical_capability":           "read_document",
		"mcp_adapter_name":                 "loopback-fixture",
		"mcp_adapter_version":              "v1",
		"mcp_provider_tool_name":           "read_document",
		"mcp_object_id":                    "AHE-42",
		"mcp_revision":                     "revision-42",
		"mcp_coverage_complete":            "true",
		"mcp_coverage_truncated":           "false",
		"mcp_coverage_completion_reason":   MCPReadCompletionComplete,
		"mcp_document_content_hash":        contentHash(wantText),
		"global_absence_inference_allowed": "false",
	} {
		if origin[key] != value {
			t.Fatalf("origin[%q] = %q, want %q; origin=%+v", key, origin[key], value, origin)
		}
	}

	replayInput := input
	replayInput.Command.Path = "/does/not/exist"
	replayInput.Command.Directory = "/does/not/exist"
	replayedCollection, err := CollectMCPReadDelivery(ctx, pool, replayInput)
	if err != nil {
		t.Fatalf("replay CollectMCPReadDelivery() error = %v", err)
	}
	if !replayedCollection.Receipt.Replayed ||
		replayedCollection.Receipt.ConnectorDeliveryID != first.Receipt.ConnectorDeliveryID {
		t.Fatalf("replayed MCP collection = %+v", replayedCollection)
	}
	changedRequest := input
	changedRequest.Arguments = json.RawMessage(`{"object_id":"AHE-42","expand":"history"}`)
	changedRequest.Command.Path = "/does/not/exist"
	changedRequest.Command.Directory = "/does/not/exist"
	_, err = CollectMCPReadDelivery(ctx, pool, changedRequest)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)
	replayedSource, err := ProcessMCPReadDelivery(ctx, pool, first.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("replay ProcessMCPReadDelivery() error = %v", err)
	}
	if !replayedSource.Source.Replayed ||
		replayedSource.Source.SourceSnapshotID != processed.Source.SourceSnapshotID ||
		replayedSource.Source.ExtractionViewID != processed.Source.ExtractionViewID {
		t.Fatalf("replayed MCP source = %+v", replayedSource)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationMCPReadPartialCoverageRemainsBounded(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	collected, err := CollectMCPReadDelivery(
		ctx,
		pool,
		integrationMCPReadInput(t, "partial", "collect-mcp-ahe-43", "AHE-43"),
	)
	if err != nil {
		t.Fatalf("CollectMCPReadDelivery() error = %v", err)
	}
	if collected.Coverage.Complete || !collected.Coverage.Truncated ||
		collected.Coverage.CompletionReason != MCPReadCompletionNextPage ||
		collected.NextPageCursor != "cursor-2" {
		t.Fatalf("partial MCP collection = %+v", collected)
	}
	processed, err := ProcessMCPReadDelivery(ctx, pool, collected.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	var originData []byte
	if err := pool.QueryRow(ctx, `
		SELECT origin_metadata
		FROM source_snapshots
		WHERE source_snapshot_id = $1
	`, processed.Source.SourceSnapshotID).Scan(&originData); err != nil {
		t.Fatalf("read partial MCP origin: %v", err)
	}
	var origin map[string]string
	if err := json.Unmarshal(originData, &origin); err != nil {
		t.Fatalf("decode partial MCP origin: %v", err)
	}
	if origin["mcp_next_page_cursor"] != "cursor-2" ||
		origin["mcp_coverage_complete"] != "false" ||
		origin["mcp_coverage_truncated"] != "true" ||
		origin["global_absence_inference_allowed"] != "false" {
		t.Fatalf("partial MCP origin = %+v", origin)
	}
}

func TestIntegrationMCPReadFailsClosedBeforeReceiptOnContractMismatch(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	tests := []struct {
		name  string
		input MCPReadCollectionInput
		kind  ErrorKind
	}{
		{
			name:  "malformed result",
			input: integrationMCPReadInput(t, "malformed", "collect-mcp-malformed", "AHE-44"),
			kind:  ErrorMCPContractViolation,
		},
		{
			name: "schema mismatch",
			input: func() MCPReadCollectionInput {
				input := integrationMCPReadInput(t, "complete", "collect-mcp-schema", "AHE-45")
				input.ProviderToolInputSchemaHash = contentHash([]byte("different schema"))
				return input
			}(),
			kind: ErrorMCPContractViolation,
		},
		{
			name:  "advertised destructive tool",
			input: integrationMCPReadInput(t, "destructive", "collect-mcp-write", "AHE-46"),
			kind:  ErrorMCPContractViolation,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CollectMCPReadDelivery(ctx, pool, test.input)
			assertDetectiveKind(t, err, test.kind)
		})
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 0)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
}

func TestIntegrationMCPReadImmutableRemoteRevisionRejectsChangedBytes(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	firstInput := integrationMCPReadInput(t, "complete", "collect-mcp-stable", "AHE-47")
	first, err := CollectMCPReadDelivery(ctx, pool, firstInput)
	if err != nil {
		t.Fatalf("first CollectMCPReadDelivery() error = %v", err)
	}
	changedInput := integrationMCPReadInput(t, "changed", "collect-mcp-changed", "AHE-47")
	_, err = CollectMCPReadDelivery(ctx, pool, changedInput)
	assertDetectiveKind(t, err, ErrorConnectorDeliveryConflict)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)

	processed, err := ProcessMCPReadDelivery(ctx, pool, first.Receipt.ConnectorDeliveryID)
	if err != nil {
		t.Fatalf("ProcessMCPReadDelivery() error = %v", err)
	}
	if processed.Source.RawContentHash != contentHash([]byte("Refunds must be completed within 7 days.\n")) {
		t.Fatalf("preserved source = %+v", processed.Source)
	}
}

func TestMCPReadIntegrationHelperProcess(t *testing.T) {
	mode := os.Getenv("AHE_MCP_READ_HELPER")
	if mode == "" {
		return
	}
	server, err := mcpstdio.NewServer(
		"loopback-read-adapter",
		"v1",
		&integrationMCPReadBackend{mode: mode},
	)
	if err != nil {
		t.Fatalf("mcpstdio.NewServer() error = %v", err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		t.Fatalf("MCP helper Serve() error = %v", err)
	}
}

type integrationMCPReadBackend struct {
	mode string
}

func (b *integrationMCPReadBackend) Tools() []mcpstdio.Tool {
	readOnly := true
	destructive := b.mode == "destructive"
	return []mcpstdio.Tool{{
		Name:        "read_document",
		Description: "Returns one exact loopback document.",
		InputSchema: testMCPReadInputSchema(),
		Annotations: mcpstdio.Annotations{
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
		},
	}}
}

func (b *integrationMCPReadBackend) CallTool(
	_ context.Context,
	_ string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	if b.mode == "tool-error" {
		return nil, errors.New("injected provider failure")
	}
	if b.mode == "slow" {
		time.Sleep(200 * time.Millisecond)
	}
	var input struct {
		ObjectID string `json:"object_id"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, err
	}
	text := "Refunds must be completed within 7 days.\n"
	revision := "revision-42"
	coverage := MCPReadCoverage{
		Complete:         true,
		CompletionReason: MCPReadCompletionComplete,
	}
	nextPageCursor := ""
	limitations := []string{}
	proposalCandidates := []MCPReadProposalCandidate{{
		LocalID:      "document-line-1",
		SelectorKind: MCPReadProposalSelectorLine,
		Selector:     "1",
	}}
	if b.mode == "partial" {
		text = "First page of issue history.\n"
		coverage = MCPReadCoverage{
			Truncated:        true,
			CompletionReason: MCPReadCompletionNextPage,
		}
		nextPageCursor = "cursor-2"
		limitations = []string{"additional page available"}
	}
	if b.mode == "changed" {
		text = "Refunds must be completed within 30 days.\n"
	}
	if b.mode == "structured-transition" {
		state, err := os.ReadFile("transition-state")
		if err != nil {
			return nil, err
		}
		switch string(state) {
		case "A":
			revision = "revision-A"
			text = "{\n" +
				"  \"description\": \"Refunds must be completed within 7 days.\",\n" +
				"  \"updated\": \"2026-07-26 01:02:03 UTC\"\n" +
				"}\n"
		case "B":
			revision = "revision-B"
			text = "{\n" +
				"  \"description\": \"Contact support for account assistance.\",\n" +
				"  \"updated\": \"2026-07-27 02:03:04 UTC\"\n" +
				"}\n"
		case "partial-A", "partial-B":
			revision = "revision-" + string(state)
			text = "{\n" +
				"  \"description\": \"Partial source state " + string(state) + ".\"\n" +
				"}\n"
			coverage = MCPReadCoverage{
				Truncated:        true,
				CompletionReason: MCPReadCompletionNextPage,
			}
			nextPageCursor = "cursor-2"
			limitations = []string{"additional page available"}
		case "over-budget-A":
			revision = "revision-over-budget-A"
			text = "{\n" +
				"  \"description\": \"" + strings.Repeat("A", 8193) + "\"\n" +
				"}\n"
		case "over-budget-B":
			revision = "revision-over-budget-B"
			text = "{\n" +
				"  \"description\": \"" + strings.Repeat("B", 8193) + "\"\n" +
				"}\n"
		default:
			return nil, errors.New("unsupported transition state")
		}
		proposalCandidates = []MCPReadProposalCandidate{{
			LocalID:      "jira-description",
			SelectorKind: MCPReadProposalSelectorJSONPointerString,
			Selector:     "/description",
		}}
	}
	if b.mode == "sectioned" {
		description := "1. Project\n\nPurpose fact.\n\n  1. Boundaries\n\nIt is not autonomous.\n\n  1. Rollback\n\nRollback restores artifacts.\n"
		document, err := json.Marshal(map[string]string{"description": description})
		if err != nil {
			return nil, err
		}
		text = string(document) + "\n"
		proposalCandidates = []MCPReadProposalCandidate{{
			LocalID:      "jira-description",
			SelectorKind: MCPReadProposalSelectorJSONPointerString,
			Selector:     "/description",
		}}
	}
	rawProvider := []byte(`{"key":"` + input.ObjectID + `","revision":"` + revision + `"}`)
	result := MCPReadDocumentResult{
		Contract:       MCPReadDocumentResultContract,
		ObjectID:       input.ObjectID,
		Revision:       revision,
		SourceLocation: "https://fixture.invalid/browse/" + input.ObjectID,
		NextPageCursor: nextPageCursor,
		Coverage:       coverage,
		Document: MCPReadDocument{
			ID:          input.ObjectID + "-description",
			Title:       input.ObjectID,
			Text:        text,
			ContentHash: contentHash([]byte(text)),
		},
		ProposalCandidates:        proposalCandidates,
		RawProviderResponseBase64: base64.StdEncoding.EncodeToString(rawProvider),
		RawProviderResponseHash:   contentHash(rawProvider),
		Limitations:               limitations,
	}
	if b.mode == "no-candidates" {
		result.ProposalCandidates = nil
	}
	if b.mode == "malformed" {
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		value["unsupported_claim"] = "invented answer"
		return json.Marshal(value)
	}
	return json.Marshal(result)
}

func integrationMCPReadWorkspace(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	sourceID string,
) Workspace {
	t.Helper()
	registration, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID:     "register-mcp-runtime-" + suffix,
		WorkspaceID:   "workspace:mcp-runtime-" + suffix,
		WorkspaceRoot: t.TempDir(),
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName:    SourceCapabilityMCPReadDocument,
			CapabilityVersion: SourceCapabilityMCPReadDocumentVersion,
			SourceID:          sourceID,
			RelativePath:      ".",
		}},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	return registration.Workspace
}

func integrationMCPReadSourceConfig(
	t *testing.T,
	mode string,
	objectID string,
) MCPReadSourceConfig {
	t.Helper()
	input := integrationMCPReadInput(t, mode, "unused-runtime-request", objectID)
	command, err := mcpstdio.MacOSLoopbackOnlyCommand(input.Command)
	if err != nil {
		t.Fatalf("MacOSLoopbackOnlyCommand() error = %v", err)
	}
	return MCPReadSourceConfig{
		ConnectorID:                 input.ConnectorID,
		Provider:                    input.Provider,
		LogicalCapability:           input.LogicalCapability,
		AdapterName:                 input.AdapterName,
		AdapterVersion:              input.AdapterVersion,
		ProviderToolName:            input.ProviderToolName,
		ProviderToolInputSchemaHash: input.ProviderToolInputSchemaHash,
		Arguments:                   input.Arguments,
		Command:                     command,
	}
}

func integrationMCPReadInput(
	t *testing.T,
	mode string,
	requestID string,
	objectID string,
) MCPReadCollectionInput {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	return MCPReadCollectionInput{
		RequestID:                   requestID,
		ConnectorID:                 "loopback-read-connector",
		Provider:                    "fixture",
		LogicalCapability:           "read_document",
		AdapterName:                 "loopback-fixture",
		AdapterVersion:              "v1",
		ProviderToolName:            "read_document",
		ProviderToolInputSchemaHash: mustMCPReadSchemaHash(t),
		Arguments:                   json.RawMessage(`{"object_id":"` + objectID + `"}`),
		Command: mcpstdio.CommandConfig{
			Path:        executable,
			Args:        []string{"-test.run=TestMCPReadIntegrationHelperProcess"},
			Directory:   t.TempDir(),
			Environment: []string{"AHE_MCP_READ_HELPER=" + mode},
		},
	}
}
