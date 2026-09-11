package newsfeed

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/bbc_world.xml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func nasaFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/nasa_news_releases.xml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNASASnapshotDoesNotRelabelHistoricalBBC(t *testing.T) {
	when := time.Date(2026, 9, 10, 3, 0, 0, 0, time.UTC)
	nasa, err := NewSnapshot("nasa_news_releases", nasaFixture(t), 2, when)
	if err != nil || nasa.FeedURL != nasaFeedURL || len(nasa.Items) != 2 || nasa.Items[0].Title != "Synthetic mission briefing scheduled" || nasa.Items[0].PublishedAt != "Thu, 10 Sep 2026 02:00:00 +0000" || strings.Contains(nasa.Items[0].Description, "Ignored extended content") {
		t.Fatalf("NASA fixture projection failed: %v", err)
	}
	bbc, err := NewSnapshot("bbc_world", fixture(t), 2, when)
	if err != nil || bbc.FeedURL != bbcFeedURL {
		t.Fatalf("historical BBC fixture lost its original source coordinates: %v", err)
	}
	for _, snapshot := range []Snapshot{nasa, bbc} {
		body, _ := json.Marshal(snapshot)
		parsed, err := ParseSnapshot(string(body))
		if err != nil || !reflect.DeepEqual(parsed, snapshot) {
			t.Fatalf("source snapshot could not be read back unchanged: %v", err)
		}
	}
	bbc.FeedID, bbc.FeedURL = nasa.FeedID, nasa.FeedURL
	relabeled, _ := json.Marshal(bbc)
	if _, err := ParseSnapshot(string(relabeled)); err == nil {
		t.Fatal("BBC snapshot metadata was relabeled as NASA without rederiving its item identities")
	}
}

func TestSnapshotRoundTripPreservesSourceProjection(t *testing.T) {
	raw := fixture(t)
	when := time.Date(2026, 9, 10, 9, 30, 0, 123, time.FixedZone("test", 8*3600))
	snapshot, err := NewSnapshot("bbc_world", raw, 2, when)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RawXML != string(raw) || snapshot.RawSHA256 != digest(raw) || snapshot.CapturedAt != "2026-09-10T01:30:00.000000123Z" || len(snapshot.Items) != 2 {
		t.Fatal("source bytes, digest, capture time, or item count changed")
	}
	item := snapshot.Items[0]
	if item.Title != "Synthetic headline & source boundary" || item.Description != "<p>Synthetic feed summary only; not the full article.</p>" || item.URL != "https://www.bbc.com/news/articles/synthetic-first?x=1&y=2" || item.PublishedAt != "Thu, 10 Sep 2026 01:00:00 GMT" || len(item.ID) != 64 {
		t.Fatal("XML text projection lost its exact meaning or exposed extensions")
	}
	if snapshot.Items[1].PublishedAt != "" || snapshot.Items[1].Description != "Characters & XML <escaping> stay readable." || snapshot.Items[1].ID == item.ID {
		t.Fatal("optional date or item identity was fabricated")
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSnapshot(string(body))
	if err != nil || !reflect.DeepEqual(snapshot, parsed) {
		t.Fatalf("round trip failed: %v", err)
	}
	one, err := NewSnapshot("bbc_world", raw, 1, when)
	if err != nil || len(one.Items) != 1 || one.Items[0] != item || one.RawXML != snapshot.RawXML {
		t.Fatal("limit truncated source bytes or changed a retained item")
	}
}

func TestSnapshotRejectsForgedOrIncompleteJSON(t *testing.T) {
	snapshot, err := NewSnapshot("bbc_world", fixture(t), 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snapshot)
	var base map[string]json.RawMessage
	if json.Unmarshal(encoded, &base) != nil {
		t.Fatal("invalid test fixture")
	}
	for key := range base {
		t.Run("missing-"+key, func(t *testing.T) {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(encoded, &fields)
			delete(fields, key)
			body, _ := json.Marshal(fields)
			if _, err := ParseSnapshot(string(body)); err == nil {
				t.Fatal("missing required field accepted")
			}
		})
	}
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.SchemaVersion = "future" }, func(s *Snapshot) { s.FeedID = "unknown" },
		func(s *Snapshot) { s.FeedURL = "https://example.invalid/feed" }, func(s *Snapshot) { s.CapturedAt = "not-a-date" },
		func(s *Snapshot) { s.RawSHA256 = strings.Repeat("a", 64) }, func(s *Snapshot) { s.RawXML += " " },
		func(s *Snapshot) { s.Limit = 0 }, func(s *Snapshot) { s.Items[0].Title = "forged" },
		func(s *Snapshot) { s.Items[0].ID = strings.Repeat("b", 64) }, func(s *Snapshot) { s.Items[0].URL = "https://example.invalid/forged" },
		func(s *Snapshot) { s.Items[0].PublishedAt = "forged" }, func(s *Snapshot) { s.Items[0].Description = "forged" },
		func(s *Snapshot) { s.Items[0], s.Items[1] = s.Items[1], s.Items[0] }, func(s *Snapshot) { s.Items = nil },
	} {
		var changed Snapshot
		_ = json.Unmarshal(encoded, &changed)
		mutate(&changed)
		body, _ := json.Marshal(changed)
		if _, err := ParseSnapshot(string(body)); err == nil {
			t.Fatal("self-described metadata or items bypassed XML reconstruction")
		}
	}
	for _, body := range []string{
		string(encoded) + "{}", strings.Replace(string(encoded), `"limit":2`, `"limit":2,"limit":2`, 1),
		strings.Replace(string(encoded), `"feed_id":`, `"Feed_ID":`, 1),
		strings.Replace(string(encoded), `"items":[{`, `"items":[{"unknown":1,`, 1),
		strings.Replace(string(encoded), `"description":"`, `"description":null,"other":"`, 1),
		strings.Repeat(" ", MaxSnapshotBytes) + string(encoded),
		`{"invalid":"\ud800"}`, `null`, `[]`,
	} {
		if _, err := ParseSnapshot(body); err == nil {
			t.Fatal("non-closed snapshot JSON accepted")
		}
	}
}

