//go:build integration

package mcpintegration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

const practicalLabMode = "practical_multisurface_lexical_v1"
const practicalLabSchema = "grounded-evidence-brief-v7"
const practicalResearchPlanSHA = "97b23dc47f4921197dd2421be421e6762b6ca4c48c448ede9f10c2a4ee0210d7"
const practicalResearchResultSHA = "8d50e4c4603bcb2fb0a022c52e14e15a5be21a87b944514dcea78c70828a5865"

// This wrapper freezes the new mode without rewriting historical questions,
// relevance labels, or the saved three-mode research plan and responses.
type practicalLabPlan struct {
	Contract                string                  `json:"contract"`
	ResearchPlan            hanOutcomeFile          `json:"research_plan"`
	ResearchResult          hanOutcomeFile          `json:"research_result"`
	Mode                    multisurfaceLabModePlan `json:"mode"`
	PlannedBriefCalls       int                     `json:"planned_brief_calls"`
	Replays                 int                     `json:"replays"`
	ExpectedRelevantPairs   int                     `json:"expected_relevant_pairs"`
	ExpectedNonemptyQueries int                     `json:"expected_nonempty_queries"`
}

func practicalDecodePlan(data []byte) (practicalLabPlan, error) {
	var p practicalLabPlan
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, fmt.Errorf("practical plan must contain one known JSON object")
	}
	mode := multisurfaceLabModePlan{"practical", practicalLabMode, practicalLabSchema}
	if p.Contract != "practical-multisurface-read-only-plan-v1" || p.Mode != mode ||
		p.PlannedBriefCalls != 96 || p.Replays != 2 || p.ExpectedRelevantPairs != 18 ||
		p.ExpectedNonemptyQueries != 11 || p.ResearchPlan.SHA256 != practicalResearchPlanSHA ||
		p.ResearchResult.SHA256 != practicalResearchResultSHA || !filepath.IsAbs(p.ResearchPlan.Path) ||
		!filepath.IsAbs(p.ResearchResult.Path) {
		return p, fmt.Errorf("practical plan changed frozen research inputs or the four-mode matrix")
	}
	return p, nil
}

func practicalReadPlan(t *testing.T, researchPlanData []byte) (*practicalLabPlan, map[string]evidencequerymcp.GroundedEvidenceBriefResponse, map[string]string) {
	t.Helper()
	path := os.Getenv("AHE_PRACTICAL_LAB_PLAN")
	if !filepath.IsAbs(path) {
		t.Fatal("practical plan path must be absolute")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read frozen practical plan")
	}
	p, err := practicalDecodePlan(data)
	if err != nil {
		t.Fatal(err)
	}
	if p.ResearchPlan.Path != os.Getenv("AHE_MULTISURFACE_LAB_PLAN") || hanOutcomeHash(researchPlanData) != practicalResearchPlanSHA {
		t.Fatal("practical plan does not name the unchanged research plan")
	}
	resultData, err := os.ReadFile(p.ResearchResult.Path)
	if err != nil || hanOutcomeHash(resultData) != practicalResearchResultSHA {
		t.Fatal("frozen research full-response hash mismatch")
	}
	var saved multisurfaceLabReport
	decoder := json.NewDecoder(bytes.NewReader(resultData))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&saved) != nil || !saved.Passed || !saved.Unchanged || saved.Completed != 72 || saved.PlanSHA256 != practicalResearchPlanSHA {
		t.Fatal("saved research result is not the completed frozen read-only matrix")
	}
	responses := map[string]evidencequerymcp.GroundedEvidenceBriefResponse{}
	for _, o := range saved.Observations {
		if o.Replay != 0 {
			continue
		}
		key := o.CaseID + "/" + o.Mode
		if _, exists := responses[key]; exists || !slices.Contains([]string{"baseline", "han", "multisurface"}, o.Mode) {
			t.Fatal("saved research response keys are duplicated or unknown")
		}
		responses[key] = o.Response
	}
	if len(responses) != 36 {
		t.Fatal("saved research must contain 36 distinct full responses")
	}
	hashes := map[string]string{path: hanOutcomeHash(data), p.ResearchPlan.Path: practicalResearchPlanSHA, p.ResearchResult.Path: practicalResearchResultSHA}
	return &p, responses, hashes
}

func practicalCheckRawResponse(t *testing.T, o hanOutcomeObservation, raw json.RawMessage, frozen map[string]evidencequerymcp.GroundedEvidenceBriefResponse, first map[string]json.RawMessage) {
	t.Helper()
	var actual any
	if json.Unmarshal(raw, &actual) != nil {
		t.Fatal("practical run did not retain the raw structured response")
	}
	key := o.CaseID + "/" + o.Mode
	if o.Mode != "practical" {
		data, err := json.Marshal(frozen[key])
		var expected any
		if err != nil || json.Unmarshal(data, &expected) != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal("old mode raw JSON differs from the complete frozen response")
		}
	}
	if o.Replay == 0 {
		first[key] = slices.Clone(raw)
		return
	}
	var expected any
	if json.Unmarshal(first[key], &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatal("raw JSON response differs on identical replay")
	}
}

func practicalCheckFiles(t *testing.T, hashes map[string]string) {
	t.Helper()
	for path, hash := range hashes {
		data, err := os.ReadFile(path)
		if err != nil || hanOutcomeHash(data) != hash {
			t.Fatal("frozen practical plan or historical research artifact changed")
		}
	}
}

