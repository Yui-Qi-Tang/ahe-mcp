package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5/pgxpool"
)

const InterimGovernanceV1 = "interim-evidence-governance/v1"

// InterimGovernanceRecord is a versioned append-only interchange record, not a
// native approval. Original intake, actual checked materials, historical human
// decision and checker execution are deliberately separate. Missing fields are
// explicit and block migration; never fill a historical reviewer from a launcher.
type InterimGovernanceRecord struct {
	Version              string                          `json:"version"`
	RecordID             string                          `json:"record_id"`
	Revision             int64                           `json:"revision"`
	PreviousRecordHash   string                          `json:"previous_record_hash,omitempty"`
	StoreID              string                          `json:"store_id"`
	Kind                 string                          `json:"kind"` // exclusion, source_assessment, verification
	CanonicalID          string                          `json:"canonical_id,omitempty"`
	ClaimHash            string                          `json:"claim_hash,omitempty"`
	OccurrenceID         string                          `json:"occurrence_id,omitempty"`
	SourceSnapshotID     string                          `json:"source_snapshot_id,omitempty"`
	SourceContentHash    string                          `json:"source_content_hash,omitempty"`
	Purpose              string                          `json:"purpose"`
	Scope                string                          `json:"scope"`
	Reason               string                          `json:"reason"`
	Outcome              string                          `json:"outcome"`
	HistoricalReviewer   string                          `json:"historical_reviewer"`
	HistoricalReviewedAt time.Time                       `json:"historical_reviewed_at"`
	CheckerName          string                          `json:"checker_name,omitempty"`
	CheckerVersion       string                          `json:"checker_version,omitempty"`
	CheckedAt            time.Time                       `json:"checked_at,omitzero"`
	Criterion            string                          `json:"criterion,omitempty"`
	CriterionVersion     string                          `json:"criterion_version,omitempty"`
	FreshnessRule        string                          `json:"freshness_rule,omitempty"`
	Limitations          []string                        `json:"limitations"`
	OriginalMaterials    []evidenceuse.Material          `json:"original_materials"`
	CheckedMaterials     []evidenceuse.Material          `json:"checked_materials"`
	MissingFields        []string                        `json:"missing_fields"`
	Verification         *evidenceuse.VerificationReport `json:"verification,omitempty"`
	Native               GovernanceRequest               `json:"native"`
}
type governanceMigrationReference struct {
	RecordID   string `json:"record_id"`
	Revision   int64  `json:"revision"`
	RecordHash string `json:"record_hash"`
}

type PreparedGovernanceItem struct {
	RecordID        string            `json:"record_id"`
	RecordHash      string            `json:"record_hash"`
	Status          string            `json:"status"`
	Error           string            `json:"error,omitempty"`
	NativeRequestID string            `json:"native_request_id,omitempty"`
	Review          *GovernanceReview `json:"review,omitempty"`
}
type GovernanceBatchDecision struct {
	RecordID string             `json:"record_id"`
	Decision GovernanceDecision `json:"decision"`
}
type GovernanceBatchResult struct {
	RecordID string              `json:"record_id"`
	Status   string              `json:"status"`
	Error    string              `json:"error,omitempty"`
	Receipt  *GovernanceReceipt  `json:"receipt,omitempty"`
	Readback []GovernanceReceipt `json:"readback,omitempty"`
}

// A human stop-use decision need not have run a checker or produced a semantic
// verification report. MissingFields records genuinely unavailable history;
// absent inapplicable checker/cache fields must not be fabricated to migrate it.
func validateInterimGovernanceHistory(r InterimGovernanceRecord) error {
	for _, v := range []string{r.Purpose, r.Scope, r.Reason, r.Outcome, r.HistoricalReviewer} {
		if !consistencyText(v) {
			return fmt.Errorf("missing historical human decision")
		}
	}
	if r.HistoricalReviewedAt.IsZero() || len(r.OriginalMaterials) == 0 {
		return fmt.Errorf("missing historical review time or original materials")
	}
	if r.CheckerName != "" || r.CheckerVersion != "" {
		if !consistencyText(r.CheckerName) || !consistencyText(r.CheckerVersion) || r.CheckedAt.IsZero() {
			return fmt.Errorf("partial checker contract: name, version and check time required when used")
		}
	}
	if r.Kind != "exclusion" {
		if !consistencyText(r.Criterion) || !consistencyText(r.CriterionVersion) || r.CheckedAt.IsZero() || len(r.CheckedMaterials) == 0 {
			return fmt.Errorf("finding requires criterion, version, check time and checked materials")
		}
	}
	if r.Kind == "verification" && !consistencyText(r.FreshnessRule) {
		return fmt.Errorf("verification report requires its freshness rule")
	}
	for _, materials := range [][]evidenceuse.Material{r.OriginalMaterials, r.CheckedMaterials} {
		seen := map[string]bool{}
		for _, m := range materials {
			if !consistencyText(m.Identity) || !consistencyText(m.Revision) || !consistencyText(m.ContentHash) || seen[m.Identity] {
				return fmt.Errorf("material coordinates are incomplete or duplicate")
			}
			seen[m.Identity] = true
		}
	}
	return nil
}

