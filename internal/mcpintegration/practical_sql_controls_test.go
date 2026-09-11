//go:build integration

package mcpintegration

import (
	"strings"
	"testing"
)

// A smaller overlap threshold must not remove exact scope, source integrity,
// resource limits or a deterministic result limit. All fixtures remain CTEs.
func practicalSQLControls() []multisurfaceSQLControl {
	var controls []multisurfaceSQLControl
	for _, c := range multisurfaceSQLControls() {
		if !strings.HasPrefix(c.name, "filter-") && !strings.HasPrefix(c.name, "lifecycle-") {
			continue
		}
		c.name = "practical-single-term-" + c.name
		c.minimumEnglish = 1
		for _, row := range c.rows {
			row["statement"] = "alpha"
		}
		controls = append(controls, c)
	}
	row := func(id, statement, body string) map[string]any {
		return map[string]any{"id": id, "statement": statement, "body": body, "snapshot": "snapshot-" + id,
			"source": "source", "version": "v1", "outcome": "admitted", "repo": "", "generation": "",
			"active": false, "span_count": 1, "span_bytes": 1}
	}
	control := func(name string, rows []map[string]any, minimum int, ids ...string) multisurfaceSQLControl {
		return multisurfaceSQLControl{name: name, rows: rows, ids: ids, terms: []string{"alpha", "beta"},
			filters: [7]string{"", "", "", "", "", "", "all"}, limit: 21, searched: len(rows), minimumEnglish: minimum}
	}
	controls = append(controls,
		control("research-single-source-term-stays-empty", []map[string]any{row("source", "other", "alpha")}, 2),
		control("practical-single-source-term-recovers", []map[string]any{row("source", "other", "alpha")}, 1, "source"),
		control("research-single-statement-term-stays-empty", []map[string]any{row("statement", "alpha", "other")}, 2),
		control("practical-single-statement-term-recovers", []map[string]any{row("statement", "alpha", "other")}, 1, "statement"),
		control("practical-zero-overlap-stays-empty", []map[string]any{row("none", "other", "other")}, 1),
	)
	empty := control("practical-no-terms-does-not-match-all", []map[string]any{row("none", "alpha", "alpha")}, 1)
	empty.terms = nil
	controls = append(controls, empty)
	tied := control("practical-single-term-dedup-tie-limit", []map[string]any{
		row("C", "alpha", "alpha"), row("B", "other", "alpha"), row("A", "alpha", "other")}, 1, "A", "B")
	tied.limit, tied.scores = 2, []int{1, 1}
	controls = append(controls, tied)
	return controls
}

func TestPracticalSQLControlPlan(t *testing.T) {
	controls := practicalSQLControls()
	if len(controls) != 16 {
		t.Fatal("practical SQL control matrix changed")
	}
	seen := map[string]bool{}
	for _, c := range controls {
		if seen[c.name] || (c.minimumEnglish != 1 && c.minimumEnglish != 2) {
			t.Fatal("duplicate or invalid practical SQL control")
		}
		seen[c.name] = true
		multisurfaceSQLFixture(t, c)
	}
}

func TestIntegrationPracticalSQLControls(t *testing.T) {
	runMultisurfaceSQLControls(t, true)
}
