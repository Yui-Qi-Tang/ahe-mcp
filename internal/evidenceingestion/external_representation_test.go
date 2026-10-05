package evidenceingestion

import (
	"encoding/json"
	"strings"
	"testing"
)

func externalRepresentationFixture(s ExternalCheckSubject, request, missing string) ExternalRepresentationInput {
	key := ExternalDependencyKey{Namespace: "release-docs", LocalID: missing, ScopeRef: "release-2026", Revision: "1"}
	return ExternalRepresentationInput{Contract: ExternalRepresentationContract, RequestID: request, Subject: s,
		Name: "發布條件", Version: "1", Role: "source", Format: "external-condition-json", FormatVersion: "1", RecordedBy: "TEST APPROVAL STUB recorder", Producer: "external-author", MappingClaim: "外部表示；未證實忠於原文",
		Content:      "{\n  \"condition\": [\"and\", \"approved\", {\"dependency\": \"" + key.ID() + "\"}], \"amount\":9007199254740993, \"ratio\":1.2300e+2, \"label\":\"附件\"\n}",
		Dependencies: []ExternalDependency{{Key: key, Status: "missing", Pointers: []string{"/condition/2/dependency"}, Reason: "原始資料缺少附件"}}}
}

func TestExternalDependencyIdentityDoesNotCollapseUnknowns(t *testing.T) {
	original := ExternalDependencyKey{"namespace", "附件甲", "合同甲", "1"}
	for _, changed := range []ExternalDependencyKey{{"namespace", "附件乙", "合同甲", "1"}, {"namespace", "附件甲", "合同乙", "1"}, {"namespace", "附件甲", "合同甲", "2"}, {"namespac", "e附件甲", "合同甲", "1"}} {
		if original.ID() == changed.ID() {
			t.Fatal("different missing identities collapsed")
		}
	}
	if (ExternalDependencyKey{"a", "bc", "scope", "1"}).ID() == (ExternalDependencyKey{"ab", "c", "scope", "1"}).ID() {
		t.Fatal("identity framing ambiguous")
	}
}

func TestExternalRepresentationRejectsUnresolvableDependencies(t *testing.T) {
	s := ExternalCheckSubject{"occ:test", "snap:test", "view:test", "1", "raw", "rendered", "fingerprint"}
	for name, mutate := range map[string]func(*ExternalRepresentationInput){
		"duplicate JSON keys":        func(v *ExternalRepresentationInput) { v.Content = `{"x":1,"x":2}`; v.Dependencies = nil },
		"nested duplicate JSON keys": func(v *ExternalRepresentationInput) { v.Content = `{"x":[{"y":1,"y":2}]}`; v.Dependencies = nil },
		"multiple JSON values":       func(v *ExternalRepresentationInput) { v.Content = `{} {}`; v.Dependencies = nil },
		"wrong identity":             func(v *ExternalRepresentationInput) { v.Dependencies[0].Key.LocalID = "附件乙" },
		"missing pointer":            func(v *ExternalRepresentationInput) { v.Dependencies[0].Pointers = []string{"/absent"} },
		"noncanonical array index": func(v *ExternalRepresentationInput) {
			v.Dependencies[0].Pointers = []string{"/condition/02/dependency"}
		},
		"negative array index": func(v *ExternalRepresentationInput) {
			v.Dependencies[0].Pointers = []string{"/condition/-1/dependency"}
		},
		"invalid escape":     func(v *ExternalRepresentationInput) { v.Dependencies[0].Pointers = []string{"/condition/~2"} },
		"duplicate identity": func(v *ExternalRepresentationInput) { v.Dependencies = append(v.Dependencies, v.Dependencies[0]) },
		"duplicate location": func(v *ExternalRepresentationInput) {
			v.Dependencies[0].Pointers = append(v.Dependencies[0].Pointers, v.Dependencies[0].Pointers[0])
		},
		"missing with evidence":     func(v *ExternalRepresentationInput) { v.Dependencies[0].SourceSnapshotID = "source:fake" },
		"supplied without evidence": func(v *ExternalRepresentationInput) { v.Dependencies[0].Status = "supplied" },
		"version missing":           func(v *ExternalRepresentationInput) { v.Version = "" },
		"missing previous":          func(v *ExternalRepresentationInput) { v.RevisionReason = "changed" },
		"missing reason":            func(v *ExternalRepresentationInput) { v.PreviousID = "representation:old" },
		"derivation without node":   func(v *ExternalRepresentationInput) { v.DerivationID = "derivation:other" },
		"oversized":                 func(v *ExternalRepresentationInput) { v.Content = strings.Repeat(" ", 262145) + `{}` },
		"deep JSON": func(v *ExternalRepresentationInput) {
			v.Content = strings.Repeat("[", 66) + "1" + strings.Repeat("]", 66)
			v.Dependencies = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := externalRepresentationFixture(s, "request", "附件甲")
			if err := in.validate(); err != nil {
				t.Fatal(err)
			}
			mutate(&in)
			if err := in.validate(); err == nil {
				t.Fatal("invalid structure accepted")
			}
		})
	}
}

func TestExternalRepresentationMaterialIncludesDependencyEnvelope(t *testing.T) {
	s := ExternalCheckSubject{"occ:test", "snap:test", "view:test", "1", "raw", "rendered", "fingerprint"}
	encode := func(in ExternalRepresentationInput) ExternalCheckMaterial {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		r := ExternalRepresentationRecord{ID: "representation:sha256:" + externalDigest(b), Definition: in}
		m, err := r.CheckMaterial("actual-input")
		if err != nil {
			t.Fatal(err)
		}
		var decoded ExternalRepresentationInput
		if err := json.Unmarshal([]byte(m.Content), &decoded); err != nil || decoded.Content != in.Content {
			t.Fatal("JSON bytes lost", err)
		}
		return m
	}
	original := externalRepresentationFixture(s, "request", "附件甲")
	before := encode(original)
	for name, change := range map[string]func(*ExternalRepresentationInput){
		"version": func(v *ExternalRepresentationInput) { v.Version = "2" },
		"mapping": func(v *ExternalRepresentationInput) { v.MappingClaim = "更正外部對應" },
		"supplied": func(v *ExternalRepresentationInput) {
			v.Dependencies[0].Status = "supplied"
			v.Dependencies[0].SourceSnapshotID = "snap:supplied"
			v.Dependencies[0].ExtractionViewID = "view:supplied"
			v.Dependencies[0].RenderedContentHash = "hash"
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := externalRepresentationFixture(s, "request", "附件甲")
			change(&v)
			if v.Content != original.Content {
				t.Fatal("fixture changed tree")
			}
			if encode(v).Content == before.Content {
				t.Fatal("dependency envelope omitted")
			}
		})
	}
	original.Content = `{"large":"` + strings.Repeat("x", 262132) + `"}`
	original.Dependencies = nil
	if err := original.validate(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(original)
	if len(b) <= 262144 {
		t.Fatalf("fixture envelope is not oversized: %d", len(b))
	}
	if _, err := (ExternalRepresentationRecord{ID: "representation:sha256:" + externalDigest(b), Definition: original}).CheckMaterial("input"); err == nil {
		t.Fatal("oversized checker material silently returned")
	}
}
