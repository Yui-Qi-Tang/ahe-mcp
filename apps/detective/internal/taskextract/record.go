package taskextract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const (
	// InputVersion identifies a frozen, caller-declared local task input.
	InputVersion = "detective-task-input/v1"
	// RecordVersion identifies local selection records, not AHE receipts.
	RecordVersion      = "detective-task-run/v1"
	maxInputJSONBytes  = 1 << 20
	maxRecordJSONBytes = 2 << 20
)

// Input preserves all supplied source fields, including fields outside the task.
// ParseInput checks its wire shape; Prepare validates its semantics with the
// operator's actual model, without inventing a model during input parsing.
type Input struct {
	Version string `json:"version"`
	Task    Task   `json:"task"`
	Source  Source `json:"source"`
}

// Record preserves a local attempt and its frozen input. Failed attempts retain
// bounded final text but no Result, even if that text resembles a valid selection.
// Internal consistency is not source authentication, proof a model ran, human
// approval, or an AHE attempt/receipt.
type Record struct {
	Version   string  `json:"version"`
	Input     Input   `json:"input"`
	Model     string  `json:"model"`
	InputID   string  `json:"input_id"`
	Status    string  `json:"status"`
	ErrorCode string  `json:"error_code"`
	RawText   string  `json:"raw_text"`
	Result    *Result `json:"result"`
}

// ParseInput decodes one bounded exact input, preserving decoded source text.
func ParseInput(raw []byte) (Input, error) {
	var input Input
	if len(raw) == 0 || len(raw) > maxInputJSONBytes || sourcemcp.ValidateRecordedJSON(string(raw)) != nil ||
		decodeRecordShape(raw, &input) != nil || input.Version != InputVersion {
		return Input{}, errors.New("invalid task input")
	}
	return input, nil
}

// NewRecord detaches a bounded attempt for private persistence. Successful
// results must replay exactly; an attempt error always suppresses candidates.
func NewRecord(request *Request, result Result, attemptErr error) (Record, error) {
	if request == nil || request.id == "" {
		return Record{}, errors.New("task record requires a prepared request")
	}
	record := Record{
		Version: RecordVersion, Input: Input{Version: InputVersion, Task: request.Task(), Source: request.Source()},
		Model: request.model, InputID: request.id, Status: "failed", RawText: result.RawText,
	}
	if attemptErr == nil {
		checked, err := Replay(request, result)
		if err != nil {
			return Record{}, err
		}
		record.Status, record.Result = checked.Status, &checked
	} else {
		record.ErrorCode = "extraction_failed"
		switch {
		case errors.Is(attemptErr, context.DeadlineExceeded):
			record.ErrorCode = "timeout"
		case errors.Is(attemptErr, context.Canceled):
			record.ErrorCode = "cancelled"
		case errors.Is(attemptErr, ErrReplay), errors.Is(attemptErr, ErrSelection):
			record.ErrorCode = "extraction_failed"
		case reflect.DeepEqual(result, Result{}):
			record.ErrorCode = "model_unavailable"
		}
	}
	if _, err := validateRecord(record); err != nil {
		return Record{}, err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil || len(raw)+1 > maxRecordJSONBytes {
		return Record{}, errors.New("task record exceeds encoded record limit")
	}
	return record, nil
}

// ParseRecord checks a saved record without I/O or reclassifying failed output.
func ParseRecord(raw []byte) (Record, error) {
	var record Record
	if len(raw) == 0 || len(raw) > maxRecordJSONBytes || validateRecordJSON(raw) != nil || decodeRecordShape(raw, &record) != nil {
		return Record{}, errors.New("invalid saved task record")
	}
	if _, err := validateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateRecord(record Record) (*Request, error) {
	bad := errors.New("task record does not match its frozen input")
	if record.Version != RecordVersion || record.Input.Version != InputVersion ||
		len(record.RawText) > maxOutputBytes || !utf8.ValidString(record.RawText) {
		return nil, bad
	}
	request, err := Prepare(record.Input.Task, record.Input.Source, record.Model)
	if err != nil || request.InputID() != record.InputID {
		return nil, bad
	}
	if record.Status == "failed" {
		if record.Result != nil {
			return nil, bad
		}
		switch record.ErrorCode {
		case "cancelled", "timeout", "extraction_failed":
		case "model_unavailable":
			if record.RawText != "" {
				return nil, bad
			}
		default:
			return nil, bad
		}
		return request, nil
	}
	if record.ErrorCode != "" || record.Result == nil || record.Status != record.Result.Status || record.RawText != record.Result.RawText {
		return nil, bad
	}
	if _, err := Replay(request, *record.Result); err != nil {
		return nil, bad
	}
	return request, nil
}

// Large overlapping candidate lists may exceed the source JSON validator's
// one-MiB bound. Validate the record's fixed outer objects and each candidate
// separately; do not enlarge the existing source/transport parser's limits.
func validateRecordJSON(raw []byte) error {
	fields, err := recordObject(raw)
	if err != nil {
		return err
	}
	for key, value := range fields {
		if key != "result" || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			if validateRecordValue(value) != nil {
				return errors.New("invalid task record field")
			}
			continue
		}
		result, err := recordObject(value)
		if err != nil {
			return err
		}
		for key, value := range result {
			if key != "candidates" {
				if validateRecordValue(value) != nil {
					return errors.New("invalid task result field")
				}
				continue
			}
			var candidates []json.RawMessage
			if json.Unmarshal(value, &candidates) != nil || candidates == nil || len(candidates) > MaxCandidates {
				return errors.New("invalid task candidates")
			}
			for _, candidate := range candidates {
				if validateRecordValue(candidate) != nil {
					return errors.New("invalid task candidate")
				}
			}
		}
	}
	return nil
}

func validateRecordValue(raw []byte) error {
	return sourcemcp.ValidateRecordedJSON(`{"value":` + string(raw) + `}`)
}

func recordObject(raw []byte) (map[string]json.RawMessage, error) {
	bad := errors.New("invalid task record object")
	if !utf8.Valid(raw) {
		return nil, bad
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, bad
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields[name] != nil {
			return nil, bad
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, bad
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, bad
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, bad
	}
	return fields, nil
}

// Compare the typed encoding's shape to require every field and reject null
// containers. Only the top-level optional Result may be null on failed records.
func decodeRecordShape(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil {
		return errors.New("invalid task record shape")
	}
	canonical, err := json.Marshal(output)
	if err != nil {
		return err
	}
	var got, want any
	if json.Unmarshal(raw, &got) != nil || json.Unmarshal(canonical, &want) != nil || !recordShape(got, want, "") {
		return errors.New("invalid task record shape")
	}
	return nil
}

func recordShape(got, want any, path string) bool {
	if got == nil || want == nil {
		return path == "/result" && got == nil && want == nil
	}
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok || len(actual) != len(expected) {
			return false
		}
		for key, value := range expected {
			other, ok := actual[key]
			if !ok || !recordShape(other, value, path+"/"+key) {
				return false
			}
		}
	case []any:
		actual, ok := got.([]any)
		if !ok || len(actual) != len(expected) {
			return false
		}
		for i, value := range expected {
			if !recordShape(actual[i], value, path+"/[]") {
				return false
			}
		}
	}
	return true
}
