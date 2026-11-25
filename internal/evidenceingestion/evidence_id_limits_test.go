package evidenceingestion

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCanonicalReadAcceptsAtMost64Roots(t *testing.T) {
	for _, count := range []int{64, 65} {
		roots := make([]string, count)
		for i := range roots {
			roots[i] = fmt.Sprintf("canon-node:root-%03d", i)
		}
		_, err := normalizeCanonicalReadInput(CanonicalReadInput{RootNodeIDs: roots, MaxNodes: 1024})
		if count == 64 && err != nil {
			t.Fatalf("64 roots rejected: %v", err)
		}
		if count == 65 && (err == nil || !strings.Contains(err.Error(), "at most 64")) {
			t.Fatalf("65 roots: err = %v, want root limit", err)
		}
	}
}

func TestPropositionInventoryLimitStaysAt32(t *testing.T) {
	// The limit is checked before the transaction is used, so a nil tx is safe.
	for _, limit := range []int{0, 33} {
		if _, err := readPropositionMembersTx(context.Background(), nil, PropositionKey{}, limit); err == nil || !strings.Contains(err.Error(), "invalid proposition inventory limit") {
			t.Fatalf("limit %d: err = %v, want inventory limit rejection", limit, err)
		}
	}
}
