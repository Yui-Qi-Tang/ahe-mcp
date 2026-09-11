package sourcepilot

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func readbackFixture(t *testing.T, responses ...string) GuidedReport {
	t.Helper()
	reviewer, err := NewGuidedReviewer(&guidedModel{responses: responses})
	if err != nil {
		t.Fatal(err)
	}
	report, _ := reviewer.Review(context.Background(), guidedFixture())
	return report
}

func assertGuidedReadback(t *testing.T, report GuidedReport) {
	t.Helper()
	if err := ValidateGuidedReport(report); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseGuidedReport(raw)
	if err != nil || !reflect.DeepEqual(got, report) {
		t.Fatalf("report did not round trip: %v", err)
	}
}

func TestGuidedReadbackAcceptsCompleteInspectedAndStoppedReports(t *testing.T) {
	initial, err := NewGuidedReport(guidedFixture())
	if err != nil {
		t.Fatal(err)
	}
	assertGuidedReadback(t, initial)
	// No-response transport failures and malformed final output retain exactly
	// the previously applied stages, including an initially empty inspection.
	for _, responses := range [][]string{
		nil,
		{`not JSON`},
		{guidedClaimJSON},
		{guidedClaimJSON, `{"assessments":[]}`},
		{guidedClaimJSON, guidedCheckJSON},
		{guidedClaimJSON, guidedCheckJSON, `{"observations":[],"reason":""}`},
		{guidedClaimJSON, guidedCheckJSON, guidedBodyJSON},
		{`{"claims":[],"reason":"No factual claim."}`, guidedBodyJSON},
		{`{"claims":[],"reason":"No factual claim."}`, `not JSON`},
		{`{"claims":[],"reason":"No factual claim."}`, `{"observations":[],"reason":"No source facts."}`},
	} {
		assertGuidedReadback(t, readbackFixture(t, responses...))
	}
	// Review checks cancellation after applying each successful stage, so its
	// final Stage need not be complete even when the last valid text was applied.
	for stop := 1; stop <= 3; stop++ {
		report, err := NewGuidedReport(guidedFixture())
		if err != nil {
			t.Fatal(err)
		}
		for i, attempt := range []GuidedAttempt{{"summary_claims", guidedClaimJSON}, {"source_check", guidedCheckJSON}, {"body_observations", guidedBodyJSON}}[:stop] {
			if err := guidedApply(&report, attempt.Stage, attempt.RawText); err != nil {
				t.Fatalf("stage %d: %v", i, err)
			}
			report.Attempts = append(report.Attempts, attempt)
			report.Stage = attempt.Stage
		}
		assertGuidedReadback(t, report)
	}
}

func TestGuidedReadbackRejectsChangedDerivedResultsAndAuthority(t *testing.T) {
	changes := map[string]func(*GuidedReport){
		"version":           func(r *GuidedReport) { r.Version = "v2" },
		"body hash":         func(r *GuidedReport) { r.BodySHA256 = strings.Repeat("0", 64) },
		"summary hash":      func(r *GuidedReport) { r.SummarySHA256 = strings.Repeat("0", 64) },
		"source body":       func(r *GuidedReport) { r.Source.Body += " Extra source." },
		"source summary":    func(r *GuidedReport) { r.Source.Summary += " Extra summary." },
		"projection":        func(r *GuidedReport) { r.Projection.Segments[0].Text = "Changed." },
		"offset":            func(r *GuidedReport) { r.Projection.Segments[0].StartByte++ },
		"claim number":      func(r *GuidedReport) { r.Claims[0].Number = 2 },
		"claim":             func(r *GuidedReport) { r.Claims[0].Statement.Text = "Payment API did not recover." },
		"reading aid":       func(r *GuidedReport) { r.Claims[0].Statement.Object = "Payment API" },
		"claims reason":     func(r *GuidedReport) { r.ClaimsReason = "Invented reason." },
		"relation":          func(r *GuidedReport) { r.Assessments[1].Relation = "supported" },
		"references":        func(r *GuidedReport) { r.Assessments[0].Segments = []int{2} },
		"quote":             func(r *GuidedReport) { r.Assessments[0].Citations[0].Text = "Changed quote." },
		"explanation":       func(r *GuidedReport) { r.Assessments[0].Explanation = "Changed." },
		"observation":       func(r *GuidedReport) { r.Observations[0].Statement.Predicate = "已確認" },
		"observation quote": func(r *GuidedReport) { r.Observations[0].Citations = []Segment{} },
		"observation reason": func(r *GuidedReport) {
			r.ObservationsReason = "Invented reason."
		},
		"raw final":       func(r *GuidedReport) { r.Attempts[2].RawText = "invalid" },
		"review":          func(r *GuidedReport) { r.HumanReview = "approved" },
		"readability":     func(r *GuidedReport) { r.Readability = "readable" },
		"authority":       func(r *GuidedReport) { r.AuthorityEffect = "admit" },
		"nil array":       func(r *GuidedReport) { r.Attempts = nil },
		"unknown stage":   func(r *GuidedReport) { r.Stage = "admitted" },
		"inspected stage": func(r *GuidedReport) { r.Stage = "inspected" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			report := readbackFixture(t, guidedClaimJSON, guidedCheckJSON, guidedBodyJSON)
			change(&report)
			if ValidateGuidedReport(report) == nil {
				t.Fatal("inconsistent report accepted")
			}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseGuidedReport(raw); err == nil {
				t.Fatal("inconsistent JSON accepted")
			}
		})
	}
}

