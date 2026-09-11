package sourcepilot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This invented fixture is not a report of an actual earthquake.
const syntheticUSGSFeature = `{"type":"Feature","id":"synthetic-01","properties":{"time":1788998400123,"updated":1788998460456,"mag":-0.5,"place":"Synthetic test location","status":"automatic","url":"https://earthquake.usgs.gov/earthquakes/eventpage/synthetic-01"},"geometry":{"type":"Point","coordinates":[121.25,23.75,-0.8]}}`

func usgsCollection(feature string) []byte {
	return []byte(`{"type":"FeatureCollection","features":[` + feature + `]}`)
}

func TestParseUSGSPreservesSourceFields(t *testing.T) {
	quakes, err := ParseUSGS(usgsCollection(syntheticUSGSFeature))
	if err != nil || len(quakes) != 1 {
		t.Fatalf("ParseUSGS() = %v, %v", quakes, err)
	}
	quake := quakes[0]
	if quake.ID != "synthetic-01" || quake.TimeMS != 1788998400123 || quake.UpdatedMS != 1788998460456 || quake.Magnitude == nil || *quake.Magnitude != -0.5 || quake.Place == nil || *quake.Place != "Synthetic test location" || quake.Status == nil || *quake.Status != "automatic" || quake.URL == nil || *quake.URL != "https://earthquake.usgs.gov/earthquakes/eventpage/synthetic-01" {
		t.Fatalf("source fields changed: %+v", quake)
	}
	if quake.Coordinates != [3]float64{121.25, 23.75, -0.8} || len(quake.MissingFields) != 0 {
		t.Fatal("coordinate order, negative depth, or field presence changed")
	}
	if got := time.UnixMilli(quake.TimeMS).UTC().Format(time.RFC3339Nano); got != "2026-09-10T00:00:00.123Z" {
		t.Fatalf("Unix milliseconds or timezone changed: %s", got)
	}
	encoded, err := json.Marshal(quake)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"time_ms":1788998400123`, `"updated_ms":1788998460456`, `"mag":-0.5`, `"coordinates":[121.25,23.75,-0.8]`} {
		if !strings.Contains(string(encoded), fragment) {
			t.Errorf("JSON lost %s: %s", fragment, encoded)
		}
	}
}

func TestParseUSGSOptionalNullMissingAndZeroStayDistinct(t *testing.T) {
	for _, field := range []string{"mag", "place", "status", "url"} {
		t.Run(field, func(t *testing.T) {
			var feature map[string]json.RawMessage
			if err := json.Unmarshal([]byte(syntheticUSGSFeature), &feature); err != nil {
				t.Fatal(err)
			}
			var properties map[string]json.RawMessage
			if err := json.Unmarshal(feature["properties"], &properties); err != nil {
				t.Fatal(err)
			}
			for _, missing := range []bool{false, true} {
				properties[field] = json.RawMessage("null")
				if missing {
					delete(properties, field)
				}
				var err error
				feature["properties"], err = json.Marshal(properties)
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(feature)
				if err != nil {
					t.Fatal(err)
				}
				quakes, err := ParseUSGS(usgsCollection(string(body)))
				if err != nil || len(quakes) != 1 {
					t.Fatalf("nullable property rejected: %v", err)
				}
				wantMissing := []string{}
				if missing {
					wantMissing = []string{field}
				}
				if !reflect.DeepEqual(quakes[0].MissingFields, wantMissing) {
					t.Fatalf("missing and null conflated: %+v", quakes[0])
				}
				encoded, err := json.Marshal(quakes[0])
				if err != nil || !strings.Contains(string(encoded), `"`+field+`":null`) {
					t.Fatalf("absent value replaced: %s, %v", encoded, err)
				}
			}
		})
	}
	zero := strings.NewReplacer(`"mag":-0.5`, `"mag":0`, `1788998400123`, `0`, `1788998460456`, `0`, `[121.25,23.75,-0.8]`, `[0,0,0]`).Replace(syntheticUSGSFeature)
	quakes, err := ParseUSGS(usgsCollection(zero))
	if err != nil || len(quakes) != 1 || quakes[0].Magnitude == nil || *quakes[0].Magnitude != 0 || quakes[0].TimeMS != 0 || quakes[0].Coordinates != [3]float64{} {
		t.Fatalf("explicit zero was treated as missing: %v, %v", quakes, err)
	}
}

func TestParseUSGSEmptyAndExtendedCollections(t *testing.T) {
	quakes, err := ParseUSGS(usgsCollection(""))
	if err != nil || quakes == nil || len(quakes) != 0 {
		t.Fatalf("empty feed failed: %v, %v", quakes, err)
	}
	extended := strings.Replace(syntheticUSGSFeature, `"mag":-0.5`, `"mag":-0.5,"new_property":{"value":true}`, 1)
	if _, err := ParseUSGS(usgsCollection(extended)); err != nil {
		t.Fatalf("upstream additional property rejected: %v", err)
	}
}

func TestParseUSGSRejectsInvalidFeatures(t *testing.T) {
	for name, replacement := range map[string][2]string{
		"feature-type":          {`"type":"Feature"`, `"type":"Point"`},
		"point-type":            {`"type":"Point"`, `"type":"LineString"`},
		"missing-id":            {`"id":"synthetic-01",`, ``},
		"null-id":               {`"id":"synthetic-01"`, `"id":null`},
		"blank-id":              {`"id":"synthetic-01"`, `"id":" "`},
		"missing-time":          {`"time":1788998400123,`, ``},
		"null-time":             {`"time":1788998400123`, `"time":null`},
		"fraction-time":         {`1788998400123`, `1788998400123.5`},
		"overflow-time":         {`1788998400123`, `9223372036854775808`},
		"unformattable-time":    {`1788998400123`, `9223372036854775807`},
		"missing-updated":       {`"updated":1788998460456,`, ``},
		"null-updated":          {`"updated":1788998460456`, `"updated":null`},
		"longitude":             {`[121.25,23.75,-0.8]`, `[180.1,23.75,1]`},
		"latitude":              {`[121.25,23.75,-0.8]`, `[121.25,-90.1,1]`},
		"short-coordinate":      {`[121.25,23.75,-0.8]`, `[121.25,23.75]`},
		"long-coordinate":       {`[121.25,23.75,-0.8]`, `[121.25,23.75,1,2]`},
		"null-coordinate":       {`[121.25,23.75,-0.8]`, `[null,23.75,1]`},
		"string-coordinate":     {`[121.25,23.75,-0.8]`, `["121.25",23.75,1]`},
		"infinite-depth":        {`[121.25,23.75,-0.8]`, `[121.25,23.75,1e999]`},
		"infinite-mag":          {`"mag":-0.5`, `"mag":1e999`},
		"string-mag":            {`"mag":-0.5`, `"mag":"-0.5"`},
		"wrong-status-type":     {`"status":"automatic"`, `"status":true`},
		"duplicate-property":    {`"mag":-0.5`, `"mag":-0.5,"mag":4`},
		"duplicate-object-type": {`"type":"Point"`, `"type":"Point","type":"Point"`},
		"unsafe-url":            {`https://earthquake.usgs.gov/earthquakes/eventpage/synthetic-01`, `javascript:alert(1)`},
		"credential-url":        {`https://earthquake.usgs.gov/earthquakes/eventpage/synthetic-01`, `https://user:pass@example.invalid/event`},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := strings.Replace(syntheticUSGSFeature, replacement[0], replacement[1], 1)
			if _, err := ParseUSGS(usgsCollection(invalid)); err == nil {
				t.Fatal("invalid feature accepted")
			}
		})
	}
}

func TestParseUSGSRejectsInvalidCollections(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty-input": nil, "null": []byte(`null`), "missing": []byte(`{}`),
		"wrong-type":         []byte(`{"type":"Feature","features":[]}`),
		"missing-features":   []byte(`{"type":"FeatureCollection"}`),
		"null-features":      []byte(`{"type":"FeatureCollection","features":null}`),
		"null-feature":       usgsCollection("null"),
		"duplicate-id":       usgsCollection(syntheticUSGSFeature + "," + syntheticUSGSFeature),
		"trailing-json":      append(usgsCollection(""), []byte(`{}`)...),
		"duplicate-features": []byte(`{"type":"FeatureCollection","features":[],"features":[]}`),
		"oversize":           []byte(strings.Repeat(" ", maxUSGSBytes+1)),
		"invalid-utf8":       []byte{'{', '"', 0xff, '"', ':', '1', '}'},
	} {
		t.Run(name, func(t *testing.T) {
			if quakes, err := ParseUSGS(raw); err == nil || quakes != nil {
				t.Fatalf("invalid collection returned partial data: %v, %v", quakes, err)
			}
		})
	}
}
