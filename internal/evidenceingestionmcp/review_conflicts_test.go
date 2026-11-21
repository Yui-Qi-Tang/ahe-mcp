package evidenceingestionmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestSourceReviewAdvisoryUsesRemainingEnvelopeBudget(t *testing.T) {
	snapshot, display, subject := sourceReviewGolden(t)
	response := GetSourceClaimReviewResponse{ContractVersion: SourceClaimReviewMCPContract, Display: display, Subject: subject}
	response.Display.PayloadUTF8 = strings.Repeat("x", SourceClaimReviewMaxResponseBytes-3500)
	advisory := *snapshot.ConflictCandidates
	advisory.Targets = []evidenceingestion.ReviewConflictTarget{{ProposalOccurrenceID: "occ:fixture",
		Suggestions: []evidenceingestion.ConflictSuggestion{{CanonicalID: "canon:fixture", Statement: strings.Repeat("\"<>&", 3000)}},
		Coverage:    evidenceingestion.ConflictSuggestionCoverage{ScanLimit: 1000, MatchedClaims: 1, ReturnedClaims: 1}}}
	got, err := boundSourceClaimReviewConflicts(response, &advisory)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalSourceClaimReviewResponse(got)
	if err != nil || len(encoded) > SourceClaimReviewMaxResponseBytes || got.Display != response.Display || got.Subject != response.Subject || !got.ConflictCandidates.ByteBudgetTruncated {
		t.Fatal("complete envelope or exact display changed", len(encoded), err)
	}
	if got.ConflictCandidates.Targets[0].Coverage.ReturnedClaims != 0 || got.ConflictCandidates.Targets[0].Coverage.MatchedClaims != 1 {
		t.Fatal("byte omission masquerades as no candidates")
	}
	base, _ := json.Marshal(response)
	response.Display.PayloadUTF8 += strings.Repeat("x", SourceClaimReviewMaxResponseBytes-len(base))
	if _, err := boundSourceClaimReviewConflicts(response, snapshot.ConflictCandidates); err == nil {
		t.Fatal("metadata that cannot fit returned success")
	}
	if _, err := boundSourceClaimReviewConflicts(GetSourceClaimReviewResponse{}, nil); err == nil {
		t.Fatal("missing candidate reader disguised as empty")
	}
}
