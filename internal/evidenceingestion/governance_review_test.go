package evidenceingestion

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

func TestGovernanceHistoricalSourceAssessmentReceipt(t *testing.T) {
	// Native TEST APPROVAL STUB receipt captured at 6baecc6979e6 in a disposable
	// database before the external-revision wording changed. Never regenerate it
	// from the current display template: historical display bytes are authority.
	raw, err := os.ReadFile("testdata/governance_source_assessment_v1_receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Receipt GovernanceReceipt
		Body    string
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	want := fixture.Receipt
	row := mockRow{values: []any{want.EventID, want.RequestID, want.TargetID, want.Revision, want.PreviousEventID, want.RecordedAt, fixture.Body}}
	got, err := scanGovernanceReceipt(row)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("historical native receipt changed", err)
	}
	if !strings.Contains(strings.Join(got.Display.Limitations, " "), "Missing historical provider revision") {
		t.Fatal("original review wording was replaced")
	}
	row.values[6] = strings.Replace(fixture.Body, "historical provider revision", "declared external revision", 1)
	if _, err := scanGovernanceReceipt(row); err == nil {
		t.Fatal("rewritten historical display accepted")
	}
}

func TestGovernanceReviewReservesCompleteEventEnvelope(t *testing.T) {
	// A retained source is JSON base64, so legitimate material alone can take a
	// display to within a few bytes of the old 1 MiB bound.
	display := func(n int) []byte {
		raw, err := json.Marshal(GovernanceDisplay{Contract: GovernanceReviewV1,
			StoreID: "store:synthetic", Materials: []GovernanceSource{{RetainedBytes: bytes.Repeat([]byte("a"), n)}}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	lo, hi := 0, governanceEventMaxBytes
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(display(mid)) <= governanceEventMaxBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	oversized := display(lo)
	body, _ := json.Marshal(governanceEventBody{Contract: GovernanceEventV1, Display: string(oversized), Reason: "synthetic", Decision: "approved"})
	if len(body) <= governanceEventMaxBytes || validateGovernanceDisplaySize(oversized) == nil {
		t.Fatal("review offered a display whose event cannot be stored")
	}
	lo, hi = 0, lo
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if validateGovernanceDisplaySize(display(mid)) == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	raw := display(lo)
	for _, reason := range []string{strings.Repeat("x", 2000), strings.Repeat("\x01", 2000), strings.Repeat("<", 2000)} {
		body, err := json.Marshal(governanceEventBody{Contract: GovernanceEventV1, Display: string(raw), Decision: "approved",
			RequestID: strings.Repeat("\x01", 200), ReviewerID: strings.Repeat("\x01", 200), Reason: reason,
			Subject: governanceDigest("governance-review:sha256:", raw), BatchSubject: strings.Repeat("\x01", 100)})
		if err != nil || len(body) > governanceEventMaxBytes {
			t.Fatal("approved boundary exceeds event limit", len(body), err)
		}
	}
}

func TestInterimHumanExclusionHasNoInventedCheckerRequirement(t *testing.T) {
	r := InterimGovernanceRecord{Kind: "exclusion", Purpose: "current-action", Scope: "claim", Reason: "synthetic human stop-use",
		Outcome: "exclude", HistoricalReviewer: "TEST APPROVAL STUB", HistoricalReviewedAt: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
		OriginalMaterials: []evidenceuse.Material{{Identity: "srcsnap:synthetic", Revision: "r1", ContentHash: "sha256:synthetic"}}}
	if err := validateInterimGovernanceHistory(r); err != nil {
		t.Fatal(err)
	}
	r.CheckerName = "synthetic checker"
	if err := validateInterimGovernanceHistory(r); err == nil {
		t.Fatal("partial checker contract accepted")
	}
	r.CheckerName = ""
	r.Kind = "verification"
	if err := validateInterimGovernanceHistory(r); err == nil {
		t.Fatal("verification report accepted without actual checks")
	}
}
