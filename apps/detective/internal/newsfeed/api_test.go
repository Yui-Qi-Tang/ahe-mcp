package newsfeed

import (
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func apiFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/nasa_news_releases_api.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAPISnapshotPreservesRawJSONAndDisplayStrings(t *testing.T) {
	raw := apiFixture(t)
	snapshot, err := NewAPISnapshot(nasaAPIID, raw, 1, time.Date(2026, 9, 10, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != APISchemaVersion || snapshot.RawJSON != string(raw) || snapshot.RawXML != "" || snapshot.RawSHA256 != digest(raw) || len(snapshot.Items) != 1 || snapshot.Items[0].PublishedAt != "2026-09-10T02:00:00" || snapshot.Items[0].Description != "<p>Synthetic NASA API summary &amp; retained HTML; not an actual NASA statement.</p>\n" {
		t.Fatal("API snapshot changed raw JSON, HTML strings, or publication metadata")
	}
	body, _ := json.Marshal(snapshot)
	if strings.Contains(string(body), `"raw_xml"`) || !strings.Contains(string(body), `"raw_json"`) {
		t.Fatal("API response was disguised as XML")
	}
	parsed, err := ParseSnapshot(string(body))
	if err != nil || !reflect.DeepEqual(parsed, snapshot) {
		t.Fatalf("v2 round trip failed: %v", err)
	}
	endpoint, err := url.Parse(snapshot.FeedURL)
	wantQuery := url.Values{"context": {"view"}, "page": {"1"}, "per_page": {"1"}, "orderby": {"date"}, "order": {"desc"}, "_fields": {"id,date_gmt,link,title.rendered,excerpt.rendered,excerpt.protected"}}
	if err != nil || endpoint.Scheme != "https" || endpoint.Host != "www.nasa.gov" || endpoint.Path != "/wp-json/wp/v2/press-release" || !reflect.DeepEqual(endpoint.Query(), wantQuery) || endpoint.RawQuery != wantQuery.Encode() {
		t.Fatal("API endpoint did not bind exact fixed first-page query")
	}
	for _, id := range []string{"bbc_world", "nasa_news_releases"} {
		rss := fixture(t)
		if id == "nasa_news_releases" {
			rss = nasaFixture(t)
		}
		legacy, err := NewSnapshot(id, rss, 1, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(legacy)
		if strings.Contains(string(encoded), `"raw_json"`) || !strings.Contains(string(encoded), `"raw_xml"`) {
			t.Fatal("legacy v1 serialization changed")
		}
		if got, err := ParseSnapshot(string(encoded)); err != nil || !reflect.DeepEqual(got, legacy) {
			t.Fatal("legacy RSS compatibility lost")
		}
	}
}

func TestAPIRejectsUnsealedOrPrivateItems(t *testing.T) {
	raw := string(apiFixture(t))
	for name, source := range map[string]string{
		"missing-id":             strings.Replace(raw, `"id": 1001,`, "", 1),
		"zero-id":                strings.Replace(raw, `"id": 1001`, `"id": 0`, 1),
		"negative-id":            strings.Replace(raw, `"id": 1001`, `"id": -1`, 1),
		"fraction-id":            strings.Replace(raw, `"id": 1001`, `"id": 1.5`, 1),
		"string-id":              strings.Replace(raw, `"id": 1001`, `"id": "1001"`, 1),
		"null-id":                strings.Replace(raw, `"id": 1001`, `"id": null`, 1),
		"duplicate-id":           strings.Replace(raw, `"id": 1001`, `"id": 1001,"id": 1002`, 1),
		"alias-id":               strings.Replace(raw, `"id": 1001`, `"ID": 1001`, 1),
		"extra-field":            strings.Replace(raw, `"id": 1001`, `"id": 1001,"content":"not selected"`, 1),
		"null-title":             strings.Replace(raw, `{"rendered": "Synthetic mission briefing scheduled"}`, `null`, 1),
		"null-title-rendered":    strings.Replace(raw, `"rendered": "Synthetic mission briefing scheduled"`, `"rendered": null`, 1),
		"missing-title-rendered": strings.Replace(raw, `{"rendered": "Synthetic mission briefing scheduled"}`, `{}`, 1),
		"extra-title":            strings.Replace(raw, `"rendered": "Synthetic mission briefing scheduled"`, `"rendered": "ok","raw":"not selected"`, 1),
		"duplicate-title":        strings.Replace(raw, `"rendered": "Synthetic mission briefing scheduled"`, `"rendered": "first","rendered": "second"`, 1),
		"unicode-title":          strings.Replace(raw, `Synthetic mission briefing scheduled`, `\ud800`, 1),
		"protected":              strings.Replace(raw, `"protected": false`, `"protected": true`, 1),
		"null-protected":         strings.Replace(raw, `"protected": false`, `"protected": null`, 1),
		"missing-protected":      strings.Replace(raw, `, "protected": false`, ``, 1),
		"duplicate-protected":    strings.Replace(raw, `"protected": false`, `"protected": false,"protected": true`, 1),
		"invalid-date":           strings.Replace(raw, `2026-09-10T02:00:00`, `not-a-date`, 1),
		"credential-url":         strings.Replace(raw, `https://www.nasa.gov/news-release/synthetic-api-briefing/`, `https://user:password@example.invalid/article`, 1),
		"trailing":               raw + `{}`, "null": `null`, "object": `{}`, "not-json": "error page",
		"oversize": raw + strings.Repeat(" ", maxAPIBytes),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAPISnapshot(nasaAPIID, []byte(source), 1, time.Now()); err == nil {
				t.Fatal("invalid API source accepted")
			}
		})
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &entries) != nil {
		t.Fatal("invalid test fixture")
	}
	for _, field := range []string{"id", "date_gmt", "link", "title", "excerpt"} {
		var copy []map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &copy)
		delete(copy[0], field)
		body, _ := json.Marshal(copy)
		if _, err := NewAPISnapshot(nasaAPIID, body, 1, time.Now()); err == nil {
			t.Fatalf("missing API field %s accepted", field)
		}
	}
	for _, excerpt := range []string{
		`null`, `[]`, `{}`, `{"rendered":null,"protected":false}`,
		`{"rendered":12,"protected":false}`, `{"rendered":"text","protected":"false"}`,
		`{"rendered":"text","protected":false,"unknown":1}`,
	} {
		var copy []map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &copy)
		copy[0]["excerpt"] = json.RawMessage(excerpt)
		body, _ := json.Marshal(copy)
		if _, err := NewAPISnapshot(nasaAPIID, body, 1, time.Now()); err == nil {
			t.Fatal("null, mistyped, or non-closed excerpt accepted")
		}
	}
	tooMany, _ := json.Marshal(append(entries, entries[0]))
	if _, err := NewAPISnapshot(nasaAPIID, tooMany, 1, time.Now()); err == nil {
		t.Fatal("response with more than per_page items was silently truncated")
	}
}

