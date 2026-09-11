package sourcepilot

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func fixedFixture() FixedClaims {
	return FixedClaims{Version: FixedClaimsVersion, Source: guidedFixture(), Claims: []string{"Payment API 已恢復。", "Refund API 已恢復。"}}
}

func TestFixedClaimsStayCallerSuppliedInOneCheck(t *testing.T) {
	input := fixedFixture()
	llm := &guidedModel{responses: []string{guidedCheckJSON}}
	reviewer, _ := NewGuidedReviewer(llm)
	report, err := reviewer.CheckFixed(context.Background(), input)
	if err != nil || report.Stage != "complete" || len(llm.requests) != 1 {
		t.Fatalf("single fixed check: %v %+v", err, report)
	}
	if !reflect.DeepEqual(report.Input, input) || report.ClaimsOrigin != "caller_supplied_unverified" || report.HumanReview != "not_reviewed" || report.AuthorityEffect != "none" {
		t.Fatal("caller input or authority changed")
	}
	raw := llm.requests[0].Contents[0].Parts[0].Text
	for _, forbidden := range []string{input.Source.Summary, input.Source.SourceID, "subject", "predicate", "object", "contradicted", "caller_supplied"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("unexpected input hint %q: %s", forbidden, raw)
		}
	}
	var wire struct {
		Claims []struct {
			Number int    `json:"number"`
			Text   string `json:"text"`
		} `json:"claims"`
		Segments []Line `json:"source_segments"`
	}
	if json.Unmarshal([]byte(raw), &wire) != nil || len(wire.Claims) != 2 || wire.Claims[1].Text != input.Claims[1] || len(wire.Segments) != 3 || wire.Segments[2].Text != "The cause remains unconfirmed." {
		t.Fatal("fixed claims or complete context changed")
	}
	if report.Assessments[0].Relation != "supported" || report.Assessments[1].Relation != "contradicted" {
		t.Fatal("claim-specific results missing")
	}
	encoded, _ := json.Marshal(report)
	decoded, err := ParseFixedReport(encoded)
	if err != nil || !reflect.DeepEqual(decoded, report) {
		t.Fatalf("readback: %v", err)
	}
	var display bytes.Buffer
	if err := WriteFixedText(&display, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"caller_supplied_unverified", "未執行摘要拆解", "未執行獨立內文觀察", input.Source.Body, "未引用", "不是文法"} {
		if !strings.Contains(display.String(), want) {
			t.Fatalf("missing boundary %q", want)
		}
	}
	if strings.Contains(display.String(), "主體＝") {
		t.Fatal("fixed input acquired invented grammar hints")
	}
}

func TestFixedInputValidationAndCopy(t *testing.T) {
	input := fixedFixture()
	raw, _ := json.Marshal(input)
	if _, err := ParseFixedClaims(raw); err != nil {
		t.Fatal(err)
	}
	for _, claims := range [][]string{nil, {}, {"API recovery"}, {"API recovered?"}, {"API...recovered."}, {" API recovered."}, {"API recovered.", "API recovered."}, {"A recovered.", "B recovered.", "C recovered.", "D recovered."}} {
		bad := input
		bad.Claims = claims
		if _, err := NewFixedReport(bad); err == nil {
			t.Fatalf("accepted invalid claims %q", claims)
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"claims":`, `"admit":true,"claims":`, 1),
		strings.Replace(string(raw), `"claims":`, `"claims":[],"claims":`, 1),
		strings.Replace(string(raw), `"source_id":"synthetic-payments",`, "", 1),
		string(raw) + `{}`,
	} {
		if _, err := ParseFixedClaims([]byte(bad)); err == nil {
			t.Fatal("accepted malformed fixed input")
		}
	}
	report, _ := NewFixedReport(input)
	input.Claims[0] = "Changed."
	if report.Input.Claims[0] == input.Claims[0] {
		t.Fatal("caller can mutate frozen claims")
	}
	// This lexical boundary deliberately does not claim grammar proof.
	input.Claims = []string{"API recovery."}
	if report, err := NewFixedReport(input); err != nil || report.Readability != "requires_human_review" {
		t.Fatal("invented automatic grammar acceptance")
	}
}

func TestFixedFailedReportsRemainReadableWithoutRetry(t *testing.T) {
	for _, bad := range []string{"", `{"assessments":[]}`, `{"assessments":[{"claim":1,"relation":"supported","segments":[999],"explanation":"Invalid citation."}]}`} {
		llm := &guidedModel{responses: []string{bad}}
		reviewer, _ := NewGuidedReviewer(llm)
		report, err := reviewer.CheckFixed(context.Background(), fixedFixture())
		if err == nil || len(llm.requests) != 1 || report.Stage != "source_check" || len(report.Assessments) != 0 {
			t.Fatal("failure retried or acquired completed results")
		}
		raw, _ := json.Marshal(report)
		if _, err := ParseFixedReport(raw); err != nil {
			t.Fatalf("failed result cannot be read: %v", err)
		}
		report.Stage = "complete"
		raw, _ = json.Marshal(report)
		if _, err := ParseFixedReport(raw); err == nil {
			t.Fatal("failed attempt promoted to complete")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	llm := &guidedModel{}
	reviewer, _ := NewGuidedReviewer(llm)
	report, err := reviewer.CheckFixed(ctx, fixedFixture())
	if err == nil || len(llm.requests) != 0 || report.Stage != "inspected" {
		t.Fatal("cancelled check called model")
	}
	raw, _ := json.Marshal(report)
	if _, err := ParseFixedReport(raw); err != nil {
		t.Fatal(err)
	}
}

func TestFixedReadbackRejectsAlteredDerivedFields(t *testing.T) {
	llm := &guidedModel{responses: []string{guidedCheckJSON}}
	reviewer, _ := NewGuidedReviewer(llm)
	report, _ := reviewer.CheckFixed(context.Background(), fixedFixture())
	raw, _ := json.Marshal(report)
	for _, pair := range [][2]string{
		{`"authority_effect":"none"`, `"authority_effect":"admit"`},
		{`"claims_origin":"caller_supplied_unverified"`, `"claims_origin":"human_approved"`},
		{`"human_review":"not_reviewed"`, `"human_review":null`},
		{`"readability":"requires_human_review",`, ""},
		{`"start_byte":0`, `"start_byte":1`},
		{`"relation":"supported"`, `"relation":"contradicted"`},
		{`"stage":"complete"`, `"stage":"inspected"`},
		{`"version":"detective-fixed-check/v1"`, `"version":"detective-summary-check/v1"`},
	} {
		altered := strings.Replace(string(raw), pair[0], pair[1], 1)
		if altered == string(raw) {
			t.Fatalf("test did not change %s", pair[0])
		}
		if _, err := ParseFixedReport([]byte(altered)); err == nil {
			t.Fatalf("accepted altered field %s", pair[0])
		}
	}
}
