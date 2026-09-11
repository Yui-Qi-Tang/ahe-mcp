package ahemcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBatchCandidateSubmissionPreservesSingleAttemptAndTerminalRetry(t *testing.T) {
	for _, state := range []string{"pending", "admitted", "rejected", "audit_only"} {
		t.Run(state, func(t *testing.T) {
			doc, records := handoffDocument(t, "exact synthetic evidence\n")
			launcher, _ := handoffLauncher(t, "batch_"+state)
			locator, err := SubmitBatchCandidate(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records)
			if err != nil || locator.SubmissionOutcome != state || !reviewID(locator.ProposalOccurrenceID, "occ:") {
				t.Fatalf("state=%s locator=%+v err=%v", state, locator, err)
			}
		})
	}
	for _, mode := range []string{"batch_multi", "batch_fresh_terminal"} {
		t.Run(mode, func(t *testing.T) {
			doc, records := handoffDocument(t, "exact synthetic evidence\n")
			launcher, _ := handoffLauncher(t, mode)
			locator, err := SubmitBatchCandidate(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records)
			if err == nil || locator != (CandidateLocator{}) {
				t.Fatal("unsupported acknowledgement accepted")
			}
		})
	}
	// The old single-candidate public pending contract is deliberately unchanged.
	doc, records := handoffDocument(t, "exact synthetic evidence\n")
	launcher, _ := handoffLauncher(t, "batch_admitted")
	if h, err := Submit(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records); err == nil || h != (Handoff{}) {
		t.Fatal("legacy Submit accepted terminal result")
	}
}

func TestBatchObservationNeverConfusesTerminalWithPending(t *testing.T) {
	doc, records := handoffDocument(t, "exact synthetic evidence\n")
	h := queryHandoff()
	h.SourceSnapshotID = fixtureID("srcsnap:")
	h.ExtractionViewID = fixtureID("view:")
	h.ExtractionAttemptID = fixtureID("attempt:")
	h.ProposalOccurrenceID = fixtureID("occ:")
	locator := CandidateLocator{h.SourceSnapshotID, h.ExtractionViewID, h.ExtractionAttemptID, h.ProposalOccurrenceID, "pending", false}
	for _, state := range []string{"pending", "admitted", "rejected", "audit_only", "unknown"} {
		t.Run(state, func(t *testing.T) {
			result := queryFixture(doc, records[0], h)
			result.AdmissionOutcome = state
			canonical := ""
			if state == "admitted" {
				canonical = "canon-node:" + strings.Repeat("a", 16)
				result.CanonicalRef, _ = json.Marshal(canonical)
			}
			observed, err := verifyBatchCandidateRecord(result, "lab-status", doc, handoffExtractor(), records[0], locator)
			if state == "unknown" {
				if err == nil || observed != (CandidateObservation{}) {
					t.Fatal("unknown lifecycle accepted")
				}
				return
			}
			if err != nil || observed.AdmissionOutcome != state || observed.CanonicalRef != canonical {
				t.Fatal("lifecycle projection drifted", err)
			}
			result.StatementText = "changed"
			if _, err := verifyBatchCandidateRecord(result, "lab-status", doc, handoffExtractor(), records[0], locator); err == nil {
				t.Fatal("changed terminal source accepted")
			}
		})
	}
	result := queryFixture(doc, records[0], h)
	locator.SubmissionOutcome, locator.Replayed = "rejected", true
	if _, err := verifyBatchCandidateRecord(result, "lab-status", doc, handoffExtractor(), records[0], locator); err == nil {
		t.Fatal("terminal observation changed to pending")
	}
}

func TestBatchLocatorRejectsIncompleteAuthority(t *testing.T) {
	valid := CandidateLocator{fixtureID("srcsnap:"), fixtureID("view:"), fixtureID("attempt:"), fixtureID("occ:"), "pending", false}
	if err := ValidateCandidateLocator(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*CandidateLocator){func(l *CandidateLocator) { l.ProposalOccurrenceID = "" }, func(l *CandidateLocator) { l.ExtractionAttemptID = "attempt:first-only" }, func(l *CandidateLocator) { l.SubmissionOutcome = "admitted" }, func(l *CandidateLocator) { l.SubmissionOutcome = "approved" }} {
		copy := valid
		change(&copy)
		if ValidateCandidateLocator(copy) == nil {
			t.Fatal("invalid locator accepted")
		}
	}
}

func batchHandoffResponse(value any, mode, stage string) any {
	data, _ := json.Marshal(value)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	for key, prefix := range map[string]string{"source_snapshot_id": "srcsnap:", "extraction_view_id": "view:", "extraction_attempt_id": "attempt:", "proposal_occurrence_id": "occ:"} {
		if _, ok := fields[key]; ok {
			fields[key] = fixtureID(prefix)
		}
	}
	if stage == "submit_extractor_output" {
		state := strings.TrimPrefix(mode, "batch_")
		fields["replayed"] = true
		if state == "multi" {
			fields["proposal_count"] = 2
			state = "pending"
		}
		if state == "fresh_terminal" {
			fields["replayed"] = false
			state = "admitted"
		}
		fields["status"] = state
	}
	return fields
}

func TestBatchCandidateCannotAcceptSeveralRecords(t *testing.T) {
	doc, records := handoffDocument(t, "exact synthetic evidence\n")
	records = append(records, records[0])
	got, err := SubmitBatchCandidate(t.Context(), "/unavailable", "lab-status", doc, handoffExtractor(), records)
	if err == nil || !reflect.DeepEqual(got, CandidateLocator{}) {
		t.Fatal("multi-proposal request reached single-attempt adapter")
	}
}
