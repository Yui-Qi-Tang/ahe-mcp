package sourcepilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const maxUSGSBytes = 1 << 20

// Earthquake preserves selected USGS source fields without a model interpretation.
// Times are Unix milliseconds; coordinates are longitude, latitude, and depth in
// kilometers. Nil optional fields represent source nulls unless MissingFields
// names the absent property. USGS Status is not an AHE review or admission state.
type Earthquake struct {
	ID            string     `json:"id"`
	TimeMS        int64      `json:"time_ms"`
	UpdatedMS     int64      `json:"updated_ms"`
	Magnitude     *float64   `json:"mag"`
	Place         *string    `json:"place"`
	Status        *string    `json:"status"`
	URL           *string    `json:"url"`
	Coordinates   [3]float64 `json:"coordinates"`
	MissingFields []string   `json:"missing_fields"`
}

// ParseUSGS parses a bounded GeoJSON summary without fetching or inferring data.
// All features must validate; an empty feature array is a successful empty result.
// Unknown properties are allowed so upstream additions do not break the pilot.
func ParseUSGS(raw []byte) ([]Earthquake, error) {
	if len(raw) == 0 || len(raw) > maxUSGSBytes || !utf8.Valid(raw) {
		return nil, errors.New("invalid USGS input size or encoding")
	}
	collection, err := usgsObject(raw)
	if err != nil || !usgsType(collection["type"], "FeatureCollection") {
		return nil, errors.New("invalid USGS feature collection")
	}
	var features []json.RawMessage
	if err := json.Unmarshal(collection["features"], &features); err != nil || features == nil {
		return nil, errors.New("invalid USGS features array")
	}
	result := make([]Earthquake, 0, len(features))
	seen := make(map[string]bool, len(features))
	for i, feature := range features {
		quake, err := usgsFeature(feature)
		if err != nil {
			return nil, fmt.Errorf("USGS feature %d: %w", i, err)
		}
		if seen[quake.ID] {
			return nil, fmt.Errorf("duplicate USGS feature id at index %d", i)
		}
		seen[quake.ID] = true
		result = append(result, quake)
	}
	return result, nil
}

func usgsFeature(raw []byte) (Earthquake, error) {
	bad := errors.New("invalid USGS feature fields")
	feature, err := usgsObject(raw)
	if err != nil || !usgsType(feature["type"], "Feature") {
		return Earthquake{}, bad
	}
	quake := Earthquake{MissingFields: []string{}}
	if json.Unmarshal(feature["id"], &quake.ID) != nil || strings.TrimSpace(quake.ID) == "" {
		return Earthquake{}, bad
	}
	properties, err := usgsObject(feature["properties"])
	if err != nil || !usgsMillis(properties["time"], &quake.TimeMS) || !usgsMillis(properties["updated"], &quake.UpdatedMS) {
		return Earthquake{}, bad
	}
	for _, field := range []struct {
		name string
		into any
	}{
		{"mag", &quake.Magnitude}, {"place", &quake.Place},
		{"status", &quake.Status}, {"url", &quake.URL},
	} {
		value, present := properties[field.name]
		if !present {
			quake.MissingFields = append(quake.MissingFields, field.name)
			continue
		}
		if json.Unmarshal(value, field.into) != nil {
			return Earthquake{}, bad
		}
	}
	if quake.Magnitude != nil && !usgsFinite(*quake.Magnitude) {
		return Earthquake{}, bad
	}
	if quake.URL != nil {
		parsed, err := url.Parse(*quake.URL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
			return Earthquake{}, bad
		}
	}
	geometry, err := usgsObject(feature["geometry"])
	if err != nil || !usgsType(geometry["type"], "Point") {
		return Earthquake{}, bad
	}
	var coordinates []*float64
	if json.Unmarshal(geometry["coordinates"], &coordinates) != nil || len(coordinates) != 3 {
		return Earthquake{}, bad
	}
	for i, coordinate := range coordinates {
		if coordinate == nil || !usgsFinite(*coordinate) {
			return Earthquake{}, bad
		}
		quake.Coordinates[i] = *coordinate
	}
	if math.Abs(quake.Coordinates[0]) > 180 || math.Abs(quake.Coordinates[1]) > 90 {
		return Earthquake{}, bad
	}
	// USGS documents typical magnitude/depth values, not hard bounds; negative
	// values are possible. Keep finite values without imposing a made-up range.
	return quake, nil
}

func usgsMillis(raw []byte, into *int64) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, into) != nil {
		return false
	}
	year := time.UnixMilli(*into).UTC().Year()
	return year >= 1 && year <= 9999
}

func usgsFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func usgsType(raw []byte, want string) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && value == want
}

// usgsObject rejects duplicate keys in interpreted objects instead of silently
// picking the final value. Values of unknown keys remain uninterpreted.
func usgsObject(raw []byte) (map[string]json.RawMessage, error) {
	bad := errors.New("invalid USGS JSON object")
	if !json.Valid(raw) {
		return nil, bad
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, bad
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, bad
		}
		if _, exists := fields[key]; exists {
			return nil, bad
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, bad
		}
		fields[key] = value
	}
	return fields, nil
}
