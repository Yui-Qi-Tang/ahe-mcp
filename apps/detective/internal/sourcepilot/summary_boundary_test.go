package sourcepilot

import "testing"

// Added after the second live cohort to reproduce its interface boundary with
// synthetic text, not to repair or reinterpret the frozen failed output.
func TestSummaryPhysicalLineBoundary(t *testing.T) {
	body := "Synthetic API errors increased.<br /><br />The change was reverted."
	lines, err := Lines(body)
	if err != nil || len(lines) != 1 || lines[0].Text != body {
		t.Fatal("HTML separators must not silently change the source lines")
	}
	repeated := `{"items":[{"line":1,"summary":"API 錯誤增加。"},{"line":1,"summary":"已還原變更。"}],"reason":""}`
	result, err := ParseSummary(repeated, lines)
	if err == nil || len(result.Items) != 0 {
		t.Fatal("multiple summaries of the same physical line must remain rejected")
	}
	combined := `{"items":[{"line":1,"summary":"API 錯誤增加，已還原變更。"}],"reason":""}`
	if _, err := ParseSummary(combined, lines); err != nil {
		t.Fatal("one summary for the original line should be structurally valid")
	}
	// This is a separate synthetic input, not an automatic HTML transformation.
	otherLines, err := Lines("Synthetic API errors increased.\nThe change was reverted.")
	if err != nil {
		t.Fatal(err)
	}
	distinct := `{"items":[{"line":1,"summary":"API 錯誤增加。"},{"line":2,"summary":"已還原變更。"}],"reason":""}`
	if _, err := ParseSummary(distinct, otherLines); err != nil {
		t.Fatal("different physical lines should allow separate cited summaries")
	}
}
