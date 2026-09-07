package mcpadmin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

func TestRuntimeProfileRequiresExplicitExactName(t *testing.T) {
	for _, value := range []string{"", "query", "trusted", "intake ", " intake", "INTAKE", "secret-unknown-profile"} {
		_, err := ParseRuntimeProfile(value)
		if !errors.Is(err, runtimeauth.ErrUnauthorized) || strings.Contains(err.Error(), "secret-unknown-profile") {
			t.Fatalf("invalid profile did not fail closed with a safe error: %v", err)
		}
	}
	if got := RuntimeProfileNames(); !reflect.DeepEqual(got, []string{"intake", "source-claim-reviewer", "legacy-reviewer", "legacy-operator"}) {
		t.Fatalf("unexpected accepted profile inventory: %v", got)
	}
}

func TestAuthorizedBackendScopesBothInventoryAndCalls(t *testing.T) {
	intake := []string{
		evidenceingestionmcp.ToolSubmitManualEvidence,
		evidenceingestionmcp.ToolSubmitTextSource,
		evidenceingestionmcp.ToolSubmitExternalSource,
		evidenceingestionmcp.ToolSubmitExtractorOutput,
		evidenceingestionmcp.ToolGetExtractorInput,
	}
	reviewer := []string{
		evidenceingestionmcp.ToolAdmitPendingProposal,
		evidenceingestionmcp.ToolRecordPendingProposalDisposition,
		evidenceingestionmcp.ToolSubmitCanonicalContradictionProposal,
		evidenceingestionmcp.ToolAdmitPendingCanonicalContradiction,
		evidenceingestionmcp.ToolRecordPendingCanonicalContradictionDisposition,
		evidenceingestionmcp.ToolAdmitPendingSupersession,
	}
	for _, tc := range []struct {
		profile RuntimeProfile
		want    []string
	}{
		{RuntimeProfileIntake, intake},
		{RuntimeProfileLegacyReviewer, reviewer},
		{RuntimeProfileLegacyOperator, toolNames(legacyIngestionTools())},
	} {
		t.Run(string(tc.profile), func(t *testing.T) {
			recorder, backend := authorizedFixture(t, tc.profile)
			got := toolNames(backend.Tools())
			want := append([]string(nil), tc.want...)
			slices.Sort(got)
			slices.Sort(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("listed tools = %v, want %v", got, want)
			}
			for _, tool := range ingestionTools() {
				before := recorder.count()
				_, err := backend.CallTool(t.Context(), tool.Name, json.RawMessage(`{}`))
				if slices.Contains(want, tool.Name) {
					if err != nil || recorder.count() != before+1 {
						t.Errorf("authorized tool not delegated: %s %v", tool.Name, err)
					}
				} else if !errors.Is(err, runtimeauth.ErrUnauthorized) || recorder.count() != before {
					t.Errorf("hidden tool reached the backend: %s %v", tool.Name, err)
				}
			}
			before := recorder.count()
			_, err := backend.CallTool(t.Context(), "secret-unknown-tool", json.RawMessage(`{"secret":"never delegate"}`))
			if !errors.Is(err, runtimeauth.ErrUnauthorized) || recorder.count() != before || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unknown tool was delegated or reflected: %v", err)
			}
			if backend.Principal().ID != "reviewer:test" || backend.Profile() != tc.profile {
				t.Fatal("launcher binding changed")
			}
		})
	}
}

