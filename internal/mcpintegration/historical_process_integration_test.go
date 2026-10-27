//go:build integration

package mcpintegration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const historicalProcessPrincipal = "TEST APPROVAL STUB launcher"

type historicalProcessFixture struct {
	ctx       context.Context
	authority authorityProcessFixture
	processes map[string]*authorityProcess
}

func newHistoricalProcessFixture(t *testing.T) historicalProcessFixture {
	t.Helper()
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileEndpointReviewer, dbrole.ProfileHistoricalDerivationReviewer)
	directory := provisioningProtectedDirectory(t, dsn)
	binaries := buildProvisioningCommands(t, ctx, directory)
	processes := make(map[string]*authorityProcess)
	for _, login := range []authorityProcessLogin{f.query, f.intake, f.reviewer, f.endpoints, f.historical} {
		profile := string(login.profile)
		cfg, err := pgxpool.ParseConfig(login.dsn)
		if err != nil {
			t.Fatal("invalid synthetic historical runtime configuration")
		}
		cfg.ConnConfig.Host = "127.0.0.1"
		credentialPath := filepath.Join(directory, profile+".dsn")
		writeProvisioningProtectedFile(t, credentialPath, []byte(provisioningExplicitURL(cfg, cfg.ConnConfig.Database, cfg.ConnConfig.User, cfg.ConnConfig.Password)))
		binary := "ahe-ingest-mcp"
		if login.profile == dbrole.ProfileQuery {
			binary = "ahe-query-mcp"
		}
		body, err := json.Marshal(map[string]string{
			"schema_version": "ahe-mcp-launcher/v1", "binary_path": binaries[binary],
			"database_dns_file": credentialPath, "database": cfg.ConnConfig.Database,
			"session_user": cfg.ConnConfig.User, "schema": f.schema, "role": login.group,
			"profile": profile, "principal_id": historicalProcessPrincipal,
		})
		if err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(directory, profile+".json")
		writeProvisioningProtectedFile(t, configPath, body)
		processes[profile] = startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], configPath, binary)
	}
	processes[string(dbrole.ProfileHistoricalDerivationReviewer)].assertTools(t, []string{mcpendpoints.ToolHistoricalReview, mcpendpoints.ToolHistoricalDecision})
	return historicalProcessFixture{ctx: ctx, authority: f, processes: processes}
}

func historicalProcessSources(t *testing.T, f historicalProcessFixture) (evidenceingestionmcp.SubmitTextSourceResponse, []string) {
	t.Helper()
	intake, reviewer := f.processes["intake"], f.processes["source-claim-reviewer"]
	var first evidenceingestionmcp.SubmitTextSourceResponse
	var parents []string
	for i := range 3 {
		label := fmt.Sprintf("product-history-source-%d", i)
		source := relationTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", evidenceingestionmcp.SubmitTextSourceRequest{
			RequestID: label, SourceID: label, SourceVersion: "1", RawText: fmt.Sprintf("Application %d lacked contact information and was returned.", i),
			ManualReviewProfile: &evidenceingestion.ManualReviewProfileInput{ContractVersion: evidenceingestion.ManualSourceReviewProfileV1,
				Title: "Synthetic application record", Location: "manual:" + label, Coverage: evidenceingestion.ManualReviewCoverageSubmittedText, Limitations: []string{"TEST APPROVAL STUB"}},
		})
		if i == 0 {
			first = source
		}
		output := relationTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
			RequestID: label + "-extract", SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
			ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "synthetic-product-history", Version: "v1"},
			ExtractorOutput:     evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: source.Spans[0].QuotedText, EvidenceRefs: []string{source.Spans[0].SpanID}}}},
		})
		review := relationTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, "get_source_claim_review", evidenceingestionmcp.GetSourceClaimReviewRequest{
			ExtractionAttemptID: output.ExtractionAttemptID, ProposalOccurrenceID: output.ProposalOccurrenceID,
		})
		var reviewPackage evidenceingestion.ReviewPackage
		if err := json.Unmarshal([]byte(review.Display.PayloadUTF8), &reviewPackage); err != nil {
			t.Fatal(err)
		}
		if reviewPackage.ProposalBasis.ManualReviewProfile == nil {
			t.Fatal("manual first-capture authority missing from source review")
		}
		admitted := relationTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", evidenceingestionmcp.AdmitReviewedSourceClaimRequest{
			ExtractionAttemptID: output.ExtractionAttemptID, ExpectedSubject: review.Subject,
			Decision: "approved", DecisionReason: "TEST APPROVAL STUB: exact synthetic sample",
		})
		parents = append(parents, admitted.CanonicalRef)
	}
	slices.Sort(parents)
	return first, parents
}

