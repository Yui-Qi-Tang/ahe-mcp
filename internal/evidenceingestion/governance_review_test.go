package evidenceingestion

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

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
