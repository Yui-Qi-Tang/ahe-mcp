//go:build integration

package mcpintegration

import (
	"slices"
	"strings"
	"testing"
)

func practicalAnchorSQLControls() []multisurfaceSQLControl {
	row := func(id, statement, body string) map[string]any {
		return map[string]any{"id": id, "statement": statement, "body": body, "snapshot": "snapshot-" + id,
			"source": "source", "version": "v1", "outcome": "admitted", "repo": "", "generation": "",
			"active": false, "span_count": 1, "span_bytes": 1}
	}
	control := func(name string, rows []map[string]any, ids ...string) multisurfaceSQLControl {
		return multisurfaceSQLControl{name: name, rows: rows, ids: ids, han: []string{"影響", "火山"}, anchors: []string{"火山"},
			filters: [7]string{"", "", "", "", "", "", "all"}, limit: 21, searched: len(rows), minimumEnglish: 2}
	}
	controls := []multisurfaceSQLControl{
		control("anchor-generic-only-partial-rejected", []map[string]any{row("unrelated", "影響", "other")}),
		control("anchor-one-topic-statement-preserved", []map[string]any{row("topic", "火山", "other")}, "topic"),
		control("anchor-one-topic-source-preserved", []map[string]any{row("topic", "other", "火山")}, "topic"),
	}
	for _, auxiliary := range practicalAnchorAuxiliaryTerms() {
		c := control("anchor-auxiliary-variant-"+auxiliary, []map[string]any{row("unrelated", auxiliary, auxiliary)})
		c.han = []string{auxiliary, "火山"}
		controls = append(controls, c)
	}
	short := control("anchor-short-auxiliary-only-query-preserved", []map[string]any{row("broad", "影響", "other")}, "broad")
	short.han, short.anchors = []string{"影響"}, []string{"影響"}
	controls = append(controls, short)
	mixed := control("anchor-mixed-english-not-auxiliary-alone", []map[string]any{row("unrelated", "影響", "other")})
	mixed.han, mixed.anchors, mixed.terms = []string{"影響"}, nil, []string{"alpha", "beta"}
	controls = append(controls, mixed)
	english := control("anchor-mixed-english-overlap-preserved", []map[string]any{row("english", "影響 alpha beta", "other")}, "english")
	english.han, english.anchors, english.terms, english.scores = []string{"影響"}, nil, []string{"alpha", "beta"}, []int{3}
	controls = append(controls, english)
	fallback := control("anchor-mixed-english-fallback-remains-lexical", []map[string]any{row("english", "影響 alpha", "other"), row("unrelated", "影響", "other")}, "english")
	fallback.han, fallback.anchors, fallback.terms, fallback.scores, fallback.minimumEnglish = []string{"影響"}, nil, []string{"alpha", "beta"}, []int{2}, 1
	controls = append(controls, fallback)
	score := control("anchor-retains-auxiliary-score-and-full-terms", []map[string]any{row("combined", "影響 火山", "影響")}, "combined")
	score.scores = []int{2}
	controls = append(controls, score)
	limit := control("anchor-filter-before-limit-keeps-topic-survivor", nil, "Z-topic")
	limit.han = []string{"影響", "造成", "是否", "火山"}
	for _, id := range []string{"A", "B", "C", "D", "E", "F"} {
		limit.rows = append(limit.rows, row(id, "影響 造成 是否", "other"))
	}
	limit.rows = append(limit.rows, row("Z-topic", "火山", "other"))
	limit.limit, limit.searched, limit.scores = 1, len(limit.rows), []int{1}
	controls = append(controls, limit)
	for _, c := range multisurfaceSQLControls() {
		if !strings.HasPrefix(c.name, "filter-") && !strings.HasPrefix(c.name, "lifecycle-") {
			continue
		}
		c.name = "anchor-" + c.name
		c.terms, c.han, c.anchors = nil, []string{"影響", "火山"}, []string{"火山"}
		for _, r := range c.rows {
			r["statement"] = "火山"
		}
		controls = append(controls, c)
	}
	return controls
}

func TestPracticalAnchorSQLControlPlan(t *testing.T) {
	controls := practicalAnchorSQLControls()
	if len(controls) != 31 {
		t.Fatalf("anchor SQL plan changed: %d controls", len(controls))
	}
	seen := map[string]bool{}
	for _, c := range controls {
		if seen[c.name] {
			t.Fatal("duplicate anchor SQL control")
		}
		seen[c.name] = true
		for _, anchor := range c.anchors {
			if !slices.Contains(c.han, anchor) {
				t.Fatal("SQL anchor is not an original Han term")
			}
		}
		multisurfaceSQLFixture(t, c)
	}
}

func TestIntegrationPracticalAnchorSQLControls(t *testing.T) {
	controls := append(multisurfaceSQLControls(), practicalSQLControls()...)
	controls = append(controls, practicalAnchorSQLControls()...)
	runSelectedMultisurfaceSQLControls(t, "AHE_PRACTICAL_ANCHOR_SQL_CONTROLS", controls)
}
