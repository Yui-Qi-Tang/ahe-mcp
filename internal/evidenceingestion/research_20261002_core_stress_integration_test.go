//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type coreStressMaterial struct {
	Source     ExternalSourceIntakeResult
	Submission IngestResult
	View       BoundedSourceViewResult
	Proposal   ProposalQueryResult
	Display    SourceClaimReviewDisplayArtifact
	Subject    ExactDisplayedReviewSubject
}

func coreStressEnvelope(id, content string) ExternalSourceEnvelopeV1 {
	return ExternalSourceEnvelopeV1{
		SchemaVersion: ExternalSourceEnvelopeSchemaV1, RequestID: "stress-source-" + id,
		SourceSystem: "ahe-core-stress", SourceNamespace: "test/tenant-a", ObjectType: "policy",
		ObjectID: id, Revision: "v1", SourceLocation: "https://example.invalid/policy/" + id,
		Title: "Main-Codex authored stress source", ContentFormat: ExternalSourceContentFormatMarkdown,
		ContentFidelity: ExternalSourceContentFidelityVerbatim, Content: content,
		Coverage: ExternalSourceCoverageFullDocument, CollectorID: "collector-a", ConnectorID: "fixture",
		ObservedAt: "2026-10-02T05:00:00Z",
	}
}

func coreStressCapture(ctx context.Context, pool *pgxpool.Pool, env ExternalSourceEnvelopeV1, statement, request string) (coreStressMaterial, error) {
	var m coreStressMaterial
	var err error
	m.Source, err = CaptureExternalSource(ctx, pool, env)
	if err != nil {
		return m, fmt.Errorf("capture: %w", err)
	}
	m.View, err = LoadBoundedSourceView(ctx, pool, m.Source.SourceSnapshotID, m.Source.ExtractionViewID)
	if err != nil {
		return m, fmt.Errorf("source view: %w", err)
	}
	if m.View.Input == nil {
		return m, fmt.Errorf("source view withheld: %s/%s", m.View.Status, m.View.LimitReason)
	}
	refs := make([]string, 0, len(m.Source.Spans))
	for _, span := range m.Source.Spans {
		refs = append(refs, span.SpanID)
	}
	m.Submission, err = SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID: "stress-output-" + request, SourceSnapshotID: m.Source.SourceSnapshotID,
		ExtractionViewID: m.Source.ExtractionViewID, ProducerSessionRef: "main-codex-stress",
		ExtractorDefinition: ExtractorDefinitionInput{Name: "codex-stress-simulation", Version: "v2", Config: map[string]string{"mode": "supplied-representations", "fixture_attempt": request}},
		Output:              FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "candidate", StatementText: statement, EvidenceRefs: refs}}},
	})
	if err != nil {
		return m, fmt.Errorf("submit: %w", err)
	}
	m.Proposal, err = GetProposalByOccurrenceID(ctx, pool, m.Submission.ProposalOccurrenceID)
	if err != nil {
		return m, fmt.Errorf("proposal: %w", err)
	}
	m.Display, m.Subject, err = LoadSourceClaimReviewDisplayArtifact(ctx, pool, m.Submission.ExtractionAttemptID, m.Submission.ProposalOccurrenceID)
	if err != nil {
		return m, fmt.Errorf("display: %w", err)
	}
	return m, nil
}

func coreStressApproval(m coreStressMaterial) ReviewedSourceClaimAdmissionInput {
	return ReviewedSourceClaimAdmissionInput{ExtractionAttemptID: m.Submission.ExtractionAttemptID,
		ExpectedSubject: m.Subject, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "core stress v2 synthetic approval"}
}

