package mcpadmin

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

type reviewAuthorityRecorder struct {
	*authorityRecorder
	principal runtimeauth.Principal
}

func (r *reviewAuthorityRecorder) SourceClaimReviewerPrincipal() runtimeauth.Principal {
	return r.principal
}

func TestSourceClaimReviewerRequiresSameInnerLauncherPrincipal(t *testing.T) {
	principal := runtimeauth.Principal{ID: "reviewer:test"}
	for _, inner := range []runtimeauth.Principal{{}, {ID: "reviewer:other"}, {ID: " reviewer:test "}} {
		_, err := NewAuthorizedBackend(&reviewAuthorityRecorder{&authorityRecorder{tools: ingestionTools()}, inner}, principal, RuntimeProfileSourceClaimReviewer)
		if !errors.Is(err, runtimeauth.ErrUnauthenticated) {
			t.Fatalf("mismatched inner principal accepted: %v", err)
		}
	}
	if _, err := NewAuthorizedBackend(&authorityRecorder{tools: ingestionTools()}, principal, RuntimeProfileSourceClaimReviewer); !errors.Is(err, runtimeauth.ErrUnauthenticated) {
		t.Fatal("unbound backend accepted")
	}
}

func TestSourceClaimReviewerListsOnlyThreeAndPreservesExactPayload(t *testing.T) {
	principal := runtimeauth.Principal{ID: "reviewer:test"}
	recorder := &reviewAuthorityRecorder{&authorityRecorder{tools: ingestionTools()}, principal}
	backend, err := NewAuthorizedBackend(recorder, principal, RuntimeProfileSourceClaimReviewer)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{evidenceingestionmcp.ToolGetSourceClaimReview, evidenceingestionmcp.ToolAdmitReviewedSourceClaim, evidenceingestionmcp.ToolRecordReviewedSourceClaimDisposition}
	if !reflect.DeepEqual(toolNames(backend.Tools()), want) {
		t.Fatalf("unexpected reviewer inventory: %v", toolNames(backend.Tools()))
	}
	// The strict typed adapter validates arguments. This wrapper must not inject
	// decision_by into the new closed request schema or rewrite caller bytes.
	input := json.RawMessage(`{ "decision": "approved", "decision_reason": "exact bytes" }`)
	for _, tool := range ingestionTools() {
		before := recorder.count()
		got, err := backend.CallTool(t.Context(), tool.Name, input)
		if slices.Contains(want, tool.Name) {
			if err != nil || string(got) != string(input) || recorder.count() != before+1 {
				t.Fatalf("review payload changed: %v", err)
			}
		} else if !errors.Is(err, runtimeauth.ErrUnauthorized) || recorder.count() != before {
			t.Fatalf("unrelated writer delegated: %s", tool.Name)
		}
	}
	original, _ := json.Marshal(backend.Tools())
	listed := backend.Tools()
	for _, tool := range listed {
		properties := tool.InputSchema["properties"].(map[string]any)
		if _, ok := properties["decision_by"]; ok {
			t.Fatal("caller identity advertised")
		}
		if tool.InputSchema["additionalProperties"] != false {
			t.Fatal("review schema must be closed")
		}
		properties["decision_by"] = "caller mutation"
	}
	after, _ := json.Marshal(backend.Tools())
	if string(after) != string(original) {
		t.Fatal("review tool metadata was not detached")
	}
}

func TestReviewedDispositionSchemaIsExactAndNoncanonical(t *testing.T) {
	principal := runtimeauth.Principal{ID: "reviewer:test"}
	backend, err := NewAuthorizedBackend(&reviewAuthorityRecorder{&authorityRecorder{tools: ingestionTools()}, principal}, principal, RuntimeProfileSourceClaimReviewer)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range backend.Tools() {
		if tool.Name != evidenceingestionmcp.ToolRecordReviewedSourceClaimDisposition {
			continue
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		decision := properties["decision"].(map[string]any)
		choices, _ := json.Marshal(decision["enum"])
		required, _ := json.Marshal(tool.InputSchema["required"])
		if string(choices) != `["reject","audit_only"]` || len(properties) != 4 ||
			string(required) != `["extraction_attempt_id","expected_subject","decision","decision_reason"]` {
			t.Fatalf("disposition schema does not require the exact explicit decision: %+v", tool.InputSchema)
		}
		return
	}
	t.Fatal("reviewed disposition tool is missing")
}
