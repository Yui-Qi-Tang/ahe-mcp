package evidenceingestion

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ReviewConflictCandidatesV1 is live advisory context, not approval authority.
	ReviewConflictCandidatesV1 = "review-conflict-candidates/v1"
	// ReviewConflictMaxBytes bounds the complete encoded advisory, including metadata.
	ReviewConflictMaxBytes   = 128 << 10
	reviewConflictMaxTargets = 65 // One statement plus the bounded 64-node ancestry.
)

// ReviewConflictCandidates describes a current read cut separately from the
// immutable approval display. Receipts do not preserve this advisory.
type ReviewConflictCandidates struct {
	Contract            string                 `json:"contract"`
	Status              string                 `json:"status"`
	Observation         string                 `json:"observation"`
	Binding             string                 `json:"binding"`
	StoreID             string                 `json:"store_id"`
	GovernanceRevision  int64                  `json:"governance_revision"`
	DatabaseSnapshot    string                 `json:"database_snapshot"`
	TargetSelection     string                 `json:"target_selection"`
	TargetsComplete     bool                   `json:"targets_complete"`
	MaxTargets          int                    `json:"max_targets"`
	MaxBytes            int                    `json:"max_bytes"`
	ByteBudgetTruncated bool                   `json:"byte_budget_truncated"`
	Targets             []ReviewConflictTarget `json:"targets"`
	Limitations         []string               `json:"limitations"`
}

// ReviewConflictTarget keeps per-statement coverage after transport bounding.
type ReviewConflictTarget struct {
	ProposalOccurrenceID string                     `json:"proposal_occurrence_id"`
	CanonicalID          string                     `json:"canonical_id,omitempty"`
	ReturnLimit          int                        `json:"return_limit"`
	Suggestions          []ConflictSuggestion       `json:"suggestions"`
	Coverage             ConflictSuggestionCoverage `json:"coverage"`
	ByteBudgetOmitted    int                        `json:"byte_budget_omitted"`
}

func loadReviewConflictCandidates(ctx context.Context, tx sqlTx, ids []string) (*ReviewConflictCandidates, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > reviewConflictMaxTargets {
		return nil, newDomainError(ErrorInvalidInput, "complete review conflict targets exceed %d", reviewConflictMaxTargets)
	}
	if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return nil, err
	}
	out := &ReviewConflictCandidates{Contract: ReviewConflictCandidatesV1, Status: "observed", Observation: "current_read_snapshot",
		Binding: "advisory_not_bound_to_review_subject_or_receipt", TargetSelection: "reviewed_statement_and_statement_ancestor_origins",
		TargetsComplete: true, MaxTargets: reviewConflictMaxTargets, Targets: []ReviewConflictTarget{}, Limitations: []string{
			"Live advisory from this read transaction, separate from the exact approval display and subject. It is not persisted in the decision receipt, does not invalidate an approval when candidates change, and does not prove a reviewer saw or recognized any candidate. Retain this response separately if its observation must be audited.",
			"Each target scans at most 1000 canonical claims and selects at most 20 candidates using the existing deterministic matching/order. Pending proposals and claims outside that bounded scan are not covered. Empty suggestions do not prove absence of conflict; candidates are not contradiction verdicts or current-use clearance.",
			"The encoded advisory is byte-bounded. Only whole candidates may be omitted; matched_claims is within the bounded scan, returned_claims is the delivered count. Every target and its coverage remain visible, including omissions. Errors fail the getter rather than becoming empty results.",
		}}
	if err := tx.queryRow(ctx, `SELECT store_id,revision,pg_current_snapshot()::text FROM evidence_governance_head WHERE singleton`).Scan(&out.StoreID, &out.GovernanceRevision, &out.DatabaseSnapshot); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		out.Status = "not_applicable"
	}
	for _, id := range ids {
		r, err := loadConflictSuggestionsInTx(ctx, tx, ConflictSuggestionRequest{ProposalOccurrenceID: id, Limit: 20, ScanLimit: 1000})
		if err != nil {
			return nil, err
		}
		if r.StoreID != out.StoreID || r.GovernanceRevision != out.GovernanceRevision {
			return nil, newDomainError(ErrorReviewContractConflict, "conflict candidates differ from review read cut")
		}
		out.Targets = append(out.Targets, ReviewConflictTarget{ProposalOccurrenceID: id, CanonicalID: r.Proposal.CanonicalRef, ReturnLimit: 20,
			Suggestions: r.Suggestions, Coverage: r.Coverage})
	}
	return BoundReviewConflictCandidates(out, ReviewConflictMaxBytes)
}

