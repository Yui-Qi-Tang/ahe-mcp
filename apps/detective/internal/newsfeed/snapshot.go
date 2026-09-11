// Package newsfeed captures a fixed public news API without following links and
// retains offline compatibility with historical RSS snapshots.
package newsfeed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

// SchemaVersion identifies the complete, reparsable RSS capture contract.
const SchemaVersion = "detective-news-feed/v1"

// APISchemaVersion identifies a complete reparsable JSON API capture.
const APISchemaVersion = "detective-news-feed/v2"

// MaxSnapshotBytes bounds the encoded snapshot, not an upstream document.
const MaxSnapshotBytes = 128 << 10

const (
	bbcFeedURL  = "https://feeds.bbci.co.uk/news/world/rss.xml"
	nasaFeedURL = "https://www.nasa.gov/news-release/feed/"
	maxXMLBytes = 64 << 10
	maxAPIBytes = 64 << 10
)

// Item is a feed entry, not an article's complete text or a verified event.
// PublishedAt preserves RSS pubDate or API date_gmt without inferring event time.
type Item struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
	Description string `json:"description"`
}

// Snapshot retains exact XML (v1) or JSON (v2) and its deterministic projection.
// Its hashes establish local consistency, not independent source authenticity.
type Snapshot struct {
	SchemaVersion string `json:"schema_version"`
	FeedID        string `json:"feed_id"`
	FeedURL       string `json:"feed_url"`
	CapturedAt    string `json:"captured_at"`
	RawXML        string `json:"raw_xml,omitempty"`
	RawJSON       string `json:"raw_json,omitempty"`
	RawSHA256     string `json:"raw_sha256"`
	Limit         int    `json:"limit"`
	Items         []Item `json:"items"`
}

// NewSnapshot validates bounded RSS bytes and derives every item from them.
func NewSnapshot(feedID string, raw []byte, limit int, observed time.Time) (Snapshot, error) {
	sourceURL := snapshotFeedURL(feedID)
	if sourceURL == "" || limit < 1 || limit > 10 || observed.IsZero() || observed.Year() < 1 || observed.Year() > 9999 {
		return Snapshot{}, errors.New("invalid news feed capture coordinates")
	}
	items, err := parseRSS(raw, limit)
	if err != nil {
		return Snapshot{}, err
	}
	rawDigest := digest(raw)
	for i := range items {
		// Bind position to this exact observation, not to invented upstream lineage.
		items[i].ID = digest([]byte(feedID + "\n" + rawDigest + "\n" + strconv.Itoa(i+1)))
	}
	snapshot := Snapshot{SchemaVersion: SchemaVersion, FeedID: feedID, FeedURL: sourceURL,
		CapturedAt: observed.UTC().Format(time.RFC3339Nano), RawXML: string(raw),
		RawSHA256: rawDigest, Limit: limit, Items: items}
	body, err := json.Marshal(snapshot)
	if err != nil || len(body) > MaxSnapshotBytes {
		return Snapshot{}, errors.New("news feed snapshot exceeds its bound")
	}
	return snapshot, nil
}

// ParseSnapshot accepts only a closed complete snapshot contract, then reparses
// its retained source and compares every field. No network or repair occurs.
func ParseSnapshot(text string) (Snapshot, error) {
	bad := errors.New("news feed snapshot does not match its retained source")
	if len(text) > MaxSnapshotBytes || sourcemcp.ValidateRecordedJSON(text) != nil {
		return Snapshot{}, bad
	}
	var snapshot Snapshot
	if json.Unmarshal([]byte(text), &snapshot) != nil || snapshot.Items == nil {
		return Snapshot{}, bad
	}
	rawKey := "raw_xml"
	switch snapshot.SchemaVersion {
	case SchemaVersion:
	case APISchemaVersion:
		rawKey = "raw_json"
	default:
		return Snapshot{}, bad
	}
	if !exactObject([]byte(text), "schema_version", "feed_id", "feed_url", "captured_at", rawKey, "raw_sha256", "limit", "items") {
		return Snapshot{}, bad
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(text), &fields) // The exact object was validated above.
	var items []json.RawMessage
	if json.Unmarshal(fields["items"], &items) != nil {
		return Snapshot{}, bad
	}
	for _, item := range items {
		if !exactObject(item, "id", "title", "url", "published_at", "description") {
			return Snapshot{}, bad
		}
	}
	observed, err := time.Parse(time.RFC3339Nano, snapshot.CapturedAt)
	if err != nil || observed.UTC().Format(time.RFC3339Nano) != snapshot.CapturedAt {
		return Snapshot{}, bad
	}
	var expected Snapshot
	if snapshot.SchemaVersion == SchemaVersion {
		expected, err = NewSnapshot(snapshot.FeedID, []byte(snapshot.RawXML), snapshot.Limit, observed)
	} else {
		expected, err = NewAPISnapshot(snapshot.FeedID, []byte(snapshot.RawJSON), snapshot.Limit, observed)
	}
	if err != nil || !reflect.DeepEqual(snapshot, expected) {
		return Snapshot{}, bad
	}
	return expected, nil
}

func exactObject(raw []byte, names ...string) bool {
	if sourcemcp.ValidateRecordedJSON(string(raw)) != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		value, ok := fields[name]
		if !ok || string(value) == "null" {
			return false
		}
	}
	return true
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Historical BBC fixtures remain readable without enabling BBC network access
// or relabeling retained snapshots as NASA material.
func snapshotFeedURL(feedID string) string {
	switch feedID {
	case "bbc_world":
		return bbcFeedURL
	case "nasa_news_releases":
		return nasaFeedURL
	default:
		return ""
	}
}
