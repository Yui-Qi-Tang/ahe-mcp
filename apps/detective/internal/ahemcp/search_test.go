package ahemcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func practicalInput() PracticalQueryInput {
	return PracticalQueryInput{Question: "Was the earthquake measured on the Richter scale?", LifecycleScope: "all", Limit: 20}
}

func practicalFixture(input PracticalQueryInput, recovered bool, outcomes ...string) map[string]any {
	minimum := 2
	if recovered {
		minimum = 1
	}
	matches := []any{}
	for i, outcome := range outcomes {
		matches = append(matches, map[string]any{"record_ref": map[string]string{"kind": "proposal", "id": fmt.Sprintf("occ:test-%d", i)}, "statement_text": "候選寫了芮氏，原文未指定規模類型。", "admission_outcome": outcome,
			"source_refs": []any{map[string]string{"quoted_text": "A preliminary magnitude 4.2 earthquake.", "span_id": "span:S1"}}, "record_state": map[string]string{"authority_status": outcome}, "retrieval_basis": map[string]any{"baseline_matched": false, "surfaces": []any{map[string]string{"surface": "extraction_views.rendered_content"}}}})
	}
	return map[string]any{"schema_version": PracticalQuerySchema, "query": input.Question,
		"query_execution": map[string]any{"original_query": input.Question, "query_mode": PracticalQueryMode, "limit": input.Limit, "candidate_count": len(matches), "global_absence_inference_allowed": false, "truncated": false, "multisurface": map[string]any{"excluded_source_views": 0}, "practical_recovery": map[string]any{"fallback_attempted": recovered, "configured_minimum_english_terms": minimum}, "filters": map[string]string{"source_id": input.SourceID, "admission_outcome": input.AdmissionOutcome, "lifecycle_scope": input.LifecycleScope}},
		"counts":          map[string]int{"returned_matches": len(matches)}, "matches": matches, "source_scopes": []any{map[string]string{"source_id": "synthetic-source"}}, "unknown_future_field": json.Number("9007199254740993")}
}

func practicalLauncher(t *testing.T, mode string, body any) (string, string) {
	t.Helper()
	directory := t.TempDir()
	trace := filepath.Join(directory, "trace.jsonl")
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "response.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nexport DETECTIVE_PRACTICAL_HELPER=" + quote(mode) + "\nexport DETECTIVE_PRACTICAL_TRACE=" + quote(trace) + "\nexec " + quote(os.Args[0]) + " -test.run='^TestPracticalQueryHelper$'\n"
	launcher := filepath.Join(directory, "query-launcher")
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return launcher, trace
}

func TestPracticalQueryReadsOnlyBriefAndPreservesNativeJSON(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		input := practicalInput()
		fixture := practicalFixture(input, recovered, "admitted", "rejected", "audit_only", "pending")
		launcher, trace := practicalLauncher(t, "valid", fixture)
		result, err := SearchPractical(t.Context(), launcher, input)
		if err != nil || result.ReturnedMatches != 4 || result.FallbackAttempted != recovered {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.HanTermPolicy != nil {
			t.Fatal("old v7 without Han policy acquired an invented policy")
		}
		want, _ := json.Marshal(fixture)
		if !sameJSON(result.Response, want) {
			t.Fatal("native provenance, state or additive data lost")
		}
		calls := practicalCalls(t, trace)
		if len(calls) != 4 || calls[3]["method"] != "tools/call" {
			t.Fatalf("calls=%v", calls)
		}
		params := calls[3]["params"].(map[string]any)
		wantArgs := map[string]any{"query": input.Question, "query_mode": PracticalQueryMode, "response_schema": PracticalQuerySchema, "limit": float64(input.Limit), "lifecycle_scope": "all"}
		if params["name"] != "get_grounded_evidence_brief" || !reflect.DeepEqual(params["arguments"], wantArgs) {
			t.Fatalf("params=%v", params)
		}
	}
}

func practicalHanFixture(auxiliary, anchors []string, preserved bool) map[string]any {
	return map[string]any{"version": "han-auxiliary-anchor-v1", "auxiliary_terms": auxiliary,
		"eligible_anchor_terms": anchors, "auxiliary_only_query_preserved": preserved}
}