func historicalProcessProposal(t *testing.T, f historicalProcessFixture, source evidenceingestionmcp.SubmitTextSourceResponse, parents []string, label string) evidenceingestion.HistoricalDerivationRequest {
	t.Helper()
	output := relationTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, f.processes["intake"], "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
		RequestID: label, SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
		ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "synthetic-product-induction", Version: "v1"},
		ExtractorOutput: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "hypothesis",
			StatementText: "Hypothesis: contact information may be required; only the declared samples, no control or causal proof. " + label,
			EvidenceRefs:  []string{source.Spans[0].SpanID}}}},
	})
	return evidenceingestion.HistoricalDerivationRequest{ProposalOccurrenceID: output.ProposalOccurrenceID,
		Derivation: evidenceingestion.HistoricalDerivation{ParentNodeIDs: slices.Clone(parents), Method: "Bounded induction; other return reasons and representativeness are unknown.", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:" + label}}
}

// historicalProcessState hashes all fixture table rows and user-sequence state.
// This does not claim that rejected calls produce no WAL or session effects.
func historicalProcessState(t *testing.T, f historicalProcessFixture) map[string]string {
	t.Helper()
	rows, err := f.authority.pool.Query(f.ctx, `SELECT c.relname,c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','S') ORDER BY c.relname`, f.authority.schema)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		var raw string
		if err := f.authority.pool.QueryRow(f.ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{f.authority.schema, name}.Sanitize()+` r`).Scan(&raw); err != nil {
			t.Fatal("cannot snapshot synthetic table or sequence", name)
		}
		out[name] = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	}
	return out
}

