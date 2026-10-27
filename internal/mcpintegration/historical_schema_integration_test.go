//go:build integration

package mcpintegration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
)

func historicalInvalidFreshRequests(t *testing.T, f historicalProcessFixture, in evidenceingestion.HistoricalDerivationDecision) {
	t.Helper()
	history := f.processes["historical-derivation-reviewer"]
	for _, field := range []string{"use_policy", "use_class", "reviewer_id"} {
		for _, value := range []any{"", nil, "caller"} {
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			var bad map[string]any
			if err := json.Unmarshal(raw, &bad); err != nil {
				t.Fatal(err)
			}
			bad["review"].(map[string]any)["derivation"].(map[string]any)[field] = value
			relationRejected(t, history, mcpendpoints.ToolHistoricalDecision, bad)
			relationRejected(t, history, mcpendpoints.ToolHistoricalReview, bad["review"])
		}
	}
	for _, mutate := range []func(*evidenceingestion.HistoricalDerivationDecision){
		func(x *evidenceingestion.HistoricalDerivationDecision) { x.Review.Derivation.ParentNodeIDs = nil },
		func(x *evidenceingestion.HistoricalDerivationDecision) {
			x.Review.Derivation.ParentNodeIDs = []string{in.Review.Derivation.ParentNodeIDs[0], in.Review.Derivation.ParentNodeIDs[0]}
		},
		func(x *evidenceingestion.HistoricalDerivationDecision) {
			x.Review.Derivation.ParentNodeIDs = []string{"canon-node:missing"}
		},
		func(x *evidenceingestion.HistoricalDerivationDecision) {
			x.Review.Derivation.Method = strings.Repeat("界", 1400)
		},
		func(x *evidenceingestion.HistoricalDerivationDecision) { x.Review.Derivation.Producer = "" },
		func(x *evidenceingestion.HistoricalDerivationDecision) { x.Review.Derivation.TraceRef = "" },
		func(x *evidenceingestion.HistoricalDerivationDecision) {
			x.ExpectedSubject = "historical-derivation-review:sha256:" + strings.Repeat("0", 64)
		},
		func(x *evidenceingestion.HistoricalDerivationDecision) { x.DecisionReason = "" },
		func(x *evidenceingestion.HistoricalDerivationDecision) { x.Decision = "unknown" },
	} {
		bad := in
		mutate(&bad)
		relationRejected(t, history, mcpendpoints.ToolHistoricalDecision, bad)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := json.RawMessage(strings.TrimSuffix(string(raw), "}") + `,"decision":"approved"}`)
	relationRejected(t, history, mcpendpoints.ToolHistoricalDecision, duplicate)
}

func TestIntegrationHistoricalManualProfileMCPBoundary(t *testing.T) {
	f := newHistoricalProcessFixture(t)
	intake := f.processes["intake"]
	before := historicalProcessState(t, f)
	for _, body := range []string{
		`null`, `{}`, `"not an object"`,
		`{"contract_version":"manual-source-review-profile/v1","title":"sample","location":"manual:sample","coverage":"submitted_text","limitations":null}`,
		`{"contract_version":"manual-source-review-profile/v1","title":"sample","location":"manual:sample","coverage":"submitted_text","limitations":[],"title":"duplicate"}`,
		`{"contract_version":"manual-source-review-profile/v1","title":"sample","location":"manual:sample","coverage":"exact_excerpt","limitations":[]}`,
	} {
		bad := json.RawMessage(`{"request_id":"invalid-qualified","source_id":"invalid-qualified","source_version":"1","raw_text":"synthetic","manual_review_profile":` + body + `}`)
		relationRejected(t, intake, "submit_text_source", bad)
	}
	if !reflect.DeepEqual(before, historicalProcessState(t, f)) {
		t.Fatal("invalid manual qualification wrote source authority")
	}
	// The same actual process must still accept first-capture qualification and
	// display it in source review after all fresh invalid requests.
	historicalProcessSources(t, f)
}
