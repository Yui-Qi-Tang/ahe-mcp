package sourcepilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBriefExcerptSelectsExactRangeFromLargeUnsegmentedParent(t *testing.T) {
	parent := briefFixture()
	prefix := strings.Repeat("Synthetic context.\r\n", 1800)
	selected := "  公告🙂 remains unresolved.\r\nOnly the named region is affected.  "
	parent.Body = prefix + selected + "\r\nRetained parent ending."
	raw, _ := json.Marshal(parent)
	parsed, err := ParseBriefSelectionSource(raw)
	if err != nil || !reflect.DeepEqual(parsed, parent) {
		t.Fatalf("bounded parent should be selectable: %v", err)
	}
	if _, err := NewBriefReport(parent); err == nil {
		t.Fatal("large parent became eligible for model input")
	}
	source, err := SelectBriefExcerpt(parent, len(prefix), len(prefix)+len(selected), "Read the named incident and its unresolved status.")
	if err != nil || source.Body != selected || source.Coverage != "exact_excerpt" || source.Excerpt.ParentBodyBytes != len(parent.Body) {
		t.Fatalf("selected bytes or parent coordinate changed: %v", err)
	}
	if err := VerifyBriefExcerpt(parent, source); err != nil {
		t.Fatal(err)
	}
	report, err := NewBriefReport(source)
	if err != nil || len(report.Projection.Segments) != 2 || report.AuthorityEffect != "none" {
		t.Fatalf("selected input failed ordinary Brief bounds: %v", err)
	}
	assertBriefRoundTrip(t, report)
	var out bytes.Buffer
	if err := WriteBriefText(&out, report); err != nil || !strings.Contains(out.String(), "尚未重新比對父原文") || !strings.Contains(out.String(), source.Excerpt.ParentBodySHA256) {
		t.Fatalf("standalone report must disclose unverified parent relationship: %v", err)
	}
	source.Excerpt.SelectionReason = "caller mutation"
	if report.Source.Excerpt.SelectionReason == source.Excerpt.SelectionReason {
		t.Fatal("report retained mutable excerpt metadata")
	}
}

func TestBriefExcerptRejectsAlteredParentAndRange(t *testing.T) {
	parent := briefFixture()
	parent.Body = "Prelude.\n公告🙂 unresolved.\nAfterward."
	start := len("Prelude.\n")
	end := start + len("公告🙂 unresolved.")
	source, err := SelectBriefExcerpt(parent, start, end, "Named incident.")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*BriefSource){
		"body":     func(s *BriefSource) { s.Body = strings.Replace(s.Body, "Afterward", "Following", 1) },
		"revision": func(s *BriefSource) { s.SourceRevision = "another revision" },
		"identity": func(s *BriefSource) { s.SourceID = "another source" },
		"URL":      func(s *BriefSource) { s.SourceURL = "https://example.invalid/another" },
		"time":     func(s *BriefSource) { s.ObservedAt = "2026-09-11T00:00:00Z" },
		"kind":     func(s *BriefSource) { s.SourceKind = "public_event" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := parent
			mutate(&changed)
			if VerifyBriefExcerpt(changed, source) == nil {
				t.Fatal("changed parent passed offline revalidation")
			}
		})
	}
	for _, bounds := range [][2]int{{-1, end}, {start, start}, {end, start}, {start + 1, end}, {start, end - len(" unresolved.") - 1}, {0, len(parent.Body) + 1}} {
		if _, err := SelectBriefExcerpt(parent, bounds[0], bounds[1], "Reason."); err == nil {
			t.Fatalf("invalid byte range accepted: %v", bounds)
		}
	}
	if _, err := SelectBriefExcerpt(source, 0, len(source.Body), "Nested excerpt."); err == nil {
		t.Fatal("excerpt used as full-document parent")
	}
	changed := source
	coordinate := *source.Excerpt
	changed.Excerpt = &coordinate
	changed.Excerpt.StartByte++
	changed.Excerpt.EndByte++
	if VerifyBriefExcerpt(parent, changed) == nil {
		t.Fatal("same-length shifted range passed")
	}
}