func practicalCheckCatalog(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var catalog struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]struct{ Enum []string } `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if json.Unmarshal(raw, &catalog) != nil {
		t.Fatal("cannot decode live practical catalog")
	}
	for _, tool := range catalog.Tools {
		if tool.Name == "get_grounded_evidence_brief" && slices.Contains(tool.InputSchema.Properties["query_mode"].Enum, practicalLabMode) && slices.Contains(tool.InputSchema.Properties["response_schema"].Enum, practicalLabSchema) {
			return
		}
	}
	t.Fatal("live query catalog lacks explicit practical mode/v7")
}

// Inspect the public JSON shape independently of the adapter's Go field names.
func practicalFallbackCount(t *testing.T, r evidencequerymcp.GroundedEvidenceBriefResponse) int {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal("cannot inspect practical public execution metadata")
	}
	var envelope struct {
		Execution struct {
			Practical *struct {
				Attempted bool `json:"fallback_attempted"`
				Minimum   int  `json:"configured_minimum_english_terms"`
			} `json:"practical_recovery"`
		} `json:"query_execution"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Execution.Practical == nil {
		t.Fatal("practical execution metadata is missing")
	}
	meta := envelope.Execution.Practical
	if meta.Attempted {
		if meta.Minimum != 1 {
			t.Fatal("practical fallback did not disclose its single-English-term threshold")
		}
		return 1
	}
	if meta.Minimum != 2 {
		t.Fatal("unattempted practical fallback changed its research threshold")
	}
	return 0
}

func practicalCheckObservation(t *testing.T, o hanOutcomeObservation, tc hanOutcomeCase, frozen map[string]evidencequerymcp.GroundedEvidenceBriefResponse) {
	t.Helper()
	if o.Mode != "practical" {
		want, ok := frozen[o.CaseID+"/"+o.Mode]
		if !ok || !reflect.DeepEqual(o.Response, want) {
			t.Fatal("an old mode's full response differs from the frozen research result")
		}
		return
	}
	research, ok := frozen[o.CaseID+"/multisurface"]
	if !ok {
		t.Fatal("practical response lacks its frozen research control")
	}
	attempts, originalAttempts := o.Response.QueryExecution.Attempts, research.QueryExecution.Attempts
	if len(attempts) < len(originalAttempts) || !reflect.DeepEqual(attempts[:len(originalAttempts)], originalAttempts) {
		t.Fatal("practical mode changed the research first-round attempt trace")
	}
	if tc.ID == "Q08" {
		if practicalFallbackCount(t, o.Response) != 1 || !slices.Equal(o.ReturnedIDs, tc.ExpectedRelevantIDs) || len(o.ExtraIDs) != 0 || len(o.MissingRelevantIDs) != 0 || len(research.Matches) != 0 {
			t.Fatal("Q08 did not recover exactly its pre-labeled review record")
		}
		if len(attempts) != len(originalAttempts)+1 || attempts[len(attempts)-1].Strategy != "practical_single_english_term_recovery" {
			t.Fatal("Q08 did not perform exactly one disclosed practical fallback")
		}
		return
	}
	if practicalFallbackCount(t, o.Response) != 0 || !reflect.DeepEqual(o.Response.Matches, research.Matches) || !reflect.DeepEqual(o.Response.SourceScopes, research.SourceScopes) || len(o.MissingRelevantIDs) != 0 {
		t.Fatal("practical mode changed a nonempty research result or lost a pre-labeled record")
	}
}

func TestIntegrationPracticalReadOnlyLab(t *testing.T) {
	if os.Getenv("AHE_PRACTICAL_LAB_PLAN") == "" {
		t.Skip("set AHE_PRACTICAL_LAB_PLAN and the five dedicated multisurface settings after freezing runtime")
	}
	runMultisurfaceReadOnlyLab(t, true, false)
}

func TestPracticalPlanContract(t *testing.T) {
	p := practicalLabPlan{
		Contract:          "practical-multisurface-read-only-plan-v1",
		ResearchPlan:      hanOutcomeFile{Path: "/frozen/plan.json", SHA256: practicalResearchPlanSHA},
		ResearchResult:    hanOutcomeFile{Path: "/frozen/result.json", SHA256: practicalResearchResultSHA},
		Mode:              multisurfaceLabModePlan{"practical", practicalLabMode, practicalLabSchema},
		PlannedBriefCalls: 96, Replays: 2, ExpectedRelevantPairs: 18, ExpectedNonemptyQueries: 11,
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := practicalDecodePlan(data); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"mode", "schema", "calls", "labels", "research_plan", "research_result", "relative_path"} {
		changed := p
		switch change {
		case "mode":
			changed.Mode.QueryMode = multisurfaceLabMode
		case "schema":
			changed.Mode.ResponseSchema = multisurfaceLabSchema
		case "calls":
			changed.PlannedBriefCalls = 72
		case "labels":
			changed.ExpectedRelevantPairs = 17
		case "research_plan":
			changed.ResearchPlan.SHA256 = "changed"
		case "research_result":
			changed.ResearchResult.SHA256 = "changed"
		case "relative_path":
			changed.ResearchResult.Path = "result.json"
		}
		data, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := practicalDecodePlan(data); err == nil {
			t.Fatalf("accepted changed practical plan: %s", change)
		}
	}
}

func TestPracticalFrozenPlan(t *testing.T) {
	if os.Getenv("AHE_PRACTICAL_PLAN_CHECK") == "" {
		t.Skip("optional offline check; never opens DB")
	}
	t.Setenv("AHE_PRACTICAL_LAB_PLAN", os.Getenv("AHE_PRACTICAL_PLAN_CHECK"))
	data, err := os.ReadFile(os.Getenv("AHE_MULTISURFACE_LAB_PLAN"))
	if err != nil {
		t.Fatal("cannot read frozen research plan")
	}
	p, responses, hashes := practicalReadPlan(t, data)
	practicalCheckFiles(t, hashes)
	if p.PlannedBriefCalls != 96 || len(responses) != 36 {
		t.Fatal("practical offline matrix mismatch")
	}
}
