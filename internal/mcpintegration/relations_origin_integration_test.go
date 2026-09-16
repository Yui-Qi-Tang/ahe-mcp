//go:build integration

package mcpintegration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceimplements"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencereferences"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcprelations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sources and approvals are deterministic synthetic fixtures. Existing typed
// node writers only bootstrap endpoints; this does not expose them over MCP.
type canonicalReferencesSupersessionFixture struct {
	References canonicalReferencesFixture
	Basis      evidenceingestion.SupersessionLineageBasis
	Current    evidenceingestion.SupersessionAdmissionResult
	ObjectID   string
}
type implementsSupersessionOriginFixture struct {
	Basis     evidenceingestion.SupersessionLineageBasis
	Current   evidenceingestion.SupersessionAdmissionResult
	DepthOne  evidenceimplements.DerivedReviewRequest
	Recursive evidenceimplements.RecursiveReviewRequest
}

func TestIntegrationRelationsReferencesSupersessionOrigin(t *testing.T) {
	ctx, f := relationFixture(t)
	fixture := canonicalReferencesSupersessionOrigin(t, ctx, f.pool, "mcp-origin")
	p := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.relations, f.schema, "relation-reviewer")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
	review := relationTool[evidencereferences.Review](t, p, mcprelations.ToolGetReferencesReview, fixture.References.Request)
	before := relationCounts(t, ctx, f.pool)
	req := mcprelations.ReferencesAdmissionRequest{RequestID: "references-supersession-origin", Review: review.Request, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "Synthetic relation approval; not real human authentication."}
	result := relationTool[evidencereferences.AdmissionResult](t, p, mcprelations.ToolAdmitReviewedReferences, req)
	if result.Replayed || result.Edge.From != fixture.Current.CanonicalRef {
		t.Fatal("incorrect independent reference origin")
	}
	after := relationCounts(t, ctx, f.pool)
	if after != [5]int{before[0], before[1] + 1, before[2], before[3], before[4] + 1} {
		t.Fatal("unexpected first relation state change")
	}
	canonicalReferencesAdvanceSupersession(t, ctx, f.pool, fixture)
	before = relationCounts(t, ctx, f.pool)
	fresh := relationTool[evidencereferences.Review](t, p, mcprelations.ToolGetReferencesReview, fixture.References.Request)
	if !reflect.DeepEqual(fresh, review) {
		t.Fatal("later head changed exact historical reference review")
	}
	replay := relationTool[evidencereferences.AdmissionResult](t, p, mcprelations.ToolAdmitReviewedReferences, req)
	if !replay.Replayed || replay.Edge.ID != result.Edge.ID {
		t.Fatal("historical reference replay changed")
	}
	read := relationTool[evidencequerymcp.RelationProvenanceResponse](t, query, "get_relation_provenance", map[string]any{"canonical_edge_id": result.Edge.ID})
	if read.ReferencesAdmission == nil {
		t.Fatal("missing independently verified reference receipt")
	}
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("historical reference review/replay/readback wrote state")
	}
	p.finish(t)
	query.finish(t)
}

func TestIntegrationRelationsImplementsSupersessionOrigin(t *testing.T) {
	ctx, f := relationFixture(t)
	fixture := implementsSupersessionOriginSetup(t, ctx, f.pool, "mcp-origin")
	p := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.relations, f.schema, "relation-reviewer")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
	review := relationTool[mcprelations.ImplementsReviewResponse](t, p, mcprelations.ToolGetImplementsReview, fixture.Recursive)
	if !strings.Contains(review.Display.PayloadUTF8, fixture.Current.CanonicalRef) {
		t.Fatal("missing supersession-origin ancestor")
	}
	before := relationCounts(t, ctx, f.pool)
	req := mcprelations.ImplementsAdmissionRequest{RequestID: "implements-supersession-origin", Review: review.Request, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "Synthetic relation approval; not real human authentication."}
	result := relationTool[evidenceimplements.DerivedAdmissionResult](t, p, mcprelations.ToolAdmitReviewedImplements, req)
	if result.Replayed {
		t.Fatal("first implements write reported replay")
	}
	after := relationCounts(t, ctx, f.pool)
	if after != [5]int{before[0], before[1] + 1, before[2], before[3] + 1, before[4]} {
		t.Fatal("unexpected first relation state change")
	}
	implementsSupersessionOriginAdvance(t, ctx, f.pool, fixture)
	before = relationCounts(t, ctx, f.pool)
	fresh := relationTool[mcprelations.ImplementsReviewResponse](t, p, mcprelations.ToolGetImplementsReview, fixture.Recursive)
	if !reflect.DeepEqual(fresh, review) {
		t.Fatal("later head changed exact historical implements review")
	}
	replay := relationTool[evidenceimplements.DerivedAdmissionResult](t, p, mcprelations.ToolAdmitReviewedImplements, req)
	if !replay.Replayed || replay.Edge.ID != result.Edge.ID {
		t.Fatal("historical implements replay changed")
	}
	read := relationTool[evidencequerymcp.RelationProvenanceResponse](t, query, "get_relation_provenance", map[string]any{"canonical_edge_id": result.Edge.ID})
	if read.ImplementsAdmission == nil {
		t.Fatal("missing independently verified implements receipt")
	}
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("historical implements review/replay/readback wrote state")
	}
	p.finish(t)
	query.finish(t)
}

func canonicalReferencesExternalVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, object, version, raw string) canonicalReferencesSource {
	t.Helper()
	result, err := evidenceingestion.CaptureExternalSource(ctx, pool, evidenceingestion.ExternalSourceEnvelopeV1{
		SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "references-origin:" + object + ":" + version,
		SourceSystem: "lab", SourceNamespace: "canonical-references-synthetic", ObjectType: "document", ObjectID: object, Revision: version,
		SourceLocation: "https://example.invalid/references/" + object, Title: "Synthetic versioned reference document",
		ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
		Content: raw, Coverage: evidenceingestion.ExternalSourceCoverageExactExcerpt,
		Limitations: []string{"Authored synthetic immutable token source; not provider authenticity or semantic correctness proof."},
		CollectorID: "test-stub:references-collector", ConnectorID: "test-stub:references-connector", ObservedAt: "2026-09-06T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return canonicalReferencesSource{Intake: result.SourceIntakeResult, Raw: raw}
}

func canonicalReferencesSupersessionOrigin(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) canonicalReferencesSupersessionFixture {
	t.Helper()
	object := "references-origin-" + suffix
	initial := canonicalReferencesExternalVersion(t, ctx, pool, object, "v1", "[[ahe-anchor:initial]] Initial synthetic rule.\n")
	initial.Proposals = canonicalReferencesExtract(t, ctx, pool, initial, suffix+"-initial", []evidenceingestion.ExtractorProposalOutput{
		{ProposalLocalID: "initial", StatementText: initial.Intake.Spans[0].QuotedText, EvidenceRefs: []string{initial.Intake.Spans[0].SpanID}},
	})
	old, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: initial.Proposals[0].ProposalOccurrenceID,
		DecisionBy: "test-stub:ordinary-origin", DecisionReason: "APPROVE STUB: create the original source materializer."})
	if err != nil {
		t.Fatal(err)
	}
	initial.NodeIDs = []string{old.CanonicalRef}
	// A fixed external target can itself cite v1. The v2 source is subsequently
	// authored to reference this already captured target; no hash cycle exists.
	targetToken := "[[ahe-ref:" + initial.Intake.SourceSnapshotID + "#initial]]"
	target := canonicalReferencesCapture(t, ctx, pool, suffix+"-external-target", "[[ahe-anchor:source]] External target cites the fixed original "+targetToken+"\n", true)
	target = canonicalReferencesAdmitSource(t, ctx, pool, target, suffix+"-external-target", 0)
	token := "[[ahe-ref:" + target.Intake.SourceSnapshotID + "#source]]"
	replacement := canonicalReferencesExternalVersion(t, ctx, pool, object, "v2", "[[ahe-anchor:replacement]] Replacement rule explicitly cites "+token+"\n")
	replacement.Proposals = canonicalReferencesExtract(t, ctx, pool, replacement, suffix+"-replacement", []evidenceingestion.ExtractorProposalOutput{
		{ProposalLocalID: "replacement", StatementText: replacement.Intake.Spans[0].QuotedText, EvidenceRefs: []string{replacement.Intake.Spans[0].SpanID}},
	})
	basis := evidenceingestion.SupersessionLineageBasis{SourceSystem: "lab", SourceNamespace: "canonical-references-synthetic", ObjectType: "document", ObjectID: object, SlotKind: "field", SlotID: "rule"}
	current, err := evidenceingestion.AdmitPendingSupersession(ctx, pool, evidenceingestion.SupersessionAdmissionInput{ProposalOccurrenceID: replacement.Proposals[0].ProposalOccurrenceID,
		DecisionBy: "test-stub:supersession-origin", DecisionReason: "APPROVE STUB: real supersession materializer for independent references attribution.", Basis: basis, TargetNodeIDs: []string{old.CanonicalRef}})
	if err != nil {
		t.Fatal(err)
	}
	replacement.NodeIDs = []string{current.CanonicalRef}
	request := canonicalReferencesRequest(t, replacement, target, 0, 0, 0, 0, token, "source", evidencereferences.ResolverNativeSnapshotV1)
	return canonicalReferencesSupersessionFixture{References: canonicalReferencesFixture{From: replacement, To: target, Request: request}, Basis: basis, Current: current, ObjectID: object}
}

