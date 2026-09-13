package ahemcp

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func briefSubmissionFixture(t *testing.T, body string, first, last int) BriefSubmission {
	t.Helper()
	report, err := sourcepilot.NewBriefReport(sourcepilot.BriefSource{Version: sourcepilot.BriefSourceVersion, SourceKind: "public_event",
		SourceID: "synthetic-brief-source", SourceRevision: "fixture-v1", SourceURL: "https://example.invalid/brief",
		ObservedAt: "2026-09-11T00:00:00Z", Coverage: "exact_excerpt", Limitations: []string{"Synthetic excerpt; no production claim."}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	report.Stage, report.RawText, report.Text = "complete", "Synthetic reading overview.", "Synthetic reading overview."
	document, err := labstatus.RestoreDocument("/synthetic/source.json", body)
	if err != nil {
		t.Fatal(err)
	}
	quote, ok := document.ExactQuote(first, last)
	if !ok {
		t.Fatal("fixture source range unavailable")
	}
	input, err := NewBriefSubmission(report, "/synthetic/source.json", "test-model", "The selected source reports a bounded incident.", labstatus.Citation{StartLine: first, EndLine: last, ExactQuote: quote})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestBriefSubmissionPreservesOriginalAndDoesNotAssertSupport(t *testing.T) {
	body := "Context retained.\r\n臺灣來源🙂 reports partial impact.\r\nNo complete outage is established.\r\n"
	input := briefSubmissionFixture(t, body, 2, 3)
	if input.Report.Source.Body != body || input.Report.AuthorityEffect != "none" || input.Report.HumanReview != "not_reviewed" || input.Citation.ExactQuote != "臺灣來源🙂 reports partial impact.\nNo complete outage is established." {
		t.Fatal("source, quote or authority boundary changed")
	}
	// Exact quotation is checkable; semantic entailment is deliberately left to
	// the subsequent review. The adapter does not pretend to prove this claim.
	selected, err := NewBriefSubmission(input.Report, input.SourcePath, input.Model, "An unsupported stronger candidate awaiting review.", input.Citation)
	if err != nil || selected.Statement == selected.Report.Text {
		t.Fatal("selection was replaced or falsely constrained to model text")
	}
	input.Report.Source.Limitations[0] = "Changed caller-owned slice."
	if selected.Report.Source.Limitations[0] == input.Report.Source.Limitations[0] || ValidateBriefSubmission(selected) != nil {
		t.Fatal("constructor retained mutable caller-owned data")
	}
}

func TestBriefExcerptProvenanceSurvivesPendingSubmission(t *testing.T) {
	parent := sourcepilot.BriefSource{Version: sourcepilot.BriefSourceVersion, SourceKind: "news", SourceID: "synthetic-parent", SourceRevision: "fixture-v1",
		SourceURL: "https://example.invalid/parent", ObservedAt: "2026-09-13T00:00:00Z", Coverage: "full_document", Limitations: []string{},
		Body: strings.Repeat("Original parent context.\n", 100) + "The named service remains delayed.\nCause unknown."}
	start := strings.Index(parent.Body, "The named service")
	source, err := sourcepilot.SelectBriefExcerpt(parent, start, len(parent.Body), "Read the service status and unknown cause.")
	if err != nil {
		t.Fatal(err)
	}
	report, err := sourcepilot.NewBriefReport(source)
	if err != nil {
		t.Fatal(err)
	}
	report.Stage, report.RawText, report.Text = "complete", "Service remains delayed; cause unknown.", "Service remains delayed; cause unknown."
	input, err := NewBriefSubmission(report, "/synthetic/excerpt.json", "test-model", "The named service remains delayed.", labstatus.Citation{StartLine: 1, EndLine: 1, ExactQuote: "The named service remains delayed."})
	if err != nil {
		t.Fatal(err)
	}
	launcher, trace := handoffLauncher(t, "batch_pending")
	if _, err := SubmitBrief(t.Context(), launcher, input); err != nil {
		t.Fatal(err)
	}
	calls := readHandoffTrace(t, trace)
	var sent sourceRequest
	if json.Unmarshal(calls[3].Arguments, &sent) != nil || sent.SourceID != briefSourceID(input) || sent.RawText != source.Body || !reflect.DeepEqual(sent.OriginMetadata, briefOrigin(input)) {
		t.Fatal("pending source lost exact excerpt or provenance")
	}
	var recorded sourcepilot.BriefExcerpt
	if json.Unmarshal([]byte(sent.OriginMetadata["declared_excerpt_json"]), &recorded) != nil || recorded != *source.Excerpt {
		t.Fatal("pending origin lost parent source identity, body digest, range or reason")
	}
	if _, ok := sent.OriginMetadata["provider_verified"]; ok {
		t.Fatal("provenance pretended to verify provider")
	}
	beforeOrigin := digest(briefOrigin(input))
	beforeDefinition := digest(briefDefinition(input))
	input.Report.Source.Excerpt.SelectionReason = "Changed reason."
	if ValidateBriefSubmission(input) == nil || digest(briefOrigin(input)) == beforeOrigin || digest(briefDefinition(input)) == beforeDefinition {
		t.Fatal("excerpt metadata was not bound into frozen submission and request identity")
	}
}

func TestBriefSubmissionRejectsInvalidFrozenInputsBeforeLauncher(t *testing.T) {
	base := briefSubmissionFixture(t, "One source line.\nTwo source lines.\n", 1, 2)
	for name, mutate := range map[string]func(*BriefSubmission){
		"digest":         func(s *BriefSubmission) { s.Digest = "wrong" },
		"version":        func(s *BriefSubmission) { s.Version = "future" },
		"changed body":   func(s *BriefSubmission) { s.Report.Source.Body += "changed" },
		"changed report": func(s *BriefSubmission) { s.Report.Text = "changed" },
		"not complete":   func(s *BriefSubmission) { s.Report.Stage = "model_requested" },
		"authority":      func(s *BriefSubmission) { s.Report.HumanReview = "approved" },
		"path":           func(s *BriefSubmission) { s.SourcePath = "relative" },
		"model":          func(s *BriefSubmission) { s.Model = "" },
		"statement":      func(s *BriefSubmission) { s.Statement = "" },
		"oversize":       func(s *BriefSubmission) { s.Statement = strings.Repeat("a", 2001) },
		"whitespace":     func(s *BriefSubmission) { s.Statement += " " },
		"control":        func(s *BriefSubmission) { s.Statement += "\x1b" },
		"bidi":           func(s *BriefSubmission) { s.Statement += "\u202e" },
		"quote":          func(s *BriefSubmission) { s.Citation.ExactQuote = "not source" },
		"start":          func(s *BriefSubmission) { s.Citation.StartLine = 0 },
		"end":            func(s *BriefSubmission) { s.Citation.EndLine = 13 },
		"reversed":       func(s *BriefSubmission) { s.Citation.StartLine = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var input BriefSubmission
			_ = json.Unmarshal(raw, &input)
			mutate(&input)
			if name != "digest" {
				input.Digest = ""
				input.Digest = digest(input)
			}
			launcher, trace := handoffLauncher(t, "batch_pending")
			if got, err := SubmitBrief(t.Context(), launcher, input); err == nil || got != (Handoff{}) {
				t.Fatal("invalid submission accepted")
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatal("invalid selection started launcher")
			}
		})
	}
	for _, body := range []string{"First\n\nThird", "First\rSecond"} {
		report, err := sourcepilot.NewBriefReport(sourcepilot.BriefSource{Version: sourcepilot.BriefSourceVersion, SourceKind: "public_event", SourceID: "fixture", SourceRevision: "v1", SourceURL: "https://example.invalid", ObservedAt: "2026-09-11T00:00:00Z", Coverage: "full_document", Limitations: []string{}, Body: body})
		if err != nil {
			continue // A stricter source parser rejecting lone CR is also safe.
		}
		report.Stage, report.RawText, report.Text = "complete", "Reading.", "Reading."
		doc, _ := labstatus.RestoreDocument("/fixture", body)
		quote, _ := doc.ExactQuote(1, doc.Source().Lines)
		if _, err := NewBriefSubmission(report, "/fixture", "test-model", "Selection.", labstatus.Citation{StartLine: 1, EndLine: doc.Source().Lines, ExactQuote: quote}); err == nil {
			t.Fatal("empty selected line or ambiguous CR accepted")
		}
	}
}

func TestLegacyBriefRemainsReadableButCannotCreateOrResubmit(t *testing.T) {
	input := briefSubmissionFixture(t, "The service is degraded.\nThe cause remains unknown.", 1, 2)
	input.Report.Source.Version = "detective-brief-source/v1"
	input.Report.Source.SourceKind = ""
	input.Digest = ""
	input.Digest = digest(input)
	if err := ValidateBriefSubmission(input); err != nil {
		t.Fatalf("legacy submission cannot be read: %v", err)
	}
	if _, found := briefOrigin(input)["declared_source_kind"]; found {
		t.Fatal("legacy origin identity was rewritten")
	}
	if _, err := NewBriefSubmission(input.Report, input.SourcePath, input.Model, input.Statement, input.Citation); err == nil {
		t.Fatal("legacy report created a new candidate")
	}
	launcher, trace := handoffLauncher(t, "batch_pending")
	if _, err := SubmitBrief(t.Context(), launcher, input); err == nil {
		t.Fatal("legacy submission reached intake")
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("legacy submission started launcher")
	}
}

func TestSubmitBriefKeepsOriginalSourceAndNativePendingBoundary(t *testing.T) {
	for _, outcome := range []string{"pending", "admitted", "rejected", "audit_only"} {
		t.Run(outcome, func(t *testing.T) {
			input := briefSubmissionFixture(t, "Uncited context.\r\nSelected source line🙂.\r\nMore original context.\r\n", 2, 2)
			launcher, trace := handoffLauncher(t, "batch_"+outcome)
			h, err := SubmitBrief(t.Context(), launcher, input)
			if err != nil || h.Status != outcome || !h.Replayed {
				t.Fatalf("submission: %+v, %v", h, err)
			}
			calls := readHandoffTrace(t, trace)
			if !slices.Equal(handoffCallNames(calls), []string{"initialize", "notifications/initialized", "tools/list", "submit_text_source", "get_extractor_input", "submit_extractor_output"}) {
				t.Fatal("unexpected write or automatic retry")
			}
			var source sourceRequest
			var output extractorRequest
			_ = json.Unmarshal(calls[3].Arguments, &source)
			_ = json.Unmarshal(calls[5].Arguments, &output)
			if source.RawText != input.Report.Source.Body || source.RawText == input.Report.Text || !reflect.DeepEqual(source.OriginMetadata, briefOrigin(input)) || source.SourceVersion != contentHash(input.Report.Source.Body) {
				t.Fatal("source bytes or caller declarations were substituted")
			}
			if output.ExtractorDefinition.Name != "detective-brief-selection" || output.ExtractorOutput.Proposals[0].StatementText != input.Statement || !slices.Equal(output.ExtractorOutput.Proposals[0].EvidenceRefs, []string{"span:S2"}) {
				t.Fatal("selected claim/excerpt changed")
			}
		})
	}
	for _, mode := range []string{"batch_multi", "batch_fresh_terminal", "valid"} {
		input := briefSubmissionFixture(t, "Exact source.\n", 1, 1)
		launcher, _ := handoffLauncher(t, mode)
		if _, err := SubmitBrief(t.Context(), launcher, input); err == nil {
			t.Fatal("invalid native receipt accepted")
		}
	}
}

func briefReviewFixture(t *testing.T) (BriefSubmission, SourceClaimReview, Handoff, pendingRecord) {
	t.Helper()
	review, doc, _, handoff := reviewFixture(t)
	input := briefSubmissionFixture(t, doc.RawText(), 1, 1)
	return briefReviewFixtureForInput(t, input, review, handoff)
}

func briefReviewFixtureForInput(t *testing.T, input BriefSubmission, review SourceClaimReview, handoff Handoff) (BriefSubmission, SourceClaimReview, Handoff, pendingRecord) {
	t.Helper()
	doc, err := briefDocument(input)
	if err != nil {
		t.Fatal(err)
	}
	q := queryFixture(doc, labstatus.Record{Statement: input.Statement, Citation: input.Citation}, handoff)
	q.Source.SourceID = briefSourceID(input)
	q.Extractor.Name, q.Extractor.Version = briefDefinition(input).Name, briefDefinition(input).Version
	q.Extractor.ConfigHash = "sha256:" + digest(briefDefinition(input).Config)
	var p reviewPackage
	_ = json.Unmarshal([]byte(review.Display.PayloadUTF8), &p)
	b := &p.ProposalBasis
	b.StatementText, b.SourceID = input.Statement, briefSourceID(input)
	b.ExtractorName, b.ExtractorVersion, b.ExtractorConfigHash = q.Extractor.Name, q.Extractor.Version, q.Extractor.ConfigHash
	b.OriginMetadataHash = "sha256:" + digest(briefOrigin(input))
	request, err := briefRequest(input, sourceResult{SourceSnapshotID: handoff.SourceSnapshotID, ExtractionViewID: handoff.ExtractionViewID}, []span{{SpanID: "span:S1", DisplayLine: 1}})
	if err != nil {
		t.Fatal(err)
	}
	review.SubmissionReceipt.RequestID = request.RequestID
	review.SubmissionReceipt.ExtractorOutputHash = "sha256:" + digest(request.ExtractorOutput)
	review.ProposalManifest.ExtractorOutputHash = review.SubmissionReceipt.ExtractorOutputHash
	setReviewPayload(t, &review, p)
	return input, review, handoff, q
}

func TestBriefReviewBindsCompleteDisplayAndDeclaredProvenance(t *testing.T) {
	input, review, handoff, _ := briefReviewFixture(t)
	if err := ValidateBriefReview(review, input, handoff); err != nil {
		t.Fatal(err)
	}
	launcher, trace := reviewLauncher(t, "read", review, fixtureAdmission(handoff), nil, nil)
	got, err := ReviewBrief(t.Context(), launcher, input, handoff)
	if err != nil || !reflect.DeepEqual(got, review) {
		t.Fatalf("review: %v", err)
	}
	assertReviewOnlyCall(t, trace, "get_source_claim_review")
	for _, key := range []string{"statement_text", "source_id", "source_version", "raw_content_hash", "origin_metadata_hash", "extractor_config_hash", "extractor_name", "renderer_name"} {
		t.Run(key, func(t *testing.T) {
			changed := review
			var body map[string]any
			_ = json.Unmarshal([]byte(review.Display.PayloadUTF8), &body)
			body["proposal_basis"].(map[string]any)[key] = "changed"
			raw, _ := json.Marshal(body)
			var pkg reviewPackage
			_ = json.Unmarshal(raw, &pkg)
			setReviewPayload(t, &changed, pkg)
			if ValidateBriefReview(changed, input, handoff) == nil {
				t.Fatal("changed review material accepted")
			}
		})
	}
	for _, change := range []func(*SourceClaimReview){
		func(r *SourceClaimReview) { r.SubmissionReceipt.RequestID = "other" },
		func(r *SourceClaimReview) { r.Display.PayloadUTF8 += " " },
		func(r *SourceClaimReview) { r.Subject.ReviewSubject.ProposalOccurrenceID = fixtureID("wrong:") },
	} {
		changed := review
		change(&changed)
		if ValidateBriefReview(changed, input, handoff) == nil {
			t.Fatal("changed envelope accepted")
		}
	}
}

func TestBriefObserveChecksEveryTerminalStateAndAdmittedCanonical(t *testing.T) {
	for _, outcome := range []string{"pending", "admitted", "rejected", "audit_only"} {
		t.Run(outcome, func(t *testing.T) {
			input, review, handoff, q := briefReviewFixture(t)
			admission := fixtureAdmission(handoff)
			canonical := canonicalReviewFixture(t, q, admission)
			q.AdmissionOutcome = outcome
			if outcome == "admitted" {
				q.CanonicalRef, _ = json.Marshal(admission.CanonicalRef)
			}
			launcher, _ := reviewLauncher(t, "query", review, admission, &q, &canonical)
			observed, err := ObserveBrief(t.Context(), launcher, input, handoff)
			if err != nil || observed.AdmissionOutcome != outcome || (outcome == "admitted" && observed.CanonicalRef != admission.CanonicalRef) {
				t.Fatalf("observation: %+v, %v", observed, err)
			}
			launcher, _ = reviewLauncher(t, "query", review, admission, &q, &canonical)
			if outcome == "admitted" {
				if err := VerifyBriefAdmitted(t.Context(), launcher, input, handoff, admission); err != nil {
					t.Fatal(err)
				}
			} else if outcome != "pending" {
				d := ReviewedDisposition{ProposalOccurrenceID: handoff.ProposalOccurrenceID, AdmissionDecisionID: fixtureID("adm:"), AdmissionOutcome: outcome, DecisionBy: "synthetic-reviewer", DecisionReason: "Synthetic test decision."}
				if err := VerifyBriefDisposed(t.Context(), launcher, input, handoff, d); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBriefReadbackRejectsDriftAndInvalidLocalInputs(t *testing.T) {
	input, review, handoff, original := briefReviewFixture(t)
	for path, value := range map[string]any{"statement_text": "wrong", "extractor.config_hash": "wrong", "source.source_id": "wrong", "source.raw_content_hash": "wrong", "source_refs": []any{}, "admission_outcome": "unrecognized", "canonical_ref": "unexpected", "code_fact": map[string]any{}} {
		t.Run(path, func(t *testing.T) {
			changed := mutateQueryFixture(t, original, path, value, false)
			if _, err := verifyBriefRecord(changed, input, handoff); err == nil {
				t.Fatal("mismatched Query record accepted")
			}
		})
	}
	input.Digest = "bad"
	launcher, trace := reviewLauncher(t, "read", review, fixtureAdmission(handoff), nil, nil)
	if _, err := ReviewBrief(t.Context(), launcher, input, handoff); err == nil {
		t.Fatal("invalid local review started")
	}
	if _, err := ObserveBrief(t.Context(), launcher, input, handoff); err == nil {
		t.Fatal("invalid local Query started")
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("invalid local inputs started launcher")
	}
}
