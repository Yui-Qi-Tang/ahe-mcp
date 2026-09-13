package taskextract_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// This invented ticket and scripted selector prove wiring and byte preservation,
// not that a real small model can recognize relevance or semantic completeness.

type scriptedModel struct {
	calls int
	run   func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error]
}

func (*scriptedModel) Name() string { return "synthetic-selector" }

func (m *scriptedModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.calls++
	return m.run(ctx, req, stream)
}

func finalResponse(text string) *model.LLMResponse {
	return &model.LLMResponse{
		Content: genai.NewContentFromText(text, genai.RoleModel), FinishReason: genai.FinishReasonStop,
	}
}

func fixedModel(text string) *scriptedModel {
	return &scriptedModel{run: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) { yield(finalResponse(text), nil) }
	}}
}

func ticketInput(t *testing.T) (taskextract.Task, taskextract.Source) {
	t.Helper()
	raw, err := os.ReadFile("testdata/ticket.json")
	if err != nil {
		t.Fatal(err)
	}
	var body string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	task, source := contractInput()
	task.Parts = []string{"description", "comments"}
	source.Parts[0].Text = body
	source.Parts[1].Text = "Unrequested private-history marker: HISTORY_NOT_FOR_MODEL."
	return task, source
}

func TestTaskPairedGoalsSelectDifferentReadableEvidence(t *testing.T) {
	var results []taskextract.Result
	var requests []*taskextract.Request
	for _, test := range []struct {
		name, objective, answer string
		wanted                  [][2]int
		unselected              []string
	}{
		{
			name: "release", objective: "採集這張 ticket 的上線條件，包含尚未完成的條件。",
			answer: `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001"},{"start_id":"u002","end_id":"u002"}],"reason":""}`,
			wanted: [][2]int{{0, 0}, {1, 1}}, unselected: []string{"u003", "u004", "u005", "u006"},
		},
		{
			name: "recovery", objective: "採集這張 ticket 的回復方案、適用例外與失敗紀錄。",
			answer: `{"outcome":"selected","ranges":[{"start_id":"u003","end_id":"u004"},{"start_id":"u005","end_id":"u005"}],"reason":""}`,
			wanted: [][2]int{{2, 3}, {4, 4}}, unselected: []string{"u001", "u002", "u006"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			task, source := ticketInput(t)
			task.Objective = test.objective
			request := prepareContract(t, task, source, "synthetic-selector")
			llm := fixedModel(test.answer)
			base := llm.run
			llm.run = func(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
				if stream || len(req.Tools) != 0 || req.Config == nil || len(req.Config.Tools) != 0 ||
					req.Config.ResponseMIMEType != "application/json" || req.Config.ResponseJsonSchema == nil {
					t.Fatal("selector did not use a single tool-free structured request")
				}
				var input struct {
					Objective string
					Units     []taskextract.Unit
				}
				raw := req.Contents[0].Parts[0].Text
				if json.Unmarshal([]byte(raw), &input) != nil || input.Objective != test.objective ||
					len(input.Units) != 6 ||
					strings.Contains(raw, "HISTORY_NOT_FOR_MODEL") {
					t.Fatal("model did not receive the exact task-scoped source projection")
				}
				return base(ctx, req, stream)
			}
			result, err := taskextract.Extract(t.Context(), llm, request)
			if err != nil || result.Status != "selected" || llm.calls != 1 {
				t.Fatalf("Extract() = %s, %v; calls=%d", result.Status, err, llm.calls)
			}
			if len(result.Candidates) != len(test.wanted) || result.HumanReview != "not_reviewed" ||
				result.AuthorityEffect != "none" || result.Scope.FactCompletenessAssessed {
				t.Fatal("selection count or authority boundary changed")
			}
			units := request.Units()
			for i, wanted := range test.wanted {
				start, end := units[wanted[0]].StartByte, units[wanted[1]].EndByte
				candidate := result.Candidates[i]
				if candidate.Text != source.Parts[0].Text[start:end] || candidate.StartByte != start ||
					candidate.EndByte != end || candidate.SourceID != source.ID || candidate.SourceRevision != source.Revision ||
					candidate.Part != "description" || candidate.BodySHA256 == "" || candidate.ID == "" {
					t.Fatalf("candidate %d changed original wording, conditions or coordinates", i)
				}
			}
			if !slices.Equal(result.Scope.UnselectedUnitIDs, test.unselected) ||
				!slices.Equal(result.Scope.NotCollectedParts, []string{"comments"}) ||
				!slices.Equal(result.Scope.NotProvidedParts, []string{"history"}) {
				t.Fatalf("scope was concealed or inferred: %+v", result.Scope)
			}
			replayed, err := taskextract.Replay(request, result)
			if err != nil || !reflect.DeepEqual(result, replayed) || llm.calls != 1 {
				t.Fatal("exact local replay changed output or invoked a model")
			}
			results = append(results, result)
			requests = append(requests, request)
		})
	}
	if len(results) != 2 {
		t.Fatal("paired selection did not complete")
	}
	if results[0].InputID == results[1].InputID {
		t.Fatal("different objectives reused the same execution input")
	}
	for i := range results {
		if _, err := taskextract.Replay(requests[1-i], results[i]); !errors.Is(err, taskextract.ErrReplay) {
			t.Fatal("the other question accepted this question's result")
		}
	}
}

