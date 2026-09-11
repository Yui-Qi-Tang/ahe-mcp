package newsfeed

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const nasaAPIID = "nasa_news_releases_api"

func nasaAPIURL(limit int) string {
	query := url.Values{
		"context": {"view"},
		"page":    {"1"}, "per_page": {strconv.Itoa(limit)}, "orderby": {"date"}, "order": {"desc"},
		"_fields": {"id,date_gmt,link,title.rendered,excerpt.rendered,excerpt.protected"},
	}
	return "https://www.nasa.gov/wp-json/wp/v2/press-release?" + query.Encode()
}

// NewAPISnapshot validates an entire bounded first-page API response, retaining
// original HTML strings as text. It does not fetch, strip HTML, or infer events.
func NewAPISnapshot(feedID string, raw []byte, limit int, observed time.Time) (Snapshot, error) {
	if feedID != nasaAPIID || limit < 1 || limit > 10 || observed.IsZero() || observed.Year() < 1 || observed.Year() > 9999 {
		return Snapshot{}, errors.New("invalid news API capture coordinates")
	}
	items, err := parseAPI(raw, limit)
	if err != nil {
		return Snapshot{}, err
	}
	rawDigest := digest(raw)
	for i := range items {
		items[i].ID = digest([]byte(feedID + "\n" + rawDigest + "\n" + strconv.Itoa(i+1)))
	}
	snapshot := Snapshot{SchemaVersion: APISchemaVersion, FeedID: feedID, FeedURL: nasaAPIURL(limit),
		CapturedAt: observed.UTC().Format(time.RFC3339Nano), RawJSON: string(raw),
		RawSHA256: rawDigest, Limit: limit, Items: items}
	body, err := json.Marshal(snapshot)
	if err != nil || len(body) > MaxSnapshotBytes {
		return Snapshot{}, errors.New("news API snapshot exceeds its bound")
	}
	return snapshot, nil
}

func parseAPI(raw []byte, limit int) ([]Item, error) {
	bad := errors.New("news API response does not match the bounded public-item contract")
	if len(raw) == 0 || len(raw) > maxAPIBytes || !utf8.Valid(raw) {
		return nil, bad
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil || len(entries) > limit {
		return nil, bad
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if !exactObject(entry, "id", "date_gmt", "link", "title", "excerpt") {
			return nil, bad
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(entry, &fields) // The exact object was validated above.
		if !exactObject(fields["title"], "rendered") || !exactObject(fields["excerpt"], "rendered", "protected") {
			return nil, bad
		}
		var wire struct {
			ID      int64  `json:"id"`
			DateGMT string `json:"date_gmt"`
			Link    string `json:"link"`
			Title   struct {
				Rendered string `json:"rendered"`
			} `json:"title"`
			Excerpt struct {
				Rendered  string `json:"rendered"`
				Protected bool   `json:"protected"`
			} `json:"excerpt"`
		}
		if json.Unmarshal(entry, &wire) != nil || wire.ID <= 0 || wire.Excerpt.Protected || strings.TrimSpace(wire.Title.Rendered) == "" || !articleURL(wire.Link) {
			return nil, bad
		}
		published, err := time.Parse("2006-01-02T15:04:05", wire.DateGMT)
		if err != nil || published.Format("2006-01-02T15:04:05") != wire.DateGMT {
			return nil, bad
		}
		items = append(items, Item{Title: wire.Title.Rendered, Description: wire.Excerpt.Rendered, URL: wire.Link, PublishedAt: wire.DateGMT})
	}
	return items, nil
}
