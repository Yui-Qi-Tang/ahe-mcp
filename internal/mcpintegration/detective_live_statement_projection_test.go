//go:build integration

package mcpintegration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDetectiveLiveStatementProjection(t *testing.T) {
	const raw = "來源記載此能力已實作，但未證明部署。"
	record := detectiveLiveRecord{
		Statement: raw, RecordType: "capability_state", Subject: "Ordinary admission integrity",
		EpistemicClass: "claim", Status: "implemented", Scope: "runtime_core", SelectionState: "selected",
		BlockedBy: []string{"human review"}, DoesNotEstablish: []string{"deployment", "independent truth"},
		Qualifiers: []string{"selected scope < whole system", "繁體中文"},
	}
	for _, version := range []string{"0.1.0", "0.1.1"} {
		t.Run("legacy_"+version, func(t *testing.T) {
			legacy := record
			legacy.Statement = legacy.Subject
			got, err := detectiveLiveStatement(version, legacy)
			if err != nil || got != legacy.Statement {
				t.Fatalf("historical title-only statement changed: got=%q err=%v", got, err)
			}
			legacy.Statement = "來源 < & >\n原始第二行"
			got, err = detectiveLiveStatement(version, legacy)
			if err != nil || got != legacy.Statement {
				t.Fatalf("historical raw bytes changed: got=%q err=%v", got, err)
			}
		})
	}
	t.Run("current_exact_context", func(t *testing.T) {
		before, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		got, err := detectiveLiveStatement("0.1.2", record)
		want := raw + "\n\nDetective extraction context (model-classified candidate, not independent verification):\n" +
			`{"record_type":"capability_state","subject":"Ordinary admission integrity","epistemic_class":"claim","status":"implemented","scope":"runtime_core","selection_state":"selected","blocked_by":["human review"],"does_not_establish":["deployment","independent truth"],"qualifiers":["selected scope \u003c whole system","繁體中文"]}`
		if err != nil || got != want {
			t.Fatalf("current native projection differs:\ngot  %q\nwant %q\nerr  %v", got, want, err)
		}
		after, err := json.Marshal(record)
		if err != nil || string(after) != string(before) {
			t.Fatal("projection changed the model record")
		}
	})
	t.Run("all_context_fields_retained", func(t *testing.T) {
		record := detectiveLiveRecord{Statement: raw, Subject: "capability", BlockedBy: []string{}, Qualifiers: []string{}}
		got, err := detectiveLiveStatement("0.1.2", record)
		want := raw + "\n\nDetective extraction context (model-classified candidate, not independent verification):\n" +
			`{"record_type":"","subject":"capability","epistemic_class":"","status":"","scope":"","selection_state":"","blocked_by":[],"does_not_establish":null,"qualifiers":[]}`
		if err != nil || got != want {
			t.Fatalf("projection omitted or normalized a typed field: got=%q err=%v", got, err)
		}
	})
	for _, version := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		t.Run("byte_limit_"+version, func(t *testing.T) {
			bounded := record
			projected, err := detectiveLiveStatement(version, bounded)
			if err != nil {
				t.Fatal(err)
			}
			rawBytes := 2000 - (len(projected) - len(bounded.Statement))
			bounded.Statement = strings.Repeat("界", rawBytes/3) + strings.Repeat("x", rawBytes%3)
			projected, err = detectiveLiveStatement(version, bounded)
			if err != nil || len(projected) != 2000 {
				t.Fatalf("exact 2000-byte projection rejected: bytes=%d err=%v", len(projected), err)
			}
			bounded.Statement += "x"
			if got, err := detectiveLiveStatement(version, bounded); err == nil || got != "" {
				t.Fatal("2001-byte projection was accepted or truncated")
			}
		})
	}
	for _, statement := range []string{"", " \t", " leading", "trailing\n", "ORDINARY ADMISSION INTEGRITY"} {
		t.Run("current_form_"+statement, func(t *testing.T) {
			invalid := record
			invalid.Statement = statement
			if got, err := detectiveLiveStatement("0.1.2", invalid); err == nil || got != "" {
				t.Fatal("invalid current statement form was accepted or rewritten")
			}
		})
	}
	for _, version := range []string{"", "0.1.3", "0.1.2 ", "0.2.0"} {
		t.Run("unknown_"+version, func(t *testing.T) {
			if got, err := detectiveLiveStatement(version, record); err == nil || got != "" {
				t.Fatal("unknown extractor projection version was accepted")
			}
		})
	}
	for _, version := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		for _, statement := range []string{"invalid\x00statement", "invalid\xffstatement"} {
			t.Run("invalid_raw_"+version+"_"+statement, func(t *testing.T) {
				invalid := record
				invalid.Statement = statement
				if got, err := detectiveLiveStatement(version, invalid); err == nil || got != "" {
					t.Fatal("invalid raw text was accepted or replaced")
				}
			})
		}
	}
	t.Run("invalid_context_utf8_not_replaced", func(t *testing.T) {
		for _, invalid := range []detectiveLiveRecord{
			{Statement: raw, Subject: "invalid\xffsubject"},
			{Statement: raw, Subject: "capability", Qualifiers: []string{"invalid\xffqualifier"}},
		} {
			before := invalid
			before.Qualifiers = append([]string(nil), invalid.Qualifiers...)
			if got, err := detectiveLiveStatement("0.1.2", invalid); err == nil || got != "" {
				t.Fatal("JSON replacement changed invalid context bytes into a successful projection")
			}
			if !reflect.DeepEqual(invalid, before) {
				t.Fatal("JSON roundtrip modified the input context")
			}
		}
	})
}
