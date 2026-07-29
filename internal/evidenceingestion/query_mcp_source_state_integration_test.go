//go:build integration

package evidenceingestion

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationQueryMCPReadSourceStatesPreservesRevisionBoundaries(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := insertMCPReadSourceStateQueryFixture(t, ctx, pool)

	latest, err := QueryMCPReadSourceStates(ctx, pool, MCPReadSourceStateQueryInput{
		SourceBindingID: fixture.sourceBindingID,
		Mode:            MCPReadSourceStateModeLatestObserved,
	})
	if err != nil {
		t.Fatalf("QueryMCPReadSourceStates(latest) error = %v", err)
	}
	if latest.SchemaVersion != MCPReadSourceStateQuerySchemaV1 ||
		len(latest.States) != 1 ||
		latest.States[0].SourceSnapshotID != fixture.snapshotB ||
		latest.States[0].ProviderRevision != fixture.revisionB ||
		latest.States[0].ObservationCount != 2 ||
		latest.States[0].FirstObservedCycle != 2 ||
		latest.States[0].LastObservedCycle != 3 ||
		!latest.States[0].LatestObserved ||
		latest.GlobalAbsenceInferenceAllowed {
		t.Fatalf("latest source state = %+v", latest)
	}

	history, err := QueryMCPReadSourceStates(ctx, pool, MCPReadSourceStateQueryInput{
		SourceBindingID: fixture.sourceBindingID,
		Mode:            MCPReadSourceStateModeHistory,
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("QueryMCPReadSourceStates(history) error = %v", err)
	}
	if len(history.States) != 2 ||
		history.States[0].SourceSnapshotID != fixture.snapshotB ||
		history.States[1].SourceSnapshotID != fixture.snapshotA ||
		history.States[1].LatestObserved ||
		history.Truncated {
		t.Fatalf("source state history = %+v", history)
	}

	exactA, err := QueryMCPReadSourceStates(ctx, pool, MCPReadSourceStateQueryInput{
		SourceBindingID: fixture.sourceBindingID,
		Mode:            MCPReadSourceStateModeExactRevision,
		Revision:        fixture.revisionA,
	})
	if err != nil {
		t.Fatalf("QueryMCPReadSourceStates(exact A) error = %v", err)
	}
	if len(exactA.States) != 1 ||
		exactA.States[0].SourceSnapshotID != fixture.snapshotA ||
		exactA.States[0].ExactEvidenceFilter.SourceVersion != fixture.revisionA ||
		exactA.States[0].LatestObserved {
		t.Fatalf("exact A source state = %+v", exactA)
	}

	comparison, err := QueryMCPReadSourceStates(ctx, pool, MCPReadSourceStateQueryInput{
		SourceBindingID: fixture.sourceBindingID,
		Mode:            MCPReadSourceStateModeCompare,
		FromRevision:    fixture.revisionA,
		ToRevision:      fixture.revisionB,
	})
	if err != nil {
		t.Fatalf("QueryMCPReadSourceStates(compare) error = %v", err)
	}
	if len(comparison.States) != 2 ||
		comparison.Comparison == nil ||
		!comparison.Comparison.SourceSnapshotChanged ||
		!comparison.Comparison.RawContentChanged ||
		comparison.Comparison.CrossRevisionJoinPerformed ||
		comparison.Comparison.From.SourceSnapshotID != fixture.snapshotA ||
		comparison.Comparison.To.SourceSnapshotID != fixture.snapshotB {
		t.Fatalf("source state comparison = %+v", comparison)
	}

	_, err = QueryMCPReadSourceStates(ctx, pool, MCPReadSourceStateQueryInput{
		SourceBindingID: fixture.sourceBindingID,
		Mode:            MCPReadSourceStateModeExactRevision,
		Revision:        "missing-revision",
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorSourceStateNotFound {
		t.Fatalf("missing revision error = %v, kind = %q", err, kind)
	}
}

type mcpReadSourceStateQueryFixture struct {
	sourceBindingID string
	snapshotA       string
	snapshotB       string
	revisionA       string
	revisionB       string
}

func insertMCPReadSourceStateQueryFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) mcpReadSourceStateQueryFixture {
	t.Helper()
	fixture := mcpReadSourceStateQueryFixture{
		sourceBindingID: "workspace-source:" + strings.Repeat("1", 64),
		revisionA:       "provider-revision-a",
		revisionB:       "provider-revision-b",
	}
	workspaceID := "workspace:mcp-source-state-query"
	sourceID := "atlassian:jira:AHE-42"
	rawA := []byte("Refunds must be completed within 7 days.\n")
	rawB := []byte("Contact support for account assistance.\n")
	sourceA, err := CaptureManualSource(ctx, pool, ManualTextInput{
		SourceSystem:  SourceSystemMCPReadDocument,
		SourceID:      sourceID,
		SourceVersion: fixture.revisionA,
		Raw:           rawA,
		RequestID:     "mcp-source-state-query-source-a",
	})
	if err != nil {
		t.Fatalf("capture source revision A: %v", err)
	}
	sourceB, err := CaptureManualSource(ctx, pool, ManualTextInput{
		SourceSystem:  SourceSystemMCPReadDocument,
		SourceID:      sourceID,
		SourceVersion: fixture.revisionB,
		Raw:           rawB,
		RequestID:     "mcp-source-state-query-source-b",
	})
	if err != nil {
		t.Fatalf("capture source revision B: %v", err)
	}
	fixture.snapshotA = sourceA.SourceSnapshotID
	fixture.snapshotB = sourceB.SourceSnapshotID
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspaces (workspace_id, canonical_root)
		VALUES ($1, $2)
	`, workspaceID, t.TempDir()); err != nil {
		t.Fatalf("insert detective workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspace_sources (
		  source_binding_id, workspace_id, capability_name, capability_version,
		  source_system, source_id, relative_path, path_kind
		)
		VALUES ($1,$2,'mcp-read-document','v1','mcp_read_document',$3,'.','remote')
	`, fixture.sourceBindingID, workspaceID, sourceID); err != nil {
		t.Fatalf("insert MCP workspace source: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_mcp_read_source_bindings (
		  source_binding_id, workspace_id, source_id, connector_id, provider,
		  logical_capability, adapter_name, adapter_version, provider_tool_name,
		  provider_tool_input_schema_hash, arguments, command_hash, binding_hash
		)
		VALUES (
		  $1,$2,$3,'fixture-connector','fixture','read_document',
		  'fixture-adapter','v1','read_document',$4,'{}'::jsonb,$5,$6
		)
	`, fixture.sourceBindingID, workspaceID, sourceID,
		"sha256:"+strings.Repeat("4", 64),
		"sha256:"+strings.Repeat("5", 64),
		"sha256:"+strings.Repeat("6", 64),
	); err != nil {
		t.Fatalf("insert MCP source binding: %v", err)
	}
	for index, cycle := range []struct {
		snapshot string
		revision string
	}{
		{snapshot: fixture.snapshotA, revision: fixture.revisionA},
		{snapshot: fixture.snapshotB, revision: fixture.revisionB},
		{snapshot: fixture.snapshotB, revision: fixture.revisionB},
	} {
		number := index + 1
		deliveryID := "detective-connector-delivery:" + strings.Repeat(
			string(rune('7'+index)),
			64,
		)
		cycleID := "detective-mcp-read-cycle:" + strings.Repeat(
			string(rune('a'+index)),
			64,
		)
		if _, err := pool.Exec(ctx, `
			INSERT INTO detective_connector_inbox_deliveries (
			  connector_delivery_id, connector_id, external_delivery_id,
			  content_type, payload_hash, payload_bytes, byte_length
			)
			VALUES ($1,'fixture-connector',$2,'application/json',$3,$4,$5)
		`, deliveryID, "delivery-"+string(rune('a'+index)),
			"sha256:"+strings.Repeat(string(rune('a'+index)), 64),
			[]byte("{}"), 2,
		); err != nil {
			t.Fatalf("insert connector delivery %d: %v", number, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO detective_mcp_read_collection_cycles (
			  cycle_id, workspace_id, source_binding_id, cycle_number,
			  binding_hash, collection_request_id, status,
			  connector_delivery_id, source_snapshot_id,
			  provider_object_id, provider_revision, document_id,
			  coverage_complete, coverage_truncated, completion_reason,
			  finished_at
			)
			VALUES (
			  $1,$2,$3,$4,$5,$6,'completed',$7,$8,
			  'AHE-42',$9,'AHE-42-description',true,false,'complete',now()
			)
		`, cycleID, workspaceID, fixture.sourceBindingID, number,
			"sha256:"+strings.Repeat("6", 64),
			"mcp-source-state-request-"+string(rune('a'+index)),
			deliveryID, cycle.snapshot, cycle.revision,
		); err != nil {
			t.Fatalf("insert MCP cycle %d: %v", number, err)
		}
	}
	return fixture
}