func TestBriefExcerptStrictNestedShape(t *testing.T) {
	parent := briefFixture()
	source, err := SelectBriefExcerpt(parent, 0, 20, "Reason.")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	for _, bad := range []string{
		strings.Replace(string(raw), `"start_byte":0`, `"start_byte":null`, 1),
		strings.Replace(string(raw), `"start_byte":0,`, "", 1),
		strings.Replace(string(raw), `"start_byte":0`, `"start_byte":0,"start_byte":1`, 1),
		strings.Replace(string(raw), `"start_byte":0`, `"start_byte":0,"verified":true`, 1),
		strings.Replace(string(raw), `"selection_reason":"Reason."`, `"selection_reason":""`, 1),
		strings.Replace(string(raw), `"coverage":"exact_excerpt"`, `"coverage":"full_document"`, 1),
		strings.Replace(string(raw), `"parent_source_revision":"fixture-v1"`, `"parent_source_revision":"wrong"`, 1),
		strings.Replace(string(raw), `"parent_body_bytes":`, `"ParentBodyBytes":`, 1),
	} {
		if _, err := ParseBriefSource([]byte(bad)); err == nil {
			t.Fatal("malformed excerpt shape accepted")
		}
	}
	legacy := briefFixture()
	legacy.Version, legacy.SourceKind = "detective-brief-source/v1", ""
	legacy.Excerpt = source.Excerpt
	if validateBriefSource(legacy) == nil {
		t.Fatal("legacy v1 acquired new excerpt fields")
	}
}

func TestBriefExcerptPreservesCoverageLimitationsContract(t *testing.T) {
	parent := briefFixture()
	for _, count := range []int{2, 8} {
		changed := parent
		changed.Limitations = make([]string, count)
		for i := range changed.Limitations {
			changed.Limitations[i] = "Caller source limitation."
		}
		if _, err := SelectBriefExcerpt(changed, 0, 20, "Read the selected range."); err == nil {
			t.Fatal("selection accepted a full_document parent with contradictory nonempty limitations")
		}
	}
	source, err := SelectBriefExcerpt(parent, 0, 20, "Read the selected range.")
	if err != nil {
		t.Fatal(err)
	}
	for _, limitations := range [][]string{{}, {"Replaced original range limitation."}, {source.Limitations[0], "Unrecorded extra limitation."}} {
		changed := source
		changed.Limitations = limitations
		if VerifyBriefExcerpt(parent, changed) == nil {
			t.Fatal("offline verification allowed altered coverage limitations")
		}
	}
}

func TestBriefLimitsAreExactAndDoNotRetainPartialProjection(t *testing.T) {
	for _, delimiter := range []string{"\n", "\r", "\r\n", "<br>", "<BR/>", "<br />"} {
		body := strings.Repeat("  公告🙂  "+delimiter+delimiter, BriefSegmentLimit)
		projection, err := SegmentBody(body)
		if err != nil || len(projection.Segments) != BriefSegmentLimit {
			t.Fatalf("exact paragraph bound failed for %q: %v", delimiter, err)
		}
		body += "final" + delimiter + "one more"
		projection, err = SegmentBody(body)
		var limit *InputLimitError
		if !errors.As(err, &limit) || limit.Resource != "nonempty_segments" || limit.Observed != 66 || limit.Limit != 64 || limit.AtLeast || !reflect.DeepEqual(projection, SegmentedBody{}) {
			t.Fatalf("paragraph diagnostic lost count or returned partial result: %#v %v", limit, err)
		}
		source := briefFixture()
		source.Body = body
		raw, _ := json.Marshal(source)
		if _, err := ParseBriefSource(raw); !errors.As(err, &limit) || limit.Resource != "nonempty_segments" {
			t.Fatal("source validation erased segmentation error")
		}
	}
	parent := briefFixture()
	parent.Body = strings.Repeat("x", BriefBodyLimit)
	if _, err := NewBriefReport(parent); err != nil {
		t.Fatal("exact body limit failed", err)
	}
	parent.Body += "x"
	var limit *InputLimitError
	if _, err := NewBriefReport(parent); !errors.As(err, &limit) || limit.Resource != "body_bytes" || limit.Observed != BriefBodyLimit+1 {
		t.Fatal("body overflow was generalized", err)
	}
	parent.Body = strings.Repeat("row\n", 65)
	if _, err := SelectBriefExcerpt(parent, 0, len(parent.Body), "All rows."); !errors.As(err, &limit) || limit.Resource != "nonempty_segments" {
		t.Fatal("selection bypassed the child paragraph limit", err)
	}
	parent.Body = strings.Repeat("x", BriefParentBodyLimit)
	if _, err := SelectBriefExcerpt(parent, 0, 1, "One byte."); err != nil {
		t.Fatal("exact parent body limit failed", err)
	}
	parent.Body += "x"
	if _, err := SelectBriefExcerpt(parent, 0, 1, "One byte."); !errors.As(err, &limit) || limit.Resource != "parent_body_bytes" {
		t.Fatal("parent body bound was relaxed", err)
	}
	if _, err := ParseBriefSelectionSource(bytes.Repeat([]byte(" "), BriefParentJSONLimit+1)); !errors.As(err, &limit) || limit.Resource != "parent_json_bytes" {
		t.Fatal("parent JSON bound was relaxed", err)
	}
}
