//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceimplements"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runtimeDerivedFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) (evidenceimplements.DerivedAdmissionInput, evidenceimplements.DerivedReview) {
	t.Helper()
	parents := []string{
		runtimeDerivedSource(t, ctx, pool, suffix+"-a", "refund.ValidateRefundWindow identifies the integer refund-window predicate."),
		runtimeDerivedSource(t, ctx, pool, suffix+"-b", "The refund-window predicate contract here is only days <= 30; no time origin or nonnegative bound is specified."),
	}
	root := runtimeDerivedNode(t, ctx, pool, suffix, parents)
	code, fact := canonicalImplementsCreateCodeClaim(t, ctx, pool, suffix)
	excerpt := "refund.ValidateRefundWindow identifies the integer refund-window predicate."
	request := evidenceimplements.DerivedReviewRequest{SpecificationNodeID: root, ImplementationNodeID: code,
		RuleStatement: "Combine the two AND parents: identify the named predicate and limit this mapping to the explicitly stated integer inequality.",
		Mapping: evidenceimplements.ReviewMapping{ProposalSentence: "The reviewed derived specification maps to the refund.ValidateRefundWindow declaration.",
			Coverage:    "Declaration-level traceability only; semantic correctness is an explicit synthetic reviewer assertion.",
			Limitations: []string{"Deterministic APPROVE STUB; not human authentication, execution proof, or source truth."},
			Witnesses: []evidenceimplements.Witness{{Kind: evidenceimplements.ReviewedBehavior, EndpointNodeIDs: []string{root, code},
				SourceTitle: "Synthetic refund contract", SourceLocation: "https://example.invalid/" + suffix + "-a", ExactExcerpt: excerpt, ExcerptHash: evidenceimplements.ExcerptHash(excerpt)}}}}
	if fact.QualifiedName != "refund.ValidateRefundWindow" {
		t.Fatal("unexpected native code fact")
	}
	review, err := evidenceimplements.LoadDerivedReview(ctx, pool, request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := runtimeDerivedReceipt(t, review, "test-stub:always-approve", "Synthetic writer verification only; no human reviewed this test fixture.")
	return evidenceimplements.DerivedAdmissionInput{RequestID: "derived-implements-" + suffix, Review: review.Request, Receipt: receipt, Decision: "approved", ProducerSessionRef: "session:test-stub:" + suffix}, review
}

func runtimeDerivedSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix, statement string) string {
	t.Helper()
	source, err := evidenceingestion.CaptureExternalSource(ctx, pool, evidenceingestion.ExternalSourceEnvelopeV1{
		SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "native-source-" + suffix,
		SourceSystem: "lab", SourceNamespace: "derived-implements-runtime", ObjectType: "document", ObjectID: suffix, Revision: "v1",
		SourceLocation: "https://example.invalid/" + suffix, Title: "Synthetic refund contract", ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText,
		ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim, Content: statement + "\n", Coverage: evidenceingestion.ExternalSourceCoverageFullDocument,
		Limitations: []string{}, CollectorID: "synthetic-test-collector", ConnectorID: "synthetic-test-connector", ObservedAt: "2026-09-06T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, evidenceingestion.ExtractorOutputInput{
		RequestID: "native-extract-" + suffix, SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
		ProducerSessionRef: "session:synthetic:" + suffix, ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "synthetic-fixture-extractor", Version: "v1"},
		Output: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim-1", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: submitted.ProposalOccurrenceID,
		DecisionBy: "test-stub:source-approve", DecisionReason: "Synthetic source endpoint setup; no human approval claim."})
	if err != nil {
		t.Fatal(err)
	}
	return admitted.CanonicalRef
}

