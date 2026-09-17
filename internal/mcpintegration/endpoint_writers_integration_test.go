//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceimplements"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcprelations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// No native canonical bootstrap: every source, proposal, endpoint and relation
// below is created over a real standard stdio MCP under separately granted roles.
// All content, extraction and approvals are explicitly synthetic test stubs.
func TestIntegrationEndpointWritersStandardMCP(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileRelationReviewer, dbrole.ProfileEndpointReviewer, dbrole.ProfileRepositoryIntake)
	directory := t.TempDir()
	binaries := buildProvisioningCommands(t, ctx, directory)
	start := func(login authorityProcessLogin, profile, repositoryRoot string) *authorityProcess {
		config, err := pgxpool.ParseConfig(login.dsn)
		if err != nil {
			t.Fatal("invalid synthetic runtime configuration")
		}
		credential := provisioningExplicitURL(config, config.ConnConfig.Database, config.ConnConfig.User, config.ConnConfig.Password)
		credentialPath := filepath.Join(directory, profile+".dsn")
		writeProvisioningProtectedFile(t, credentialPath, []byte(credential))
		binary := "ahe-ingest-mcp"
		if profile == "query" {
			binary = "ahe-query-mcp"
		}
		fields := map[string]string{"schema_version": "ahe-mcp-launcher/v1", "binary_path": binaries[binary],
			"database_dns_file": credentialPath, "database": config.ConnConfig.Database, "session_user": config.ConnConfig.User,
			"schema": f.schema, "role": login.group, "profile": profile, "principal_id": "mock:protected:" + profile}
		if profile == "repository-intake" {
			fields["repository_root"] = repositoryRoot
			fields["repository_id"] = "synthetic:refund"
		}
		payload, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, profile+".json")
		writeProvisioningProtectedFile(t, path, payload)
		return startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], path, binary)
	}
	intake := start(f.intake, "intake", "")
	reviewer := start(f.reviewer, "source-claim-reviewer", "")
	endpoints := start(f.endpoints, "endpoint-reviewer", "")
	relations := start(f.relations, "relation-reviewer", "")
	query := start(f.query, "query", "")
	endpoints.assertTools(t, []string{mcpendpoints.ToolReview, mcpendpoints.ToolAdmit})
	for _, name := range []string{"admit_pending_proposal", "submit_external_source", "admit_reviewed_implements", "admit_reviewed_source_claim", "capture_repository_snapshot"} {
		endpoints.assertDenied(t, name, map[string]any{})
	}
	var sources []evidenceingestionmcp.SubmitExternalSourceResponse
	var parents []string
	statements := []string{"refund.ValidateRefundWindow identifies the integer refund-window predicate.",
		"The refund-window predicate contract is only days <= 30; no time origin or nonnegative bound is specified."}
	submit := func(i int, request, statement string) evidenceingestionmcp.SubmitExtractorOutputResponse {
		return relationTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
			RequestID: request, SourceSnapshotID: sources[i].SourceSnapshotID, ExtractionViewID: sources[i].ExtractionViewID,
			ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "synthetic-endpoint-extractor", Version: "v1"},
			ExtractorOutput: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{
				{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{sources[i].Spans[0].SpanID}},
			}},
		})
	}
	for i, statement := range statements {
		label := []string{"a", "b"}[i]
		source := relationTool[evidenceingestionmcp.SubmitExternalSourceResponse](t, intake, "submit_external_source", evidenceingestion.ExternalSourceEnvelopeV1{
			SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "source-" + label,
			SourceSystem: "lab", SourceNamespace: "endpoint-synthetic", ObjectType: "document", ObjectID: label, Revision: "v1",
			SourceLocation: "https://example.invalid/endpoint-" + label, Title: "Synthetic refund contract",
			ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
			Content: statement + "\n", Coverage: evidenceingestion.ExternalSourceCoverageFullDocument, Limitations: []string{},
			CollectorID: "synthetic-collector", ConnectorID: "synthetic-connector", ObservedAt: "2026-09-16T00:00:00Z",
		})
		sources = append(sources, source)
		proposal := submit(i, "extract-"+label, statement)
		review := relationTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, "get_source_claim_review", evidenceingestionmcp.GetSourceClaimReviewRequest{
			ExtractionAttemptID: proposal.ExtractionAttemptID, ProposalOccurrenceID: proposal.ProposalOccurrenceID})
		var staleReview evidenceingestion.EndpointReview
		if i == 1 {
			// A source admission can invalidate an already prepared endpoint review.
			staleReview = relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, evidenceingestion.EndpointReviewRequest{
				Kind: "derived_spec", ProposalOccurrenceID: proposal.ProposalOccurrenceID,
				Derivation: &evidenceingestion.EndpointDerivation{ParentNodeIDs: []string{parents[0]},
					Method: "Synthetic pending review before a different admission.", Producer: "synthetic-extractor", TraceRef: "synthetic:stale-review"},
			})
		}
		admission := relationTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", evidenceingestionmcp.AdmitReviewedSourceClaimRequest{
			ExtractionAttemptID: proposal.ExtractionAttemptID, ExpectedSubject: review.Subject, Decision: "approved",
			DecisionReason: "APPROVE STUB: exact synthetic source only, no real human or model.",
		})
		parents = append(parents, admission.CanonicalRef)
		if i == 1 {
			before := relationCounts(t, ctx, f.pool)
			original := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": admission.CanonicalRef})
			if original.AdmissionOutcome != "admitted" || original.EndpointAdmission != nil {
				t.Fatal("expected a source admission without an endpoint receipt")
			}
			endpointConflict(t, endpoints, mcpendpoints.ToolReview, staleReview.Display.Request, evidenceingestion.ErrorAdmissionStateConflict)
			endpointConflict(t, endpoints, mcpendpoints.ToolAdmit, evidenceingestion.ReviewedEndpointAdmissionInput{
				RequestID: "stale-endpoint", Review: staleReview.Display.Request, ExpectedSubject: staleReview.Subject,
				Decision: "approved", DecisionReason: "APPROVE STUB: stale review must not authorize a different admission.",
			}, evidenceingestion.ErrorAdmissionStateConflict)
			after := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": admission.CanonicalRef})
			if relationCounts(t, ctx, f.pool) != before || !reflect.DeepEqual(original, after) {
				t.Fatal("rejected endpoint review/write changed the existing source admission")
			}
		}
	}
	slices.Sort(parents)
	derived := submit(0, "extract-derived", "refund.ValidateRefundWindow is the integer refund-window predicate governed by the reviewed days <= 30 contract.")
	req := evidenceingestion.EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: derived.ProposalOccurrenceID,
		Derivation: &evidenceingestion.EndpointDerivation{ParentNodeIDs: parents, Method: "Combine every AND parent without adding a time origin or nonnegative bound.", Producer: "synthetic-extractor", TraceRef: "synthetic:derived"}}
	before := relationCounts(t, ctx, f.pool)
	review := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, req)
	assertPendingEndpointReview(t, review)
	if relationCounts(t, ctx, f.pool) != before || len(review.Display.SourceLeaves) != 2 || len(review.Display.Ancestors) != 2 || review.Display.CodeFile != nil {
		t.Fatal("derived review context/effect differs")
	}
	approve := evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: "derived-endpoint", Review: req, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "APPROVE STUB: complete synthetic AND derivation."}
	changed := approve
	changed.ExpectedSubject += "0"
	relationRejected(t, endpoints, mcpendpoints.ToolAdmit, changed)
	changed = approve
	changed.Decision = "admit"
	relationRejected(t, endpoints, mcpendpoints.ToolAdmit, changed)
	intake.assertDenied(t, mcpendpoints.ToolAdmit, approve)
	reviewer.assertDenied(t, mcpendpoints.ToolAdmit, approve)
	relations.assertDenied(t, mcpendpoints.ToolAdmit, approve)
	verifyEndpointLockAndRollback(t, ctx, f, endpoints, approve)
	admitted := relationTool[evidenceingestion.EndpointAdmissionReceipt](t, endpoints, mcpendpoints.ToolAdmit, approve)
	reviewAgain := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, req)
	assertAdmittedEndpointReview(t, review, reviewAgain, admitted)
	changedReview := req
	changedDerivation := *req.Derivation
	changedDerivation.Method += " changed"
	changedReview.Derivation = &changedDerivation
	afterAdmission := relationCounts(t, ctx, f.pool)
	endpointConflict(t, endpoints, mcpendpoints.ToolReview, changedReview, evidenceingestion.ErrorReviewContractConflict)
	if relationCounts(t, ctx, f.pool) != afterAdmission {
		t.Fatal("read-only replay inspection changed canonical state")
	}
	replay := relationTool[evidenceingestion.EndpointAdmissionReceipt](t, endpoints, mcpendpoints.ToolAdmit, approve)
	if !replay.Admission.Replayed || replay.Admission.CanonicalRef != admitted.Admission.CanonicalRef {
		t.Fatal("derived endpoint exact replay failed")
	}
	changed = approve
	changed.DecisionReason += " changed"
	relationRejected(t, endpoints, mcpendpoints.ToolAdmit, changed)
	changed = approve
	changed.RequestID = "different-request"
	relationRejected(t, endpoints, mcpendpoints.ToolAdmit, changed)
	node := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": admitted.Admission.CanonicalRef})
	if node.Canonical == nil || node.Canonical.NodeKind != "derived_claim" || node.EndpointAdmission == nil || node.EndpointAdmission.ReviewSubject != review.Subject {
		t.Fatal("Query lost endpoint origin")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-C", dir}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL="}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("synthetic Git: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	codeText := "package refund\n\nfunc ValidateRefundWindow(days int) bool { return days <= 30 }\n"
	if err := os.WriteFile(filepath.Join(dir, "refund.go"), []byte(codeText), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "refund.go")
	git("-c", "user.name=Synthetic Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "Synthetic endpoint fixture")
	sha := git("rev-parse", "HEAD")
	repository := start(f.repository, "repository-intake", dir)
	repository.assertTools(t, []string{mcpendpoints.ToolCapture, mcpendpoints.ToolExtract})
	repository.assertDenied(t, mcpendpoints.ToolAdmit, approve)
	relationRejected(t, repository, mcpendpoints.ToolCapture, map[string]any{"request_id": "bad", "commit_sha": "HEAD"})
	relationRejected(t, repository, mcpendpoints.ToolCapture, map[string]any{"request_id": "bad", "commit_sha": sha, "workspace_root": dir})
	captured := relationTool[evidenceingestion.RepositorySnapshotCaptureResult](t, repository, mcpendpoints.ToolCapture, mcpendpoints.CaptureRequest{RequestID: "git-capture", CommitSHA: sha})
	parsed := relationTool[evidenceingestion.RepositoryIngestResult](t, repository, mcpendpoints.ToolExtract, mcpendpoints.ExtractRequest{RequestID: "git-extract", RepositorySnapshotID: captured.RepositorySnapshot.ID})
	var codeProposal string
	for _, id := range parsed.ProposalOccurrenceIDs {
		p := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": id})
		if p.CodeFact != nil && p.CodeFact.QualifiedName == "refund.ValidateRefundWindow" {
			codeProposal = id
		}
	}
	if codeProposal == "" {
		t.Fatal("missing deterministic declaration proposal")
	}
	codeReq := evidenceingestion.EndpointReviewRequest{Kind: "repository_code", ProposalOccurrenceID: codeProposal}
	codeReview := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, codeReq)
	assertPendingEndpointReview(t, codeReview)
	if codeReview.Display.CodeFile == nil || codeReview.Display.CodeFile.Text != codeText || codeReview.Display.CodeFile.FileSnapshot.GitBlobOID == "" {
		t.Fatal("exact code/revision missing")
	}
	codeApprove := evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: "code-endpoint", Review: codeReq, ExpectedSubject: codeReview.Subject, Decision: "approved", DecisionReason: "APPROVE STUB: synthetic immutable Go declaration."}
	code := relationTool[evidenceingestion.EndpointAdmissionReceipt](t, endpoints, mcpendpoints.ToolAdmit, codeApprove)
	codeReviewAgain := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, codeReq)
	assertAdmittedEndpointReview(t, codeReview, codeReviewAgain, code)
	codeReplay := relationTool[evidenceingestion.EndpointAdmissionReceipt](t, endpoints, mcpendpoints.ToolAdmit, codeApprove)
	if !codeReplay.Admission.Replayed {
		t.Fatal("code replay failed")
	}
	mapping := evidenceimplements.ReviewMapping{
		ProposalSentence: "The reviewed derived specification maps to the refund.ValidateRefundWindow declaration.",
		Coverage:         "Declaration-level traceability only; semantic correctness is an explicit synthetic reviewer assertion.",
		Limitations:      []string{"APPROVE STUB, not human review, execution proof or source truth."},
		Witnesses: []evidenceimplements.Witness{{Kind: evidenceimplements.ReviewedBehavior, EndpointNodeIDs: []string{admitted.Admission.CanonicalRef, code.Admission.CanonicalRef},
			SourceTitle: "Synthetic refund contract", SourceLocation: "https://example.invalid/endpoint-a", ExactExcerpt: statements[0], ExcerptHash: evidenceimplements.ExcerptHash(statements[0])}},
	}
	relationReq := evidenceimplements.RecursiveReviewRequest{SpecificationNodeID: admitted.Admission.CanonicalRef, ImplementationNodeID: code.Admission.CanonicalRef, Mapping: mapping,
		Rules: []evidenceimplements.RecursiveDerivationRule{{NodeID: admitted.Admission.CanonicalRef, DerivationID: admitted.Admission.DerivationID, RuleStatement: req.Derivation.Method}}}
	relationReview := relationTool[mcprelations.ImplementsReviewResponse](t, relations, mcprelations.ToolGetImplementsReview, relationReq)
	relationTool[map[string]any](t, relations, mcprelations.ToolAdmitReviewedImplements, mcprelations.ImplementsAdmissionRequest{
		RequestID: "endpoint-implements", Review: relationReview.Request, ExpectedSubject: relationReview.Subject, Decision: "approved", DecisionReason: "APPROVE STUB: independent synthetic mapping only.",
	})
	var nodes, edges, decisions, endpointBindings, sourceBindings, unreviewed, active int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_graph_nodes),(SELECT count(*) FROM canonical_graph_edges),
 (SELECT count(*) FROM admission_decisions),(SELECT count(*) FROM canonical_endpoint_review_bindings),
 (SELECT count(*) FROM canonical_source_claim_review_bindings),(SELECT count(*) FROM admission_decisions WHERE review_binding_contract_version IS NULL),
 (SELECT count(*) FROM repository_source_heads)`).Scan(&nodes, &edges, &decisions, &endpointBindings, &sourceBindings, &unreviewed, &active); err != nil {
		t.Fatal(err)
	}
	if nodes != 7 || edges != 6 || decisions != 4 || endpointBindings != 2 || sourceBindings != 2 || unreviewed != 0 || active != 0 {
		t.Fatalf("unexpected fully reviewed MCP graph: nodes=%d edges=%d decisions=%d endpoints=%d sources=%d unreviewed=%d active=%d", nodes, edges, decisions, endpointBindings, sourceBindings, unreviewed, active)
	}
	t.Log("standard MCP only: two reviewed source claims, one AND-derived endpoint, one repository code endpoint, one independently reviewed implements; no native canonical bootstrap")
}

func endpointConflict(t *testing.T, p *authorityProcess, tool string, args any, kind evidenceingestion.ErrorKind) {
	t.Helper()
	response := p.request(t, "tools/call", map[string]any{"name": tool, "arguments": args})
	var envelope struct {
		IsError bool                          `json:"isError"`
		Failure evidenceingestion.DomainError `json:"structuredContent"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &envelope) != nil || !envelope.IsError {
		t.Fatalf("%s did not reject the conflicting endpoint review", tool)
	}
	if envelope.Failure.Kind != kind {
		t.Fatalf("%s error kind = %q, want %q", tool, envelope.Failure.Kind, kind)
	}
}

func assertPendingEndpointReview(t *testing.T, review evidenceingestion.EndpointReview) {
	t.Helper()
	state := review.Lifecycle
	if state.Mode != "pending_admission" || state.AdmissionOutcome != "pending" ||
		state.CanonicalRef != "" || state.Receipt != nil || review.Display.Proposal.AdmissionOutcome != "pending" {
		t.Fatal("pending endpoint review has conflicting lifecycle state")
	}
}

func assertAdmittedEndpointReview(t *testing.T, before, after evidenceingestion.EndpointReview, receipt evidenceingestion.EndpointAdmissionReceipt) {
	t.Helper()
	state := after.Lifecycle
	if state.Mode != "exact_replay_only" || state.AdmissionOutcome != "admitted" ||
		state.CanonicalRef != receipt.Admission.CanonicalRef || !reflect.DeepEqual(state.Receipt, &receipt) {
		t.Fatal("admitted endpoint review lost its current state or exact receipt")
	}
	if before.Subject != after.Subject || !reflect.DeepEqual(before.Display, after.Display) {
		t.Fatal("lifecycle inspection changed the immutable pre-admission review")
	}
}
