package sourcepilot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEventOverviewContract(t *testing.T) {
	body := "Harbor API reported increased errors.<br /><br />The team is investigating."
	segments, err := SegmentBody(body)
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"overview":"Harbor API 錯誤增加，團隊正在調查。","segments":[1,2],"reason":""}`
	got, err := ParseEventOverview(valid, body, segments)
	if err != nil || got.Overview != "Harbor API 錯誤增加，團隊正在調查。" || !reflect.DeepEqual(got.Segments, []int{1, 2}) || got.Reason != "" {
		t.Fatalf("valid overview: %+v, %v", got, err)
	}
	for _, number := range got.Segments {
		segment := segments.Segments[number-1]
		if segment.Text != body[segment.StartByte:segment.EndByte] {
			t.Fatal("location is not an exact original byte range")
		}
	}
	abstain := `{"overview":"","segments":[],"reason":"只有導覽項目，沒有事件內容。"}`
	navigation := "View history<br/>Notification preferences"
	navigationSegments, err := SegmentBody(navigation)
	if err != nil {
		t.Fatal(err)
	}
	got, err = ParseEventOverview(abstain, navigation, navigationSegments)
	if err != nil || got.Overview != "" || got.Segments == nil || len(got.Segments) != 0 || got.Reason == "" {
		t.Fatalf("valid abstention: %+v, %v", got, err)
	}
	// Exact references are review locations, not a semantic correctness oracle.
	unsupported := strings.Replace(valid, "Harbor API 錯誤增加，團隊正在調查。", "Harbor API 已完全恢復。", 1)
	if _, err := ParseEventOverview(unsupported, body, segments); err != nil {
		t.Fatal("structural parser unexpectedly judged source entailment")
	}
}

func TestEventOverviewRejectsInvalidOutput(t *testing.T) {
	body := "Source one.<br/>Source two.<br/>Source three.<br/>Source four."
	segments, err := SegmentBody(body)
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"overview":"來源描述一項事件。","segments":[2],"reason":""}`
	tests := map[string]string{
		"empty":               "",
		"malformed":           `{"overview":`,
		"top null":            "null",
		"top array":           "[]",
		"missing overview":    `{"segments":[1],"reason":""}`,
		"missing segments":    `{"overview":"事件。","reason":""}`,
		"missing reason":      `{"overview":"事件。","segments":[1]}`,
		"unknown":             strings.Replace(valid, `"reason":""`, `"reason":"","admit":true`, 1),
		"quote":               strings.Replace(valid, `"reason":""`, `"reason":"","quote":"source"`, 1),
		"offset":              strings.Replace(valid, `"reason":""`, `"reason":"","start_byte":0`, 1),
		"duplicate overview":  strings.Replace(valid, `"overview":`, `"overview":"另一項事件。","overview":`, 1),
		"duplicate segments":  strings.Replace(valid, `"segments":`, `"segments":[1],"segments":`, 1),
		"duplicate reason":    strings.Replace(valid, `"reason":`, `"reason":"","reason":`, 1),
		"null overview":       `{"overview":null,"segments":[],"reason":"none"}`,
		"null segments":       `{"overview":"","segments":null,"reason":"none"}`,
		"null reason":         strings.Replace(valid, `"reason":""`, `"reason":null`, 1),
		"wrong overview case": strings.Replace(valid, `"overview":`, `"Overview":`, 1),
		"wrong segments case": strings.Replace(valid, `"segments":`, `"Segments":`, 1),
		"wrong reason case":   strings.Replace(valid, `"reason":`, `"Reason":`, 1),
		"overview type":       `{"overview":42,"segments":[1],"reason":""}`,
		"segments type":       strings.Replace(valid, `[2]`, `{}`, 1),
		"reason type":         strings.Replace(valid, `"reason":""`, `"reason":false`, 1),
		"duplicate id":        strings.Replace(valid, `[2]`, `[2,2]`, 1),
		"zero id":             strings.Replace(valid, `[2]`, `[0]`, 1),
		"negative id":         strings.Replace(valid, `[2]`, `[-1]`, 1),
		"outside projection":  strings.Replace(valid, `[2]`, `[5]`, 1),
		"outside cap":         strings.Replace(valid, `[2]`, `[65]`, 1),
		"fractional id":       strings.Replace(valid, `[2]`, `[1.5]`, 1),
		"string id":           strings.Replace(valid, `[2]`, `["2"]`, 1),
		"null id":             strings.Replace(valid, `[2]`, `[null]`, 1),
		"too many ids":        strings.Replace(valid, `[2]`, `[1,2,3,4]`, 1),
		"blank overview":      `{"overview":" \n\t","segments":[1],"reason":""}`,
		"no event locations":  strings.Replace(valid, `[2]`, `[]`, 1),
		"event with reason":   strings.Replace(valid, `"reason":""`, `"reason":"none"`, 1),
		"event blank reason":  strings.Replace(valid, `"reason":""`, `"reason":" "`, 1),
		"abstain without why": `{"overview":"","segments":[],"reason":""}`,
		"abstain blank why":   `{"overview":"","segments":[],"reason":" \t"}`,
		"abstain with id":     `{"overview":"","segments":[1],"reason":"none"}`,
		"overview control":    `{"overview":"事件\u0000。","segments":[1],"reason":""}`,
		"reason control":      `{"overview":"","segments":[],"reason":"no\u0007"}`,
		"overview surrogate":  `{"overview":"\ud800","segments":[1],"reason":""}`,
		"reason surrogate":    `{"overview":"","segments":[],"reason":"\ud800"}`,
		"invalid UTF-8":       strings.Replace(valid, "來源", string([]byte{0xff}), 1),
		"oversized raw":       strings.Repeat(" ", 16<<10) + valid,
		"second object":       valid + `{}`,
		"trailing text":       valid + ` extra`,
		"markdown":            "```json\n" + valid + "\n```",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseEventOverview(raw, body, segments)
			if err == nil || !reflect.DeepEqual(got, EventOverview{}) {
				t.Fatalf("accepted invalid output: %+v, %v", got, err)
			}
		})
	}
}

