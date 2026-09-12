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
	"unicode"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

const practicalPreviousResultSHA = "a26c4b0269c3f90de5af789dd12526909fe272a73d38620b6fb0b26e3341cb43"
const practicalAnchorPolicyVersion = "han-auxiliary-anchor-v1"

// Anchor replay still names the same qualified historical clone; relocating
// the checkout must not turn an arbitrary report directory into accepted PGDATA.
func practicalAnchorPGData(t *testing.T) string {
	t.Helper()
	return filepath.Join(multisurfaceLabBin(t), "multisurface-lab.pi2AGw", "pgdata")
}

// This independent policy list is part of the regression plan, not imported
// from the implementation under test. It is not a language-complete stop list.
func practicalAnchorAuxiliaryTerms() []string {
	return []string{"影響", "影响", "造成", "導致", "导致", "發生", "发生", "是否", "哪些", "什麼", "什么", "多少", "如何"}
}

type practicalAnchorLabPlan struct {
	practicalLabPlan
	PreviousPracticalResult hanOutcomeFile `json:"previous_practical_result"`
	HanTermPolicyVersion    string         `json:"han_term_policy_version"`
	AuxiliaryTerms          []string       `json:"auxiliary_terms"`
	ExpectedExtraPairs      int            `json:"expected_extra_pairs"`
}

func practicalAnchorDecodePlan(data []byte) (practicalAnchorLabPlan, error) {
	var p practicalAnchorLabPlan
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, fmt.Errorf("anchor plan must be one known JSON object")
	}
	if p.Contract != "practical-han-anchor-read-only-plan-v1" || p.HanTermPolicyVersion != practicalAnchorPolicyVersion ||
		!slices.Equal(p.AuxiliaryTerms, practicalAnchorAuxiliaryTerms()) || p.ExpectedExtraPairs != 8 ||
		p.PreviousPracticalResult.SHA256 != practicalPreviousResultSHA || !filepath.IsAbs(p.PreviousPracticalResult.Path) {
		return p, fmt.Errorf("anchor plan changed its fixed policy or previous practical result")
	}
	previous := p.practicalLabPlan
	previous.Contract = "practical-multisurface-read-only-plan-v1"
	baseline, err := json.Marshal(previous)
	if err != nil {
		return p, err
	}
	if _, err := practicalDecodePlan(baseline); err != nil {
		return p, err
	}
	return p, nil
}

func practicalAnchorReadPlan(t *testing.T, researchPlanData []byte) (*practicalAnchorLabPlan, map[string]evidencequerymcp.GroundedEvidenceBriefResponse, map[string]string) {
	t.Helper()
	path := os.Getenv("AHE_PRACTICAL_ANCHOR_LAB_PLAN")
	if !filepath.IsAbs(path) {
		t.Fatal("anchor plan path must be absolute")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read frozen anchor plan")
	}
	p, err := practicalAnchorDecodePlan(data)
	if err != nil {
		t.Fatal(err)
	}
	if p.ResearchPlan.Path != os.Getenv("AHE_MULTISURFACE_LAB_PLAN") || hanOutcomeHash(researchPlanData) != practicalResearchPlanSHA {
		t.Fatal("anchor plan does not use the original research questions")
	}
	hashes := map[string]string{path: hanOutcomeHash(data), p.ResearchPlan.Path: practicalResearchPlanSHA,
		p.ResearchResult.Path: practicalResearchResultSHA, p.PreviousPracticalResult.Path: practicalPreviousResultSHA}
	practicalCheckFiles(t, hashes)
	previous, err := os.ReadFile(p.PreviousPracticalResult.Path)
	if err != nil {
		t.Fatal("cannot read frozen previous practical result")
	}
	var saved multisurfaceLabReport
	d := json.NewDecoder(bytes.NewReader(previous))
	d.DisallowUnknownFields()
	if d.Decode(&saved) != nil || !saved.Passed || !saved.Unchanged || saved.Completed != 96 || len(saved.RawResponses) != 48 || saved.PlanSHA256 != practicalResearchPlanSHA {
		t.Fatal("previous practical result is not the completed frozen matrix")
	}
	responses := map[string]evidencequerymcp.GroundedEvidenceBriefResponse{}
	for key, raw := range saved.RawResponses {
		var response evidencequerymcp.GroundedEvidenceBriefResponse
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&response) != nil {
			t.Fatal("previous practical raw response shape changed")
		}
		responses[key] = response
	}
	return &p, responses, hashes
}

func practicalAnchorCheckObservation(t *testing.T, o hanOutcomeObservation, tc hanOutcomeCase, frozen map[string]evidencequerymcp.GroundedEvidenceBriefResponse) {
	t.Helper()
	if o.Mode != "practical" {
		practicalCheckObservation(t, o, tc, frozen)
		return
	}
	previous, ok := frozen[o.CaseID+"/practical"]
	if !ok {
		t.Fatal("anchor response lacks its frozen practical control")
	}
	if o.Response.QueryExecution.PlanVersion != "practical-multisurface-lexical-v2" || len(o.MissingRelevantIDs) != 0 {
		t.Fatal("anchor plan version or pre-labeled recall regression")
	}
	if previous.QueryExecution.Multisurface == nil || !slices.Equal(o.Response.QueryExecution.Multisurface.NormalizedQueryTerms, previous.QueryExecution.Multisurface.NormalizedQueryTerms) {
		t.Fatal("anchor eligibility changed the original normalized query terms")
	}
	if tc.ID == "Q12" {
		if len(o.Response.Matches) != 0 || len(previous.Matches) != 1 || practicalFallbackCount(t, o.Response) != 0 {
			t.Fatal("Q12 must remove the old unrelated match without English fallback")
		}
	} else if !reflect.DeepEqual(o.Response.Matches, previous.Matches) || !reflect.DeepEqual(o.Response.SourceScopes, previous.SourceScopes) ||
		practicalFallbackCount(t, o.Response) != practicalFallbackCount(t, previous) {
		t.Fatal("anchor filtering changed another question's exact records, order, source scopes or fallback")
	}
	practicalAnchorCheckPolicy(t, o.Response)
}

