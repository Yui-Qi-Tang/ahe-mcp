package sourcepilot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestGitHubProjectionAndSelection(t *testing.T) {
	update := func(incident, id, body, created string) IncidentUpdate {
		return IncidentUpdate{IncidentID: incident, ID: id, Body: body, Status: "investigating", CreatedAt: created, UpdatedAt: created}
	}
	incidents := []Incident{
		{ID: "old", UpdatedAt: "2026-09-09T10:00:00Z", Updates: []IncidentUpdate{update("old", "u3", "Old update.", "2026-09-09T00:00:00Z")}},
		{ID: "new", UpdatedAt: "2026-09-10T10:00:00Z", Updates: []IncidentUpdate{
			update("new", "u2", "Recovered; monitoring continues.", "2026-09-10T02:00:00Z"),
			update("new", "u1", "Errors increased.\nThe cause is unknown.", "2026-09-10T01:00:00Z"),
		}},
		{ID: "empty", UpdatedAt: "2026-09-10T11:00:00Z", Updates: []IncidentUpdate{}},
	}
	raw, err := json.Marshal(map[string]any{"incidents": incidents, "future_field": true})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseGitHub(raw)
	if err != nil || !reflect.DeepEqual(parsed, incidents) {
		t.Fatalf("parse: %v / %#v", err, parsed)
	}
	selected := SelectGitHub(parsed)
	if len(selected) != 2 || selected[0].ID != "u1" || selected[1].ID != "u3" {
		t.Fatalf("selection: %#v", selected)
	}
	if !reflect.DeepEqual(parsed, incidents) {
		t.Fatal("selection mutated source order")
	}
	lines, err := Lines(selected[0].Body)
	if err != nil || len(lines) != 2 || lines[1].Text != "The cause is unknown." {
		t.Fatalf("lines: %v %#v", err, lines)
	}
	input, _ := json.Marshal(struct {
		Lines []Line `json:"lines"`
	}{lines})
	for _, forbidden := range []string{"investigating", "incident_id", "created_at", "updated_at", "2026-"} {
		if strings.Contains(string(input), forbidden) {
			t.Fatalf("metadata entered input: %s", forbidden)
		}
	}
}

func TestGitHubRejectsMalformedSource(t *testing.T) {
	base := `{"incidents":[{"id":"a","updated_at":"2026-09-10T00:00:00Z","incident_updates":[{"incident_id":"a","id":"u","body":"Service errors increased.","status":"investigating","created_at":"2026-09-10T00:00:00Z","updated_at":"2026-09-10T00:00:00Z"}]}]}`
	for _, raw := range []string{
		`{}`, `{"incidents":null}`, `{"incidents":[],"incidents":[]}`,
		strings.Replace(base, `"incident_id":"a"`, `"incident_id":"wrong"`, 1),
		strings.Replace(base, `"created_at":"2026-09-10T00:00:00Z"`, `"created_at":null`, 1),
		strings.Replace(base, `"body":"Service errors increased."`, `"body":42`, 1),
		strings.Replace(base, `"body":"Service errors increased."`, `"body":null`, 1),
		strings.Replace(base, `"body":"Service errors increased.",`, ``, 1),
		strings.Replace(base, `"status":"investigating"`, `"status":null`, 1),
		base + `{}`, strings.Repeat(" ", 1<<20) + base,
	} {
		if _, err := ParseGitHub([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed source: %.100s", raw)
		}
	}
	if got, err := ParseGitHub([]byte(`{"incidents":[]}`)); err != nil || got == nil || len(got) != 0 {
		t.Fatal("empty feed must remain a valid empty result")
	}
}

func TestSummaryContract(t *testing.T) {
	lines, _ := Lines("Errors increased.\n\nThe cause remains unknown.")
	valid := `{"items":[{"line":3,"summary":"原因仍不明。"}],"reason":""}`
	got, err := ParseSummary(valid, lines)
	if err != nil || got.Items[0].Line != 3 || lines[got.Items[0].Line-1].Text != "The cause remains unknown." {
		t.Fatalf("parse: %#v %v", got, err)
	}
	if _, err := ParseSummary(`{"items":[],"reason":"只有導覽"}`, lines); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"items":[],"reason":""}`, `{"items":null,"reason":"none"}`,
		`{"items":[],"reason":null}`, `{"items":[],"reason":"none","admit":true}`,
		strings.Replace(valid, `"line":3`, `"line":2`, 1),
		strings.Replace(valid, `"line":3`, `"line":4`, 1),
		strings.Replace(valid, `"line":3`, `"line":3.5`, 1),
		strings.Replace(valid, `"line":3`, `"line":3,"line":1`, 1),
		strings.Replace(valid, `"line":3`, `"Line":3`, 1),
		strings.Replace(valid, `"line":3`, `"line":3,"Line":1`, 1),
		strings.Replace(valid, `"summary":"原因仍不明。"`, `"summary":"`+strings.Repeat("x", 513)+`"`, 1),
		strings.Replace(valid, `"reason":""`, `"reason":" "`, 1),
		strings.Replace(valid, `"summary":"原因仍不明。"`, `"summary":null`, 1),
		strings.Replace(valid, `"reason":""`, `"reason":"also abstained"`, 1),
		"```json\n" + valid + "\n```", valid + "{}",
	} {
		if _, err := ParseSummary(raw, lines); err == nil {
			t.Fatalf("accepted malformed output: %s", raw)
		}
	}
}
