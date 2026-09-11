package sourcepilot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSegmentBodyExactOffsets(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want []Segment
	}{
		{"empty", "", []Segment{}},
		{"only whitespace and delimiters", " \t\u3000\r\n<br><BR/><Br />\n", []Segment{}},
		{"unicode", " \t第一段\u3000\r\n \u00a0第二段 😀 \t", []Segment{
			{Number: 1, StartByte: 2, EndByte: 11, Text: "第一段"},
			{Number: 2, StartByte: 19, EndByte: 33, Text: "第二段 😀"},
		}},
		{"line endings", "\r\nalpha\rbravo\ncharlie\r\n", []Segment{
			{Number: 1, StartByte: 2, EndByte: 7, Text: "alpha"},
			{Number: 2, StartByte: 8, EndByte: 13, Text: "bravo"},
			{Number: 3, StartByte: 14, EndByte: 21, Text: "charlie"},
		}},
		{"repeated br", "a<br><BR/> <bR />b<Br>c", []Segment{
			{Number: 1, StartByte: 0, EndByte: 1, Text: "a"},
			{Number: 2, StartByte: 17, EndByte: 18, Text: "b"},
			{Number: 3, StartByte: 22, EndByte: 23, Text: "c"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			set, err := SegmentBody(test.body)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(set.Segments, test.want) {
				t.Fatalf("segments = %+v; want %+v", set.Segments, test.want)
			}
			assertSegmentBodyProperties(t, test.body, set)
		})
	}
}

func TestSegmentBodyKeepsOtherMarkupLiteral(t *testing.T) {
	body := `<p>A &amp; B. Still one segment!</p><br class="x"><br ><br  /><br\t/><br x/><hr>&lt;br&gt;`
	body += "<br\t/>甲\u2028乙"
	set, err := SegmentBody(body)
	if err != nil || len(set.Segments) != 1 || set.Segments[0].Text != body || set.Segments[0].StartByte != 0 || set.Segments[0].EndByte != len(body) {
		t.Fatalf("markup, entity, or sentence was changed: %+v, %v", set, err)
	}
	// The fixed rule deliberately does not interpret HTML context.
	literal := `Use "<BR>" literally; <script>"<br/>"</script>`
	set, err = SegmentBody(literal)
	if err != nil || len(set.Segments) != 3 || set.Segments[0].Text != `Use "` || set.Segments[1].Text != `" literally; <script>"` || set.Segments[2].Text != `"</script>` {
		t.Fatalf("literal BR behavior changed: %+v, %v", set, err)
	}
	decodedJSON := `"甲\n乙"`
	var decoded string
	if err := json.Unmarshal([]byte(decodedJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	set, err = SegmentBody(decoded)
	if err != nil || len(set.Segments) != 2 || set.Segments[1].StartByte != 4 || set.Segments[1].EndByte != 7 {
		t.Fatalf("offsets must refer to decoded UTF-8, not raw JSON: %+v, %v", set, err)
	}
}

func TestSegmentBodyJSONFieldNames(t *testing.T) {
	set, err := SegmentBody("a")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":"detective-source-segments/v1","body_sha256":"` + set.BodySHA256 + `","segments":[{"number":1,"start_byte":0,"end_byte":1,"text":"a"}]}`
	if string(body) != want {
		t.Fatalf("JSON field contract changed: %s", body)
	}
}

func TestSegmentBodyBounds(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{"32 KiB", strings.Repeat("a", 32<<10), 1},
		{"64 segments", strings.Repeat("a\n", 64), 64},
		{"empty pieces do not count", strings.Repeat("\n", 1000) + strings.Repeat("a<br>\r\n", 64), 64},
	} {
		t.Run(test.name, func(t *testing.T) {
			set, err := SegmentBody(test.body)
			if err != nil || len(set.Segments) != test.want {
				t.Fatalf("valid boundary failed: %d segments, %v", len(set.Segments), err)
			}
			assertSegmentBodyProperties(t, test.body, set)
		})
	}
	for _, body := range []string{strings.Repeat("a", (32<<10)+1), strings.Repeat("a\n", 65), string([]byte{0xff})} {
		if set, err := SegmentBody(body); err == nil || !reflect.DeepEqual(set, SegmentedBody{}) {
			t.Fatalf("invalid input returned success or partial set: %+v, %v", set, err)
		}
		if err := ValidateSegments(body, SegmentedBody{}); err == nil {
			t.Fatal("invalid source body passed validation")
		}
	}
}

func TestValidateSegmentsRejectsIncompleteOrForgedSets(t *testing.T) {
	body := " 甲 \r\n 乙 <br>丙 "
	original, err := SegmentBody(body)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SegmentedBody){
		"version":         func(set *SegmentedBody) { set.Version = "other" },
		"hash":            func(set *SegmentedBody) { set.BodySHA256 = strings.Repeat("0", 64) },
		"text":            func(set *SegmentedBody) { set.Segments[0].Text = "wrong" },
		"negative offset": func(set *SegmentedBody) { set.Segments[0].StartByte = -1 },
		"out of bounds":   func(set *SegmentedBody) { set.Segments[0].EndByte = len(body) + 1 },
		"half rune":       func(set *SegmentedBody) { set.Segments[0].StartByte++ },
		"number":          func(set *SegmentedBody) { set.Segments[0].Number = 0 },
		"omitted":         func(set *SegmentedBody) { set.Segments = set.Segments[:2] },
		"reordered":       func(set *SegmentedBody) { set.Segments[0], set.Segments[1] = set.Segments[1], set.Segments[0] },
		"duplicate":       func(set *SegmentedBody) { set.Segments = append(set.Segments, set.Segments[0]) },
		"nil":             func(set *SegmentedBody) { set.Segments = nil },
	} {
		t.Run(name, func(t *testing.T) {
			set := original
			set.Segments = append([]Segment(nil), original.Segments...)
			mutate(&set)
			if err := ValidateSegments(body, set); err == nil {
				t.Fatal("altered segment set accepted")
			}
		})
	}
	// Even body changes outside retained text must invalidate the source binding.
	if err := ValidateSegments(body+" ", original); err == nil {
		t.Fatal("changed whitespace did not invalidate the body hash")
	}
	empty, err := SegmentBody("")
	if err != nil {
		t.Fatal(err)
	}
	empty.Segments = nil
	if err := ValidateSegments("", empty); err == nil {
		t.Fatal("nil set accepted in place of deterministic empty set")
	}
}