func TestPracticalQueryHanPolicyPreservesNativeResponse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		question  string
		auxiliary []string
		anchors   []string
		preserved bool
	}{
		{"q12-empty", "太陽閃焰影響了哪些衛星？", []string{"哪些", "影響"}, []string{"太陽", "衛星", "閃焰"}, false},
		{"auxiliary-only", "影響", []string{"影響"}, []string{"影響"}, true},
		{"english-no-han", "Richter earthquake", []string{}, []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := practicalInput()
			input.Question = tc.question
			fixture := practicalFixture(input, false)
			policy := practicalHanFixture(tc.auxiliary, tc.anchors, tc.preserved)
			policy["unknown_additive_number"] = json.Number("9007199254740993")
			fixture["query_execution"].(map[string]any)["practical_recovery"].(map[string]any)["han_term_policy"] = policy
			launcher, _ := practicalLauncher(t, "valid", fixture)
			result, err := SearchPractical(t.Context(), launcher, input)
			if err != nil || result.ReturnedMatches != 0 || result.HanTermPolicy == nil || result.HanTermPolicy.AuxiliaryOnlyQueryPreserved != tc.preserved || !reflect.DeepEqual(result.HanTermPolicy.AuxiliaryTerms, tc.auxiliary) || !reflect.DeepEqual(result.HanTermPolicy.EligibleAnchorTerms, tc.anchors) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			want, _ := json.Marshal(fixture)
			if !sameJSON(result.Response, want) {
				t.Fatal("native policy, terms or additive field changed")
			}
		})
	}
}

func TestPracticalQueryRejectsMalformedHanPolicy(t *testing.T) {
	for _, kind := range []string{"null", "wrong-object-type", "version", "missing-version", "missing-auxiliary", "null-auxiliary", "wrong-auxiliary", "unknown-auxiliary", "missing-anchors", "null-anchors", "wrong-anchor-type", "english-anchor", "auxiliary-anchor", "missing-preserved", "null-preserved", "wrong-preserved", "duplicate-auxiliary", "duplicate-anchor", "invalid-preserved"} {
		t.Run(kind, func(t *testing.T) {
			policy := practicalHanFixture([]string{"影響"}, []string{"地震"}, false)
			var value any = policy
			switch kind {
			case "null":
				value = nil
			case "wrong-object-type":
				value = []string{}
			case "version":
				policy["version"] = "unrecognized-version"
			case "missing-version":
				delete(policy, "version")
			case "missing-auxiliary":
				delete(policy, "auxiliary_terms")
			case "null-auxiliary":
				policy["auxiliary_terms"] = nil
			case "wrong-auxiliary":
				policy["auxiliary_terms"] = "影響"
			case "unknown-auxiliary":
				policy["auxiliary_terms"] = []string{"地震"}
			case "missing-anchors":
				delete(policy, "eligible_anchor_terms")
			case "null-anchors":
				policy["eligible_anchor_terms"] = nil
			case "wrong-anchor-type":
				policy["eligible_anchor_terms"] = []int{1}
			case "english-anchor":
				policy["eligible_anchor_terms"] = []string{"earthquake"}
			case "auxiliary-anchor":
				policy["eligible_anchor_terms"] = []string{"影響"}
			case "missing-preserved":
				delete(policy, "auxiliary_only_query_preserved")
			case "null-preserved":
				policy["auxiliary_only_query_preserved"] = nil
			case "wrong-preserved":
				policy["auxiliary_only_query_preserved"] = "false"
			case "duplicate-auxiliary":
				policy["auxiliary_terms"] = []string{"影響", "影響"}
			case "duplicate-anchor":
				policy["eligible_anchor_terms"] = []string{"地震", "地震"}
			case "invalid-preserved":
				policy["auxiliary_only_query_preserved"] = true
			}
			fixture := practicalFixture(practicalInput(), false)
			fixture["query_execution"].(map[string]any)["practical_recovery"].(map[string]any)["han_term_policy"] = value
			launcher, _ := practicalLauncher(t, "valid", fixture)
			result, err := SearchPractical(t.Context(), launcher, practicalInput())
			if err == nil || len(result.Response) != 0 || result.HanTermPolicy != nil {
				t.Fatal("malformed new policy silently accepted", err)
			}
		})
	}
}

