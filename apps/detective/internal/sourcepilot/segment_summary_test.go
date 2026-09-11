package sourcepilot

import (
	"strings"
	"testing"
)

func TestSegmentSummaryContract(t *testing.T) {
	body := " 第一段原文。<br />Second source paragraph. "
	segments, err := SegmentBody(body)
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"items":[{"segment":2,"summary":"來源第二段。"}],"reason":""}`
	got, err := ParseSegmentSummary(valid, body, segments)
	if err != nil || len(got.Items) != 1 || got.Items[0].Segment != 2 {
		t.Fatalf("valid selection: %+v, %v", got, err)
	}
	for _, raw := range []string{
		`{"items":[],"reason":""}`, `{"items":null,"reason":"none"}`,
		`{"items":[],"reason":null}`, `{"items":[],"reason":"none","admit":true}`,
		`{"items":[{"segment":1,"summary":"甲"},{"segment":1,"summary":"乙"}],"reason":""}`,
		strings.Replace(valid, `"segment":2`, `"segment":0`, 1),
		strings.Replace(valid, `"segment":2`, `"segment":3`, 1),
		strings.Replace(valid, `"segment":2`, `"segment":1.5`, 1),
		strings.Replace(valid, `"segment":2`, `"segment":null`, 1),
		strings.Replace(valid, `"segment":2`, `"segment":"2"`, 1),
		strings.Replace(valid, `"segment":2`, `"segment":2,"segment":1`, 1),
		strings.Replace(valid, `"segment":2`, `"Segment":2`, 1),
		strings.Replace(valid, `"segment":2`, `"line":2`, 1),
		strings.Replace(valid, `"segment":2`, `"segment":2,"start_byte":0`, 1),
		strings.Replace(valid, `"summary":"來源第二段。"`, `"summary":null`, 1),
		strings.Replace(valid, `"summary":"來源第二段。"`, `"Summary":"來源第二段。"`, 1),
		strings.Replace(valid, `"summary":"來源第二段。"`, `"summary":" "`, 1),
		strings.Replace(valid, `"summary":"來源第二段。"`, `"summary":"`+strings.Repeat("甲", 513)+`"`, 1),
		strings.Replace(valid, `"reason":""`, `"reason":" "`, 1),
		strings.Replace(valid, `"reason":""`, `"Reason":""`, 1),
		strings.Replace(valid, `"reason":""`, `"reason":"","quote":"fake"`, 1),
		valid + `{}`, "```json\n" + valid + "\n```", strings.Repeat(" ", 16<<10) + valid,
		`{"items":[],"reason":"\ud800"}`,
	} {
		if result, err := ParseSegmentSummary(raw, body, segments); err == nil || len(result.Items) != 0 {
			t.Fatalf("accepted unsupported output: %.150s", raw)
		}
	}
	if _, err := ParseSegmentSummary(`{"items":[],"reason":"沒有可整理內容。"}`, body, segments); err != nil {
		t.Fatal("structural abstention should remain valid")
	}
	if _, err := ParseSegmentSummary(valid, body+"changed", segments); err == nil {
		t.Fatal("accepted changed source body")
	}
	segments.Segments[0].Text = "forged"
	if _, err := ParseSegmentSummary(valid, body, segments); err == nil {
		t.Fatal("unselected segment tampering was ignored")
	}
}

func TestSegmentSummaryKeepsUniqueCitationRule(t *testing.T) {
	body := "Synthetic API errors increased.<br /><br />The change was reverted."
	segments, err := SegmentBody(body)
	if err != nil || len(segments.Segments) != 2 {
		t.Fatal("synthetic two-paragraph input failed")
	}
	raw := `{"items":[{"segment":1,"summary":"API 錯誤增加。"},{"segment":2,"summary":"已還原變更。"}],"reason":""}`
	result, err := ParseSegmentSummary(raw, body, segments)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range result.Items {
		quote := segments.Segments[item.Segment-1]
		if quote.Text != body[quote.StartByte:quote.EndByte] {
			t.Fatal("citation was not an exact original byte range")
		}
	}
	repeated := strings.Replace(raw, `"segment":2`, `"segment":1`, 1)
	if _, err := ParseSegmentSummary(repeated, body, segments); err == nil {
		t.Fatal("segment experiment relaxed duplicate selection")
	}
}