func TestResearch20261002CoreStress(t *testing.T) {
	root := os.Getenv("AHE_CORE_STRESS_DIR")
	if root == "" {
		t.Skip("explicit stress bundle required")
	}
	b, err := os.ReadFile(filepath.Join(root, "external-reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reports []corePilotReport
	if err := json.Unmarshal(b, &reports); err != nil || len(reports) != 24 {
		t.Fatalf("24 reports required: %v", err)
	}
	for _, arm := range []string{"approval_control", "external_checked"} {
		t.Run(arm, func(t *testing.T) {
			ctx, pool, _ := fullLabReplayPool(t)
			for _, report := range reports {
				t.Run(report.ID, func(t *testing.T) {
					before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
					m, err := coreStressCapture(ctx, pool, coreStressEnvelope(report.ID, report.Source), report.Statement, report.ID)
					if err != nil {
						t.Fatal(err)
					}
					if reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before || m.Proposal.AdmissionOutcome != admissionOutcomePending {
						t.Fatal("submission changed authority")
					}
					bound, err := corePilotBind(report, m.View, m.Proposal, m.Subject)
					if err != nil {
						t.Fatal(err)
					}
					input := coreStressApproval(m)
					wrong := input
					wrong.ExpectedSubject.ReviewDisplayArtifactID = differentReviewStableID(m.Subject.ReviewDisplayArtifactID)
					_, wrongErr := AdmitReviewedSourceClaim(ctx, pool, wrong)
					if wrongErr == nil || reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
						t.Fatal("wrong display admitted or wrote authority")
					}
					var admitted *AdmissionResult
					if arm == "approval_control" || bound.Report.FidelityPass {
						result, err := AdmitReviewedSourceClaim(ctx, pool, input)
						if err != nil {
							t.Fatalf("own exact-subject admission: %v", err)
						}
						assertPersistedSourceClaimReviewBinding(t, ctx, pool, result, input)
						canonical, err := GetCanonicalEvidenceByID(ctx, pool, result.CanonicalRef)
						if err != nil || canonical.OriginProposal.StatementText != report.Statement {
							t.Fatalf("canonical readback: %v", err)
						}
						admitted = &result
					}
					corePilotWrite(t, filepath.Join(root, "run", arm+"-"+report.ID+".json"), map[string]any{
						"id": report.ID, "arm": arm, "material": m, "bound_report": bound,
						"before": before, "after": reviewedSourceClaimAuthorityCounts(t, ctx, pool),
						"wrong_display_error": wrongErr.Error(), "admitted": admitted,
					})
				})
			}
		})
	}
}

func TestResearch20261002ReportBindingStress(t *testing.T) {
	root := os.Getenv("AHE_CORE_STRESS_DIR")
	if root == "" {
		t.Skip("explicit stress bundle required")
	}
	names := []string{"namespace", "object", "revision", "coverage", "location", "collector",
		"source_bytes", "candidate_bytes", "report_contract", "mixed_native_view", "unavailable_view", "native_over_budget"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctx, pool, _ := fullLabReplayPool(t)
			text := "當已核准且不在凍結期間時，可以發布。"
			env := coreStressEnvelope("binding-base", text)
			base, err := coreStressCapture(ctx, pool, env, text, "base")
			if err != nil {
				t.Fatal(err)
			}
			report := corePilotReport{ID: "base-report", Source: text, Statement: text,
				SourceSHA256: hashHex([]byte(base.View.Input.RenderedText)), CandidateSHA256: hashHex([]byte(base.Proposal.StatementText)),
				Contract: "supplied-semantics/bool-three-state-and-five-fields/v1", FidelityPass: true}
			old, err := corePilotBind(report, base.View, base.Proposal, base.Subject)
			if err != nil {
				t.Fatal(err)
			}
			before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
			env.RequestID = "stress-source-target"
			statement := text
			kind := "native"
			makeTarget := true
			switch name {
			case "namespace":
				env.SourceNamespace = "test/tenant-b"
			case "object":
				env.ObjectID = "binding-other"
			case "revision":
				env.Revision = "v2"
			case "coverage":
				env.Coverage = ExternalSourceCoverageTruncatedDocument
				env.Limitations = []string{"Only this excerpt was supplied."}
			case "location":
				env.SourceLocation = "https://example.invalid/other-owner"
			case "collector":
				env.CollectorID = "collector-b"
			case "source_bytes", "mixed_native_view":
				env.Revision = "v2"
				env.Content += " 附註：必須另外通知值班人員。"
			case "candidate_bytes":
				statement = "當已核准時，可以發布。"
			case "report_contract":
				report.Contract = "other-contract"
				kind, makeTarget = "assembly", false
			case "unavailable_view":
				kind, makeTarget = "assembly", false
			case "native_over_budget":
				env.ObjectID = "over-budget"
				env.Content = strings.Repeat("x", 8193)
			}
			target := base
			stageError := ""
			if makeTarget {
				target, err = coreStressCapture(ctx, pool, env, statement, "target")
				if err != nil {
					stageError = err.Error()
				}
			}
			view := target.View
			if name == "mixed_native_view" {
				view, kind = base.View, "assembly_of_native_views"
			}
			if name == "unavailable_view" {
				view.Input = nil
			}
			binderEvaluated := stageError == "" || name == "native_over_budget"
			bindError := ""
			if binderEvaluated {
				_, err := corePilotBind(report, view, target.Proposal, target.Subject)
				if err != nil {
					bindError = err.Error()
				}
			}
			oldSubjectEvaluated := makeTarget && target.Submission.ExtractionAttemptID != "" && target.Submission.ExtractionAttemptID != base.Submission.ExtractionAttemptID
			oldSubjectError := ""
			if oldSubjectEvaluated {
				input := coreStressApproval(target)
				input.ExpectedSubject = old.Subject
				_, err := AdmitReviewedSourceClaim(ctx, pool, input)
				if err == nil {
					t.Fatal("old native subject accepted for new attempt")
				}
				oldSubjectError = err.Error()
			}
			if reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
				t.Fatal("binding probe changed authority")
			}
			corePilotWrite(t, filepath.Join(root, "run", "binding-"+name+".json"), map[string]any{
				"id": name, "kind": kind, "base": base, "target": target, "old_report": old,
				"intake_or_materialization_error": stageError, "binder_evaluated": binderEvaluated,
				"binder_error": bindError, "old_subject_evaluated": oldSubjectEvaluated,
				"old_subject_error": oldSubjectError, "before": before,
				"after": reviewedSourceClaimAuthorityCounts(t, ctx, pool),
			})
		})
	}
}