func FuzzSegmentBodyProperties(f *testing.F) {
	for _, seed := range []string{"", "a", "甲\r\n 😀 <BR />乙", "a<br><br/>\n<br />b", "<br class=\"x\">&amp;", strings.Repeat("x\n", 65), string([]byte{0xff}), strings.Repeat("x", 32<<10)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		set, err := SegmentBody(body)
		if err != nil {
			if !reflect.DeepEqual(set, SegmentedBody{}) {
				t.Fatal("error returned partial segments")
			}
			return
		}
		assertSegmentBodyProperties(t, body, set)
	})
}

func assertSegmentBodyProperties(t *testing.T, body string, set SegmentedBody) {
	t.Helper()
	digest := sha256.Sum256([]byte(body))
	if set.Version != "detective-source-segments/v1" || set.BodySHA256 != hex.EncodeToString(digest[:]) || set.Segments == nil || len(set.Segments) > 64 || len(body) > 32<<10 || !utf8.ValidString(body) {
		t.Fatal("segmentation envelope invariant failed")
	}
	previousEnd := 0
	// This independent coverage check does not reuse the production scanner or
	// recomputation. Only the fixed ASCII BR spellings and whitespace may be lost.
	assertGap := func(gap string) {
		t.Helper()
		folded := strings.NewReplacer("B", "b", "R", "r").Replace(gap)
		withoutBR := strings.NewReplacer("<br>", "", "<br/>", "", "<br />", "").Replace(folded)
		if strings.TrimSpace(withoutBR) != "" {
			t.Fatalf("source text omitted between segments: %q", gap)
		}
	}
	for i, segment := range set.Segments {
		if segment.Number != i+1 || segment.StartByte < previousEnd || segment.StartByte < 0 || segment.EndByte > len(body) || segment.StartByte >= segment.EndByte || segment.Text != body[segment.StartByte:segment.EndByte] || !utf8.ValidString(segment.Text) || strings.TrimSpace(segment.Text) != segment.Text {
			t.Fatalf("segment invariant failed: %+v", segment)
		}
		assertGap(body[previousEnd:segment.StartByte])
		previousEnd = segment.EndByte
	}
	assertGap(body[previousEnd:])
	if err := ValidateSegments(body, set); err != nil {
		t.Fatalf("self validation failed: %v", err)
	}
	replayed, err := SegmentBody(body)
	if err != nil || !reflect.DeepEqual(replayed, set) {
		t.Fatal("replay was not deterministic")
	}
}
