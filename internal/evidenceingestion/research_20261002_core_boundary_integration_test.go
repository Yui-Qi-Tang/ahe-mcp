//go:build integration && labreplay

package evidenceingestion

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The semantic evaluator lives outside Core. This test consumes its Lab report.
type corePilotReport struct {
	ID              string `json:"id"`
	CaseID          string `json:"case_id"`
	Source          string `json:"source"`
	Statement       string `json:"statement"`
	SourceSHA256    string `json:"source_sha256"`
	CandidateSHA256 string `json:"candidate_sha256"`
	Contract        string `json:"contract"`
	FidelityPass    bool   `json:"fidelity_pass"`
}

type corePilotBoundReport struct {
	Report  corePilotReport
	Subject ExactDisplayedReviewSubject
}

// Lab-only controller policy: a report has no native admission authority.
func corePilotBind(report corePilotReport, source BoundedSourceViewResult, proposal ProposalQueryResult, subject ExactDisplayedReviewSubject) (corePilotBoundReport, error) {
	if source.Status != BoundedSourceViewStatusAvailable || source.Input == nil {
		return corePilotBoundReport{}, errors.New("source unavailable")
	}
	if report.Contract != "supplied-semantics/bool-three-state-and-five-fields/v1" ||
		report.SourceSHA256 != hashHex([]byte(source.Input.RenderedText)) ||
		report.CandidateSHA256 != hashHex([]byte(proposal.StatementText)) ||
		source.Input.SourceSnapshotID != proposal.SourceSnapshotID ||
		source.Input.ExtractionViewID != proposal.ExtractionViewID ||
		subject.ReviewSubject.ProposalOccurrenceID != proposal.ProposalOccurrenceID {
		return corePilotBoundReport{}, errors.New("external report material mismatch")
	}
	return corePilotBoundReport{Report: report, Subject: subject}, nil
}

