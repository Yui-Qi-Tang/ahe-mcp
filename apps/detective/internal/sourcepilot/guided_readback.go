package sourcepilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

// ParseGuidedReport reads a bounded, strict v1 report and replays its recorded
// final responses. Successful readback establishes internal consistency only,
// not file authenticity, factual accuracy, readability, or human approval.
func ParseGuidedReport(raw []byte) (GuidedReport, error) {
	var report GuidedReport
	if err := guidedReportDecode(raw, &report); err != nil {
		return GuidedReport{}, err
	}
	if err := ValidateGuidedReport(report); err != nil {
		return GuidedReport{}, err
	}
	return report, nil
}

// ValidateGuidedReport reconstructs every derived field from the declared
// source and recorded final text, without models, source access, or persistence.
// Failed and cancelled runs may retain only the results applied before stopping.
func ValidateGuidedReport(report GuidedReport) error {
	bad := errors.New("summary report does not match its recorded source and attempts")
	if len(report.Attempts) > 3 {
		return bad
	}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 2<<20 {
		return bad
	}
	want, err := NewGuidedReport(report.Source)
	if err != nil {
		return bad
	}
	next := "summary_claims"
	for i, attempt := range report.Attempts {
		if attempt.Stage != next || next == "" || len(attempt.RawText) > 32<<10 || !utf8.ValidString(attempt.RawText) {
			return bad
		}
		want.Attempts = append(want.Attempts, attempt)
		want.Stage = attempt.Stage
		if err := guidedApply(&want, attempt.Stage, attempt.RawText); err != nil {
			// Invalid final text is retained for inspection, but it can never
			// produce fields, advance the stage, or precede another attempt.
			if i != len(report.Attempts)-1 {
				return bad
			}
			next = "failed"
			break
		}
		switch attempt.Stage {
		case "summary_claims":
			next = "source_check"
			if len(want.Claims) == 0 {
				next = "body_observations"
			}
		case "source_check":
			next = "body_observations"
		case "body_observations":
			next = ""
		}
	}
	if report.Stage == "complete" {
		if next != "" {
			return bad
		}
		want.Stage = "complete"
	}
	// Keeping the last applied stage is valid when Review observes cancellation
	// after apply. No other stage, authority field, or derived result is mutable.
	if !reflect.DeepEqual(report, want) {
		return bad
	}
	return nil
}

// guidedReportDecode preserves the existing 2 MiB report-writer bound. Each
// top-level field is independently strict JSON (under 1 MiB in these bounded
// report schemas); required fields and nulls are checked at every nesting level.
func guidedReportDecode(raw []byte, target any) error {
	bad := errors.New("invalid summary report JSON")
	typ := reflect.TypeOf(target)
	if len(raw) == 0 || len(raw) > 2<<20 || !utf8.Valid(raw) || typ == nil || typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct || reflect.ValueOf(target).IsNil() {
		return bad
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return bad
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return bad
		}
		key, ok := token.(string)
		if !ok || fields[key] != nil {
			return bad
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || sourcemcp.ValidateRecordedJSON(`{"value":`+string(value)+`}`) != nil {
			return bad
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return bad
	}
	if _, err := decoder.Token(); err != io.EOF || !guidedReportObjectShape(fields, typ.Elem()) {
		return bad
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return bad
	}
	return nil
}

func guidedReportObjectShape(fields map[string]json.RawMessage, typ reflect.Type) bool {
	matched := 0
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		key, option, _ := strings.Cut(field.Tag.Get("json"), ",")
		if key == "" || (option != "" && option != "omitempty") {
			return false
		}
		raw, present := fields[key]
		if !present && option == "omitempty" {
			continue
		}
		if !guidedReportValueShape(raw, field.Type) {
			return false
		}
		matched++
	}
	return matched == len(fields)
}

func guidedReportValueShape(raw json.RawMessage, typ reflect.Type) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return guidedReportValueShape(raw, typ.Elem())
	case reflect.Struct:
		var fields map[string]json.RawMessage
		return json.Unmarshal(raw, &fields) == nil && guidedReportObjectShape(fields, typ)
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || items == nil {
			return false
		}
		for _, item := range items {
			if !guidedReportValueShape(item, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