// PrepareGovernanceBatch never writes. Every migratable item has its own native
// display and future request ID; a historical local approval approves neither.
func PrepareGovernanceBatch(ctx context.Context, pool *pgxpool.Pool, requests []GovernanceBatchRequest) (PreparedGovernanceBatch, error) {
	if pool == nil || len(requests) < 1 || len(requests) > 50 {
		return PreparedGovernanceBatch{}, fmt.Errorf("pool and 1..50 records required")
	}
	out := make([]PreparedGovernanceItem, len(requests))
	seenRecords, seenTargets := map[string]bool{}, map[string]bool{}
	var store string
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
			return err
		}
		if err := tx.queryRow(ctx, `SELECT store_id FROM evidence_governance_head WHERE singleton`).Scan(&store); err != nil {
			return err
		}
		for i, request := range requests {
			r := request.Record
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			hash := governanceDigest("sha256:", raw)
			item := PreparedGovernanceItem{RecordID: r.RecordID, RecordHash: hash, Status: "blocked"}
			validate := func() error {
				if r.Version != InterimGovernanceV1 || r.StoreID != store || !consistencyText(r.RecordID) || r.Revision < 1 || (r.Revision > 1 && r.PreviousRecordHash == "") || len(r.MissingFields) > 0 {
					return fmt.Errorf("record version, identity, store, chain or completeness mismatch")
				}
				key := r.RecordID
				target := r.Kind + ":" + r.Native.TargetID
				if seenRecords[key] || r.Kind != "verification" && seenTargets[target] {
					return fmt.Errorf("duplicate local record or native target")
				}
				seenRecords[key] = true
				seenTargets[target] = true
				if err := validateInterimGovernanceHistory(r); err != nil {
					return err
				}
				if r.Kind != "verification" && !validateEndpointText(request.Reason, 2000) {
					return fmt.Errorf("explicit proposed native reason required")
				}
				if r.Kind == "verification" {
					if r.Verification == nil || r.Verification.Version != evidenceuse.VerificationV1 {
						return fmt.Errorf("verification record lacks report")
					}
					c, err := getCanonicalEvidenceByID(ctx, tx, r.CanonicalID)
					if err != nil {
						return err
					}
					payload, err := json.Marshal(c.Payload)
					if err != nil {
						return err
					}
					want := evidenceuse.VerificationSubject{StoreID: store, CanonicalID: r.CanonicalID, ClaimHash: r.ClaimHash, Criterion: r.Criterion, CriterionVersion: r.CriterionVersion, Scope: r.Scope}
					if r.ClaimHash != governanceDigest("sha256:", payload) || r.OccurrenceID != c.OriginProposalOccurrenceID || r.Verification.Subject != want || r.Outcome != string(r.Verification.Result) || !r.CheckedAt.Equal(r.Verification.CheckedAt) || !reflect.DeepEqual(r.Verification.Materials, r.CheckedMaterials) {
						return fmt.Errorf("verification target, fingerprint or checked materials mismatch")
					}
					item.Status = "verification_only_no_native_effect"
					return nil
				}
				if r.Kind == "exclusion" {
					if r.Native.Kind != "withdrawal" || r.Native.TargetID != r.CanonicalID || r.Native.Operation != "withdraw" || r.Outcome != "exclude" {
						return fmt.Errorf("exclusion migration must explicitly withdraw its exact claim")
					}
					c, err := getCanonicalEvidenceByID(ctx, tx, r.CanonicalID)
					if err != nil {
						return err
					}
					payload, err := json.Marshal(c.Payload)
					if err != nil {
						return err
					}
					if r.ClaimHash != governanceDigest("sha256:", payload) || r.OccurrenceID != c.OriginProposalOccurrenceID {
						return fmt.Errorf("claim hash or occurrence mismatch")
					}
				} else if r.Kind == "source_assessment" {
					if r.Native.Kind != "source_assessment" || r.Native.TargetID != r.SourceSnapshotID {
						return fmt.Errorf("source migration target mismatch")
					}
					s, err := loadGovernanceSource(ctx, tx, r.SourceSnapshotID)
					if err != nil {
						return err
					}
					if s.Snapshot.RawContentHash != r.SourceContentHash {
						return fmt.Errorf("source migration hash mismatch")
					}
				} else {
					return fmt.Errorf("unsupported local record kind")
				}
				req := r.Native
				ref, err := json.Marshal(governanceMigrationReference{RecordID: r.RecordID, Revision: r.Revision, RecordHash: hash})
				if err != nil {
					return err
				}
				req.MigrationRef = string(ref)
				if err := validateGovernanceRequest(req); err != nil {
					return err
				}
				review, err := loadGovernanceReview(ctx, tx, req)
				if err != nil {
					return err
				}
				item.Review = &review
				item.Status = "requires_exact_native_approval"
				item.NativeRequestID = governanceDigest("migration:", []byte(store+"\n"+req.MigrationRef+"\n"+review.Subject))
				return nil
			}
			if err := validate(); err != nil {
				item.Error = err.Error()
			}
			out[i] = item
		}
		return nil
	})
	if err != nil {
		return PreparedGovernanceBatch{}, err
	}
	return prepareGovernanceManifest(store, requests, out)
}