func corePilotWrite(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResearch20261002CoreBoundary(t *testing.T) {
	root := os.Getenv("AHE_CORE_PILOT_DIR")
	if root == "" {
		t.Skip("explicit Core pilot input/output directory required")
	}
	b, err := os.ReadFile(filepath.Join(root, "external-reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reports []corePilotReport
	if err := json.Unmarshal(b, &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 17 {
		t.Fatal("pilot requires 16 primary candidates and one shared-mapping stress control")
	}
	for _, arm := range []string{"approval_control", "external_checked"} {
		t.Run(arm, func(t *testing.T) {
			ctx, pool, _ := fullLabReplayPool(t)
			var previousPass *corePilotBoundReport
			for _, report := range reports {
				t.Run(report.ID, func(t *testing.T) {
					before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
					source, err := CaptureExternalSource(ctx, pool, ExternalSourceEnvelopeV1{
						SchemaVersion: ExternalSourceEnvelopeSchemaV1, RequestID: "pilot-source-" + report.ID,
						SourceSystem: "ahe-core-lab", SourceNamespace: "paper-boundary-v1/" + arm,
						ObjectType: "synthetic-policy", ObjectID: report.ID, Revision: "v1",
						SourceLocation:  "https://example.invalid/core-pilot/" + report.ID,
						Title:           "Codex-authored source; no independent gold",
						ContentFormat:   ExternalSourceContentFormatMarkdown,
						ContentFidelity: ExternalSourceContentFidelityVerbatim,
						Content:         report.Source, Coverage: ExternalSourceCoverageFullDocument,
						CollectorID: "main-codex-simulation", ConnectorID: "lab-fixture",
						ObservedAt: "2026-10-02T00:00:00Z",
					})
					if err != nil {
						t.Fatalf("capture: %v", err)
					}
					refs := make([]string, 0, len(source.Spans))
					for _, span := range source.Spans {
						refs = append(refs, span.SpanID)
					}
					if len(refs) == 0 {
						t.Fatal("source has no spans")
					}
					submission, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
						RequestID: "pilot-extraction-" + report.ID, SourceSnapshotID: source.SourceSnapshotID,
						ExtractionViewID: source.ExtractionViewID, ProducerSessionRef: "codex-simulation:" + arm,
						ExtractorDefinition: ExtractorDefinitionInput{Name: "external-codex-simulation", Version: "v1", Config: map[string]string{"mode": "authored-fixture"}},
						Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
							ProposalLocalID: report.ID, StatementText: report.Statement, EvidenceRefs: refs,
						}}},
					})
					if err != nil {
						t.Fatalf("submit: %v", err)
					}
					proposal, err := GetProposalByOccurrenceID(ctx, pool, submission.ProposalOccurrenceID)
					if err != nil {
						t.Fatal(err)
					}
					if proposal.AdmissionOutcome != admissionOutcomePending || proposal.StatementText != report.Statement ||
						proposal.CanonicalRef != "" || reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
						t.Fatal("submission changed canonical authority or candidate text")
					}
					view, err := LoadBoundedSourceView(ctx, pool, source.SourceSnapshotID, source.ExtractionViewID)
					if err != nil || view.Input == nil || view.Input.RenderedText != report.Source {
						t.Fatalf("full native source readback: %v", err)
					}
					display, subject, err := LoadSourceClaimReviewDisplayArtifact(ctx, pool, submission.ExtractionAttemptID, submission.ProposalOccurrenceID)
					if err != nil {
						t.Fatal(err)
					}
					bound, err := corePilotBind(report, view, proposal, subject)
					if err != nil {
						t.Fatal(err)
					}
					staleChecked := previousPass != nil
					if previousPass != nil {
						if _, err := corePilotBind(previousPass.Report, view, proposal, subject); err == nil {
							t.Fatal("old PASS report rebound to different native source/candidate")
						}
					}
					if report.FidelityPass {
						previousPass = &bound
					}
					for _, auxiliary := range []string{"none", "status: PASS", "status: FAIL"} {
						// Deliberately absent from both native read and writer inputs.
						got, gotSubject, err := LoadSourceClaimReviewDisplayArtifact(ctx, pool, submission.ExtractionAttemptID, submission.ProposalOccurrenceID)
						if err != nil || !reflect.DeepEqual(got, display) || gotSubject != subject {
							t.Fatalf("excluded auxiliary %q changed material: %v", auxiliary, err)
						}
					}
					input := ReviewedSourceClaimAdmissionInput{
						ExtractionAttemptID: submission.ExtractionAttemptID, ExpectedSubject: bound.Subject,
						DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic Core boundary " + arm,
					}
					wrong := input
					wrong.ExpectedSubject.ReviewDisplayArtifactID = differentReviewStableID(subject.ReviewDisplayArtifactID)
					_, wrongErr := AdmitReviewedSourceClaim(ctx, pool, wrong)
					if wrongErr == nil || reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
						t.Fatal("wrong display admitted or changed authority")
					}
					var admitted *AdmissionResult
					if arm == "approval_control" || bound.Report.FidelityPass {
						result, err := AdmitReviewedSourceClaim(ctx, pool, input)
						if err != nil {
							t.Fatalf("own fresh exact-subject admission: %v", err)
						}
						if result.Replayed || result.CanonicalRef == "" || result.AdmissionOutcome != admissionOutcomeAdmitted {
							t.Fatal("unexpected admission result")
						}
						assertPersistedSourceClaimReviewBinding(t, ctx, pool, result, input)
						canonical, err := GetCanonicalEvidenceByID(ctx, pool, result.CanonicalRef)
						if err != nil || canonical.OriginProposal.StatementText != report.Statement {
							t.Fatalf("canonical readback: %v", err)
						}
						counts := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
						replay, err := AdmitReviewedSourceClaim(ctx, pool, input)
						if err != nil || !replay.Replayed || replay.AdmissionDecisionID != result.AdmissionDecisionID {
							t.Fatalf("exact replay: %v", err)
						}
						changed := input
						changed.DecisionReason += " changed"
						_, err = AdmitReviewedSourceClaim(ctx, pool, changed)
						assertKind(t, err, ErrorAdmissionReplayConflict)
						if counts != reviewedSourceClaimAuthorityCounts(t, ctx, pool) {
							t.Fatal("replay or conflict wrote authority")
						}
						admitted = &result
					} else if reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
						t.Fatal("external abstention changed authority")
					}
					corePilotWrite(t, filepath.Join(root, "run", arm+"-"+report.ID+".json"), map[string]any{
						"arm": arm, "id": report.ID, "bound_report": bound, "submission": submission,
						"source_view": view, "proposal_before": proposal, "exact_display": display,
						"wrong_display_error": wrongErr.Error(), "stale_pass_rejected": staleChecked,
						"auxiliary_variants_excluded": 3, "authority_before": before,
						"authority_after": reviewedSourceClaimAuthorityCounts(t, ctx, pool), "admitted": admitted,
					})
				})
			}
			corePilotWrite(t, filepath.Join(root, "run", arm+"-summary.json"), reviewedSourceClaimAuthorityCounts(t, ctx, pool))
		})
	}
}