// BoundReviewConflictCandidates returns a detached advisory within the encoded
// byte budget. It retains every target and removes whole lowest-ranked entries
// from the most populated target first. Metadata that cannot fit fails closed.
func BoundReviewConflictCandidates(input *ReviewConflictCandidates, maxBytes int) (*ReviewConflictCandidates, error) {
	if input == nil || input.Contract != ReviewConflictCandidatesV1 || maxBytes <= 0 {
		return nil, newDomainError(ErrorInvalidInput, "review conflict advisory and positive byte budget required")
	}
	out := *input
	maxBytes = min(maxBytes, ReviewConflictMaxBytes)
	out.MaxBytes = maxBytes
	out.Targets = slices.Clone(input.Targets)
	out.Limitations = slices.Clone(input.Limitations)
	for i := range out.Targets {
		out.Targets[i].Suggestions = slices.Clone(input.Targets[i].Suggestions)
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	size := len(body)
	for size > maxBytes {
		selected := -1
		for i, target := range out.Targets {
			if len(target.Suggestions) > 0 && (selected < 0 || len(target.Suggestions) >= len(out.Targets[selected].Suggestions)) {
				selected = i
			}
		}
		if selected < 0 {
			return nil, newDomainError(ErrorInvalidInput, "complete review conflict metadata exceeds byte limit; no partial review returned")
		}
		target := &out.Targets[selected]
		// Account for one complete encoded candidate and the small metadata delta.
		// Re-encoding the entire unbounded advisory on every removal would make
		// a legal many-parent review needlessly quadratic in candidate bytes.
		candidate, err := json.Marshal(target.Suggestions[len(target.Suggestions)-1])
		if err != nil {
			return nil, err
		}
		metadata := *target
		metadata.Suggestions = nil
		before, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		size -= len(candidate)
		if len(target.Suggestions) > 1 {
			size-- // Array separator removed with the last entry.
		}
		target.Suggestions = target.Suggestions[:len(target.Suggestions)-1]
		target.ByteBudgetOmitted++
		target.Coverage.ReturnedClaims = len(target.Suggestions)
		target.Coverage.ResultsTruncated = true
		metadata = *target
		metadata.Suggestions = nil
		after, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		size += len(after) - len(before)
		if !out.ByteBudgetTruncated {
			size-- // JSON false becomes true.
			out.ByteBudgetTruncated = true
		}
	}
	// The actual final encoding is authoritative, even if accounting changes.
	body, err = json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(body) != size || len(body) > maxBytes {
		return nil, newDomainError(ErrorInvalidInput, "review conflict byte accounting differs from encoded response")
	}
	return &out, nil
}

func endpointConflictTargets(display EndpointReviewDisplay) []string {
	if display.Request.Kind == "repository_code" {
		return nil
	}
	ids := []string{display.Proposal.ProposalOccurrenceID}
	for _, ancestor := range display.Ancestors {
		if ancestor.OriginProposal.ProposalKind == ProposalKindStatement {
			ids = append(ids, ancestor.OriginProposalOccurrenceID)
		}
	}
	return ids
}

// LoadSourceClaimReviewWithConflicts loads the exact review and live advisory
// in one read-only RR transaction. Internal writer reconstruction stays unchanged.
func LoadSourceClaimReviewWithConflicts(ctx context.Context, pool *pgxpool.Pool, attemptID, occurrenceID string) (ReviewableSourceClaimReviewSnapshot, error) {
	if pool == nil {
		return ReviewableSourceClaimReviewSnapshot{}, newDomainError(ErrorInvalidInput, "postgres pool required")
	}
	if err := validateReviewSnapshotCoordinates(attemptID, occurrenceID); err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	var out ReviewableSourceClaimReviewSnapshot
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		out, err = loadReviewableSourceClaimReviewSnapshotInTx(ctx, tx, attemptID, occurrenceID)
		if err != nil {
			return err
		}
		out.ConflictCandidates, err = loadReviewConflictCandidates(ctx, tx, []string{occurrenceID})
		return err
	})
	return out, err
}