func TestPracticalQueryNoMatchAndExactFilters(t *testing.T) {
	for _, outcome := range []string{"pending", "rejected", "audit_only", "admitted"} {
		input := practicalInput()
		input.SourceID, input.AdmissionOutcome, input.LifecycleScope = "exact-source", outcome, "active"
		launcher, trace := practicalLauncher(t, "valid", practicalFixture(input, true))
		result, err := SearchPractical(t.Context(), launcher, input)
		if err != nil || result.ReturnedMatches != 0 || !result.FallbackAttempted {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		args := practicalCalls(t, trace)[3]["params"].(map[string]any)["arguments"].(map[string]any)
		if args["source_id"] != input.SourceID || args["admission_outcome"] != outcome || args["lifecycle_scope"] != "active" {
			t.Fatal("exact filters changed")
		}
	}
}

func TestPracticalQueryUnsupportedServerFailsBeforeTool(t *testing.T) {
	for _, mode := range []string{"old-mode", "old-schema", "extra-tool", "wrong-endpoint"} {
		launcher, trace := practicalLauncher(t, mode, practicalFixture(practicalInput(), false))
		_, err := SearchPractical(t.Context(), launcher, practicalInput())
		if err == nil {
			t.Fatal("unsupported server accepted")
		}
		if strings.HasPrefix(mode, "old-") && !strings.Contains(err.Error(), "upgrade") {
			t.Fatal("missing upgrade guidance", err)
		}
		for _, call := range practicalCalls(t, trace) {
			if call["method"] == "tools/call" {
				t.Fatal("unsupported tool reached child")
			}
		}
	}
}

func TestPracticalQueryRejectsResponseDrift(t *testing.T) {
	for _, kind := range []string{"schema", "question", "mode", "filter", "absence", "missing-recovery", "missing-attempted", "threshold", "counts", "outcome", "missing-source", "missing-state", "missing-basis", "duplicate", "tool-error", "nonzero-eof"} {
		t.Run(kind, func(t *testing.T) {
			fixture := practicalFixture(practicalInput(), true, "admitted")
			execution := fixture["query_execution"].(map[string]any)
			match := fixture["matches"].([]any)[0].(map[string]any)
			mode := "valid"
			switch kind {
			case "schema":
				fixture["schema_version"] = "grounded-evidence-brief-v6"
			case "question":
				fixture["query"] = "different"
			case "mode":
				execution["query_mode"] = "exact_lexical"
			case "filter":
				execution["filters"].(map[string]string)["source_id"] = "other-source"
			case "absence":
				execution["global_absence_inference_allowed"] = true
			case "missing-recovery":
				delete(execution, "practical_recovery")
			case "missing-attempted":
				delete(execution["practical_recovery"].(map[string]any), "fallback_attempted")
			case "threshold":
				execution["practical_recovery"].(map[string]any)["configured_minimum_english_terms"] = 2
			case "counts":
				fixture["counts"] = map[string]int{"returned_matches": 4}
			case "outcome":
				match["admission_outcome"] = "approved"
			case "missing-source":
				delete(match, "source_refs")
			case "missing-state":
				delete(match, "record_state")
			case "missing-basis":
				delete(match, "retrieval_basis")
			case "duplicate":
				fixture["matches"] = []any{match, match}
				execution["candidate_count"] = 2
				fixture["counts"] = map[string]int{"returned_matches": 2}
			default:
				mode = kind
			}
			launcher, _ := practicalLauncher(t, mode, fixture)
			if result, err := SearchPractical(t.Context(), launcher, practicalInput()); err == nil || len(result.Response) != 0 || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("invalid response accepted or leaked", err)
			}
		})
	}
}

func TestPracticalQueryLocalPreflightAndAuthoritySeparation(t *testing.T) {
	input := practicalInput()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, inactive := range []context.Context{nil, ctx} {
		launcher, trace := practicalLauncher(t, "valid", practicalFixture(input, false))
		if _, err := SearchPractical(inactive, launcher, input); err == nil {
			t.Fatal("inactive context accepted")
		}
		if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("inactive request launched")
		}
	}
	for _, tool := range []string{"get_evidence_record", "search_evidence_records", "submit_text_source", "admit_reviewed_source_claim", "record_reviewed_source_claim_disposition"} {
		launcher, trace := practicalLauncher(t, "valid", practicalFixture(input, false))
		c, err := startQuery(t.Context(), launcher)
		if err != nil {
			t.Fatal(err)
		}
		c.queryBriefOnly = true
		if err := c.initialize(); err != nil {
			t.Fatal(err)
		}
		if err := c.call(tool, map[string]any{}, &json.RawMessage{}); err == nil {
			t.Fatal("foreign authority accepted")
		}
		_ = c.Close()
		for _, call := range practicalCalls(t, trace) {
			if call["method"] == "tools/call" {
				t.Fatal("foreign tool reached child")
			}
		}
	}
}

