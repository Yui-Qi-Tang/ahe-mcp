package ahemcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixtureDisposition(h Handoff, decision string) ReviewedDisposition {
	if decision == "reject" {
		decision = "rejected"
	}
	return ReviewedDisposition{h.ProposalOccurrenceID, fixtureID("adm:"), decision, "synthetic-reviewer", "來源與候選的範圍不同。", true}
}

func dispositionLauncher(t *testing.T, mode string, r SourceClaimReview, d ReviewedDisposition) (string, string) {
	t.Helper()
	launcher, trace := reviewLauncher(t, mode, r, ReviewedAdmission{}, nil, nil)
	body, err := json.Marshal(map[string]any{"review": r, "disposition": d})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(trace), "review.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	return launcher, trace
}

func TestReviewedDispositionDirectExactReplay(t *testing.T) {
	for _, decision := range []string{"reject", "audit_only"} {
		t.Run(decision, func(t *testing.T) {
			r, _, _, h := reviewFixture(t)
			d := fixtureDisposition(h, decision)
			launcher, trace := dispositionLauncher(t, "admit", r, d)
			got, err := DisposeSourceClaim(t.Context(), launcher, r, decision, d.DecisionReason)
			if err != nil || !reflect.DeepEqual(got, d) {
				t.Fatalf("disposition replay mismatch: %v", err)
			}
			assertReviewOnlyCall(t, trace, "record_reviewed_source_claim_disposition")
			data, _ := os.ReadFile(trace)
			if strings.Contains(string(data), "decision_by") || strings.Contains(string(data), "get_source_claim_review\",\"arguments") {
				t.Fatal("request gained a caller identity or pending-only preflight")
			}
		})
	}
}

func TestReviewedDispositionValidatesBeforeLauncher(t *testing.T) {
	for _, mode := range []string{"empty", "pending", "admit", "rejected", "blank-reason", "untrimmed-reason", "nul-reason", "oversize-reason", "changed-display"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _, h := reviewFixture(t)
			d := fixtureDisposition(h, "reject")
			launcher, trace := dispositionLauncher(t, "admit", r, d)
			decision, reason := "reject", d.DecisionReason
			switch mode {
			case "empty":
				decision = ""
			case "pending", "admit", "rejected":
				decision = mode
			case "blank-reason":
				reason = ""
			case "untrimmed-reason":
				reason = " reason"
			case "nul-reason":
				reason = "reason\x00"
			case "oversize-reason":
				reason = strings.Repeat("界", 667)
			case "changed-display":
				r.Display.PayloadUTF8 += " "
			}
			got, err := DisposeSourceClaim(t.Context(), launcher, r, decision, reason)
			if err == nil || got != (ReviewedDisposition{}) {
				t.Fatal("invalid input produced a disposition")
			}
			if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid local decision started a launcher")
			}
		})
	}
}

func TestReviewedDispositionRejectsDriftAndErrors(t *testing.T) {
	for _, mode := range []string{"occurrence", "decision-id", "outcome", "reviewer", "reason", "tool-error", "unknown", "abnormal-eof"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _, h := reviewFixture(t)
			d := fixtureDisposition(h, "reject")
			reason := d.DecisionReason
			switch mode {
			case "occurrence":
				d.ProposalOccurrenceID = "other"
			case "decision-id":
				d.AdmissionDecisionID = "other"
			case "outcome":
				d.AdmissionOutcome = "admitted"
			case "reviewer":
				d.DecisionBy = ""
			case "reason":
				d.DecisionReason = "changed-private-reason"
			}
			launcher, _ := dispositionLauncher(t, mode, r, d)
			got, err := DisposeSourceClaim(t.Context(), launcher, r, "reject", reason)
			if err == nil || got != (ReviewedDisposition{}) || strings.Contains(err.Error(), "private-reason") || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("bad response result: %v", err)
			}
		})
	}
}

func TestVerifyDisposedRequiresExactTerminalNoncanonicalRecord(t *testing.T) {
	for _, outcome := range []string{"reject", "audit_only"} {
		for _, mode := range []string{"valid", "pending", "admitted", "canonical", "changed-source", "changed-statement"} {
			t.Run(outcome+"/"+mode, func(t *testing.T) {
				r, doc, records, h := reviewFixture(t)
				d := fixtureDisposition(h, outcome)
				proposal := queryFixture(doc, records[0], h)
				proposal.AdmissionOutcome = d.AdmissionOutcome
				switch mode {
				case "pending", "admitted":
					proposal.AdmissionOutcome = mode
				case "canonical":
					proposal.CanonicalRef = json.RawMessage(`"canon-node:aaaaaaaaaaaaaaaa"`)
				case "changed-source":
					proposal.Source.SourceID = "other"
				case "changed-statement":
					proposal.StatementText = "other"
				}
				launcher, _ := reviewLauncher(t, "query", r, ReviewedAdmission{}, &proposal, nil)
				err := VerifyDisposed(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records, h, d)
				if (err == nil) != (mode == "valid") {
					t.Fatalf("terminal readback %s: %v", mode, err)
				}
			})
		}
	}
}
