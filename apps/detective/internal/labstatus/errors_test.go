package labstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
)

func TestExtractorErrorCategories(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		response   string
		mutate     func(*CandidateSet)
		want       error
		constraint *statusConstraint
	}{
		{name: "missing JSON", response: " ", want: ErrModelJSON},
		{name: "malformed JSON", response: "{", want: ErrModelJSON},
		{name: "oversized JSON", response: strings.Repeat("x", maxModelJSONBytes+1), want: ErrModelJSON},
		{name: "citation", mutate: func(c *CandidateSet) { c.Records[0].Citation.EndLine = 99 }, want: ErrCitation},
		{name: "candidate", mutate: func(c *CandidateSet) { c.Records[0].Status = "unsupported" }, want: ErrCandidateValidation},
		{name: "statement", mutate: func(c *CandidateSet) { c.Records[0].Statement = c.Records[0].Subject }, want: ErrCandidateValidation},
		{name: "status constraint", want: ErrCandidateValidation, constraint: &statusConstraint{clause: "OPEN", allowed: []string{"open"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
			response := test.response
			if response == "" {
				candidates := validCandidates()
				candidates.Records[0].Citation.ExactQuote = ""
				if test.mutate != nil {
					test.mutate(&candidates)
				}
				body, err := json.Marshal(candidates)
				if err != nil {
					t.Fatal(err)
				}
				response = string(body)
			}
			extractor, err := NewExtractor(&staticLLM{response: response})
			if err != nil {
				t.Fatal(err)
			}
			result, err := extractor.extract(t.Context(), document, test.constraint)
			if !errors.Is(err, test.want) || result.Result.Outcome != "" {
				t.Fatalf("extract() outcome = %q, error = %v, want category %v without result", result.Result.Outcome, err, test.want)
			}
		})
	}
}

func TestRowCausePreservesModelErrorWithoutSerialization(t *testing.T) {
	document := loadRowFixture(t)
	upstream := errors.New("synthetic private upstream diagnostic")
	extractor, err := NewExtractor(&failureLLM{err: fmt.Errorf("transport: %w", upstream)})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := extractor.ExtractTableRow(t.Context(), document, "Status at a Glance", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Rows) != 1 || batch.Rows[0].Status != rowStatusFailed {
		t.Fatalf("failed rows = %+v", batch.Rows)
	}
	row := batch.Rows[0]
	if !errors.Is(row.Cause, ErrModelResponse) || !errors.Is(row.Cause, upstream) {
		t.Fatalf("row cause = %v, want both model and upstream error identity", row.Cause)
	}
	// Lab reports retain their historical Error field; a caller may replace it
	// with a safe display message without Cause adding any serialized field.
	row.Error = "safe display message"
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["row"] == nil || fields["status"] == nil || fields["error"] == nil || strings.Contains(string(body), upstream.Error()) {
		t.Fatalf("row cause changed serialized report: %s", body)
	}
	var restored RowOutcome
	if err := json.Unmarshal(body, &restored); err != nil || restored.Cause != nil {
		t.Fatalf("restored cause = %v, JSON error = %v", restored.Cause, err)
	}
}

func TestExtractorModelEventErrorCategory(t *testing.T) {
	document := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
	extractor, err := NewExtractor(&failureLLM{response: &model.LLMResponse{ErrorCode: "synthetic_error", ErrorMessage: "synthetic upstream response error"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractor.Extract(t.Context(), document); !errors.Is(err, ErrModelResponse) {
		t.Fatalf("Extract() error = %v, want model response category", err)
	}
}

func TestNotAttemptedRowCausePreservesCancellation(t *testing.T) {
	document := loadRowFixture(t)
	extractor, err := NewExtractor(&failureLLM{err: errors.New("model must not run")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	batch, err := extractor.ExtractTableRows(ctx, document, "Status at a Glance")
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Rows) != 3 || batch.Summary.Attempted != 0 {
		t.Fatalf("canceled batch = %+v", batch)
	}
	for _, row := range batch.Rows {
		if row.Status != rowStatusNotAttempted || !errors.Is(row.Cause, context.Canceled) {
			t.Fatalf("canceled row = %+v", row)
		}
	}
}

type failureLLM struct {
	err      error
	response *model.LLMResponse
}

func (*failureLLM) Name() string { return "failure-test-model" }

func (m *failureLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(m.response, m.err)
	}
}
