package labstatus

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStatementFormRejectsCapturedSubjectOnlyFailure(t *testing.T) {
	for _, text := range []string{"Ordinary admission integrity", "ordinary admission integrity", " a sentence", "a sentence\n", "", "bad\x00text", string([]byte{0xff})} {
		t.Run(text, func(t *testing.T) {
			record := Record{Subject: "Ordinary admission integrity", Statement: text}
			before := record
			if ValidateStatementForm(record) == nil {
				t.Fatal("invalid statement form accepted")
			}
			if !reflect.DeepEqual(record, before) {
				t.Fatal("statement was repaired")
			}
		})
	}
	if err := ValidateStatementForm(Record{Subject: "capability", Statement: "The source reports that the capability was tested in the laboratory only."}); err != nil {
		t.Fatal(err)
	}
}

func TestNewExtractionRejectsSubjectOnlyWithoutRepair(t *testing.T) {
	doc := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
	candidates := validCandidates()
	candidates.Records[0].Statement = candidates.Records[0].Subject
	candidates.Records[0].Citation.ExactQuote = ""
	body, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	llm := &sequenceLLM{responses: []string{string(body)}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractor.Extract(t.Context(), doc); err == nil || !strings.Contains(err.Error(), "repeats its subject") {
		t.Fatalf("subject-only extraction error = %v", err)
	}
	if llm.calls != 1 {
		t.Fatal("extractor retried invalid output")
	}
}

func TestDedupeDoesNotDiscardDistinctStatementOrLimitations(t *testing.T) {
	for _, change := range []func(*Record){
		func(r *Record) { r.Statement += " Different assertion." },
		func(r *Record) { r.Qualifiers = []string{"synthetic only"} },
		func(r *Record) { r.DoesNotEstablish = []string{"deployment"} },
		func(r *Record) { r.BlockedBy = []string{"explicit prerequisite"} },
		func(r *Record) { r.SelectionState = "deferred" },
	} {
		candidates := validCandidates()
		other := candidates.Records[0]
		change(&other)
		candidates.Records = append(candidates.Records, other)
		got := dedupeCandidateRecords(candidates)
		if !reflect.DeepEqual(got, candidates) {
			t.Fatal("distinct model record discarded before validation")
		}
		doc := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
		if err := ValidateCandidateSet(doc, got); err == nil {
			t.Fatal("conflicting atomic records accepted")
		}
	}
}

func TestStatementPromptSpecifiesStandaloneSourceReportedProposition(t *testing.T) {
	for _, want := range []string{"self-contained proposition", "not a name, heading, or noun phrase", "necessary limitations", "not independent verification", "If a complete supported proposition cannot be formed, abstain"} {
		if !strings.Contains(agentInstruction, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	schema := OutputSchema().Properties["records"].Items.Properties["statement"]
	if !strings.Contains(schema.Description, "Self-contained proposition") {
		t.Fatal("schema omitted statement contract")
	}
}
