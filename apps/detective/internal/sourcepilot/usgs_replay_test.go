package sourcepilot

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// These two snapshots are invented offline fixtures, not actual USGS events.
// They describe separate observations, not a merge or admission decision.
const syntheticUSGSObservationOne = `{
  "type": "FeatureCollection",
  "metadata": {"title": "SYNTHETIC offline observation one; not an actual USGS feed"},
  "features": [
    {
      "type": "Feature", "id": "synthetic-revision",
      "properties": {
        "time": 1788998400123, "updated": 1788998460456,
        "mag": 0, "place": "Synthetic initial location", "status": "automatic"
      },
      "geometry": {"type": "Point", "coordinates": [121.25, 23.75, 0]}
    },
    {
      "type": "Feature", "id": "synthetic-nullability",
      "properties": {"time": 0, "updated": 0, "mag": null, "status": null, "url": null},
      "geometry": {"type": "Point", "coordinates": [0, 0, 0]}
    }
  ]
}`

const syntheticUSGSObservationTwo = `{
  "type": "FeatureCollection",
  "metadata": {"title": "SYNTHETIC offline observation two; not an actual USGS feed"},
  "features": [
    {
      "type": "Feature", "id": "synthetic-revision",
      "properties": {
        "time": 1788998400123, "updated": 1788998520789,
        "mag": 2.25, "place": "Synthetic revised location", "status": "reviewed", "url": null
      },
      "geometry": {"type": "Point", "coordinates": [121.25, 23.75, 0]}
    },
    {
      "type": "Feature", "id": "synthetic-nullability",
      "properties": {"time": 0, "updated": 1000, "place": null, "status": null, "url": null},
      "geometry": {"type": "Point", "coordinates": [0, 0, 0]}
    }
  ]
}`

func TestParseUSGSReplayKeepsObservationsIndependent(t *testing.T) {
	firstRaw := []byte(syntheticUSGSObservationOne)
	secondRaw := []byte(syntheticUSGSObservationTwo)
	first := parseUSGSObservation(t, firstRaw)
	firstBefore := marshalUSGSObservation(t, first)
	second := parseUSGSObservation(t, secondRaw)
	secondBefore := marshalUSGSObservation(t, second)

	if first[0].ID != second[0].ID || first[0].TimeMS != second[0].TimeMS || first[0].Coordinates != second[0].Coordinates {
		t.Fatal("stable source event coordinates changed between observations")
	}
	for i, observation := range [][]Earthquake{first, second} {
		wantUpdated := []int64{1788998460456, 1788998520789}[i]
		wantMagnitude := []float64{0, 2.25}[i]
		wantPlace := []string{"Synthetic initial location", "Synthetic revised location"}[i]
		wantStatus := []string{"automatic", "reviewed"}[i]
		quake := observation[0]
		if quake.ID != "synthetic-revision" || quake.TimeMS != 1788998400123 || quake.UpdatedMS != wantUpdated || quake.Coordinates != [3]float64{121.25, 23.75, 0} || quake.Magnitude == nil || *quake.Magnitude != wantMagnitude || quake.Place == nil || *quake.Place != wantPlace || quake.Status == nil || *quake.Status != wantStatus {
			t.Fatalf("observation %d changed exact source fields: %+v", i+1, quake)
		}
		// Retaining these USGS status strings is not an AHE admission operation.
		var output []map[string]json.RawMessage
		if err := json.Unmarshal(marshalUSGSObservation(t, observation), &output); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"admission", "admission_status", "admitted", "canonical"} {
			if _, exists := output[0][field]; exists {
				t.Fatalf("source status produced an unexpected authority field %q", field)
			}
		}
	}

	firstReplay := parseUSGSObservation(t, firstRaw)
	secondReplay := parseUSGSObservation(t, secondRaw)
	if !reflect.DeepEqual(firstReplay, first) || !reflect.DeepEqual(secondReplay, second) {
		t.Fatal("replaying the same bytes changed an observation")
	}
	if !bytes.Equal(firstBefore, marshalUSGSObservation(t, first)) || !bytes.Equal(secondBefore, marshalUSGSObservation(t, second)) {
		t.Fatal("parsing another observation mutated a previous result")
	}
	if string(firstRaw) != syntheticUSGSObservationOne || string(secondRaw) != syntheticUSGSObservationTwo {
		t.Fatal("parser modified source bytes")
	}

	// A caller may edit its returned data; independently parsed results must not
	// share mutable pointers or slices with that result.
	*firstReplay[0].Magnitude = 99
	*firstReplay[0].Place = "synthetic caller edit"
	*firstReplay[0].Status = "synthetic caller edit"
	firstReplay[0].MissingFields[0] = "synthetic caller edit"
	firstReplay[0].Coordinates[0] = 0
	if !bytes.Equal(firstBefore, marshalUSGSObservation(t, first)) || !bytes.Equal(secondBefore, marshalUSGSObservation(t, second)) {
		t.Fatal("replayed result shares mutable state with another observation")
	}
}

func TestParseUSGSReplayDoesNotFillNullMissingOrZero(t *testing.T) {
	first := parseUSGSObservation(t, []byte(syntheticUSGSObservationOne))
	second := parseUSGSObservation(t, []byte(syntheticUSGSObservationTwo))
	if first[0].Magnitude == nil || *first[0].Magnitude != 0 || first[1].Magnitude != nil || second[1].Magnitude != nil {
		t.Fatal("explicit zero, source null, and absent magnitude were conflated")
	}
	if first[0].URL != nil || second[0].URL != nil || first[1].Place != nil || second[1].Place != nil {
		t.Fatal("an absent or null source value was invented")
	}
	for _, test := range []struct {
		name string
		got  []string
		want []string
	}{
		{"first revision", first[0].MissingFields, []string{"url"}},
		{"second revision", second[0].MissingFields, []string{}},
		{"first nullability", first[1].MissingFields, []string{"place"}},
		{"second nullability", second[1].MissingFields, []string{"mag"}},
	} {
		if !reflect.DeepEqual(test.got, test.want) {
			t.Errorf("%s missing fields = %v; want %v", test.name, test.got, test.want)
		}
	}
	if first[1].TimeMS != 0 || first[1].UpdatedMS != 0 || second[1].TimeMS != 0 || second[1].UpdatedMS != 1000 || first[1].Coordinates != [3]float64{} || second[1].Coordinates != [3]float64{} || first[1].Status != nil || second[1].Status != nil {
		t.Fatal("null status or explicit zero source coordinates changed during revision")
	}
}

func parseUSGSObservation(t *testing.T, raw []byte) []Earthquake {
	t.Helper()
	quakes, err := ParseUSGS(raw)
	if err != nil || len(quakes) != 2 {
		t.Fatalf("synthetic observation did not parse as two events: %v, %v", quakes, err)
	}
	return quakes
}

func marshalUSGSObservation(t *testing.T, quakes []Earthquake) []byte {
	t.Helper()
	body, err := json.Marshal(quakes)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
