//go:build integration

package mcpintegration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencereferences"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixtures below are synthetic documents authored with explicit AHE tokens.
// They never rewrite provider URLs and never claim to be company documents.
// Extraction and approval are deterministic stubs; capture/admission use PG.
type canonicalReferencesSource struct {
	Intake    evidenceingestion.SourceIntakeResult
	Raw       string
	Proposals []evidenceingestion.ProposalQueryResult
	NodeIDs   []string
	Approvals []evidenceingestion.ReviewedSourceClaimAdmissionInput
}

type canonicalReferencesFixture struct {
	From    canonicalReferencesSource
	To      canonicalReferencesSource
	Request evidencereferences.ReviewRequest
}

func canonicalReferencesCapture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix, raw string, external bool) canonicalReferencesSource {
	t.Helper()
	var intake evidenceingestion.SourceIntakeResult
	var err error
	if external {
		var captured evidenceingestion.ExternalSourceIntakeResult
		captured, err = evidenceingestion.CaptureExternalSource(ctx, pool, evidenceingestion.ExternalSourceEnvelopeV1{
			SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "references-capture-" + suffix,
			SourceSystem: "lab", SourceNamespace: "canonical-references-synthetic", ObjectType: "document", ObjectID: suffix, Revision: "v1",
			SourceLocation: "https://example.invalid/references/" + suffix, Title: "Synthetic explicit-reference document " + suffix,
			ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
			Content: raw, Coverage: evidenceingestion.ExternalSourceCoverageExactExcerpt, Limitations: []string{"Authored synthetic token document; no provider identity or semantic correctness proof."},
			CollectorID: "test-stub:references-collector", ConnectorID: "test-stub:references-connector", ObservedAt: "2026-09-06T00:00:00Z",
		})
		intake = captured.SourceIntakeResult
	} else {
		t.Fatal("manual reference fixture is outside the product profile")
	}
	if err != nil {
		t.Fatalf("capture explicit-reference fixture: %v", err)
	}
	return canonicalReferencesSource{Intake: intake, Raw: raw}
}

func canonicalReferencesExtract(t *testing.T, ctx context.Context, pool *pgxpool.Pool, source canonicalReferencesSource, suffix string, output []evidenceingestion.ExtractorProposalOutput) []evidenceingestion.ProposalQueryResult {
	t.Helper()
	result, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, evidenceingestion.ExtractorOutputInput{
		RequestID: "references-extract-" + suffix, SourceSnapshotID: source.Intake.SourceSnapshotID, ExtractionViewID: source.Intake.ExtractionViewID,
		ProducerSessionRef:  "session:references:synthetic:" + suffix,
		ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "references-deterministic-extractor-stub", Version: "v1"},
		Output:              evidenceingestion.FrozenExtractorOutput{Proposals: output},
	})
	if err != nil {
		t.Fatalf("submit explicit-reference extraction stub: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT proposal_occurrence_id FROM proposal_occurrences WHERE extraction_attempt_id=$1 ORDER BY proposal_occurrence_id`, result.ExtractionAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) != len(output) {
		t.Fatalf("complete extraction batch: ids=%d want=%d err=%v", len(ids), len(output), err)
	}
	byLocalID := make(map[string]evidenceingestion.ProposalQueryResult, len(ids))
	for _, id := range ids {
		proposal, err := evidenceingestion.TraceProposalProvenance(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		byLocalID[proposal.ProposalLocalID] = proposal
	}
	proposals := make([]evidenceingestion.ProposalQueryResult, 0, len(output))
	for _, expected := range output {
		proposal, ok := byLocalID[expected.ProposalLocalID]
		if !ok || proposal.StatementText != expected.StatementText || proposal.AdmissionOutcome != "pending" {
			t.Fatal("source fixture lost exact pending local-ID/statement mapping")
		}
		proposals = append(proposals, proposal)
	}
	return proposals
}

func canonicalReferencesAdmitSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, source canonicalReferencesSource, suffix string, spanIndices ...int) canonicalReferencesSource {
	t.Helper()
	output := make([]evidenceingestion.ExtractorProposalOutput, 0, len(spanIndices))
	for i, index := range spanIndices {
		span := source.Intake.Spans[index]
		output = append(output, evidenceingestion.ExtractorProposalOutput{ProposalLocalID: fmt.Sprintf("claim-%02d", i), StatementText: span.QuotedText, EvidenceRefs: []string{span.SpanID}})
	}
	source.Proposals = canonicalReferencesExtract(t, ctx, pool, source, suffix, output)
	source.NodeIDs = make([]string, 0, len(source.Proposals))
	for _, proposal := range source.Proposals {
		_, subject, err := relationSourceDisplay(ctx, pool, proposal.ExtractionAttemptID, proposal.ProposalOccurrenceID)
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := evidenceingestion.AdmitReviewedSourceClaim(ctx, pool, evidenceingestion.ReviewedSourceClaimAdmissionInput{
			ExtractionAttemptID: proposal.ExtractionAttemptID, ExpectedSubject: subject, DecisionBy: "test-stub:references-source-reviewer",
			DecisionReason: "APPROVE STUB: synthetic exact-reference source; not a human or semantic proof.",
		})
		if err != nil {
			t.Fatalf("admit reviewed fixture source: %v", err)
		}
		source.NodeIDs = append(source.NodeIDs, admitted.CanonicalRef)
		source.Approvals = append(source.Approvals, evidenceingestion.ReviewedSourceClaimAdmissionInput{ExtractionAttemptID: proposal.ExtractionAttemptID, ExpectedSubject: subject, DecisionBy: "test-stub:references-source-reviewer", DecisionReason: "APPROVE STUB: synthetic exact-reference source; not a human or semantic proof."})
	}
	return source
}

func canonicalReferencesSameSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string, external bool) canonicalReferencesFixture {
	t.Helper()
	raw := "[[ahe-anchor:alpha]] 甲規格明確引用乙 [[ahe-ref:#beta]]\n[[ahe-anchor:beta]] 乙規格明確引用甲 [[ahe-ref:#alpha]]\n"
	source := canonicalReferencesCapture(t, ctx, pool, suffix, raw, external)
	source = canonicalReferencesAdmitSource(t, ctx, pool, source, suffix, 0, 1)
	request := canonicalReferencesRequest(t, source, source, 0, 1, 0, 1, "[[ahe-ref:#beta]]", "beta", evidencereferences.ResolverSameSnapshotV1)
	return canonicalReferencesFixture{From: source, To: source, Request: request}
}

func canonicalReferencesCrossSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string, targetExternal bool) canonicalReferencesFixture {
	t.Helper()
	target := canonicalReferencesCapture(t, ctx, pool, suffix+"-target", "[[ahe-anchor:target]] 唯一且已固定的乙規格。\n", targetExternal)
	target = canonicalReferencesAdmitSource(t, ctx, pool, target, suffix+"-target", 0)
	token := "[[ahe-ref:" + target.Intake.SourceSnapshotID + "#target]]"
	// The new synthetic source is authored after the target was captured. This
	// does not retrofit a token into a captured external-provider document.
	source := canonicalReferencesCapture(t, ctx, pool, suffix+"-source", "[[ahe-anchor:source]] 甲引用固定乙規格 "+token+"\n", targetExternal)
	source = canonicalReferencesAdmitSource(t, ctx, pool, source, suffix+"-source", 0)
	request := canonicalReferencesRequest(t, source, target, 0, 0, 0, 0, token, "target", evidencereferences.ResolverNativeSnapshotV1)
	return canonicalReferencesFixture{From: source, To: target, Request: request}
}

func canonicalReferencesRequest(t *testing.T, source, target canonicalReferencesSource, fromNode, toNode, fromSpan, toSpan int, token, anchor, profile string) evidencereferences.ReviewRequest {
	t.Helper()
	reference := source.Intake.Spans[fromSpan]
	position := strings.Index(reference.QuotedText, token)
	if position < 0 || strings.Count(reference.QuotedText, token) != 1 {
		t.Fatal("fixture must contain exactly the supplied literal reference token")
	}
	span := target.Intake.Spans[toSpan]
	return evidencereferences.ReviewRequest{ResolverProfile: profile, FromNodeID: source.NodeIDs[fromNode], ToNodeID: target.NodeIDs[toNode],
		Reference: evidencereferences.ReferenceWitness{SourceSnapshotID: source.Intake.SourceSnapshotID, ExtractionViewID: source.Intake.ExtractionViewID,
			SpanID: reference.SpanID, StartByte: reference.StartByte + position, EndByte: reference.StartByte + position + len(token), Token: token, TokenHash: stdioContentHash([]byte(token))},
		Target: evidencereferences.TargetWitness{SourceSnapshotID: target.Intake.SourceSnapshotID, ExtractionViewID: target.Intake.ExtractionViewID,
			SpanID: span.SpanID, AnchorID: anchor, SpanHash: span.QuotedTextHash},
	}
}

func canonicalReferencesExtraClaims(t *testing.T, ctx context.Context, pool *pgxpool.Pool, target canonicalReferencesSource, suffix string, targetSpan, count int, admit bool) []evidenceingestion.ProposalQueryResult {
	t.Helper()
	output := make([]evidenceingestion.ExtractorProposalOutput, 0, count)
	for i := 0; i < count; i++ {
		output = append(output, evidenceingestion.ExtractorProposalOutput{ProposalLocalID: fmt.Sprintf("extra-%03d", i),
			StatementText: fmt.Sprintf("Synthetic overlapping claim %s-%03d; ambiguity fixture, not semantic validation.", suffix, i), EvidenceRefs: []string{target.Intake.Spans[targetSpan].SpanID}})
	}
	proposals := canonicalReferencesExtract(t, ctx, pool, target, suffix, output)
	if admit {
		for i := range proposals {
			result, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: proposals[i].ProposalOccurrenceID,
				DecisionBy: "test-stub:overlap-admission", DecisionReason: "APPROVE STUB: create an independently admitted overlapping claim for completeness testing."})
			if err != nil {
				t.Fatal(err)
			}
			proposals[i].CanonicalRef, proposals[i].AdmissionOutcome = result.CanonicalRef, "admitted"
		}
	}
	return proposals
}

func relationSourceDisplay(ctx context.Context, pool *pgxpool.Pool, attempt, occurrence string) (evidenceingestion.SourceClaimReviewDisplayArtifact, evidenceingestion.ExactDisplayedReviewSubject, error) {
	snap, err := evidenceingestion.LoadReviewableSourceClaimReviewSnapshot(ctx, pool, attempt, occurrence)
	if err != nil {
		return evidenceingestion.SourceClaimReviewDisplayArtifact{}, evidenceingestion.ExactDisplayedReviewSubject{}, err
	}
	return evidenceingestion.BuildSourceClaimReviewDisplayArtifact(snap)
}
