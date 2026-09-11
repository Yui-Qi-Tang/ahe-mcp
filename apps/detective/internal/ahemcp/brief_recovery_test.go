package ahemcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This exercises real pipes and child exits around a fixed synthetic response.
// It does not simulate database idempotency or Desktop persistence/confirmation.
func TestBriefWriterFaultThenExactReplay(t *testing.T) {
	for _, decision := range []string{"admit", "reject", "audit_only"} {
		for _, fault := range []string{"drop-write-once", "exit-once"} {
			t.Run(decision+"/"+fault, func(t *testing.T) {
				input, review, handoff, proposal := briefReviewFixture(t)
				admission := fixtureAdmission(handoff)
				admission.Replayed = true
				disposition := fixtureDisposition(handoff, decision)
				reason := disposition.DecisionReason
				tool, wireDecision := "record_reviewed_source_claim_disposition", decision
				var writer, trace string
				if decision == "admit" {
					tool, wireDecision = "admit_reviewed_source_claim", "approved"
					writer, trace = reviewLauncher(t, fault, review, admission, nil, nil)
					proposal.AdmissionOutcome = "admitted"
					proposal.CanonicalRef, _ = json.Marshal(admission.CanonicalRef)
				} else {
					writer, trace = dispositionLauncher(t, fault, review, disposition)
					proposal.AdmissionOutcome = disposition.AdmissionOutcome
				}
				original, err := os.ReadFile(writer)
				if err != nil {
					t.Fatal(err)
				}
				fixturePath := filepath.Join(filepath.Dir(trace), "review.json")
				fixtureBefore, err := os.ReadFile(fixturePath)
				if err != nil {
					t.Fatal(err)
				}
				apply := func(wantError bool) {
					t.Helper()
					if decision == "admit" {
						got, err := AdmitSourceClaim(t.Context(), writer, review, reason)
						if (err != nil) != wantError || (wantError && !reflect.DeepEqual(got, ReviewedAdmission{})) || (!wantError && !reflect.DeepEqual(got, admission)) {
							t.Fatalf("admission recovery mismatch, wantError=%v: %v", wantError, err)
						}
					} else {
						got, err := DisposeSourceClaim(t.Context(), writer, review, decision, reason)
						if (err != nil) != wantError || (wantError && got != (ReviewedDisposition{})) || (!wantError && got != disposition) {
							t.Fatalf("disposition recovery mismatch, wantError=%v: %v", wantError, err)
						}
					}
				}
				apply(true)
				phase := "response-written"
				if fault == "drop-write-once" {
					phase = "request-received"
				}
				assertReviewFaultPhase(t, trace, phase)
				first := reviewToolArguments(t, trace, tool, 1)[0]
				var sent struct {
					ExtractionAttemptID string        `json:"extraction_attempt_id"`
					ExpectedSubject     ReviewSubject `json:"expected_subject"`
					Decision            string        `json:"decision"`
					DecisionReason      string        `json:"decision_reason"`
				}
				if json.Unmarshal(first, &sent) != nil || sent.ExtractionAttemptID != handoff.ExtractionAttemptID || !reflect.DeepEqual(sent.ExpectedSubject, review.Subject) || sent.Decision != wireDecision || sent.DecisionReason != reason {
					t.Fatal("faulted writer did not receive the original exact decision")
				}
				canonical := canonicalReviewFixture(t, proposal, admission)
				query, _ := reviewLauncher(t, "query", review, admission, &proposal, &canonical)
				observed, err := ObserveBrief(t.Context(), query, input, handoff)
				if err != nil || observed.AdmissionOutcome != proposal.AdmissionOutcome {
					t.Fatalf("terminal observation after writer fault: %v", err)
				}
				// Query must not start another writer or reload a pending-only review.
				reviewToolArguments(t, trace, tool, 1)
				apply(false) // A second, explicit invocation with unchanged material.
				calls := reviewToolArguments(t, trace, tool, 2)
				if !bytes.Equal(first, calls[1]) {
					t.Fatal("retry altered the exact request")
				}
				if decision == "admit" {
					err = VerifyBriefAdmitted(t.Context(), query, input, handoff, admission)
				} else {
					err = VerifyBriefDisposed(t.Context(), query, input, handoff, disposition)
				}
				if err != nil {
					t.Fatalf("independent final readback: %v", err)
				}
				reviewToolArguments(t, trace, tool, 2)
				for path, before := range map[string][]byte{writer: original, fixturePath: fixtureBefore} {
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("recovery changed launcher or response fixture")
					}
				}
			})
		}
	}
}

func TestBriefQueryExitFailureKeepsAcknowledgementForReadOnlyRetry(t *testing.T) {
	for _, decision := range []string{"admit", "reject", "audit_only"} {
		t.Run(decision, func(t *testing.T) {
			input, review, handoff, proposal := briefReviewFixture(t)
			admission := fixtureAdmission(handoff)
			disposition := fixtureDisposition(handoff, decision)
			if decision == "admit" {
				proposal.AdmissionOutcome = "admitted"
				proposal.CanonicalRef, _ = json.Marshal(admission.CanonicalRef)
			} else {
				proposal.AdmissionOutcome = disposition.AdmissionOutcome
			}
			canonical := canonicalReviewFixture(t, proposal, admission)
			query, trace := reviewLauncher(t, "query-exit-once", review, admission, &proposal, &canonical)
			before, _ := json.Marshal([]any{input, review, handoff, admission, disposition})
			verify := func() error {
				if decision == "admit" {
					return VerifyBriefAdmitted(t.Context(), query, input, handoff, admission)
				}
				return VerifyBriefDisposed(t.Context(), query, input, handoff, disposition)
			}
			if verify() == nil {
				t.Fatal("nonzero Query exit reported verified success")
			}
			assertReviewFaultPhase(t, trace, "response-written")
			reads := 1
			if decision == "admit" {
				reads = 2 // Occurrence plus canonical source claim.
			}
			reviewToolArguments(t, trace, "get_evidence_record", reads)
			if err := verify(); err != nil {
				t.Fatalf("explicit read-only retry: %v", err)
			}
			reviewToolArguments(t, trace, "get_evidence_record", reads*2)
			after, _ := json.Marshal([]any{input, review, handoff, admission, disposition})
			if !bytes.Equal(before, after) {
				t.Fatal("Query recovery changed saved inputs or acknowledgements")
			}
		})
	}
}

func assertReviewFaultPhase(t *testing.T, trace, want string) {
	t.Helper()
	phase, err := os.ReadFile(filepath.Join(filepath.Dir(trace), "fault-fired"))
	if err != nil || string(phase) != want {
		t.Fatalf("fault phase=%q, want %q: %v", phase, want, err)
	}
}

func reviewToolArguments(t *testing.T, path, tool string, count int) []json.RawMessage {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var request struct {
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &request) != nil {
			t.Fatal("invalid recovery trace")
		}
		if request.Method == "tools/call" {
			if request.Params.Name != tool {
				t.Fatalf("unexpected recovery tool: %s", request.Params.Name)
			}
			calls = append(calls, request.Params.Arguments)
		}
	}
	if len(calls) != count {
		t.Fatalf("recovery calls=%d, want %d", len(calls), count)
	}
	return calls
}