func TestAuthorizedBackendRejectsIncompleteConfigurationAndRegistryDrift(t *testing.T) {
	principal := runtimeauth.Principal{ID: "reviewer:test"}
	for _, backend := range []mcpstdio.Backend{nil, (*Backend)(nil)} {
		if _, err := NewAuthorizedBackend(backend, principal, RuntimeProfileIntake); err == nil {
			t.Fatal("nil backend accepted")
		}
	}
	for _, principal := range []runtimeauth.Principal{{}, {ID: " principal "}} {
		if _, err := NewAuthorizedBackend(&authorityRecorder{tools: ingestionTools()}, principal, RuntimeProfileIntake); !errors.Is(err, runtimeauth.ErrUnauthenticated) {
			t.Fatalf("invalid principal accepted: %v", err)
		}
	}
	if _, err := NewAuthorizedBackend(&authorityRecorder{tools: ingestionTools()}, principal, ""); !errors.Is(err, runtimeauth.ErrUnauthorized) {
		t.Fatalf("missing profile accepted: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]mcpstdio.Tool) []mcpstdio.Tool
	}{
		{"missing", func(tools []mcpstdio.Tool) []mcpstdio.Tool { return tools[1:] }},
		{"added", func(tools []mcpstdio.Tool) []mcpstdio.Tool { return append(tools, tools[0]) }},
		{"duplicate", func(tools []mcpstdio.Tool) []mcpstdio.Tool { tools[0] = tools[1]; return tools }},
		{"unknown", func(tools []mcpstdio.Tool) []mcpstdio.Tool { tools[0].Name = "secret-unknown-tool"; return tools }},
		{"schema", func(tools []mcpstdio.Tool) []mcpstdio.Tool {
			tools[0].InputSchema["additionalProperties"] = true
			return tools
		}},
		{"annotations", func(tools []mcpstdio.Tool) []mcpstdio.Tool {
			readOnly := true
			tools[0].Annotations.ReadOnlyHint = &readOnly
			return tools
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAuthorizedBackend(&authorityRecorder{tools: tc.mutate(ingestionTools())}, principal, RuntimeProfileLegacyOperator)
			if err == nil || strings.Contains(err.Error(), "secret-unknown-tool") {
				t.Fatalf("inventory drift accepted or reflected: %v", err)
			}
		})
	}
}

func TestAuthorizedDecisionCallsBindLauncherIdentity(t *testing.T) {
	for _, tool := range ingestionTools() {
		if ingestionAuthorityForTool(tool.Name) != authorityDecision {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			recorder, backend := authorizedFixture(t, RuntimeProfileLegacyReviewer)
			for _, argument := range []string{`{}`, `{"decision_by":"reviewer:test"}`} {
				result, err := backend.CallTool(t.Context(), tool.Name, json.RawMessage(argument))
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(result, &fields) != nil || string(fields["decision_by"]) != `"reviewer:test"` {
					t.Fatalf("writer did not receive launcher identity: %s", result)
				}
			}
			if recorder.count() != 2 {
				t.Fatal("decision delegation count changed")
			}
			for _, listed := range backend.Tools() {
				if listed.Name != tool.Name {
					continue
				}
				for _, required := range listed.InputSchema["required"].([]any) {
					if required == "decision_by" {
						t.Fatal("advertised schema still requires caller authority")
					}
				}
				if !strings.Contains(listed.Description, "does not implement exact-reviewed-only") {
					t.Fatal("legacy boundary was omitted from decision tool metadata")
				}
			}
		})
	}
}

func TestAuthorizedDecisionRejectsAmbiguousOrForgedInputWithoutReflection(t *testing.T) {
	recorder, backend := authorizedFixture(t, RuntimeProfileLegacyReviewer)
	for _, payload := range []string{
		`{"decision_by":"secret-attacker"}`,
		`{"decision_by":"reviewer:test","decision_by":"secret-attacker"}`,
		`{"decision_by":"secret-attacker","decision_by":"reviewer:test"}`,
		`{"decision_by":"reviewer:test","decision_\u0062y":"secret-attacker"}`,
		`{"Decision_By":"secret-attacker"}`,
		`{"decision_by":"reviewer:test","Decision_By":"secret-attacker"}`,
		`{"decision_by":"reviewer:test","unknown":"secret-attacker"}`,
		`{"decision_by":null}`, `{"decision_by":42}`, `{"decision_by":""}`,
		`{"decision_by":" reviewer:test "}`, `{"decision_reason":"a","decision_reason":"b"}`,
		`[]`, `null`, `{} {}`, `{`, `{"decision_by":"reviewer:test"} trailing`,
		string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}),
		strings.Repeat("x", maxAuthorizedArgumentsBytes+1),
	} {
		_, err := backend.CallTool(t.Context(), evidenceingestionmcp.ToolAdmitPendingProposal, json.RawMessage(payload))
		if !errors.Is(err, runtimeauth.ErrUnauthorized) {
			t.Fatalf("unsafe decision arguments accepted: error=%v", err)
		}
		encoded, marshalErr := json.Marshal(err)
		if marshalErr != nil || strings.Contains(string(encoded), "secret-attacker") || strings.Contains(err.Error(), "secret-attacker") {
			t.Fatal("rejected decision payload escaped through transport")
		}
	}
	if recorder.count() != 0 {
		t.Fatal("rejected input reached the backend")
	}
}

