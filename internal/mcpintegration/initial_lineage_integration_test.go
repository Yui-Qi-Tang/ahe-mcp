//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpinitiallineage"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpsupersession"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: end-to-end synthetic sources in a separately selected,
// disposable database, using real bounded LOGINs and stdio subprocesses.
func TestIntegrationInitialLineageStandardMCP(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("dedicated acceptance database required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileInitialLineageReviewer, dbrole.ProfileSupersessionReviewer)
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.intake, f.schema, "intake")
	sources := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.reviewer, f.schema, "source-claim-reviewer")
	// Exercise the shipping protected-file launcher for the new capability.
	directory := provisioningProtectedDirectory(t, dsn)
	binaries := buildProvisioningCommands(t, ctx, directory)
	config, err := pgxpool.ParseConfig(f.initialLineage.dsn)
	if err != nil {
		t.Fatal("invalid synthetic registration configuration")
	}
	credentialPath := filepath.Join(directory, "registration.dsn")
	writeProvisioningProtectedFile(t, credentialPath, []byte(provisioningExplicitURL(config, config.ConnConfig.Database, config.ConnConfig.User, config.ConnConfig.Password)))
	launcherPath := filepath.Join(directory, "registration.json")
	launcher, err := json.Marshal(map[string]string{"schema_version": "ahe-mcp-launcher/v1", "binary_path": binaries["ahe-ingest-mcp"], "database_dns_file": credentialPath, "database": config.ConnConfig.Database, "session_user": config.ConnConfig.User, "schema": f.schema, "role": f.initialLineage.group, "profile": "initial-lineage-reviewer", "principal_id": "mock:protected:initial-lineage-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	writeProvisioningProtectedFile(t, launcherPath, launcher)
	registrations := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], launcherPath, "ahe-ingest-mcp")
	replacements := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.supersession, f.schema, "supersession-reviewer")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
	registrations.assertTools(t, []string{mcpinitiallineage.ToolReview, mcpinitiallineage.ToolAdmit})
	for _, p := range []*authorityProcess{intake, sources, replacements, query} {
		p.assertDenied(t, mcpinitiallineage.ToolAdmit, map[string]any{})
	}
	registrations.assertDenied(t, mcpsupersession.ToolAdmit, map[string]any{})
	basis := evidencesupersession.Basis{SourceSystem: "lab", SourceNamespace: "issue24-synthetic", ObjectType: "document", ObjectID: "policy", SlotKind: "field", SlotID: "value"}
	submit := func(label, revision, statement string) evidenceingestionmcp.SubmitExtractorOutputResponse {
		src := relationTool[evidenceingestionmcp.SubmitExternalSourceResponse](t, intake, "submit_external_source", evidenceingestion.ExternalSourceEnvelopeV1{
			SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "source-" + label, SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, Revision: revision,
			SourceLocation: "https://example.invalid/issue24", Title: "Synthetic policy", ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim, Content: statement + "\n", Coverage: evidenceingestion.ExternalSourceCoverageFullDocument, Limitations: []string{}, CollectorID: "synthetic-collector", ConnectorID: "synthetic-connector", ObservedAt: "2026-10-23T00:00:00Z",
		})
		return relationTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
			RequestID: "extract-" + label, SourceSnapshotID: src.SourceSnapshotID, ExtractionViewID: src.ExtractionViewID, ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "TEST APPROVAL STUB", Version: "v1"}, ExtractorOutput: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{src.Spans[0].SpanID}}}},
		})
	}
	first := submit("first", "v1", "The synthetic policy value is one.")
	sr := relationTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, sources, "get_source_claim_review", map[string]any{"extraction_attempt_id": first.ExtractionAttemptID, "proposal_occurrence_id": first.ProposalOccurrenceID})
	c := relationTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, sources, "admit_reviewed_source_claim", map[string]any{"extraction_attempt_id": first.ExtractionAttemptID, "expected_subject": sr.Subject, "decision": "approved", "decision_reason": "TEST APPROVAL STUB"})
	repeat := submit("repeat", "v1", "The synthetic policy value is one.")
	// Getter must reject self-target before requesting an impossible human approval.
	relationRejected(t, replacements, mcpsupersession.ToolReview, map[string]any{"proposal_occurrence_id": repeat.ProposalOccurrenceID, "basis": basis, "target_node_ids": []string{c.CanonicalRef}})
	r := relationTool[evidenceingestion.InitialLineageRegistrationReview](t, registrations, mcpinitiallineage.ToolReview, evidenceingestion.InitialLineageRegistrationRequest{ClaimID: c.CanonicalRef, ObservationReceiptID: "source-repeat", Basis: basis})
	input := map[string]any{"request_id": "register-C", "review": r.Request, "expected_subject": r.Subject, "decision": "approved", "decision_reason": "TEST APPROVAL STUB: independent first membership and derived effects"}
	bad := map[string]any{"request_id": "register-C", "review": r.Request, "expected_subject": r.Subject, "decision": "approved", "decision_reason": "TEST APPROVAL STUB", "reviewer_id": "caller"}
	relationRejected(t, registrations, mcpinitiallineage.ToolAdmit, bad)
	receipt := relationTool[evidenceingestion.InitialLineageRegistrationReceipt](t, registrations, mcpinitiallineage.ToolAdmit, input)
	if receipt.Replayed || receipt.Record.Payload.Reviewer != "mock:protected:initial-lineage-reviewer" {
		t.Fatal("registration did not bind fresh launcher authority")
	}
	current := relationTool[evidenceingestion.CanonicalSupersessionCurrentness](t, query, "get_canonical_supersession_currentness", map[string]any{"lineage_key": receipt.Record.Payload.Binding.LineageKey})
	if current.Projection != nil || current.JointProjection == nil || current.JointProjection.Head.Revision != 0 || !current.JointProjection.ClosureAvailable {
		t.Fatal("zero-head joint query failed")
	}
	state := relationTool[evidenceingestion.EvidenceUseProjection](t, query, "get_evidence_use_state", map[string]any{"canonical_ids": []string{c.CanonicalRef}, "policy": evidenceuse.CurrentActionV1})
	if len(state.Assessment.Nodes) != 1 || state.Assessment.Nodes[0].Outcome != evidenceuse.Eligible {
		t.Fatal("registered claim not eligible under complete current-action basis")
	}
	grounded := relationTool[evidenceingestion.EvidenceUseProjection](t, query, "get_evidence_use_state", map[string]any{"canonical_ids": []string{c.CanonicalRef}, "policy": evidenceuse.GroundedActionV1})
	if grounded.Assessment.Nodes[0].Outcome == evidenceuse.Eligible {
		t.Fatal("registration bypassed required source checks")
	}
	newer := submit("newer", "v2", "The synthetic policy value is two.")
	rr := relationTool[evidenceingestion.SupersessionReview](t, replacements, mcpsupersession.ToolReview, map[string]any{"proposal_occurrence_id": newer.ProposalOccurrenceID, "basis": basis, "target_node_ids": []string{c.CanonicalRef}})
	replaced := relationTool[evidenceingestion.SupersessionReviewReceipt](t, replacements, mcpsupersession.ToolAdmit, evidenceingestion.ReviewedSupersessionInput{RequestID: "replace-C", Review: rr.Display.Request, ExpectedSubject: rr.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: genuinely new synthetic revision"})
	if replaced.Admission.EventRevision != 1 || len(replaced.Admission.BootstrappedTargetNodeIDs) != 0 {
		t.Fatal("v2 replacement re-bootstrapped registration")
	}
	replayed := relationTool[evidenceingestion.InitialLineageRegistrationReceipt](t, registrations, mcpinitiallineage.ToolAdmit, input)
	if !replayed.Replayed || replayed.Record.ID != receipt.Record.ID {
		t.Fatal("later supersession broke historical receipt")
	}
	var outcome string
	if err := f.pool.QueryRow(ctx, `SELECT admission_outcome FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, repeat.ProposalOccurrenceID).Scan(&outcome); err != nil || outcome != "pending" {
		t.Fatal("registration consumed duplicate pending occurrence", err)
	}
}
