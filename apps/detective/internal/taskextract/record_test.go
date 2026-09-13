package taskextract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

func recordFixture(t *testing.T, answer string) (*taskextract.Request, taskextract.Result, taskextract.Record) {
	t.Helper()
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	result, err := taskextract.Extract(t.Context(), fixedModel(answer), request)
	if err != nil {
		t.Fatal(err)
	}
	record, err := taskextract.NewRecord(request, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request, result, record
}

func recordJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTaskRecordPreservesCompleteInputAndSuccessfulReplay(t *testing.T) {
	for _, answer := range []string{
		`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":"Look at these source conditions."}`,
		`{"outcome":"abstained","ranges":[],"reason":"No matching passage in the supplied units."}`,
	} {
		request, result, record := recordFixture(t, answer)
		input, err := taskextract.ParseInput(recordJSON(t, record.Input))
		if err != nil || !reflect.DeepEqual(input.Task, request.Task()) || !reflect.DeepEqual(input.Source, request.Source()) {
			t.Fatal("frozen input changed", err)
		}
		read, err := taskextract.ParseRecord(recordJSON(t, record))
		if err != nil || !reflect.DeepEqual(record, read) || !reflect.DeepEqual(*read.Result, result) {
			t.Fatal("successful record did not round trip exactly", err)
		}
		result.Scope.ProvidedParts[0] = "mutated"
		if reflect.DeepEqual(*record.Result, result) {
			t.Fatal("record retained mutable caller-owned slices")
		}
	}
}

func TestTaskRecordFailuresNeverExposeOrReclassifyCandidates(t *testing.T) {
	request, result, _ := recordFixture(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`)
	for _, test := range []struct {
		name   string
		result taskextract.Result
		err    error
		code   string
	}{
		{"extraction", result, errors.New("unsupported transport"), "extraction_failed"},
		{"cancelled after valid output", result, context.Canceled, "cancelled"},
		{"timeout after valid output", result, context.DeadlineExceeded, "timeout"},
		{"model preflight", taskextract.Result{}, errors.New("model unavailable"), "model_unavailable"},
		{"invalid success", taskextract.Result{}, taskextract.ErrReplay, "extraction_failed"},
		{"invalid selection", taskextract.Result{}, taskextract.ErrSelection, "extraction_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, err := taskextract.NewRecord(request, test.result, test.err)
			if err != nil || record.Status != "failed" || record.ErrorCode != test.code || record.Result != nil {
				t.Fatal("failed record retained candidates or lost failure", err)
			}
			read, err := taskextract.ParseRecord(recordJSON(t, record))
			if err != nil || read.Result != nil || read.Status != "failed" || read.RawText != test.result.RawText {
				t.Fatal("offline read reclassified a failure", err)
			}
		})
	}
	if _, err := taskextract.NewRecord(request, taskextract.Result{}, nil); err == nil {
		t.Fatal("zero result became a successful record")
	}
}

func TestTaskRecordRejectsInvalidFailureCoordinates(t *testing.T) {
	request, result, _ := recordFixture(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`)
	for _, mutate := range []func(*taskextract.Record){
		func(r *taskextract.Record) { r.ErrorCode = "invented" },
		func(r *taskextract.Record) { r.ErrorCode = "" },
		func(r *taskextract.Record) { r.ErrorCode = "model_unavailable" },
		func(r *taskextract.Record) { r.Status = "selected" },
		func(r *taskextract.Record) { r.Result = &result },
		func(r *taskextract.Record) { r.RawText = strings.Repeat("x", (16<<10)+1) },
	} {
		record, err := taskextract.NewRecord(request, result, errors.New("extraction failed"))
		if err != nil {
			t.Fatal(err)
		}
		mutate(&record)
		if _, err := taskextract.ParseRecord(recordJSON(t, record)); err == nil {
			t.Fatal("invalid failure coordinates accepted")
		}
	}
	if _, err := taskextract.NewRecord(nil, result, nil); err == nil {
		t.Fatal("nil request became a record")
	}
}

func TestTaskInputRejectsMalformedOrIncompleteWireShapes(t *testing.T) {
	task, source := ticketInput(t)
	raw := string(recordJSON(t, taskextract.Input{Version: taskextract.InputVersion, Task: task, Source: source}))
	for name, candidate := range map[string]string{
		"duplicate":          strings.Replace(raw, `"version":`, `"version":"duplicate","version":`, 1),
		"unknown":            strings.Replace(raw, `"task": {`, `"task": {"unexpected":true,`, 1),
		"missing":            strings.Replace(raw, `"revision": 1,`, "", 1),
		"null array":         strings.Replace(raw, `"limitations": []`, `"limitations": null`, 1),
		"unpaired surrogate": strings.Replace(raw, "Synthetic release ticket", `\ud800`, 1),
		"invalid UTF8":       strings.Replace(raw, "Synthetic release ticket", string([]byte{0xff}), 1),
		"trailing object":    raw + `{}`,
		"wrong version":      strings.Replace(raw, taskextract.InputVersion, "unsupported", 1),
		"case alias":         strings.Replace(raw, `"objective"`, `"Objective"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := taskextract.ParseInput([]byte(candidate)); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	if _, err := taskextract.ParseInput([]byte(strings.Repeat(" ", 1<<20) + raw)); err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestTaskRecordRejectsMutationAndMalformedWireShapes(t *testing.T) {
	_, _, record := recordFixture(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`)
	raw := string(recordJSON(t, record))
	for name, candidate := range map[string]string{
		"source metadata":      strings.Replace(raw, "Synthetic release ticket", "Changed title", 1),
		"unprovided source":    strings.Replace(raw, "HISTORY_NOT_FOR_MODEL", "CHANGED_HISTORY", 1),
		"task":                 strings.Replace(raw, `"revision": 1`, `"revision": 2`, 1),
		"model":                strings.Replace(raw, `"model": "synthetic-selector"`, `"model": "different-selector"`, 1),
		"candidate body":       strings.Replace(raw, `"text": "The release owner`, `"text": "The administrator`, 1),
		"scope":                strings.Replace(raw, `"fact_completeness_assessed": false`, `"fact_completeness_assessed": true`, 1),
		"approval":             strings.Replace(raw, `"human_review": "not_reviewed"`, `"human_review": "approved"`, 1),
		"duplicate result":     strings.Replace(raw, `"result": {`, `"result":null,"result": {`, 1),
		"duplicate nested":     strings.Replace(raw, `"source_revision":`, `"source_revision":"other","source_revision":`, 1),
		"unknown nested":       strings.Replace(raw, `"candidates": [`, `"unexpected":true,"candidates": [`, 1),
		"missing result field": strings.Replace(raw, `"authority_effect": "none"`, `"ignored": "none"`, 1),
		"null candidate":       strings.Replace(raw, `"candidates": [`, `"candidates": [null,`, 1),
		"null source array":    strings.Replace(raw, `"limitations": []`, `"limitations": null`, 1),
		"unpaired surrogate":   strings.Replace(raw, "Synthetic release ticket", `\ud800`, 1),
		"trailing object":      raw + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := taskextract.ParseRecord([]byte(candidate)); err == nil {
				t.Fatal("inconsistent or malformed record accepted")
			}
		})
	}
	if _, err := taskextract.ParseRecord([]byte(strings.Repeat(" ", 2<<20) + raw)); err == nil {
		t.Fatal("oversized record accepted")
	}
}

func TestTaskRecordAcceptsLargeOverlappingCandidateReport(t *testing.T) {
	task, source := contractInput()
	source.Parts[0].Text = strings.Repeat("<", 10400)
	for i := 1; i < 16; i++ {
		source.Parts[0].Text += "\n\nContext."
	}
	request := prepareContract(t, task, source, "synthetic-selector")
	ranges := []map[string]string{}
	for i := 1; i <= 16; i++ {
		ranges = append(ranges, map[string]string{"start_id": "u001", "end_id": fmt.Sprintf("u%03d", i)})
	}
	answer := recordJSON(t, map[string]any{"outcome": "selected", "ranges": ranges, "reason": ""})
	result, err := taskextract.Extract(t.Context(), fixedModel(string(answer)), request)
	if err != nil {
		t.Fatal(err)
	}
	record, err := taskextract.NewRecord(request, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := recordJSON(t, record)
	if len(raw) <= 1<<20 || len(raw) >= 2<<20 {
		t.Fatalf("fixture not between parser bounds: %d", len(raw))
	}
	if _, err := taskextract.ParseRecord(raw); err != nil {
		t.Fatal("valid large report failed its bounded chunk parser", err)
	}
}