func TestAPISnapshotRejectsRewrittenProjectionAndMixedRawFormats(t *testing.T) {
	snapshot, err := NewAPISnapshot(nasaAPIID, apiFixture(t), 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.RawJSON += " " }, func(s *Snapshot) { s.RawSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.Items[0].Title = "rewritten" }, func(s *Snapshot) { s.Items[0].Description = "rewritten" },
		func(s *Snapshot) { s.Items[0].PublishedAt = "event time" }, func(s *Snapshot) { s.Items[0].ID = "source-lineage" },
		func(s *Snapshot) { s.FeedURL += "&page=2" }, func(s *Snapshot) { s.Limit = 2 },
		func(s *Snapshot) { s.RawXML = s.RawJSON }, func(s *Snapshot) { s.RawJSON = "" },
		func(s *Snapshot) { s.SchemaVersion = SchemaVersion; s.RawXML, s.RawJSON = s.RawJSON, "" },
	} {
		var changed Snapshot
		_ = json.Unmarshal(encoded, &changed)
		mutate(&changed)
		body, _ := json.Marshal(changed)
		if _, err := ParseSnapshot(string(body)); err == nil {
			t.Fatal("API projection tampering or mixed raw formats accepted")
		}
	}
	for _, sourceID := range []string{"bbc_world", "nasa_news_releases", "unknown"} {
		if _, err := NewAPISnapshot(sourceID, apiFixture(t), 1, time.Now()); err == nil {
			t.Fatal("legacy source relabeled as API")
		}
	}
}