func runtimeDerivedNode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string, parents []string) string {
	t.Helper()
	statement := "refund.ValidateRefundWindow is the integer refund-window predicate governed by the reviewed days <= 30 contract."
	submitted, err := evidenceingestion.IngestManualText(ctx, pool, evidenceingestion.ManualTextInput{SourceID: "native-derived-" + suffix, SourceVersion: "v1",
		Raw: []byte(statement + "\n"), RequestID: "native-derived-" + suffix, AttemptNumber: 1}, evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{
		ProposalLocalID: "derived-1", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: submitted.ProposalOccurrenceID,
		DecisionBy: "test-stub:derived-approve", DecisionReason: "Synthetic AND-parent derivation setup.", Derivation: &evidenceingestion.DerivationAdmissionInput{
			ParentNodeIDs: parents, Method: "reviewed-and-combination", Producer: "synthetic-test-extractor", TraceRef: "trace:" + suffix}})
	if err != nil {
		t.Fatal(err)
	}
	return admitted.CanonicalRef
}

func runtimeDerivedReceipt(t *testing.T, review evidenceimplements.DerivedReview, reviewer, reason string) evidenceimplements.DerivedReviewReceipt {
	t.Helper()
	receipt, err := evidenceimplements.NewDerivedReviewReceipt(review.Cut, review.Report, review.Basis, evidenceimplements.DerivedReviewInput{
		CandidateID: review.Subject.CandidateID, Mapping: review.Request.Mapping, Display: review.Display, Subject: review.Subject, ReviewerID: reviewer, DecisionReason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func runtimeDerivedCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) [4]int {
	t.Helper()
	var counts [4]int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_graph_nodes),(SELECT count(*) FROM canonical_graph_edges),
		(SELECT count(*) FROM admission_decisions),(SELECT count(*) FROM canonical_implements_admissions)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
		t.Fatal(err)
	}
	return counts
}

func runtimeDerivedCopy(t *testing.T, input evidenceimplements.DerivedAdmissionInput) evidenceimplements.DerivedAdmissionInput {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var copy evidenceimplements.DerivedAdmissionInput
	if err := json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func runtimeRecursiveFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) (evidenceimplements.RecursiveAdmissionInput, evidenceimplements.RecursiveReview) {
	t.Helper()
	v1, base := runtimeDerivedFixture(t, ctx, pool, suffix+"-base")
	parents := base.Basis.Ancestors.Derivations[0].Parents
	sibling := runtimeDerivedNode(t, ctx, pool, suffix+"-sibling", parents)
	root := runtimeDerivedNode(t, ctx, pool, suffix+"-root", []string{base.Basis.RootNodeID, sibling})
	rules := make([]evidenceimplements.RecursiveDerivationRule, 0, 3)
	for _, id := range []string{base.Basis.RootNodeID, sibling, root} {
		var derivationID string
		if err := pool.QueryRow(ctx, `SELECT derivation_id FROM canonical_derivations WHERE node_id=$1`, id).Scan(&derivationID); err != nil {
			t.Fatal(err)
		}
		rules = append(rules, evidenceimplements.RecursiveDerivationRule{NodeID: id, DerivationID: derivationID,
			RuleStatement: "Combine every stated AND parent without adding a time origin or nonnegative bound; shared sources are not independent corroboration."})
	}
	mapping := v1.Review.Mapping
	for i := range mapping.Witnesses {
		mapping.Witnesses[i].EndpointNodeIDs = []string{root, v1.Review.ImplementationNodeID}
	}
	request := evidenceimplements.RecursiveReviewRequest{SpecificationNodeID: root, ImplementationNodeID: v1.Review.ImplementationNodeID, Rules: rules, Mapping: mapping}
	review, err := evidenceimplements.LoadRecursiveReview(ctx, pool, request)
	if err != nil {
		t.Fatal(err)
	}
	return evidenceimplements.RecursiveAdmissionInput{RequestID: "recursive-implements-" + suffix, Review: review.Request,
		Receipt:  runtimeRecursiveReceipt(t, review, "test-stub:always-approve", "Synthetic recursive writer verification; no human review claim."),
		Decision: "approved", ProducerSessionRef: "session:test-stub:" + suffix}, review
}

