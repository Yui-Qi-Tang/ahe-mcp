//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpsupersession"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: all provider bytes, extraction and decisions are synthetic.
// Every lifecycle write runs through compiled stdio MCP and its restricted role.
func TestIntegrationReviewedLifecycleStandardMCP(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	defer cancel()
	f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileEndpointReviewer, dbrole.ProfileSupersessionReviewer, dbrole.ProfileRepositoryIntake)
	directory := provisioningProtectedDirectory(t, dsn)
	binaries := buildProvisioningCommands(t, ctx, directory)
	start := func(login authorityProcessLogin, profile, root string) *authorityProcess {
		cfg, err := pgxpool.ParseConfig(login.dsn)
		if err != nil {
			t.Fatal("invalid synthetic launcher config")
		}
		credential := provisioningExplicitURL(cfg, cfg.ConnConfig.Database, cfg.ConnConfig.User, cfg.ConnConfig.Password)
		credentialPath := filepath.Join(directory, profile+".dsn")
		writeProvisioningProtectedFile(t, credentialPath, []byte(credential))
		binary := "ahe-ingest-mcp"
		if profile == "query" {
			binary = "ahe-query-mcp"
		}
		fields := map[string]string{"schema_version": "ahe-mcp-launcher/v1", "binary_path": binaries[binary], "database_dns_file": credentialPath, "database": cfg.ConnConfig.Database, "session_user": cfg.ConnConfig.User, "schema": f.schema, "role": login.group, "profile": profile, "principal_id": "mock:protected:" + profile}
		if profile == "repository-intake" {
			fields["repository_root"] = root
			fields["repository_id"] = "synthetic:code-disposition"
		}
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(directory, profile+".json")
		writeProvisioningProtectedFile(t, configPath, body)
		return startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], configPath, binary)
	}
	intake := start(f.intake, "intake", "")
	sources := start(f.reviewer, "source-claim-reviewer", "")
	endpoints := start(f.endpoints, "endpoint-reviewer", "")
	replacements := start(f.supersession, "supersession-reviewer", "")
	query := start(f.query, "query", "")

	replacements.assertTools(t, []string{mcpsupersession.ToolReview, mcpsupersession.ToolAdmit})
	endpoints.assertTools(t, []string{mcpendpoints.ToolReview, mcpendpoints.ToolAdmit, mcpendpoints.ToolDisposition})
	for _, proc := range []*authorityProcess{intake, sources, endpoints, query} {
		proc.assertDenied(t, mcpsupersession.ToolAdmit, map[string]any{})
	}
	replacements.assertDenied(t, "admit_pending_proposal", map[string]any{})
	replacements.assertDenied(t, mcpendpoints.ToolDisposition, map[string]any{})
	submit := func(label, revision, statement string) evidenceingestionmcp.SubmitExtractorOutputResponse {
		src := relationTool[evidenceingestionmcp.SubmitExternalSourceResponse](t, intake, "submit_external_source", evidenceingestion.ExternalSourceEnvelopeV1{
			SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "source-" + label, SourceSystem: "lab", SourceNamespace: "reviewed-lifecycle-synthetic", ObjectType: "document", ObjectID: "policy", Revision: revision,
			SourceLocation: "https://example.invalid/policy", Title: "Synthetic policy", ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim, Content: statement + "\n", Coverage: evidenceingestion.ExternalSourceCoverageFullDocument, Limitations: []string{}, CollectorID: "synthetic-collector", ConnectorID: "synthetic-connector", ObservedAt: "2026-10-12T00:00:00Z"})
		return relationTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
			RequestID: "extract-" + label, SourceSnapshotID: src.SourceSnapshotID, ExtractionViewID: src.ExtractionViewID, ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "synthetic-lifecycle", Version: "v1"}, ExtractorOutput: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{src.Spans[0].SpanID}}}}})
	}
	old := submit("old", "v1", "The synthetic policy value is one.")
	oldReview := relationTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, sources, "get_source_claim_review", map[string]any{"extraction_attempt_id": old.ExtractionAttemptID, "proposal_occurrence_id": old.ProposalOccurrenceID})
	oldAdmission := relationTool[evidenceingestionmcp.AdmitPendingProposalResponse](t, sources, "admit_reviewed_source_claim", map[string]any{"extraction_attempt_id": old.ExtractionAttemptID, "expected_subject": oldReview.Subject, "decision": "approved", "decision_reason": "TEST APPROVAL STUB: synthetic initial policy."})
	newer := submit("new", "v2", "The synthetic policy value is two.")
	selection := map[string]any{"proposal_occurrence_id": newer.ProposalOccurrenceID, "basis": evidenceingestion.SupersessionLineageBasis{SourceSystem: "lab", SourceNamespace: "reviewed-lifecycle-synthetic", ObjectType: "document", ObjectID: "policy", SlotKind: "field", SlotID: "value"}, "target_node_ids": []string{oldAdmission.CanonicalRef}}
	review := relationTool[evidenceingestion.SupersessionReview](t, replacements, mcpsupersession.ToolReview, selection)
	if review.Display.Proposal.StatementText != "The synthetic policy value is two." || len(review.Display.Targets) != 1 || len(review.Display.TargetSourceBases) != 1 || review.Lifecycle.Mode != "pending_admission" {
		t.Fatal("incomplete replacement display")
	}
	in := evidenceingestion.ReviewedSupersessionInput{RequestID: "replace-policy", Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: exact synthetic v2 replaces v1."}
	changed := in
	changed.ExpectedSubject = "supersession-review:sha256:wrong"
	endpointConflict(t, replacements, mcpsupersession.ToolAdmit, changed, evidenceingestion.ErrorReviewContractConflict)
	writeLifecycleArtifact(t, "supersession-review.json", review)
	receipt := relationTool[evidenceingestion.SupersessionReviewReceipt](t, replacements, mcpsupersession.ToolAdmit, in)
	writeLifecycleArtifact(t, "supersession-receipt.json", receipt)
	again := relationTool[evidenceingestion.SupersessionReviewReceipt](t, replacements, mcpsupersession.ToolAdmit, in)
	if !again.Admission.Replayed || again.Admission.AdmissionEventID != receipt.Admission.AdmissionEventID {
		t.Fatal("replacement exact replay failed")
	}
	terminal := relationTool[evidenceingestion.SupersessionReview](t, replacements, mcpsupersession.ToolReview, selection)
	if terminal.Subject != review.Subject || !reflect.DeepEqual(terminal.Display, review.Display) || terminal.Lifecycle.Mode != "exact_replay_only" {
		t.Fatal("replacement lifecycle lost exact review")
	}
	changed = in
	changed.DecisionReason = "different decision"
	endpointConflict(t, replacements, mcpsupersession.ToolAdmit, changed, evidenceingestion.ErrorSupersessionReplayConflict)
	current := relationTool[map[string]any](t, query, "get_canonical_supersession_currentness", map[string]any{"lineage_key": receipt.Admission.LineageKey})
	if current == nil {
		t.Fatal("missing currentness")
	}
	var direction string
	if err := f.pool.QueryRow(ctx, "SELECT relation FROM canonical_graph_edges WHERE from_node_id=$1 AND to_node_id=$2", receipt.Admission.CanonicalRef, oldAdmission.CanonicalRef).Scan(&direction); err != nil || direction != "supersedes" {
		t.Fatal("replacement direction differs")
	}
	readback := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": receipt.Admission.CanonicalRef})
	if readback.SupersessionAdmission == nil || readback.SupersessionAdmission.RequestID != in.RequestID {
		t.Fatal("Query lost supersession receipt")
	}
	later := submit("later", "v3", "The synthetic policy value is three.")
	laterSelection := map[string]any{"proposal_occurrence_id": later.ProposalOccurrenceID, "basis": review.Display.Request.Basis, "target_node_ids": []string{receipt.Admission.CanonicalRef}}
	laterReview := relationTool[evidenceingestion.SupersessionReview](t, replacements, mcpsupersession.ToolReview, laterSelection)
	laterInput := evidenceingestion.ReviewedSupersessionInput{RequestID: "replace-policy-v3", Review: laterReview.Display.Request, ExpectedSubject: laterReview.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: exact v3 replaces v2."}
	stale := laterInput
	stale.Review.ExpectedRevision = 0
	stale.Review.ExpectedHeadEventID = ""
	endpointConflict(t, replacements, mcpsupersession.ToolAdmit, stale, evidenceingestion.ErrorSupersessionHeadConflict)
	latest := relationTool[evidenceingestion.SupersessionReviewReceipt](t, replacements, mcpsupersession.ToolAdmit, laterInput)
	originalReplay := relationTool[evidenceingestion.SupersessionReviewReceipt](t, replacements, mcpsupersession.ToolAdmit, in)
	if !originalReplay.Admission.Replayed || latest.Admission.EventRevision != 2 {
		t.Fatal("later head invalidated exact replay")
	}
	closure := relationTool[evidenceingestion.CanonicalSupersessionCurrentness](t, query, "get_canonical_supersession_currentness", map[string]any{"lineage_key": latest.Admission.LineageKey})
	foundCurrent, foundSuperseded := false, false
	for _, node := range closure.Projection.Nodes {
		if node.NodeID == latest.Admission.CanonicalRef && node.Status == "current" {
			foundCurrent = true
		}
		if node.NodeID == oldAdmission.CanonicalRef && node.Status == "superseded" {
			foundSuperseded = true
		}
	}
	writeLifecycleArtifact(t, "supersession-currentness.json", closure)
	if !foundCurrent || !foundSuperseded {
		t.Fatal("currentness did not derive expected lineage states")
	}

	previousRequest := ""
	for i, decision := range []string{"reject", "audit_only"} {
		label := []string{"decline", "audit"}[i]
		p := submit(label, "v"+label, "Synthetic derived candidate "+label+" requires unimplemented behavior.")
		req := evidenceingestion.EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: p.ProposalOccurrenceID, Derivation: &evidenceingestion.EndpointDerivation{UsePolicy: "current-action/v1", ParentNodeIDs: []string{receipt.Admission.CanonicalRef}, Method: "synthetic AND derivation", Producer: "synthetic-planner", TraceRef: "synthetic:" + label}}

		if previousRequest != "" {
			pendingReview := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, req)
			collision := evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: previousRequest, Review: req, ExpectedSubject: pendingReview.Subject, Decision: decision, DecisionReason: "TEST APPROVAL STUB: request collision must fail."}
			endpointConflict(t, endpoints, mcpendpoints.ToolDisposition, collision, evidenceingestion.ErrorReviewContractConflict)
			collision.Decision = "approved"
			endpointConflict(t, endpoints, mcpendpoints.ToolAdmit, collision, evidenceingestion.ErrorReviewContractConflict)
		}
		previousRequest = "disposition-" + req.ProposalOccurrenceID
		verifyLifecycleDisposition(t, ctx, f, endpoints, query, req, decision)
	}
	raceProposal := submit("race", "vrace", "Synthetic concurrent endpoint candidate.")
	raceReq := evidenceingestion.EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: raceProposal.ProposalOccurrenceID, Derivation: &evidenceingestion.EndpointDerivation{UsePolicy: "current-action/v1", ParentNodeIDs: []string{latest.Admission.CanonicalRef}, Method: "synthetic concurrent AND", Producer: "synthetic-planner", TraceRef: "synthetic:race"}}
	faultReplacement := submit("fault-replacement", "v4", "The synthetic policy value is four.")
	faultSelection := map[string]any{"proposal_occurrence_id": faultReplacement.ProposalOccurrenceID, "basis": laterInput.Review.Basis, "target_node_ids": []string{latest.Admission.CanonicalRef}}
	faultReview := relationTool[evidenceingestion.SupersessionReview](t, replacements, mcpsupersession.ToolReview, faultSelection)
	faultInput := evidenceingestion.ReviewedSupersessionInput{RequestID: "fault-replacement", Review: faultReview.Display.Request, ExpectedSubject: faultReview.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: rollback then exact retry of v4."}
	verifyLifecycleFaults(t, ctx, f, endpoints, replacements, raceReq, faultInput)

	// Both code dispositions use native parser proposals, never source relabeling.
	dir := t.TempDir()
	git := func(args ...string) string {
		c := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-C", dir}, args...)...)
		c.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL="}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatal("synthetic Git failed")
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte("package fixture\nfunc Decline() {}\nfunc Audit() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "fixture.go")
	git("-c", "user.name=Synthetic Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "Synthetic lifecycle fixture")
	// The compiled process takes a launcher-fixed root, matching the repository contract.
	repo := start(f.repository, "repository-intake", dir)
	snap := relationTool[evidenceingestion.RepositorySnapshotCaptureResult](t, repo, mcpendpoints.ToolCapture, mcpendpoints.CaptureRequest{RequestID: "capture-code", CommitSHA: git("rev-parse", "HEAD")})
	parsed := relationTool[evidenceingestion.RepositoryIngestResult](t, repo, mcpendpoints.ToolExtract, mcpendpoints.ExtractRequest{RequestID: "extract-code", RepositorySnapshotID: snap.RepositorySnapshot.ID})
	checked := 0
	for _, id := range parsed.ProposalOccurrenceIDs {
		p := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": id})
		if p.CodeFact == nil {
			continue
		}
		decision := ""
		if p.CodeFact.QualifiedName == "fixture.Decline" {
			decision = "reject"
		}
		if p.CodeFact.QualifiedName == "fixture.Audit" {
			decision = "audit_only"
		}
		if decision != "" {
			verifyLifecycleDisposition(t, ctx, f, endpoints, query, evidenceingestion.EndpointReviewRequest{Kind: "repository_code", ProposalOccurrenceID: id}, decision)
			checked++
		}
	}
	if checked != 2 {
		t.Fatal("did not exercise both native code dispositions")
	}

	down, err := os.ReadFile("../../migrations/000056_reviewed_lifecycle_writers.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	parentDown, err := os.ReadFile("../../migrations/000057_reviewed_derivation_parent_limit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	rollbackTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rollbackTx.Exec(ctx, string(parentDown)); err != nil {
		t.Fatal(err)
	}
	if _, err = rollbackTx.Exec(ctx, string(down)); err == nil {
		t.Fatal("rollback discarded reviewed lifecycle history")
	}
	if err = rollbackTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*authorityProcess{repo, query, replacements, endpoints, sources, intake} {
		p.finish(t)
	}
	t.Log("TEST APPROVAL STUB: native MCP replacement + exact replay/currentness and all four endpoint kind/outcome combinations passed")
}

func verifyLifecycleDisposition(t *testing.T, ctx context.Context, f authorityProcessFixture, endpoints, query *authorityProcess, req evidenceingestion.EndpointReviewRequest, decision string) {
	t.Helper()
	before := relationCounts(t, ctx, f.pool)
	review := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, req)
	in := evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: "disposition-" + req.ProposalOccurrenceID, Review: req, ExpectedSubject: review.Subject, Decision: decision, DecisionReason: "TEST APPROVAL STUB: reviewed synthetic endpoint non-admission."}
	bad := in
	bad.ExpectedSubject = "endpoint-review:sha256:wrong"
	endpointConflict(t, endpoints, mcpendpoints.ToolDisposition, bad, evidenceingestion.ErrorReviewContractConflict)
	writeLifecycleArtifact(t, req.Kind+"-"+decision+"-review.json", review)
	result := relationTool[evidenceingestion.EndpointDispositionReceipt](t, endpoints, mcpendpoints.ToolDisposition, in)
	writeLifecycleArtifact(t, req.Kind+"-"+decision+"-receipt.json", result)
	replay := relationTool[evidenceingestion.EndpointDispositionReceipt](t, endpoints, mcpendpoints.ToolDisposition, in)
	if !replay.Replayed || result.AdmissionDecisionID != replay.AdmissionDecisionID {
		t.Fatal("disposition exact replay failed")
	}
	after := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, req)
	if after.Subject != review.Subject || !reflect.DeepEqual(after.Display, review.Display) || after.Lifecycle.Mode != "exact_replay_only" || after.Lifecycle.DispositionReceipt == nil {
		t.Fatal("terminal disposition review changed")
	}
	record := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": req.ProposalOccurrenceID})
	if record.EndpointDisposition == nil || record.EndpointDisposition.DecisionReason != in.DecisionReason || record.CanonicalRef != nil || record.AdmissionOutcome != result.AdmissionOutcome {
		t.Fatal("Query lost terminal disposition")
	}

	pending := relationTool[evidencequerymcp.ListEvidenceRecordsResponse](t, query, "list_evidence_records", map[string]any{"admission_outcome": "pending", "lifecycle_scope": "all", "limit": 100})
	for _, entry := range pending.Records {
		if entry.RecordRef.ID == req.ProposalOccurrenceID {
			t.Fatal("declined endpoint remained in pending list")
		}
	}
	if req.Derivation != nil {
		changedParents := req
		copyDerivation := *req.Derivation
		copyDerivation.TraceRef = "synthetic:changed-trace"
		changedParents.Derivation = &copyDerivation
		endpointConflict(t, endpoints, mcpendpoints.ToolReview, changedParents, evidenceingestion.ErrorReviewContractConflict)
	}
	approved := in
	approved.Decision = "approved"
	approved.RequestID = "later-admission-" + req.ProposalOccurrenceID
	endpointConflict(t, endpoints, mcpendpoints.ToolAdmit, approved, evidenceingestion.ErrorAdmissionStateConflict)
	changed := in
	changed.DecisionReason = "changed reason"
	endpointConflict(t, endpoints, mcpendpoints.ToolDisposition, changed, evidenceingestion.ErrorAdmissionStateConflict)
	now := relationCounts(t, ctx, f.pool)
	if now[0] != before[0] || now[1] != before[1] {
		t.Fatal("disposition created canonical evidence")
	}
}

func writeLifecycleArtifact(t *testing.T, name string, value any) {
	t.Helper()
	dir := os.Getenv("AHE_LIFECYCLE_ARTIFACT_DIR")
	if dir == "" {
		return
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