func TestSelectorRequiresExplicitAbstention(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	raw := `{"outcome":"abstained","ranges":[],"reason":"The supplied text does not describe this task."}`
	result, err := taskextract.Extract(t.Context(), fixedModel(raw), request)
	if err != nil || result.Status != "abstained" || result.Reason == "" || len(result.Candidates) != 0 ||
		len(result.Scope.UnselectedUnitIDs) != 6 || result.Scope.FactCompletenessAssessed {
		t.Fatal("explicit abstention was lost or turned into an absence/completeness claim")
	}
	if _, err := taskextract.Replay(request, result); err != nil {
		t.Fatal(err)
	}
}

func TestSelectorRejectsMalformedSelectionsWithoutPartialCandidates(t *testing.T) {
	task, source := ticketInput(t)
	task.Parts = append(task.Parts, "history")
	request := prepareContract(t, task, source, "synthetic-selector")
	for _, raw := range []string{
		`{}`,
		`{"outcome":"selected","ranges":[],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001"}]}`,
		`{"outcome":"abstained","ranges":[],"reason":""}`,
		`{"outcome":"abstained","ranges":null,"reason":"No match."}`,
		`{"outcome":"abstained","ranges":[],"reason":null}`,
		`{"outcome":"abstained","ranges":[],"reason":"No match.","admit":true}`,
		`{"outcome":"selected","ranges":[],"ranges":[],"reason":""}`,
		`{"outcome":"abstained","Ranges":[],"reason":"No match."}`,
		`{"outcome":"abstained","ranges":[],"reason":"\ud800"}`,
		`{"outcome":"abstained","ranges":[],"reason":"No match."}{}`,
		`{"outcome":"selected","ranges":[null],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001","text":"Invented text."}],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u001","start_id":"u002","end_id":"u002"}],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001"},{"start_id":"unknown","end_id":"unknown"}],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001"},{"start_id":"u001","end_id":"u001"}],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u002","end_id":"u001"}],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u002","end_id":"u002"},{"start_id":"u001","end_id":"u001"}],"reason":""}`,
		`{"outcome":"selected","ranges":[{"start_id":"u006","end_id":"u007"}],"reason":""}`,
		`{"outcome":"incomplete","ranges":[{"start_id":"u001","end_id":"u001"}],"reason":"Cannot finish."}`,
		string([]byte{0xff}),
	} {
		t.Run(raw, func(t *testing.T) {
			result, err := taskextract.Extract(t.Context(), fixedModel(raw), request)
			if err == nil || result.Status != "failed" || len(result.Candidates) != 0 || result.ErrorCode == "" {
				t.Fatal("invalid output was promoted, partially applied or hidden as abstention")
			}
			if _, err := taskextract.Replay(request, result); err == nil {
				t.Fatal("failed output was accepted for successful replay")
			}
		})
	}
}

func TestSelectorLimitsAndDeclaredIncompleteAreNotAbstention(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	tooMany := `{"outcome":"selected","ranges":[` + strings.Repeat(`{"start_id":"u001","end_id":"u001"},`, taskextract.MaxCandidates) + `{"start_id":"u002","end_id":"u002"}],"reason":""}`
	for _, raw := range []string{
		tooMany,
		`{"outcome":"incomplete","ranges":[],"reason":"Cannot finish without omitting required context."}`,
		strings.Repeat(" ", 16<<10) + "{}",
	} {
		result, err := taskextract.Extract(t.Context(), fixedModel(raw), request)
		if err == nil || result.Status != "failed" || len(result.Candidates) != 0 {
			t.Fatal("limit or declared incomplete selection became success")
		}
	}
}

