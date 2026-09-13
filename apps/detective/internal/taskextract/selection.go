package taskextract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

type selectedRange struct {
	StartID string `json:"start_id"`
	EndID   string `json:"end_id"`
}

type selection struct {
	Outcome string          `json:"outcome"`
	Ranges  []selectedRange `json:"ranges"`
	Reason  string          `json:"reason"`
}

func decodeSelection(raw string) (selection, error) {
	var output selection
	if len(raw) > maxOutputBytes || sourcemcp.ValidateRecordedJSON(raw) != nil {
		return output, ErrSelection
	}
	fields, ok := exactObject([]byte(raw), "outcome", "ranges", "reason")
	if !ok || json.Unmarshal(fields["outcome"], &output.Outcome) != nil ||
		json.Unmarshal(fields["reason"], &output.Reason) != nil {
		return output, ErrSelection
	}
	var ranges []json.RawMessage
	if json.Unmarshal(fields["ranges"], &ranges) != nil || ranges == nil || len(ranges) > MaxCandidates {
		return output, ErrSelection
	}
	output.Ranges = []selectedRange{}
	for _, rawRange := range ranges {
		fields, ok := exactObject(rawRange, "start_id", "end_id")
		var span selectedRange
		if !ok || json.Unmarshal(fields["start_id"], &span.StartID) != nil ||
			json.Unmarshal(fields["end_id"], &span.EndID) != nil || span.StartID == "" || span.EndID == "" {
			return selection{}, ErrSelection
		}
		output.Ranges = append(output.Ranges, span)
	}
	// Reasons are untrusted model annotations, never source or human review
	// text. Match the schema's bound for every outcome, including selected.
	if output.Reason != "" && (!validText(output.Reason, 2048, false) || utf8.RuneCountInString(output.Reason) > 512) {
		return selection{}, ErrSelection
	}
	switch output.Outcome {
	case "selected":
		if len(output.Ranges) == 0 {
			return selection{}, ErrSelection
		}
	case "abstained", "incomplete":
		if len(output.Ranges) != 0 || output.Reason == "" {
			return selection{}, ErrSelection
		}
	default:
		return selection{}, ErrSelection
	}
	return output, nil
}

func exactObject(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || strings.TrimSpace(string(value)) == "null" {
			return nil, false
		}
	}
	return fields, true
}

func applySelection(r *Request, raw string) (Result, error) {
	result := initialResult(r)
	result.RawText = raw
	output, err := decodeSelection(raw)
	if err != nil {
		result.ErrorCode = "invalid_selection"
		return result, err
	}
	result.Reason = output.Reason
	if output.Outcome == "incomplete" {
		result.ErrorCode = "selection_incomplete"
		return result, ErrIncomplete
	}
	positions := make(map[string]int, len(r.units))
	for i, unit := range r.units {
		positions[unit.ID] = i
	}
	parts := make(map[string]string, len(r.source.Parts))
	for _, part := range r.source.Parts {
		parts[part.Name] = part.Text
	}
	candidates := []Candidate{}
	selected := map[string]bool{}
	seen := map[selectedRange]bool{}
	lastStart := -1
	lastEnd := -1
	for _, span := range output.Ranges {
		start, startOK := positions[span.StartID]
		end, endOK := positions[span.EndID]
		if !startOK || !endOK || start > end || seen[span] ||
			start < lastStart || (start == lastStart && end < lastEnd) ||
			r.units[start].Part != r.units[end].Part {
			result.ErrorCode = "invalid_reference"
			return result, ErrSelection
		}
		seen[span] = true
		lastStart, lastEnd = start, end
		first, last := r.units[start], r.units[end]
		text := parts[first.Part]
		ids := []string{}
		for _, unit := range r.units[start : end+1] {
			selected[unit.ID] = true
			ids = append(ids, unit.ID)
		}
		candidates = append(candidates, Candidate{
			ID:       "task-candidate:sha256:" + hashText(r.id+"\n"+fmt.Sprintf("%d:%d", start, end)),
			SourceID: r.source.ID, SourceRevision: r.source.Revision,
			Part: first.Part, BodySHA256: hashText(text), StartByte: first.StartByte, EndByte: last.EndByte,
			UnitIDs: ids, Text: text[first.StartByte:last.EndByte],
		})
	}
	result.Candidates = candidates
	result.Scope.UnselectedUnitIDs = []string{}
	for _, unit := range r.units {
		if !selected[unit.ID] {
			result.Scope.UnselectedUnitIDs = append(result.Scope.UnselectedUnitIDs, unit.ID)
		}
	}
	result.Status = output.Outcome
	return result, nil
}

// Replay checks a successful local result against the current frozen request
// and reconstructs all candidate text without a model or I/O. It does not
// authenticate a saved file, prove a model ran, or replay an AHE write.
func Replay(r *Request, saved Result) (Result, error) {
	if r == nil || r.id == "" || len(r.units) == 0 || saved.InputID != r.id ||
		saved.Model != r.model || (saved.Status != "selected" && saved.Status != "abstained") {
		return Result{}, ErrReplay
	}
	want, err := applySelection(r, saved.RawText)
	if err != nil || !reflect.DeepEqual(saved, want) {
		return Result{}, ErrReplay
	}
	return want, nil
}
