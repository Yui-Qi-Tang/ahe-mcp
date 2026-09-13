package taskextract_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
	"google.golang.org/adk/v2/model"
)

func TestSelectorSuppliedScopePolicy(t *testing.T) {
	for _, coverage := range []string{"full_document", "exact_excerpt", "truncated_document"} {
		t.Run(coverage, func(t *testing.T) {
			task, source := ticketInput(t)
			source.Coverage = coverage
			if coverage != "full_document" {
				source.Limitations = []string{"Comments were not collected; only supplied description can be selected."}
			}
			request := prepareContract(t, task, source, "synthetic-selector")
			llm := &scriptedModel{run: func(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
				instruction := req.Config.SystemInstruction.Parts[0].Text
				if taskextract.PromptVersion != "detective-task-selector/v3" ||
					!strings.Contains(instruction, "Missing or excluded source parts alone are not a reason to return incomplete.") ||
					!strings.Contains(instruction, "you do not decide collection completeness") {
					t.Error("selector lacks the supplied-input completion policy")
				}
				var input struct {
					Version     string
					Objective   string
					SourceTitle string `json:"source_title"`
					Units       []taskextract.Unit
				}
				raw := []byte(req.Contents[0].Parts[0].Text)
				var fields map[string]json.RawMessage
				if json.Unmarshal(raw, &input) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 4 ||
					input.Version != taskextract.ModelInputVersion || input.Objective != task.Objective ||
					input.SourceTitle != source.Title || !reflect.DeepEqual(input.Units, request.Units()) {
					t.Error("selector input is not the versioned objective/title/supplied-unit projection")
				}
				return func(yield func(*model.LLMResponse, error) bool) {
					yield(finalResponse(`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`), nil)
				}
			}}
			result, err := taskextract.Extract(t.Context(), llm, request)
			if err != nil || llm.calls != 1 || len(result.Candidates) != 1 ||
				result.Candidates[0].Text != source.Parts[0].Text[:request.Units()[1].EndByte] ||
				!reflect.DeepEqual(request.Source(), source) || !reflect.DeepEqual(request.Task(), task) ||
				!slices.Equal(result.Scope.NotCollectedParts, []string{"comments"}) ||
				!slices.Equal(result.Scope.NotProvidedParts, []string{"history"}) ||
				result.Scope.FactCompletenessAssessed || result.HumanReview != "not_reviewed" || result.AuthorityEffect != "none" {
				t.Fatalf("scoped selection altered source, scope or authority: %v", err)
			}
		})
	}
}

func TestSelectorMissingPartsDoNotRewriteOutcome(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	for _, test := range []struct {
		name, raw, status string
		wantErr           error
	}{
		{
			name: "no relevant supplied evidence", status: "abstained",
			raw: `{"outcome":"abstained","ranges":[],"reason":"No relevant passage was found in the supplied units; missing comments were not assessed."}`,
		},
		{
			name: "unable to finish supplied evidence", status: "failed", wantErr: taskextract.ErrIncomplete,
			raw: `{"outcome":"incomplete","ranges":[],"reason":"I could not finish selecting the supplied units."}`,
		},
		{
			name: "missing part reason is still not success", status: "failed", wantErr: taskextract.ErrIncomplete,
			raw: `{"outcome":"incomplete","ranges":[],"reason":"Comments are missing."}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			llm := fixedModel(test.raw)
			result, err := taskextract.Extract(t.Context(), llm, request)
			if !errors.Is(err, test.wantErr) || result.Status != test.status || result.RawText != test.raw ||
				len(result.Candidates) != 0 || llm.calls != 1 || result.Scope.FactCompletenessAssessed ||
				!slices.Equal(result.Scope.NotCollectedParts, []string{"comments"}) ||
				!slices.Equal(result.Scope.UnselectedUnitIDs, request.Scope().UnselectedUnitIDs) {
				t.Fatalf("missing fields changed the model outcome: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSelectorRejectsPriorPromptSuccessfulResults(t *testing.T) {
	task, source := ticketInput(t)
	task.Objective = "採集這張 ticket 的上線條件，包含尚未完成的條件。"
	request := prepareContract(t, task, source, "synthetic-selector")
	// Computed from this exact synthetic fixture, not a live
	// model result. Policy changes must not accept an old successful selection.
	previousIDs := []string{
		"task-input:sha256:3929360c9300a17609f38688362c2b7861da979392ce9888227baf14426ca6c3", // selector/v1
		"task-input:sha256:ca6de8c00aac944b7a3161c80ead7f220e34d1521077c267a7bfc84ce2eec587", // selector/v2
	}
	result, err := taskextract.Extract(t.Context(), fixedModel(`{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taskextract.Replay(request, result); err != nil {
		t.Fatal("current successful selection must replay")
	}
	for _, previousID := range previousIDs {
		if request.InputID() == previousID {
			t.Fatal("current policy retained a previous request identity")
		}
		result.InputID = previousID
		result.Candidates[0].ID = fmt.Sprintf("task-candidate:sha256:%x", sha256.Sum256([]byte(previousID+"\n0:1")))
		if _, err := taskextract.Replay(request, result); !errors.Is(err, taskextract.ErrReplay) {
			t.Fatal("new request accepted the old policy's successful result")
		}
	}
}

func TestSelectorInputOmitsCollectionStateButBindsIt(t *testing.T) {
	// A narrower model input must not collapse the controller's replay identity
	// or rewrite the original task/source that a later reviewer needs.
	inputFor := func(task taskextract.Task, source taskextract.Source) (string, *taskextract.Request) {
		t.Helper()
		request := prepareContract(t, task, source, "synthetic-selector")
		var input string
		llm := &scriptedModel{run: func(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
			input = req.Contents[0].Parts[0].Text
			return func(yield func(*model.LLMResponse, error) bool) {
				yield(finalResponse(`{"outcome":"abstained","ranges":[],"reason":"Synthetic response only."}`), nil)
			}
		}}
		if _, err := taskextract.Extract(t.Context(), llm, request); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(request.Task(), task) || !reflect.DeepEqual(request.Source(), source) {
			t.Fatal("narrower model input rewrote the frozen review context")
		}
		return input, request
	}
	task, source := ticketInput(t)
	before, original := inputFor(task, source)
	for _, change := range []string{"missing part", "unprovided text", "provenance and limits"} {
		t.Run(change, func(t *testing.T) {
			task, source := ticketInput(t)
			switch change {
			case "missing part":
				task.Parts = append(task.Parts, "attachments")
			case "unprovided text":
				source.Parts[1].Text = "Different history, still not supplied."
			case "provenance and limits":
				task.SourceID, source.ID = "another-source", "another-source"
				source.Revision, source.Location = "revision-2", "https://example.invalid/another"
				source.Coverage, source.Limitations = "exact_excerpt", []string{"Comments and attachments are unavailable."}
			}
			after, request := inputFor(task, source)
			if before != after || original.InputID() == request.InputID() {
				t.Fatal("collection-only metadata entered the selector or lost its identity binding")
			}
		})
	}
}
