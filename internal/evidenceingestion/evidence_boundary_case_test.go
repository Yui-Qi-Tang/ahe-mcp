package evidenceingestion

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestEvidenceBoundaryRefundCase exports a synthetic paired case for the
// opt-in local-model pilot. It creates no database records or human approvals.
func TestEvidenceBoundaryRefundCase(t *testing.T) {
	const code = `package refund

func Eligible(ageDays int) bool { return ageDays <= 7 }
func HandleRefund(ageDays int) bool { return Eligible(ageDays) }
`
	old := refundCaseReview(t, "revision-1", "Refund requests are eligible through day 7.")
	changed := refundCaseReview(t, "revision-2", "Refund requests are eligible through day 30.")
	graph := refundCaseCalls(t, code)
	if !reflect.DeepEqual(graph, []string{"HandleRefund -> Eligible"}) {
		t.Fatalf("unexpected parsed fixture graph: %v", graph)
	}
	conditions := []map[string]any{}
	for _, test := range []struct {
		name    string
		current reviewableIngestionTestArtifacts
		code    string
		want    ErrorKind
	}{
		{"unchanged", old, code, ""},
		{"changed", changed, strings.Replace(code, "<= 7", "<= 30", 1), ErrorReviewContractConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed := refundCaseCalls(t, test.code)
			if !reflect.DeepEqual(parsed, graph) {
				t.Fatal("call graph changed; the paired structural control is invalid")
			}
			err := ValidateExactSourceClaimReviewSubject(test.current.receipt, test.current.manifest,
				test.current.current, old.reviewPackage, old.subject)
			outcome := "exact_match"
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertKind(t, err, test.want)
				var domainErr *DomainError
				if !errors.As(err, &domainErr) {
					t.Fatal("expected typed domain error")
				}
				outcome = string(domainErr.Kind)
			}
			// Check that the changed case has a valid new subject; rejection must
			// result from reusing the old subject, not an invalid replacement.
			if err := ValidateExactSourceClaimReviewSubject(test.current.receipt, test.current.manifest,
				test.current.current, test.current.reviewPackage, test.current.subject); err != nil {
				t.Fatalf("current subject is invalid: %v", err)
			}
			conditions = append(conditions, map[string]any{
				"id": test.name, "code_before": code, "code_proposed": test.code,
				"reviewed": refundCaseFacts(old), "current": refundCaseFacts(test.current),
				"parsed_calls_before": graph, "parsed_calls_proposed": parsed,
				"ahe_domain_result": map[string]any{
					"function": "ValidateExactSourceClaimReviewSubject", "result": outcome,
					"storage": "in-memory synthetic fixtures", "admission_writer_called": false,
				},
			})
		})
	}
	data, err := json.Marshal(map[string]any{"schema": "refund-review-case/v1", "conditions": conditions})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("EVIDENCE_CASE_V1=" + string(data))
}

func refundCaseReview(t *testing.T, revision, statement string) reviewableIngestionTestArtifacts {
	t.Helper()
	input := validExternalSourceEnvelope("synthetic-refund-" + revision)
	input.Revision, input.Content = revision, statement+"\n"
	input.Title = "Synthetic refund eligibility rule"
	prepared, err := prepareExternalSource(input)
	if err != nil {
		t.Fatal(err)
	}
	source, err := buildManualSourceContext(prepared.manual)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := buildAttemptContextFromSourceWithDefinitionAndSession(source, input.RequestID, 1,
		ExtractorDefinitionInput{Name: "synthetic-refund-case", Version: "v1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := materializeReviewableBatch(attempt, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: "refund-eligibility", StatementText: statement, EvidenceRefs: []string{"span:S1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	batch.ExtractionAttempt.Status, batch.ExtractionAttempt.OutputHash = attemptStatusSucceeded, batch.FixtureOutputHash
	manifest := mustReviewableIngestionManifest(t, batch)
	receipt := mustReviewableIngestionReceipt(t, batch, manifest)
	current := reviewableIngestionTestQuery(batch, 0)
	review, err := BuildSourceClaimReviewPackage(receipt, manifest, current)
	if err != nil {
		t.Fatal(err)
	}
	return reviewableIngestionTestArtifacts{batch: batch, manifest: manifest, receipt: receipt, current: current, reviewPackage: review,
		subject: ExactReviewSubject{SubmissionReceiptID: receipt.ID, ProposalManifestID: manifest.ID,
			ProposalOccurrenceID: current.ProposalOccurrenceID, ProposalBasisID: review.ProposalBasis.ID, ReviewPackageID: review.ID}}
}

func refundCaseFacts(a reviewableIngestionTestArtifacts) map[string]any {
	return map[string]any{
		"claim": a.current.StatementText, "source_revision": a.current.SourceVersion,
		"source_snapshot_id": a.current.SourceSnapshotID, "source_hash": a.current.RawContentHash,
		"exact_quote": a.current.SourceRefs[0].QuotedText, "coverage": a.reviewPackage.ProposalBasis.SourceCoverage,
		"proposal_state": a.current.AdmissionOutcome, "canonical_reference": a.current.CanonicalRef,
		"exact_review_subject": a.subject,
	}
}

// refundCaseCalls extracts only direct identifier calls in this tiny Go fixture.
// It is a reference AST projection, not a substitute for a named code-graph tool.
func refundCaseCalls(t *testing.T, source string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "refund.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, decl := range file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if target, ok := call.Fun.(*ast.Ident); ok {
					calls = append(calls, function.Name.Name+" -> "+target.Name)
				}
			}
			return true
		})
	}
	slices.Sort(calls)
	return calls
}