func canonicalReferencesAdvanceSupersession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture canonicalReferencesSupersessionFixture) {
	t.Helper()
	next := canonicalReferencesExternalVersion(t, ctx, pool, fixture.ObjectID, "v3", "[[ahe-anchor:replacement]] Later synthetic replacement; prior references retain their original endpoint.\n")
	next.Proposals = canonicalReferencesExtract(t, ctx, pool, next, fixture.ObjectID+"-later", []evidenceingestion.ExtractorProposalOutput{
		{ProposalLocalID: "later", StatementText: next.Intake.Spans[0].QuotedText, EvidenceRefs: []string{next.Intake.Spans[0].SpanID}},
	})
	result, err := evidenceingestion.AdmitPendingSupersession(ctx, pool, evidenceingestion.SupersessionAdmissionInput{ProposalOccurrenceID: next.Proposals[0].ProposalOccurrenceID,
		DecisionBy: "test-stub:supersession-origin", DecisionReason: "APPROVE STUB: advance native head without rewriting old reference authority.", Basis: fixture.Basis,
		TargetNodeIDs: []string{fixture.Current.CanonicalRef}, ExpectedRevision: fixture.Current.EventRevision, ExpectedHeadEventID: fixture.Current.AdmissionEventID})
	if err != nil || result.EventRevision != fixture.Current.EventRevision+1 {
		t.Fatalf("advance true supersession after reference observation: %v", err)
	}
}

func implementsSupersessionOriginProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, object, revision, statement string) evidenceingestion.IngestResult {
	t.Helper()
	request := "implements-origin:" + object + ":" + revision
	source, err := evidenceingestion.CaptureExternalSource(ctx, pool, evidenceingestion.ExternalSourceEnvelopeV1{
		SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: request,
		SourceSystem: "lab", SourceNamespace: "implements-supersession-origin", ObjectType: "document", ObjectID: object, Revision: revision,
		SourceLocation: "https://example.invalid/" + object, Title: "Synthetic refund contract", ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText,
		ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim, Content: statement + "\n", Coverage: evidenceingestion.ExternalSourceCoverageFullDocument,
		Limitations: []string{}, CollectorID: "synthetic-test-collector", ConnectorID: "synthetic-test-connector", ObservedAt: "2026-09-06T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, evidenceingestion.ExtractorOutputInput{
		RequestID: request + ":extract", SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
		ProducerSessionRef: "session:synthetic:" + object, ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "synthetic-fixture-extractor", Version: "v1"},
		Output: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "contract", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func implementsSupersessionOriginSetup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) implementsSupersessionOriginFixture {
	t.Helper()
	object := "implements-supersession-" + suffix
	fixture := implementsSupersessionOriginFixture{Basis: evidenceingestion.SupersessionLineageBasis{SourceSystem: "lab", SourceNamespace: "implements-supersession-origin", ObjectType: "document", ObjectID: object, SlotKind: "field", SlotID: "refund-predicate"}}
	old := implementsSupersessionOriginProposal(t, ctx, pool, object, "v1", "The refund predicate name is awaiting the reviewed revision.")
	initial, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: old.ProposalOccurrenceID, DecisionBy: "test-stub:source-approve", DecisionReason: "Synthetic ordinary source setup; no human approval claim."})
	if err != nil {
		t.Fatal(err)
	}
	excerpt := "refund.ValidateRefundWindow identifies the integer refund-window predicate."
	proposal := implementsSupersessionOriginProposal(t, ctx, pool, object, "v2", excerpt)
	fixture.Current, err = evidenceingestion.AdmitPendingSupersession(ctx, pool, evidenceingestion.SupersessionAdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy: "test-stub:supersession-approve", DecisionReason: "Synthetic reviewed replacement; no human approval claim.", Basis: fixture.Basis, TargetNodeIDs: []string{initial.CanonicalRef}})
	if err != nil {
		t.Fatal(err)
	}
	other := runtimeDerivedSource(t, ctx, pool, suffix+"-other", "The refund-window predicate contract here is only days <= 30; no time origin or nonnegative bound is specified.")
	depthOne := runtimeDerivedNode(t, ctx, pool, suffix+"-depth-one", []string{fixture.Current.CanonicalRef, other})
	root := runtimeDerivedNode(t, ctx, pool, suffix+"-recursive", []string{depthOne, other})
	code, fact := canonicalImplementsCreateCodeClaim(t, ctx, pool, suffix+"-origin-code")
	if fact.QualifiedName != "refund.ValidateRefundWindow" {
		t.Fatal("unexpected code fact")
	}
	mapping := evidenceimplements.ReviewMapping{ProposalSentence: "The reviewed derived specification maps to the refund.ValidateRefundWindow declaration.",
		Coverage: "Declaration-level traceability only; semantic correctness is an external synthetic reviewer assertion.", Limitations: []string{"Deterministic APPROVE STUB; not human authentication, execution proof, or source truth."},
		Witnesses: []evidenceimplements.Witness{{Kind: evidenceimplements.ReviewedBehavior, EndpointNodeIDs: []string{depthOne, code}, SourceTitle: "Synthetic refund contract", SourceLocation: "https://example.invalid/" + object, ExactExcerpt: excerpt, ExcerptHash: evidenceimplements.ExcerptHash(excerpt)}}}
	fixture.DepthOne = evidenceimplements.DerivedReviewRequest{SpecificationNodeID: depthOne, ImplementationNodeID: code,
		RuleStatement: "Combine the two AND parents: identify the named predicate and retain only the stated integer inequality.", Mapping: mapping}
	rules := make([]evidenceimplements.RecursiveDerivationRule, 0, 2)
	for _, node := range []string{depthOne, root} {
		var id string
		if err := pool.QueryRow(ctx, `SELECT derivation_id FROM canonical_derivations WHERE node_id=$1`, node).Scan(&id); err != nil {
			t.Fatal(err)
		}
		rules = append(rules, evidenceimplements.RecursiveDerivationRule{NodeID: node, DerivationID: id, RuleStatement: "Combine every declared AND parent without adding a time origin or a nonnegative bound."})
	}
	// Copy the witness before changing its endpoint set; the v1 request must
	// retain its own depth-one endpoint.
	mapping.Witnesses = append([]evidenceimplements.Witness(nil), mapping.Witnesses...)
	mapping.Witnesses[0].EndpointNodeIDs = []string{root, code}
	fixture.Recursive = evidenceimplements.RecursiveReviewRequest{SpecificationNodeID: root, ImplementationNodeID: code, Rules: rules, Mapping: mapping}
	return fixture
}

func implementsSupersessionOriginAdvance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture implementsSupersessionOriginFixture) {
	t.Helper()
	proposal := implementsSupersessionOriginProposal(t, ctx, pool, fixture.Basis.ObjectID, "v3", "refund.ValidateRefundWindow remains the named predicate; this later source revision adds a review note.")
	result, err := evidenceingestion.AdmitPendingSupersession(ctx, pool, evidenceingestion.SupersessionAdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy: "test-stub:supersession-approve", DecisionReason: "Synthetic later replacement advances native head without rewriting historical admission.",
		Basis: fixture.Basis, TargetNodeIDs: []string{fixture.Current.CanonicalRef}, ExpectedRevision: fixture.Current.EventRevision, ExpectedHeadEventID: fixture.Current.AdmissionEventID})
	if err != nil || result.EventRevision != fixture.Current.EventRevision+1 || result.AdmissionEventID == fixture.Current.AdmissionEventID {
		t.Fatalf("advance real supersession head: %+v %v", result, err)
	}
}