func TestEventOverviewTextLimits(t *testing.T) {
	body := "Source text."
	segments, err := SegmentBody(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"a", "甲", "🪴"} {
		for _, overLimit := range []bool{false, true} {
			length := 1536
			if overLimit {
				length++
			}
			result := EventOverview{Overview: strings.Repeat(symbol, length), Segments: []int{1}}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseEventOverview(string(raw), body, segments); (err != nil) != overLimit {
				t.Fatalf("overview codepoint limit: %q x %d: %v", symbol, length, err)
			}
			length = 512
			if overLimit {
				length++
			}
			result = EventOverview{Segments: []int{}, Reason: strings.Repeat(symbol, length)}
			raw, err = json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseEventOverview(string(raw), body, segments); (err != nil) != overLimit {
				t.Fatalf("reason codepoint limit: %q x %d: %v", symbol, length, err)
			}
		}
	}
}

func TestEventOverviewRequiresCompleteProjection(t *testing.T) {
	body := "First source paragraph.<br/>Second source paragraph."
	raw := `{"overview":"來源描述事件。","segments":[2],"reason":""}`
	for _, change := range []string{"body", "version", "digest", "omission", "order", "number", "offset", "unselected text"} {
		t.Run(change, func(t *testing.T) {
			segments, err := SegmentBody(body)
			if err != nil {
				t.Fatal(err)
			}
			input := body
			switch change {
			case "body":
				input += "changed"
			case "version":
				segments.Version += "changed"
			case "digest":
				segments.BodySHA256 = strings.Repeat("0", 64)
			case "omission":
				segments.Segments = segments.Segments[1:]
			case "order":
				segments.Segments[0], segments.Segments[1] = segments.Segments[1], segments.Segments[0]
			case "number":
				segments.Segments[0].Number = 3
			case "offset":
				segments.Segments[0].StartByte++
			case "unselected text":
				segments.Segments[0].Text = "forged"
			}
			if _, err := ParseEventOverview(raw, input, segments); err == nil {
				t.Fatal("accepted mismatched complete source projection")
			}
		})
	}
}

func TestEventOverviewSchema(t *testing.T) {
	schema := EventOverviewSchema()
	if schema["type"] != "object" || schema["additionalProperties"] != false || !reflect.DeepEqual(schema["required"], []string{"overview", "segments", "reason"}) {
		t.Fatalf("unexpected schema envelope: %+v", schema)
	}
	wantProperties := map[string]any{
		"overview": map[string]any{"type": "string", "maxLength": 1536},
		"segments": map[string]any{"type": "array", "maxItems": 3, "uniqueItems": true,
			"items": map[string]any{"type": "integer", "minimum": 1, "maximum": 64}},
		"reason": map[string]any{"type": "string", "maxLength": 512},
	}
	if !reflect.DeepEqual(schema["properties"], wantProperties) {
		t.Fatalf("unexpected schema fields: %+v", schema["properties"])
	}
}
