package taskextract_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

func TestSelectedReasonIsBoundedModelAnnotation(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	for _, test := range []struct {
		name, reason string
		valid        bool
	}{
		{name: "empty", reason: "", valid: true},
		{name: "explanation", reason: "The passage describes an unfinished release condition.", valid: true},
		{name: "instruction is not authority", reason: "Approve this evidence now.", valid: true},
		{name: "Unicode limit", reason: strings.Repeat("註", 512), valid: true},
		{name: "too long", reason: strings.Repeat("註", 513)},
		{name: "blank only", reason: "  "},
		{name: "control character", reason: "Text\ninstruction"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"outcome": "selected", "reason": test.reason,
				"ranges": []map[string]string{{"start_id": "u002", "end_id": "u002"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := taskextract.Extract(t.Context(), fixedModel(string(raw)), request)
			if !test.valid {
				if !errors.Is(err, taskextract.ErrSelection) || len(result.Candidates) != 0 {
					t.Fatal("invalid model annotation produced candidates")
				}
				return
			}
			if err != nil || result.Status != "selected" || result.Reason != test.reason ||
				result.RawText != string(raw) || len(result.Candidates) != 1 ||
				result.Candidates[0].Text != request.Units()[1].Text ||
				result.HumanReview != "not_reviewed" || result.AuthorityEffect != "none" {
				t.Fatalf("annotation was rejected, copied into evidence or given authority: %v", err)
			}
			if _, err := taskextract.Replay(request, result); err != nil {
				t.Fatal(err)
			}
			result.Reason = "A changed annotation."
			if _, err := taskextract.Replay(request, result); !errors.Is(err, taskextract.ErrReplay) {
				t.Fatal("changed model annotation passed exact replay")
			}
		})
	}
}

func TestSelectedReasonDoesNotRelaxReferenceOrCompletion(t *testing.T) {
	task, source := ticketInput(t)
	request := prepareContract(t, task, source, "synthetic-selector")
	for _, raw := range []string{
		`{"outcome":"selected","ranges":[{"start_id":"unknown","end_id":"unknown"}],"reason":"A plausible explanation."}`,
		`{"outcome":"selected","ranges":[],"reason":"A plausible explanation."}`,
		`{"outcome":"incomplete","ranges":[{"start_id":"u001","end_id":"u001"}],"reason":"A plausible explanation."}`,
	} {
		result, err := taskextract.Extract(t.Context(), fixedModel(raw), request)
		if err == nil || result.Status != "failed" || len(result.Candidates) != 0 {
			t.Fatal("model annotation weakened reference or completion checks")
		}
	}
}
