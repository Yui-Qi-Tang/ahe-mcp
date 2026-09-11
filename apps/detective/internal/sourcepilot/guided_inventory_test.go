package sourcepilot

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestGuidedReferencesSeparateSelectionFromImportance(t *testing.T) {
	llm := &guidedModel{responses: []string{guidedClaimJSON, guidedCheckJSON, guidedBodyJSON}}
	reviewer, _ := NewGuidedReviewer(llm)
	report, _ := reviewer.Review(context.Background(), guidedFixture())
	rows := GuidedReferences(report)
	if len(rows) != 3 || !reflect.DeepEqual(rows[0].Claims, []int{1}) || !reflect.DeepEqual(rows[1].Claims, []int{2}) || len(rows[2].Claims) != 0 || !reflect.DeepEqual(rows[2].Observations, []int{1}) {
		t.Fatalf("unexpected reference inventory: %+v", rows)
	}
	for _, row := range rows {
		if row.Segment.Text != report.Source.Body[row.Segment.StartByte:row.Segment.EndByte] {
			t.Fatal("inventory reduced source text")
		}
	}
	rows[0].Claims[0] = 999
	if report.Assessments[0].Claim != 1 {
		t.Fatal("inventory aliases results")
	}
	var display bytes.Buffer
	if err := WriteGuidedText(&display, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"不是語意涵蓋率", "未引用不等於不重要", "段落 3｜查證主張：未引用｜獨立觀察：[1]", report.Source.Body} {
		if !strings.Contains(display.String(), want) {
			t.Fatalf("missing inventory boundary %q", want)
		}
	}
}

func TestGuidedGrammarHintWarningDoesNotRewriteOrApprove(t *testing.T) {
	report, _ := NewGuidedReport(guidedFixture())
	statement := ReadableStatement{Text: "Payment API recovered.", Subject: "Payment API", Predicate: "recovered", Object: "Payment API"}
	report.Claims = []GuidedClaim{{Number: 1, Statement: statement}}
	var display bytes.Buffer
	if err := WriteGuidedText(&display, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(display.String(), "可能是文法提示填錯") || !strings.Contains(display.String(), "不自動判定句子錯誤") || report.Claims[0].Statement != statement || report.Readability != "requires_human_review" {
		t.Fatal("hint warning hid output or became grammar approval")
	}
}