func runtimeRecursiveReceipt(t *testing.T, review evidenceimplements.RecursiveReview, reviewer, reason string) evidenceimplements.DerivedReviewReceipt {
	t.Helper()
	r, err := evidenceimplements.NewRecursiveReviewReceipt(review.Cut, review.Report, review.Basis, evidenceimplements.DerivedReviewInput{
		CandidateID: review.Subject.CandidateID, Mapping: review.Request.Mapping, Display: review.Display, Subject: review.Subject, ReviewerID: reviewer, DecisionReason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func runtimeRecursiveCopy(t *testing.T, input evidenceimplements.RecursiveAdmissionInput) evidenceimplements.RecursiveAdmissionInput {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var copied evidenceimplements.RecursiveAdmissionInput
	if err := json.Unmarshal(data, &copied); err != nil {
		t.Fatal(err)
	}
	return copied
}

func canonicalImplementsCreateCodeClaim(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
) (string, evidenceingestion.ResolvedCodeFact) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	canonicalImplementsWriteFile(
		t, filepath.Join(root, "go.mod"),
		[]byte("module example.com/refund\n\ngo 1.27\n"),
	)
	canonicalImplementsWriteFile(
		t, filepath.Join(root, "refund.go"),
		[]byte("package refund\n\nfunc ValidateRefundWindow(days int) bool {\n\treturn days <= 30\n}\n"),
	)
	canonicalImplementsGit(t, root, "init", "--quiet")
	canonicalImplementsGit(t, root, "config", "user.name", "AHE Lab")
	canonicalImplementsGit(
		t, root, "config", "user.email", "ahe-lab@example.invalid",
	)
	canonicalImplementsGit(t, root, "add", ".")
	canonicalImplementsGit(t, root, "commit", "--quiet", "-m", "fixture")
	commitSHA := strings.TrimSpace(
		canonicalImplementsGit(t, root, "rev-parse", "HEAD"),
	)

	snapshot, err := evidenceingestion.CaptureGitRepositorySnapshot(
		ctx,
		pool,
		evidenceingestion.GitRepositorySnapshotConfig{
			WorkspaceRoot: root,
			RepoID:        "canonical-implements-" + suffix,
			CommitSHA:     commitSHA,
			RequestID:     "canonical-implements-code-snapshot-" + suffix,
		},
	)
	if err != nil {
		t.Fatalf("capture repository snapshot: %v", err)
	}
	result, err := evidenceingestion.RunRepositoryGoParserExtractor(
		ctx,
		pool,
		evidenceingestion.RepositoryGoParserRequest{
			RequestID:            "canonical-implements-go-parser-" + suffix,
			RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		},
	)
	if err != nil {
		t.Fatalf("run repository Go parser: %v", err)
	}
	for _, occurrenceID := range result.ProposalOccurrenceIDs {
		proposal, err := evidenceingestion.GetProposalByOccurrenceID(
			ctx, pool, occurrenceID,
		)
		if err != nil {
			t.Fatalf("load code proposal: %v", err)
		}
		if proposal.CodeFact == nil ||
			proposal.CodeFact.QualifiedName != "refund.ValidateRefundWindow" {
			continue
		}
		admitted, err := evidenceingestion.AdmitPendingProposal(
			ctx,
			pool,
			evidenceingestion.AdmissionInput{
				ProposalOccurrenceID: occurrenceID,
				DecisionBy:           "canonical-implements-lab-fixture",
				DecisionReason: "deterministically extracted code endpoint " +
					"admitted for the isolated Lab cut",
			},
		)
		if err != nil {
			t.Fatalf("admit code claim: %v", err)
		}
		return admitted.CanonicalRef, *proposal.CodeFact
	}
	t.Fatal("Go parser did not produce refund.ValidateRefundWindow")
	return "", evidenceingestion.ResolvedCodeFact{}
}

func canonicalImplementsWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func canonicalImplementsGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
