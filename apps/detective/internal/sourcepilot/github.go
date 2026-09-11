// Package sourcepilot holds pure projections for the bounded public-source lab.
// It does not fetch data, call models, or grant evidence admission.
package sourcepilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

// IncidentUpdate retains provider metadata separately from model input.
type IncidentUpdate struct {
	IncidentID string `json:"incident_id"`
	ID         string `json:"id"`
	Body       string `json:"body"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Incident is a source projection, not a verified service condition.
type Incident struct {
	ID        string           `json:"id"`
	UpdatedAt string           `json:"updated_at"`
	Updates   []IncidentUpdate `json:"incident_updates"`
}

// ParseGitHub reads the official Status API envelope without altering body text.
// Unknown upstream fields are preserved in the caller's raw capture, not guessed.
func ParseGitHub(raw []byte) ([]Incident, error) {
	var envelope struct {
		Incidents []Incident `json:"incidents"`
	}
	if len(raw) > 1<<20 || sourcemcp.ValidateRecordedJSON(string(raw)) != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Incidents == nil {
		return nil, errors.New("invalid github status envelope")
	}
	var rawEnvelope map[string]json.RawMessage
	var rawIncidents []map[string]json.RawMessage
	if json.Unmarshal(raw, &rawEnvelope) != nil || json.Unmarshal(rawEnvelope["incidents"], &rawIncidents) != nil || len(rawIncidents) != len(envelope.Incidents) {
		return nil, errors.New("invalid github incident fields")
	}
	for i, incident := range rawIncidents {
		var updates []map[string]json.RawMessage
		if json.Unmarshal(incident["incident_updates"], &updates) != nil || len(updates) != len(envelope.Incidents[i].Updates) {
			return nil, errors.New("invalid github update fields")
		}
		for j, update := range updates {
			body := update["body"]
			if len(body) == 0 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) || json.Unmarshal(body, &envelope.Incidents[i].Updates[j].Body) != nil {
				return nil, errors.New("github update body must be an explicit string")
			}
		}
	}
	incidentIDs, updateIDs := map[string]bool{}, map[string]bool{}
	for _, incident := range envelope.Incidents {
		if !pilotText(incident.ID, 128, false) || incidentIDs[incident.ID] || !pilotTime(incident.UpdatedAt) || incident.Updates == nil {
			return nil, errors.New("invalid github incident identity or time")
		}
		incidentIDs[incident.ID] = true
		for _, update := range incident.Updates {
			if update.IncidentID != incident.ID || !pilotText(update.ID, 128, false) || updateIDs[update.ID] || !pilotText(update.Body, 32<<10, true) || !pilotText(update.Status, 64, false) || !pilotTime(update.CreatedAt) || !pilotTime(update.UpdatedAt) {
				return nil, errors.New("invalid github incident update")
			}
			updateIDs[update.ID] = true
		}
	}
	return envelope.Incidents, nil
}

// SelectGitHub freezes at most two earliest nonempty updates from distinct
// incidents, ordered by incident update time descending and ID ascending.
func SelectGitHub(incidents []Incident) []IncidentUpdate {
	ordered := append([]Incident(nil), incidents...)
	sort.Slice(ordered, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, ordered[i].UpdatedAt)
		b, _ := time.Parse(time.RFC3339Nano, ordered[j].UpdatedAt)
		if a.Equal(b) {
			return ordered[i].ID < ordered[j].ID
		}
		return a.After(b)
	})
	selected := []IncidentUpdate{}
	for _, incident := range ordered {
		updates := append([]IncidentUpdate(nil), incident.Updates...)
		sort.Slice(updates, func(i, j int) bool {
			a, _ := time.Parse(time.RFC3339Nano, updates[i].CreatedAt)
			b, _ := time.Parse(time.RFC3339Nano, updates[j].CreatedAt)
			if a.Equal(b) {
				return updates[i].ID < updates[j].ID
			}
			return a.Before(b)
		})
		for _, update := range updates {
			if strings.TrimSpace(update.Body) != "" {
				selected = append(selected, update)
				break
			}
		}
		if len(selected) == 2 {
			break
		}
	}
	return selected
}

func pilotTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func pilotText(value string, limit int, allowEmpty bool) bool {
	if len(value) > limit || !utf8.ValidString(value) || (!allowEmpty && strings.TrimSpace(value) == "") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}