func TestSelectorRejectsIncompleteAndUnsupportedModelResponses(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	raw := `{"outcome":"abstained","ranges":[],"reason":"No match."}`
	for _, test := range []struct {
		name   string
		change func(*model.LLMResponse)
	}{
		{"missing finish reason", func(r *model.LLMResponse) { r.FinishReason = "" }},
		{"token limit", func(r *model.LLMResponse) { r.FinishReason = genai.FinishReasonMaxTokens }},
		{"partial", func(r *model.LLMResponse) { r.Partial = true }},
		{"interrupted", func(r *model.LLMResponse) { r.Interrupted = true }},
		{"error code", func(r *model.LLMResponse) { r.ErrorCode = "error" }},
		{"error message", func(r *model.LLMResponse) { r.ErrorMessage = "private-upstream-value" }},
		{"user role", func(r *model.LLMResponse) { r.Content.Role = genai.RoleUser }},
		{"missing content", func(r *model.LLMResponse) { r.Content = nil }},
		{"thought", func(r *model.LLMResponse) { r.Content.Parts[0].Thought = true }},
		{"tool call", func(r *model.LLMResponse) { r.Content.Parts[0].FunctionCall = &genai.FunctionCall{Name: "admit"} }},
		{"nil part", func(r *model.LLMResponse) { r.Content.Parts = append(r.Content.Parts, nil) }},
		{"wrong model", func(r *model.LLMResponse) { r.CustomMetadata = map[string]any{"openai_model": "another-model"} }},
		{"unknown metadata", func(r *model.LLMResponse) { r.CustomMetadata = map[string]any{"approved": true} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			llm := &scriptedModel{run: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					response := finalResponse(raw)
					test.change(response)
					yield(response, nil)
				}
			}}
			result, err := taskextract.Extract(t.Context(), llm, request)
			if err == nil || result.Status != "failed" || len(result.Candidates) != 0 || llm.calls != 1 ||
				strings.Contains(err.Error(), "private-upstream-value") {
				t.Fatal("unsupported response escaped the failed boundary")
			}
		})
	}
	for _, name := range []string{"no response", "nil response", "multiple responses", "upstream error"} {
		t.Run(name, func(t *testing.T) {
			llm := &scriptedModel{run: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					switch name {
					case "nil response":
						yield(nil, nil)
					case "multiple responses":
						if yield(finalResponse(raw), nil) {
							yield(finalResponse(raw), nil)
						}
					case "upstream error":
						yield(nil, errors.New("private-upstream-value"))
					}
				}
			}}
			result, err := taskextract.Extract(t.Context(), llm, request)
			if err == nil || result.Status != "failed" || len(result.Candidates) != 0 ||
				strings.Contains(err.Error(), "private-upstream-value") {
				t.Fatal("invalid call count or error became successful abstention")
			}
		})
	}
}

func TestSelectorStopsBeforeMissingSourceAndAfterCancellation(t *testing.T) {
	task, source := ticketInput(t)
	task.Parts = []string{"comments"}
	request := prepareContract(t, task, source, "synthetic-selector")
	llm := fixedModel(`{"outcome":"abstained","ranges":[],"reason":"No match."}`)
	result, err := taskextract.Extract(t.Context(), llm, request)
	if err == nil || result.ErrorCode != "no_readable_source" || result.Status != "failed" || llm.calls != 0 {
		t.Fatal("missing source was sent to a model or presented as abstention")
	}
	task.Parts = []string{"description"}
	request = prepareContract(t, task, source, "synthetic-selector")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := taskextract.Extract(ctx, llm, request); !errors.Is(err, context.Canceled) || llm.calls != 0 {
		t.Fatal("pre-cancelled request called the model")
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	llm.run = func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(finalResponse(`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001"}],"reason":""}`), nil)
			cancel()
		}
	}
	result, err = taskextract.Extract(ctx, llm, request)
	if !errors.Is(err, context.Canceled) || result.Status != "failed" || len(result.Candidates) != 0 {
		t.Fatal("late cancellation promoted a candidate")
	}
}

