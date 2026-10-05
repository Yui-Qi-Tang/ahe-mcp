package evidenceingestion

import (
	"encoding/json"
	"strings"
	"testing"
)

func externalCheckFixture(subject ExternalCheckSubject, request string) ExternalCheckInput {
	return ExternalCheckInput{Contract: ExternalCheckContract, RequestID: request, Subject: subject,
		CheckerName: "bounded-comparator", CheckerVersion: "1", CheckerConfiguration: `{"axes":["condition"]}`, RunRef: "fixture-run", RecordedBy: "external-test",
		Materials: []ExternalCheckMaterial{
			{ID: "source-ir", Role: "source", Format: "condition-tree", Version: "1", Content: `{"condition":["and","approved",{"unknown":"appendix-a"}]}`, MappingClaim: "author supplied; fidelity unverified"},
			{ID: "candidate-ir", Role: "candidate", Format: "condition-tree", Version: "1", Content: `{"condition":["and","approved",{"unknown":"appendix-a"}]}`, MappingClaim: "author supplied; fidelity unverified"}},
		Findings: []ExternalCheckFinding{
			{Dimension: "condition", Criterion: "three-valued output equivalence", InputIDs: []string{"source-ir", "candidate-ir"}, Result: "pass", Detail: "equal outputs under the supplied representation"},
			{Dimension: "amount", Criterion: "numeric and unit equivalence", Result: "not_checked", Detail: "outside checker scope"},
			{Dimension: "source_fidelity", Criterion: "natural language mapping", Result: "unsupported", Detail: "not established by this checker"}},
		Limitations: []string{"neither natural-language fidelity nor operational authorization is established"}}
}

func TestExternalCheckRejectsAmbiguousOrIncompleteReports(t *testing.T) {
	subject := ExternalCheckSubject{"occ:test", "snap:test", "view:test", "1", "hash1", "hash2", "fp"}
	tests := map[string]func(*ExternalCheckInput){
		"duplicate input identity":      func(v *ExternalCheckInput) { v.Materials[1].ID = v.Materials[0].ID },
		"unresolved finding input":      func(v *ExternalCheckInput) { v.Findings[0].InputIDs = []string{"absent"} },
		"duplicate finding input":       func(v *ExternalCheckInput) { v.Findings[0].InputIDs = []string{"source-ir", "source-ir"} },
		"checked without inputs":        func(v *ExternalCheckInput) { v.Findings[0].InputIDs = nil },
		"vague checked result":          func(v *ExternalCheckInput) { v.Findings[0].Result = "checked" },
		"duplicate dimension":           func(v *ExternalCheckInput) { v.Findings[1].Dimension = v.Findings[0].Dimension },
		"missing limitations":           func(v *ExternalCheckInput) { v.Limitations = nil },
		"missing mapping assertion":     func(v *ExternalCheckInput) { v.Materials[0].MappingClaim = "" },
		"missing checker configuration": func(v *ExternalCheckInput) { v.CheckerConfiguration = "" },
		"oversized material":            func(v *ExternalCheckInput) { v.Materials[0].Content = strings.Repeat("x", 262145) },
		"invalid UTF8":                  func(v *ExternalCheckInput) { v.Materials[0].Content = string([]byte{255}) },
		"revision without reason":       func(v *ExternalCheckInput) { v.RevisesID = "check:previous" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := externalCheckFixture(subject, name)
			if err := in.validate(); err != nil {
				t.Fatal(err)
			}
			mutate(&in)
			if err := in.validate(); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}

func TestExternalCheckIdentityBindsActualInputsAndVersions(t *testing.T) {
	subject := ExternalCheckSubject{"occ:test", "snap:test", "view:test", "1", "hash1", "hash2", "fp"}
	original := externalCheckFixture(subject, "request")
	digest := func(in ExternalCheckInput) string {
		b, e := json.Marshal(in)
		if e != nil {
			t.Fatal(e)
		}
		return externalDigest(b)
	}
	for name, change := range map[string]func(*ExternalCheckInput){
		"actual representation":           func(v *ExternalCheckInput) { v.Materials[0].Content = `{"condition":true}` },
		"mapping claim":                   func(v *ExternalCheckInput) { v.Materials[0].MappingClaim = "different mapping" },
		"checker version":                 func(v *ExternalCheckInput) { v.CheckerVersion = "2" },
		"format version":                  func(v *ExternalCheckInput) { v.Materials[0].Version = "2" },
		"same result different criterion": func(v *ExternalCheckInput) { v.Findings[0].Criterion = "other criterion" },
		"different request":               func(v *ExternalCheckInput) { v.RequestID = "second-request" },
	} {
		t.Run(name, func(t *testing.T) {
			v := externalCheckFixture(subject, "request")
			change(&v)
			if digest(v) == digest(original) {
				t.Fatal("identity did not change")
			}
		})
	}
}
