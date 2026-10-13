package mcpgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGovernanceProfilesBindLauncherAndDenyOtherCapabilities(t *testing.T) {
	p, err := runtimeauth.NewPrincipal("test:launcher")
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"withdrawal-reviewer", "source-assessment-reviewer"} {
		b, err := NewBackend(&pgxpool.Pool{}, p, profile)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Tools()) != 4 {
			t.Fatal("closed governance registry")
		}
		for _, name := range []string{"admit_pending_proposal", "admit_reviewed_source_claim", "submit_external_source", "apply_reviewed_wrong_kind"} {
			if _, err := b.CallTool(context.Background(), name, json.RawMessage(`{}`)); !errors.Is(err, runtimeauth.ErrUnauthorized) {
				t.Fatal("cross capability", name, err)
			}
		}
		_, err = b.CallTool(context.Background(), "apply_reviewed_"+b.kind, json.RawMessage(`{"reviewer_id":"forged-human"}`))
		if err == nil {
			t.Fatal("caller injected launcher reviewer")
		}
		for _, tool := range b.Tools() {
			if tool.InputSchema["additionalProperties"] != false {
				t.Fatal("open request schema")
			}
		}
	}
	if _, err := NewBackend(&pgxpool.Pool{}, p, "legacy-operator"); err == nil {
		t.Fatal("legacy profile enabled")
	}
}

func TestBatchSchemaSeparatesHumanHistoryAndExactManifestApproval(t *testing.T) {
	b := &Backend{kind: "withdrawal"}
	prepare, apply := b.Tools()[0].InputSchema, b.Tools()[1].InputSchema
	items := prepare["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	record := items["properties"].(map[string]any)["record"].(map[string]any)
	required := record["required"].([]string)
	for _, name := range []string{"checker_name", "checker_version", "checked_at", "criterion", "criterion_version", "freshness_rule", "checked_materials"} {
		if slices.Contains(required, name) {
			t.Fatalf("manual exclusion requires inapplicable %s", name)
		}
	}
	for _, name := range []string{"historical_reviewer", "historical_reviewed_at", "reason", "original_materials", "missing_fields"} {
		if !slices.Contains(required, name) {
			t.Fatalf("historical field %s became optional", name)
		}
	}
	if !slices.Equal(apply["required"].([]string), []string{"approved_items", "expected_manifest_subject", "manifest"}) {
		t.Fatal("writer accepts an unbound item set")
	}
}
func TestGovernanceDecodeRejectsUnknownTrailingAndOversizedInput(t *testing.T) {
	for _, raw := range []string{`{"hidden":true}`, `{} {}`, `null trailing`, `{"reason":"yes","reason":"no"}`, `{"Reason":"yes","reason":"no"}`, `null`} {
		var in struct{}
		if err := decodeRequest(json.RawMessage(raw), &in); err == nil {
			t.Fatal(raw)
		}
	}
}

func TestManifestSchemaAcceptsSerializedHistoryWithoutWeakeningDecisions(t *testing.T) {
	for _, kind := range []string{"withdrawal", "source_assessment"} {
		t.Run(kind, func(t *testing.T) {
			b := &Backend{kind: kind}
			prepare := governanceTestSchema(t, b.Tools()[0].InputSchema)
			apply := governanceTestSchema(t, b.Tools()[1].InputSchema)
			native := evidenceingestion.GovernanceRequest{Kind: kind, TargetID: "synthetic:target", Operation: "withdraw",
				CorrectionIDs: []string{}, Checks: []evidenceuse.SourceCheck{}, MaterialIDs: []string{}, ResolvesEventIDs: []string{}, Limitations: []string{}}
			if kind == "source_assessment" {
				native.Operation = "assess"
			}
			for _, name := range []string{"omitted_arrays", "empty_arrays", "verification_only", "blocked"} {
				t.Run(name, func(t *testing.T) {
					record := map[string]any{"version": evidenceingestion.InterimGovernanceV1, "record_id": "local:synthetic", "revision": 1,
						"store_id": "store:synthetic", "kind": "exclusion", "purpose": "current-action", "scope": "claim",
						"reason": "TEST APPROVAL STUB", "outcome": "exclude", "historical_reviewer": "TEST APPROVAL STUB",
						"historical_reviewed_at": "2026-10-14T00:00:00Z", "original_materials": []any{map[string]any{"identity": "synthetic:source", "revision": "v1", "content_hash": "sha256:synthetic"}},
						"missing_fields": []string{}, "native": native}
					if name == "empty_arrays" {
						record["checked_materials"], record["limitations"] = []any{}, []any{}
					}
					if name == "verification_only" || name == "blocked" {
						record["kind"] = "verification"
						delete(record, "native")
					}
					if name == "blocked" {
						record["missing_fields"] = []string{"historical_reviewer"}
						record["historical_reviewer"] = ""
					}
					input := map[string]any{"items": []any{map[string]any{"record": record, "reason": "TEST APPROVAL STUB"}}}
					if err := prepare.Validate(governanceTestJSON(t, input)); err != nil {
						t.Fatal("invalid test preparation input", err)
					}
					raw, err := json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					var decoded evidenceingestion.InterimGovernanceRecord
					if err := json.Unmarshal(raw, &decoded); err != nil {
						t.Fatal(err)
					}
					approval := evidenceingestion.GovernanceBatchApproval{
						Manifest: evidenceingestion.GovernanceBatchManifest{Contract: evidenceingestion.GovernanceBatchV1, StoreID: "store:synthetic",
							Items: []evidenceingestion.GovernanceBatchManifestItem{{Record: decoded, RecordHash: "sha256:synthetic", Status: name, Reason: "TEST APPROVAL STUB"}}},
						ExpectedManifestSubject: "governance-batch:sha256:synthetic",
						ApprovedItems: []evidenceingestion.GovernanceBatchDecision{{RecordID: "local:selected", Decision: evidenceingestion.GovernanceDecision{
							RequestID: "synthetic:request", Review: native, ExpectedSubject: "synthetic:subject", Decision: "approved", Reason: "TEST APPROVAL STUB"}}},
					}
					if err := apply.Validate(governanceTestJSON(t, approval)); err != nil {
						t.Fatal("serialized history cannot be returned unchanged", err)
					}
					// The relaxed history representation must never be an executable
					// native request, either in the manifest or the approved decision.
					approval.Manifest.Items[0].NativeRequest = &evidenceingestion.GovernanceRequest{}
					if err := apply.Validate(governanceTestJSON(t, approval)); err == nil {
						t.Fatal("empty executable manifest request accepted")
					}
					approval.Manifest.Items[0].NativeRequest = nil
					approval.ApprovedItems[0].Decision.Review = evidenceingestion.GovernanceRequest{}
					if err := apply.Validate(governanceTestJSON(t, approval)); err == nil {
						t.Fatal("empty approved native request accepted")
					}
					approval.ApprovedItems[0].Decision.Review = native
					approval.ApprovedItems[0].Decision.Review.CorrectionIDs = nil
					if err := apply.Validate(governanceTestJSON(t, approval)); err == nil {
						t.Fatal("null executable request array accepted")
					}
					record["checked_materials"] = nil
					if err := prepare.Validate(governanceTestJSON(t, input)); err == nil {
						t.Fatal("manifest schema modification weakened prepare input")
					}
				})
			}
		})
	}
}

func governanceTestSchema(t *testing.T, input map[string]any) *jsonschema.Resolved {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func governanceTestJSON(t *testing.T, input any) any {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