func practicalAnchorCheckPolicy(t *testing.T, r evidencequerymcp.GroundedEvidenceBriefResponse) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Execution struct {
			Practical struct {
				Policy *struct {
					Version   string    `json:"version"`
					Auxiliary *[]string `json:"auxiliary_terms"`
					Anchors   *[]string `json:"eligible_anchor_terms"`
					Preserved *bool     `json:"auxiliary_only_query_preserved"`
				} `json:"han_term_policy"`
			} `json:"practical_recovery"`
		} `json:"query_execution"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		t.Fatal("cannot inspect anchor public metadata")
	}
	p := envelope.Execution.Practical.Policy
	if p == nil || p.Version != practicalAnchorPolicyVersion || p.Auxiliary == nil || p.Anchors == nil || p.Preserved == nil {
		t.Fatal("anchor public policy metadata missing or malformed")
	}
	auxiliary, anchors, han := []string{}, []string{}, []string{}
	english := 0
	for _, term := range r.QueryExecution.Multisurface.NormalizedQueryTerms {
		isHan := true
		for _, character := range term {
			isHan = isHan && unicode.Is(unicode.Han, character)
		}
		if !isHan {
			english++
			continue
		}
		han = append(han, term)
		if slices.Contains(practicalAnchorAuxiliaryTerms(), term) {
			auxiliary = append(auxiliary, term)
		} else {
			anchors = append(anchors, term)
		}
	}
	preserved := len(han) > 0 && len(anchors) == 0 && english == 0
	if preserved {
		anchors = han
	}
	if !slices.Equal(*p.Auxiliary, auxiliary) || !slices.Equal(*p.Anchors, anchors) || *p.Preserved != preserved {
		t.Fatal("anchor metadata differs from independently classified original query terms")
	}
}

func TestIntegrationPracticalAnchorReadOnlyLab(t *testing.T) {
	if os.Getenv("AHE_PRACTICAL_ANCHOR_LAB_PLAN") == "" {
		t.Skip("set the frozen anchor plan and dedicated multisurface DB settings")
	}
	runMultisurfaceReadOnlyLab(t, true, true)
}

func TestPracticalAnchorPlanContract(t *testing.T) {
	p := practicalAnchorLabPlan{
		practicalLabPlan: practicalLabPlan{Contract: "practical-han-anchor-read-only-plan-v1",
			ResearchPlan: hanOutcomeFile{Path: "/frozen/plan.json", SHA256: practicalResearchPlanSHA}, ResearchResult: hanOutcomeFile{Path: "/frozen/research.json", SHA256: practicalResearchResultSHA},
			Mode: multisurfaceLabModePlan{"practical", practicalLabMode, practicalLabSchema}, PlannedBriefCalls: 96, Replays: 2, ExpectedRelevantPairs: 18, ExpectedNonemptyQueries: 11},
		PreviousPracticalResult: hanOutcomeFile{Path: "/frozen/practical.json", SHA256: practicalPreviousResultSHA}, HanTermPolicyVersion: practicalAnchorPolicyVersion, AuxiliaryTerms: practicalAnchorAuxiliaryTerms(), ExpectedExtraPairs: 8,
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := practicalAnchorDecodePlan(data); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"auxiliary", "old-result", "labels", "extra", "mode", "policy"} {
		changed := p
		switch change {
		case "auxiliary":
			changed.AuxiliaryTerms = []string{"影響"}
		case "old-result":
			changed.PreviousPracticalResult.SHA256 = "changed"
		case "labels":
			changed.ExpectedRelevantPairs = 17
		case "extra":
			changed.ExpectedExtraPairs = 9
		case "mode":
			changed.Mode.QueryMode = multisurfaceLabMode
		case "policy":
			changed.HanTermPolicyVersion = "changed"
		}
		data, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := practicalAnchorDecodePlan(data); err == nil {
			t.Fatalf("changed anchor plan accepted: %s", change)
		}
	}
}

func TestPracticalAnchorFrozenPlan(t *testing.T) {
	if os.Getenv("AHE_PRACTICAL_ANCHOR_PLAN_CHECK") == "" {
		t.Skip("optional offline anchor plan check; no DB")
	}
	t.Setenv("AHE_PRACTICAL_ANCHOR_LAB_PLAN", os.Getenv("AHE_PRACTICAL_ANCHOR_PLAN_CHECK"))
	data, err := os.ReadFile(os.Getenv("AHE_MULTISURFACE_LAB_PLAN"))
	if err != nil {
		t.Fatal("cannot read original research plan")
	}
	p, responses, hashes := practicalAnchorReadPlan(t, data)
	practicalCheckFiles(t, hashes)
	if p.ExpectedExtraPairs != 8 || len(responses) != 48 {
		t.Fatal("anchor frozen matrix mismatch")
	}
}