func TestGuidedReadbackRejectsIllegalAttemptSequence(t *testing.T) {
	changes := map[string]func(*GuidedReport){
		"omit summary": func(r *GuidedReport) { r.Attempts = r.Attempts[1:] },
		"reorder": func(r *GuidedReport) {
			r.Attempts[0], r.Attempts[1] = r.Attempts[1], r.Attempts[0]
		},
		"retry":                 func(r *GuidedReport) { r.Attempts = append(r.Attempts, r.Attempts[2]) },
		"invalid then continue": func(r *GuidedReport) { r.Attempts[1].RawText = "bad" },
		"oversize final":        func(r *GuidedReport) { r.Attempts[2].RawText = strings.Repeat("x", 32<<10+1) },
		"invalid UTF-8 final":   func(r *GuidedReport) { r.Attempts[2].RawText = string([]byte{0xff}) },
		"premature complete": func(r *GuidedReport) {
			r.Attempts = r.Attempts[:2]
			r.Observations = []GuidedObservation{}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			report := readbackFixture(t, guidedClaimJSON, guidedCheckJSON, guidedBodyJSON)
			change(&report)
			if ValidateGuidedReport(report) == nil {
				t.Fatal("illegal sequence accepted")
			}
		})
	}
	noClaims := readbackFixture(t, `{"claims":[],"reason":"No claim."}`, guidedBodyJSON)
	noClaims.Attempts = append(noClaims.Attempts[:1], GuidedAttempt{"source_check", `{"assessments":[]}`}, noClaims.Attempts[1])
	if ValidateGuidedReport(noClaims) == nil {
		t.Fatal("source-check stage allowed after empty claims")
	}
	failed := readbackFixture(t, guidedClaimJSON, "bad")
	failed.Stage = "complete"
	if ValidateGuidedReport(failed) == nil {
		t.Fatal("failed final promoted to complete")
	}
}

func TestGuidedReadbackRequiresEveryJSONFieldAtEveryDepth(t *testing.T) {
	report := readbackFixture(t, guidedClaimJSON, guidedCheckJSON, guidedBodyJSON)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for name, bad := range map[string]string{
		"missing empty top field": strings.Replace(raw, `"claims_reason":"",`, "", 1),
		"missing empty aid":       strings.Replace(raw, `,"object":""`, "", 1),
		"missing zero offset":     strings.Replace(raw, `"start_byte":0,`, "", 1),
		"missing reason":          strings.Replace(raw, `"observations_reason":"",`, "", 1),
		"null aid":                strings.Replace(raw, `"object":""`, `"object":null`, 1),
		"null array":              strings.Replace(raw, `"limitations":[]`, `"limitations":null`, 1),
		"duplicate top":           strings.Replace(raw, `"claims_reason":""`, `"claims_reason":"","claims_reason":""`, 1),
		"duplicate nested":        strings.Replace(raw, `"object":""`, `"object":"","object":""`, 1),
		"unknown":                 strings.Replace(raw, `"object":""`, `"object":"","approval":true`, 1),
		"wrong key case":          strings.Replace(raw, `"claims_reason":`, `"Claims_Reason":`, 1),
		"unpaired surrogate":      strings.Replace(raw, `"source_revision":"fixture-v1"`, `"source_revision":"\ud800"`, 1),
		"trailing object":         raw + `{}`,
		"oversize":                strings.Repeat(" ", 2<<20+1) + raw,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseGuidedReport([]byte(bad)); err == nil {
				t.Fatal("open JSON shape accepted")
			}
		})
	}
	// The complete report has the writer's 2 MiB ceiling, not the stricter
	// per-field JSON validator's 1 MiB ceiling (including harmless formatting).
	padded := append([]byte(strings.Repeat(" ", (2<<20)-len(encoded))), encoded...)
	if _, err := ParseGuidedReport(padded); err != nil {
		t.Fatalf("report at the writer size bound rejected: %v", err)
	}
	if _, err := ParseGuidedReport(append(padded, ' ')); err == nil {
		t.Fatal("report larger than writer bound accepted")
	}
	// Metadata remains caller-declared. Internal consistency is not a file
	// signature or publisher authentication, even when all projections replay.
	report.Source.SourceRevision = "caller-supplied-other-revision"
	assertGuidedReadback(t, report)
}