func TestIntegrationHistoricalDerivationStandardMCP(t *testing.T) {
	f := newHistoricalProcessFixture(t)
	source, parents := historicalProcessSources(t, f)
	history, endpoint, query := f.processes["historical-derivation-reviewer"], f.processes["endpoint-reviewer"], f.processes["query"]
	for _, decision := range []string{"approved", "reject", "audit_only"} {
		t.Run(decision, func(t *testing.T) {
			req := historicalProcessProposal(t, f, source, parents, "historical-process-"+decision)
			review := relationTool[evidenceingestion.HistoricalDerivationReview](t, history, mcpendpoints.ToolHistoricalReview, req)
			if review.Mode != "pending_decision" || review.Display.UseClass != evidencegraph.DerivationHistoricalOnly || len(review.Display.Basis.SourceLeaves) != 3 || review.Display.Basis.CodeFile != nil {
				t.Fatal("historical native display omitted its purpose or complete textual basis")
			}
			for _, leaf := range review.Display.Basis.SourceLeaves {
				if strings.Contains(leaf.RawText, "Hypothesis:") {
					t.Fatal("new conclusion was fabricated as source text")
				}
			}
			in := evidenceingestion.HistoricalDerivationDecision{RequestID: "historical-process-decision-" + decision, Review: req,
				ExpectedSubject: review.Subject, Decision: decision, DecisionReason: "TEST APPROVAL STUB: bounded historical record"}
			current := evidenceingestion.EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: req.ProposalOccurrenceID,
				Derivation: &evidenceingestion.EndpointDerivation{UsePolicy: evidenceuse.CurrentActionV1, ParentNodeIDs: req.Derivation.ParentNodeIDs, Method: req.Derivation.Method, Producer: req.Derivation.Producer, TraceRef: req.Derivation.TraceRef}}
			currentReview := relationTool[evidenceingestion.EndpointReview](t, endpoint, mcpendpoints.ToolReview, current)
			before := historicalProcessState(t, f)
			historicalInvalidFreshRequests(t, f, in)
			wrongPurpose := in
			wrongPurpose.ExpectedSubject = currentReview.Subject
			endpointConflict(t, history, mcpendpoints.ToolHistoricalDecision, wrongPurpose, evidenceingestion.ErrorReviewContractConflict)
			endpointConflict(t, endpoint, mcpendpoints.ToolAdmit, evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: "cross-purpose", Review: current, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: in.DecisionReason}, evidenceingestion.ErrorReviewContractConflict)
			for _, profile := range []string{"query", "intake", "source-claim-reviewer", "endpoint-reviewer"} {
				f.processes[profile].assertDenied(t, mcpendpoints.ToolHistoricalDecision, in)
			}
			for _, field := range []string{"reviewer_id", "use_class", "use_policy"} {
				raw, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				var invalid map[string]any
				if err := json.Unmarshal(raw, &invalid); err != nil {
					t.Fatal(err)
				}
				invalid[field] = "caller"
				relationRejected(t, history, mcpendpoints.ToolHistoricalDecision, invalid)
			}
			if !reflect.DeepEqual(before, historicalProcessState(t, f)) {
				t.Fatal("rejected fresh historical calls changed fixture state")
			}
			got := relationTool[evidenceingestion.HistoricalDerivationReceipt](t, history, mcpendpoints.ToolHistoricalDecision, in)
			if got.Replayed || got.ReviewerID != historicalProcessPrincipal {
				t.Fatal("fresh historical decision or launcher principal lost")
			}
			after := historicalProcessState(t, f)
			replay := relationTool[evidenceingestion.HistoricalDerivationReceipt](t, history, mcpendpoints.ToolHistoricalDecision, in)
			if !replay.Replayed || replay.ReviewSubject != got.ReviewSubject || !reflect.DeepEqual(after, historicalProcessState(t, f)) {
				t.Fatal("exact replay changed historical authority")
			}
			readback := relationTool[evidenceingestion.HistoricalDerivationReview](t, query, "get_evidence_governance_history", map[string]any{"kind": "historical_derivation", "target_id": req.ProposalOccurrenceID})
			if readback.Subject != review.Subject || !strings.Contains(readback.Display.Basis.Proposal.StatementText, "Hypothesis:") || readback.Display.Request.Derivation.Method != req.Derivation.Method {
				t.Fatal("Query lost new-judgment attribution or its limitations")
			}
			if decision != "approved" {
				if got.Admission != nil || got.Disposition == nil || got.Outcome != map[string]string{"reject": "rejected", "audit_only": "audit_only"}[decision] {
					t.Fatal("declined historical decision created evidence")
				}
				return
			}
			if got.Admission == nil || !slices.Equal(got.Admission.ParentNodeIDs, parents) {
				t.Fatal("historical admission lost required parents")
			}
			node := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": got.Admission.CanonicalRef})
			if node.Canonical == nil || node.Canonical.NodeKind != string(evidencegraph.CanonicalDerivedClaim) {
				t.Fatal("Query did not identify a derived judgment")
			}
			child := historicalProcessProposal(t, f, source, []string{got.Admission.CanonicalRef}, "historical-process-child")
			childReview := relationTool[evidenceingestion.HistoricalDerivationReview](t, history, mcpendpoints.ToolHistoricalReview, child)
			if len(childReview.Display.AncestorSources) != 1 || childReview.Display.AncestorSources[0].Node.CanonicalID != got.Admission.CanonicalRef || childReview.Display.AncestorSources[0].RawText == "" {
				t.Fatal("restricted MCP review omitted the derived ancestor carrier")
			}
			childReceipt := relationTool[evidenceingestion.HistoricalDerivationReceipt](t, history, mcpendpoints.ToolHistoricalDecision, evidenceingestion.HistoricalDerivationDecision{
				RequestID: "historical-process-child-decision", Review: child, ExpectedSubject: childReview.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB"})
			if childReceipt.Admission == nil {
				t.Fatal("complete historical child was not admitted")
			}
			for _, id := range []string{got.Admission.CanonicalRef, childReceipt.Admission.CanonicalRef} {
				for _, policy := range []evidenceuse.Policy{evidenceuse.CurrentActionV1, evidenceuse.GroundedActionV1} {
					plan := relationTool[evidenceuse.PlanAssessment](t, query, "check_current_evidence_plan", map[string]any{"policy": policy, "actions": []evidenceuse.ActionBasis{{ActionID: "synthetic-action", BasisIDs: []string{id}}}})
					if plan.Accepted {
						t.Fatal("historical judgment or child received current clearance")
					}
				}
			}
		})
	}
	history.assertDenied(t, mcpendpoints.ToolAdmit, map[string]any{})
}