func TestPracticalQueryRejectsInvalidInputBeforeLaunch(t *testing.T) {
	for _, kind := range []string{"empty", "large", "many-terms", "control", "format-control", "limit", "outcome", "lifecycle", "source-control"} {
		input := practicalInput()
		switch kind {
		case "empty":
			input.Question = ""
		case "large":
			input.Question = strings.Repeat("地", 257)
		case "many-terms":
			input.Question = strings.Repeat("word ", 16) + "word"
		case "control":
			input.Question = "earthquake\x1b"
		case "format-control":
			input.Question = "earthquake\u202e"
		case "limit":
			input.Limit = 101
		case "outcome":
			input.AdmissionOutcome = "reject"
		case "lifecycle":
			input.LifecycleScope = "unknown"
		case "source-control":
			input.SourceID = "source\x1b"
		}
		launcher, trace := practicalLauncher(t, "valid", practicalFixture(practicalInput(), false))
		if _, err := SearchPractical(t.Context(), launcher, input); err == nil {
			t.Fatal("invalid input accepted", kind)
		}
		if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid input launched", kind)
		}
	}
}

func TestPracticalQueryCancellationStopsBlockedRead(t *testing.T) {
	launcher, trace := practicalLauncher(t, "wait-for-cancel", practicalFixture(practicalInput(), false))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, err := startQuery(ctx, launcher)
	if err != nil {
		t.Fatal(err)
	}
	c.queryBriefOnly = true
	defer c.Close()
	if err := c.initialize(); err != nil {
		t.Fatal(err)
	}
	// Start the cancellation deadline after the real child is ready. Race-build
	// startup time is not part of the blocked-read behavior being exercised.
	timer := time.AfterFunc(time.Second, cancel)
	defer timer.Stop()
	var result json.RawMessage
	if err := c.call("get_grounded_evidence_brief", map[string]any{}, &result); err == nil || len(result) != 0 {
		t.Fatal("cancelled call accepted")
	}
	assertTransportJoined(t, c)
	if len(practicalCalls(t, trace)) != 4 {
		t.Fatal("cancelled request was retried or failed before tool call")
	}
}

func practicalCalls(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var call map[string]any
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func TestPracticalQueryHelper(t *testing.T) {
	mode := os.Getenv("DETECTIVE_PRACTICAL_HELPER")
	if mode == "" {
		return
	}
	os.Exit(servePracticalQuery(mode))
}

func servePracticalQuery(mode string) int {
	tracePath := os.Getenv("DETECTIVE_PRACTICAL_TRACE")
	trace, err := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 2
	}
	defer trace.Close()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(tracePath), "response.json"))
	if err != nil {
		return 3
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 4
		}
		if _, err := fmt.Fprintln(trace, scanner.Text()); err != nil {
			return 5
		}
		if request.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = transportInitializeResult("query-server")
			if mode == "wrong-endpoint" {
				result = transportInitializeResult("valid")
			}
		case "tools/list":
			result = queryToolsFixture(mode)
			for _, tool := range result.(map[string]any)["tools"].([]map[string]any) {
				if tool["name"] == "get_grounded_evidence_brief" {
					queryMode, schema := PracticalQueryMode, PracticalQuerySchema
					if mode == "old-mode" {
						queryMode = "exact_lexical"
					}
					if mode == "old-schema" {
						schema = "grounded-evidence-brief-v6"
					}
					tool["inputSchema"] = map[string]any{"type": "object", "properties": map[string]any{"query": map[string]string{"type": "string"}, "query_mode": map[string]any{"type": "string", "enum": []string{queryMode}}, "response_schema": map[string]any{"type": "string", "enum": []string{schema}}}}
				}
			}
		case "tools/call":
			if request.Params.Name != "get_grounded_evidence_brief" {
				return 6
			}
			if mode == "wait-for-cancel" {
				continue
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(raw)}}, "structuredContent": json.RawMessage(raw), "isError": mode == "tool-error"}
		default:
			return 7
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			return 8
		}
	}
	if mode == "nonzero-eof" {
		return 9
	}
	return 0
}