// ApplyGovernanceBatch applies ONLY the explicitly approved subset, with one
// transaction per item. Keep the local exclusion until this readback and the
// real consumer gate are both checked. An uncertain retry uses identical input.
func ApplyGovernanceBatch(ctx context.Context, pool *pgxpool.Pool, approval GovernanceBatchApproval, reviewer string) ([]GovernanceBatchResult, error) {
	approved := approval.ApprovedItems
	if pool == nil || len(approved) < 1 || len(approved) > 50 || !consistencyText(reviewer) {
		return nil, fmt.Errorf("pool, launcher reviewer and 1..50 approved items required")
	}
	subject, err := governanceBatchSubject(approval.Manifest)
	if err != nil || subject != approval.ExpectedManifestSubject {
		return nil, fmt.Errorf("batch review subject changed or invalid; exact manifest approval required")
	}
	entries := map[string]GovernanceBatchManifestItem{}
	for _, entry := range approval.Manifest.Items {
		if _, duplicate := entries[entry.Record.RecordID]; duplicate {
			// Duplicate input remains reviewable as a blocked item, but cannot
			// identify one exact selected operation by record ID.
			entries[entry.Record.RecordID] = GovernanceBatchManifestItem{}
			continue
		}
		entries[entry.Record.RecordID] = entry
	}
	out := make([]GovernanceBatchResult, 0, len(approved))
	seen := map[string]bool{}
	for _, item := range approved {
		result := GovernanceBatchResult{RecordID: item.RecordID, Status: "failed"}
		in := item.Decision
		entry, exists := entries[item.RecordID]
		if seen[item.RecordID] || !exists || entry.Record.RecordID != item.RecordID {
			result.Error = "duplicate or missing local/native binding"
			out = append(out, result)
			continue
		}
		seen[item.RecordID] = true
		if err := validateGovernanceManifestItem(approval.Manifest.StoreID, entry, in); err != nil {
			result.Error = err.Error()
			out = append(out, result)
			continue
		}
		in.BatchSubject = subject
		in.ReviewerID = reviewer
		receipt, err := ApplyGovernanceDecision(ctx, pool, in)
		if err != nil {
			result.Error = err.Error()
			if strings.Contains(err.Error(), "conflict") || strings.Contains(err.Error(), "subject changed") {
				result.Status = "conflict"
			}
			out = append(out, result)
			continue
		}
		result.Receipt = &receipt
		result.Status = "applied"
		if receipt.Replayed {
			result.Status = "replayed"
		}
		history, err := ReadGovernanceHistory(ctx, pool, in.Review.Kind, in.Review.TargetID)
		if err != nil {
			result.Status = "applied_readback_failed"
			result.Error = err.Error()
		} else {
			result.Readback = history
		}
		out = append(out, result)
	}
	return out, nil
}