func TestReplayRejectsTamperedDerivedFields(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	original, err := taskextract.Extract(t.Context(), fixedModel(`{"outcome":"selected","ranges":[{"start_id":"u003","end_id":"u004"}],"reason":""}`), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*taskextract.Result)
	}{
		{"input ID", func(r *taskextract.Result) { r.InputID += "changed" }},
		{"version", func(r *taskextract.Result) { r.Version += "changed" }},
		{"model", func(r *taskextract.Result) { r.Model = "another" }},
		{"claim text", func(r *taskextract.Result) { r.Candidates[0].Text = "Deployment is approved." }},
		{"offset", func(r *taskextract.Result) { r.Candidates[0].StartByte++ }},
		{"hash", func(r *taskextract.Result) { r.Candidates[0].BodySHA256 += "0" }},
		{"source revision", func(r *taskextract.Result) { r.Candidates[0].SourceRevision += "changed" }},
		{"unit IDs", func(r *taskextract.Result) { r.Candidates[0].UnitIDs[0] = "u001" }},
		{"candidate count", func(r *taskextract.Result) { r.Candidates = nil }},
		{"scope", func(r *taskextract.Result) { r.Scope.NotCollectedParts = nil }},
		{"completeness", func(r *taskextract.Result) { r.Scope.FactCompletenessAssessed = true }},
		{"human review", func(r *taskextract.Result) { r.HumanReview = "admitted" }},
		{"authority", func(r *taskextract.Result) { r.AuthorityEffect = "canonical_write" }},
		{"raw selection", func(r *taskextract.Result) {
			r.RawText = `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001"}],"reason":""}`
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved, err := taskextract.Replay(request, original)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&saved)
			if _, err := taskextract.Replay(request, saved); !errors.Is(err, taskextract.ErrReplay) {
				t.Fatal("modified local result passed exact replay")
			}
		})
	}
}

func TestSelectorAbstentionReasonMatchesUnicodeSchemaLimit(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	for _, length := range []int{200, 512, 513} {
		raw, err := json.Marshal(map[string]any{
			"outcome": "abstained", "ranges": []any{}, "reason": strings.Repeat("無", length),
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := taskextract.Extract(t.Context(), fixedModel(string(raw)), request)
		if length <= 512 && (err != nil || result.Status != "abstained") {
			t.Fatalf("%d Unicode characters allowed by schema were rejected: %v", length, err)
		}
		if length > 512 && (err == nil || result.Status != "failed") {
			t.Fatal("reason exceeded its Unicode character limit")
		}
	}
}

func TestSelectorRejectsMismatchedModelAndExpiredContextBeforeCalling(t *testing.T) {
	task, source := ticketInput(t)
	llm := fixedModel(`{"outcome":"abstained","ranges":[],"reason":"No match."}`)
	request := prepareContract(t, task, source, "different-model")
	if result, err := taskextract.Extract(t.Context(), llm, request); err == nil || result.ErrorCode != "invalid_request" || llm.calls != 0 {
		t.Fatal("wrong model was called for the bound request")
	}
	request = prepareContract(t, task, source, "synthetic-selector")
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer cancel()
	result, err := taskextract.Extract(ctx, llm, request)
	if !errors.Is(err, context.DeadlineExceeded) || result.ErrorCode != "timeout" || llm.calls != 0 {
		t.Fatal("expired input was called or misclassified")
	}
}

func TestSelectorAllowsSharedContextWithoutClaimingIndependentEvidence(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	raw := `{"outcome":"selected","ranges":[{"start_id":"u003","end_id":"u004"},{"start_id":"u004","end_id":"u005"}],"reason":""}`
	result, err := taskextract.Extract(t.Context(), fixedModel(raw), request)
	if err != nil || len(result.Candidates) != 2 || len(result.Scope.UnselectedUnitIDs) != 3 {
		t.Fatalf("overlapping context was removed or double-counted: %v", err)
	}
	if result.Candidates[0].ID == result.Candidates[1].ID || result.Scope.FactCompletenessAssessed {
		t.Fatal("shared context was confused with one occurrence or completeness")
	}
}

func TestSelectorAllowsSeparatePartsAndExpectedModelMetadata(t *testing.T) {
	task, source := ticketInput(t)
	task.Parts = []string{"description", "history"}
	request := prepareContract(t, task, source, "synthetic-selector")
	raw := `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u001"},{"start_id":"u007","end_id":"u007"}],"reason":""}`
	llm := &scriptedModel{run: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			response := finalResponse(raw)
			response.CustomMetadata = map[string]any{
				"openai_model": "synthetic-selector", "openai_response_id": "synthetic-response-1",
			}
			yield(response, nil)
		}
	}}
	result, err := taskextract.Extract(t.Context(), llm, request)
	if err != nil || len(result.Candidates) != 2 || result.Candidates[0].Part != "description" ||
		result.Candidates[1].Part != "history" || result.Candidates[1].Text != source.Parts[1].Text {
		t.Fatalf("valid separate parts or adapter metadata was rejected/combined: %v", err)
	}
	if strings.Contains(result.Candidates[0].Text, "HISTORY_NOT_FOR_MODEL") {
		t.Fatal("separate parts were spliced into a new statement")
	}
}