func TestAuthorizedDecisionPreservesNonAuthorityPayload(t *testing.T) {
	_, backend := authorizedFixture(t, RuntimeProfileLegacyReviewer)
	input := json.RawMessage(`{"proposal_occurrence_id":"occ:1","decision_reason":"explicit approval","derivation":{"parent_node_ids":["canon:1","canon:2"],"method":"bounded","producer":"test","trace_ref":"trace:1"}}`)
	before := append(json.RawMessage(nil), input...)
	result, err := backend.CallTool(t.Context(), evidenceingestionmcp.ToolAdmitPendingProposal, input)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if json.Unmarshal(input, &want) != nil || json.Unmarshal(result, &got) != nil {
		t.Fatal("invalid encoded payload")
	}
	want["decision_by"] = "reviewer:test"
	if !reflect.DeepEqual(want, got) || string(input) != string(before) {
		t.Fatal("binding changed non-authority arguments or caller bytes")
	}
}

func TestAuthorizedBackendMetadataIsDetachedDuringParallelCalls(t *testing.T) {
	recorder, backend := authorizedFixture(t, RuntimeProfileLegacyReviewer)
	original, err := json.Marshal(backend.Tools())
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			for range 10 {
				listed := backend.Tools()
				listed[0].Name = "caller-mutated"
				listed[0].InputSchema["properties"].(map[string]any)["decision_by"] = "caller-mutated"
				*listed[0].Annotations.ReadOnlyHint = true
				if _, err := backend.CallTool(t.Context(), evidenceingestionmcp.ToolAdmitPendingProposal, json.RawMessage(`{}`)); err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
	after, err := json.Marshal(backend.Tools())
	if err != nil || string(after) != string(original) || recorder.count() != 120 {
		t.Fatal("tool metadata or capability set was mutated by callers")
	}
}

func TestAuthorizedBackendRejectsUnavailableContextOrRuntime(t *testing.T) {
	recorder, backend := authorizedFixture(t, RuntimeProfileIntake)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := backend.CallTool(ctx, evidenceingestionmcp.ToolGetExtractorInput, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context was delegated: %v", err)
	}
	if _, err := backend.CallTool(nil, evidenceingestionmcp.ToolGetExtractorInput, nil); !errors.Is(err, runtimeauth.ErrUnauthorized) {
		t.Fatalf("nil context was delegated: %v", err)
	}
	var absent *AuthorizedBackend
	if _, err := absent.CallTool(t.Context(), evidenceingestionmcp.ToolGetExtractorInput, nil); !errors.Is(err, runtimeauth.ErrUnauthenticated) || len(absent.Tools()) != 0 {
		t.Fatalf("absent runtime did not fail closed: %v", err)
	}
	if recorder.count() != 0 {
		t.Fatal("unavailable context reached backend")
	}
}

type authorityRecorder struct {
	tools []mcpstdio.Tool
	mu    sync.Mutex
	calls int
}

func (b *authorityRecorder) Tools() []mcpstdio.Tool { return b.tools }

func (b *authorityRecorder) CallTool(_ context.Context, _ string, arguments json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	return append(json.RawMessage(nil), arguments...), nil
}

func (b *authorityRecorder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func authorizedFixture(t *testing.T, profile RuntimeProfile) (*authorityRecorder, *AuthorizedBackend) {
	t.Helper()
	recorder := &authorityRecorder{tools: ingestionTools()}
	backend, err := NewAuthorizedBackend(recorder, runtimeauth.Principal{ID: "reviewer:test"}, profile)
	if err != nil {
		t.Fatal(err)
	}
	return recorder, backend
}

func toolNames(tools []mcpstdio.Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}