func TestRSSRejectsMalformedOrAmbiguousSource(t *testing.T) {
	raw := string(fixture(t))
	for _, source := range []string{
		"", raw[:len(raw)-10], raw + "<extra/>", strings.Replace(raw, `version="2.0"`, `version="1.0"`, 1),
		`<?xml version="1.0"?>` + raw,
		strings.Replace(raw, `version="2.0"`, `version="2.0" version="2.0"`, 1),
		strings.Replace(raw, `<rss version="2.0"`, `<rss xmlns="urn:fake" version="2.0"`, 1),
		strings.Replace(raw, "<channel>", "<channel/><channel>", 1),
		strings.Replace(raw, "<rss ", "<!DOCTYPE rss [<!ENTITY secret 'injected'>]><rss ", 1),
		strings.Replace(raw, "Second synthetic item", "&custom;", 1),
		strings.Replace(raw, "UTF-8", "ISO-8859-1", 1),
		strings.Replace(raw, "Second synthetic item", "bad\xff", 1),
		strings.Replace(raw, "Second synthetic item", "bad\x00", 1),
		strings.Replace(raw, "Second synthetic item", "<b>nested title</b>", 1),
		strings.Replace(raw, "https://www.bbc.com/news/articles/synthetic-second", "file:///private/unused", 1),
		strings.Replace(raw, "https://www.bbc.com/news/articles/synthetic-second", "https://user:secret@example.invalid/article", 1),
		strings.Replace(raw, "Second synthetic item", strings.Repeat("x", maxXMLBytes), 1),
	} {
		if _, err := NewSnapshot("bbc_world", []byte(source), 2, time.Now()); err == nil {
			t.Fatal("malformed or unsupported RSS accepted")
		}
	}
	for _, field := range []string{"title", "description", "link", "pubDate"} {
		source := strings.Replace(raw, "<item>", "<item><"+field+">duplicate</"+field+">", 1)
		if _, err := NewSnapshot("bbc_world", []byte(source), 2, time.Now()); err == nil {
			t.Fatalf("duplicate RSS field %s silently replaced source", field)
		}
	}
	for _, limit := range []int{0, 11} {
		if _, err := NewSnapshot("bbc_world", []byte(raw), limit, time.Now()); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	if _, err := NewSnapshot("unknown", []byte(raw), 1, time.Now()); err == nil {
		t.Fatal("arbitrary source accepted")
	}
	if _, err := NewSnapshot("bbc_world", []byte(raw), 1, time.Time{}); err == nil {
		t.Fatal("missing observation time accepted")
	}
}

func TestRSSExtensionsNeverOverrideMainFields(t *testing.T) {
	raw := strings.Replace(string(fixture(t)), "<item>", "<item><media:title>not the title</media:title><media:description>not the summary</media:description>", 1)
	snapshot, err := NewSnapshot("bbc_world", []byte(raw), 1, time.Now())
	if err != nil || snapshot.Items[0].Title != "Synthetic headline & source boundary" || snapshot.Items[0].Description != "<p>Synthetic feed summary only; not the full article.</p>" {
		t.Fatalf("RSS extension changed a projected field: %v", err)
	}
}

func TestRSSUTF8BOMRemainsInRetainedBytes(t *testing.T) {
	raw := append([]byte{0xef, 0xbb, 0xbf}, fixture(t)...)
	snapshot, err := NewSnapshot("bbc_world", raw, 1, time.Now())
	if err != nil || snapshot.RawXML != string(raw) || snapshot.RawSHA256 != digest(raw) {
		t.Fatalf("UTF-8 BOM capture changed source bytes: %v", err)
	}
}
