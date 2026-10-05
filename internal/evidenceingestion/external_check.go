package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExternalCheckContract identifies historical external assertions, never admission authority.
const ExternalCheckContract = "external-check/v1"

// ExternalCheckSubject binds immutable source and candidate identity, not freshness or approval.
type ExternalCheckSubject struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	SourceSnapshotID     string `json:"source_snapshot_id"`
	ExtractionViewID     string `json:"extraction_view_id"`
	SourceVersion        string `json:"source_version"`
	RawContentHash       string `json:"raw_content_hash"`
	RenderedContentHash  string `json:"rendered_content_hash"`
	ProposalFingerprint  string `json:"proposal_fingerprint"`
}

// ExternalCheckMaterial records exactly what a checker claims to have received.
// Format "verbatim" is checked against authoritative text; other formats are opaque.
type ExternalCheckMaterial struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Format       string `json:"format"`
	Version      string `json:"version"`
	Content      string `json:"content"`
	MappingClaim string `json:"mapping_claim"`
}

// ExternalCheckFinding separates the criterion from its external result.
// Omitted dimensions have no check claim. Unknown is not implicit success.
type ExternalCheckFinding struct {
	Dimension string   `json:"dimension"`
	Criterion string   `json:"criterion"`
	InputIDs  []string `json:"input_ids"`
	Result    string   `json:"result"`
	Detail    string   `json:"detail"`
}

// ExternalCheckInput contains the complete immutable report. RecordedBy is audit text,
// not authentication. RevisesID is a historical link, not a unique current-result selector.
type ExternalCheckInput struct {
	Contract             string                  `json:"contract"`
	RequestID            string                  `json:"request_id"`
	Subject              ExternalCheckSubject    `json:"subject"`
	CheckerName          string                  `json:"checker_name"`
	CheckerVersion       string                  `json:"checker_version"`
	CheckerConfiguration string                  `json:"checker_configuration"`
	RunRef               string                  `json:"run_ref"`
	RecordedBy           string                  `json:"recorded_by"`
	Materials            []ExternalCheckMaterial `json:"materials"`
	Findings             []ExternalCheckFinding  `json:"findings"`
	Limitations          []string                `json:"limitations"`
	RevisesID            string                  `json:"revises_id,omitempty"`
	RevisionReason       string                  `json:"revision_reason,omitempty"`
}

// ExternalCheckRecord preserves the report without an aggregate pass or truth status.
type ExternalCheckRecord struct {
	ID         string             `json:"id"`
	Report     ExternalCheckInput `json:"report"`
	RecordedAt time.Time          `json:"recorded_at"`
}

// ExternalCheckReceipt returns a historical record even when Replayed is true.
type ExternalCheckReceipt struct {
	Record   ExternalCheckRecord `json:"record"`
	Replayed bool                `json:"replayed"`
}

// ExternalCheckList is one bounded read; truncation cannot establish absence of other checks.
type ExternalCheckList struct {
	Records   []ExternalCheckRecord `json:"records"`
	Truncated bool                  `json:"truncated"`
}

func externalText(value string, maxBytes int, allowEmpty bool) bool {
	return (allowEmpty || strings.TrimSpace(value) != "") && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func externalDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (in ExternalCheckInput) validate() error {
	if in.Contract != ExternalCheckContract || !externalText(in.RequestID, 512, false) {
		return newDomainError(ErrorInvalidInput, "invalid external check contract or request id")
	}
	for _, v := range []string{in.Subject.ProposalOccurrenceID, in.Subject.SourceSnapshotID, in.Subject.ExtractionViewID, in.Subject.SourceVersion, in.Subject.RawContentHash, in.Subject.RenderedContentHash, in.Subject.ProposalFingerprint, in.CheckerName, in.CheckerVersion, in.RunRef, in.RecordedBy} {
		if !externalText(v, 1024, false) {
			return newDomainError(ErrorInvalidInput, "invalid external check identity")
		}
	}
	if !externalText(in.CheckerConfiguration, 8192, false) || len(in.Materials) < 1 || len(in.Materials) > 32 || len(in.Findings) < 1 || len(in.Findings) > 128 || len(in.Limitations) < 1 || len(in.Limitations) > 32 {
		return newDomainError(ErrorInvalidInput, "invalid external check bounds")
	}
	ids := map[string]bool{}
	for _, m := range in.Materials {
		if !externalText(m.ID, 128, false) || ids[m.ID] || (m.Role != "source" && m.Role != "candidate") || !externalText(m.Format, 256, false) || !externalText(m.Version, 256, false) || !externalText(m.Content, 262144, false) || !externalText(m.MappingClaim, 8192, false) {
			return newDomainError(ErrorInvalidInput, "invalid or duplicate external check material")
		}
		ids[m.ID] = true
	}
	dimensions := map[string]bool{}
	for _, f := range in.Findings {
		if !externalText(f.Dimension, 256, false) || dimensions[f.Dimension] || !externalText(f.Criterion, 8192, false) || !externalText(f.Detail, 8192, false) || len(f.InputIDs) > 32 {
			return newDomainError(ErrorInvalidInput, "invalid or duplicate external check finding")
		}
		dimensions[f.Dimension] = true
		switch f.Result {
		case "pass", "fail", "inconclusive":
			if len(f.InputIDs) == 0 {
				return newDomainError(ErrorInvalidInput, "checked finding requires actual inputs")
			}
		case "not_checked", "unsupported":
		default:
			return newDomainError(ErrorInvalidInput, "invalid external check result")
		}
		seen := map[string]bool{}
		for _, id := range f.InputIDs {
			if !ids[id] || seen[id] {
				return newDomainError(ErrorInvalidInput, "unknown or duplicate finding input")
			}
			seen[id] = true
		}
	}
	for _, v := range in.Limitations {
		if !externalText(v, 8192, false) {
			return newDomainError(ErrorInvalidInput, "invalid external check limitation")
		}
	}
	if (in.RevisesID == "") != (in.RevisionReason == "") || !externalText(in.RevisesID, 128, true) || !externalText(in.RevisionReason, 8192, true) {
		return newDomainError(ErrorInvalidInput, "revision requires previous report and reason")
	}
	return nil
}

// LoadExternalCheckSubject reads the immutable subject for a source-backed text proposal.
// It also works after admission; the subject contains no lifecycle or approval state.
func LoadExternalCheckSubject(ctx context.Context, pool *pgxpool.Pool, proposalID string) (ExternalCheckSubject, error) {
	if pool == nil {
		return ExternalCheckSubject{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	p, err := getProposalByOccurrenceID(ctx, pgxDB{pool: pool}, proposalID)
	if err != nil {
		return ExternalCheckSubject{}, err
	}
	return externalSubject(p)
}

func externalSubject(p ProposalQueryResult) (ExternalCheckSubject, error) {
	if p.SourceSnapshotID == "" || p.ExtractionViewID == "" || p.ExtractionAttemptStatus != attemptStatusSucceeded || p.ProposalKind != ProposalKindStatement || p.CodeFact != nil || p.CodeRelation != nil {
		return ExternalCheckSubject{}, newDomainError(ErrorInvalidInput, "external check requires a source-snapshot text proposal")
	}
	return ExternalCheckSubject{ProposalOccurrenceID: p.ProposalOccurrenceID, SourceSnapshotID: p.SourceSnapshotID, ExtractionViewID: p.ExtractionViewID, SourceVersion: p.SourceVersion, RawContentHash: p.RawContentHash, RenderedContentHash: p.RenderedContentHash, ProposalFingerprint: p.ProposalFingerprint}, nil
}

// RecordExternalCheck appends an external report using the core-records profile.
// No canonical node, support edge, review approval, or policy result is created.
func RecordExternalCheck(ctx context.Context, pool *pgxpool.Pool, in ExternalCheckInput) (ExternalCheckReceipt, error) {
	if pool == nil {
		return ExternalCheckReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if err := in.validate(); err != nil {
		return ExternalCheckReceipt{}, err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return ExternalCheckReceipt{}, err
	}
	if len(body) > 1048576 {
		return ExternalCheckReceipt{}, newDomainError(ErrorInvalidInput, "external check exceeds byte budget")
	}
	id := "check:sha256:" + externalDigest(body)
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ExternalCheckReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Arbitrate both the request key and content-digest primary key. Targeting
	// only request_id can leak a primary-key violation during identical retries.
	// Skipping an INSERT is not yet a replay: the fresh READ COMMITTED read below
	// must find this request and match its entire immutable body.
	tag, err := tx.Exec(ctx, `INSERT INTO external_check_records(check_id,request_id,proposal_occurrence_id,body,revises_id)
	 VALUES($1,$2,$3,$4,NULLIF($5,'')) ON CONFLICT DO NOTHING`, id, in.RequestID, in.Subject.ProposalOccurrenceID, string(body), in.RevisesID)
	if err != nil {
		return ExternalCheckReceipt{}, fmt.Errorf("recording external check: %w", err)
	}
	var storedID, storedBody string
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT check_id,body,recorded_at FROM external_check_records WHERE request_id=$1`, in.RequestID).Scan(&storedID, &storedBody, &at)
	if err != nil {
		return ExternalCheckReceipt{}, err
	}
	if storedID != id || storedBody != string(body) {
		return ExternalCheckReceipt{}, newDomainError(ErrorIdempotencyKeyReused, "external check request differs from historical receipt")
	}
	if err := tx.Commit(ctx); err != nil {
		return ExternalCheckReceipt{}, err
	}
	return ExternalCheckReceipt{Record: ExternalCheckRecord{ID: id, Report: in, RecordedAt: at}, Replayed: tag.RowsAffected() == 0}, nil
}

func scanExternalCheck(row interface{ Scan(...any) error }) (ExternalCheckRecord, error) {
	var out ExternalCheckRecord
	var body string
	if err := row.Scan(&out.ID, &body, &out.RecordedAt); err != nil {
		return out, err
	}
	if out.ID != "check:sha256:"+externalDigest([]byte(body)) {
		return out, newDomainError(ErrorInvalidInput, "external check digest mismatch")
	}
	if err := json.Unmarshal([]byte(body), &out.Report); err != nil {
		return out, err
	}
	if err := out.Report.validate(); err != nil {
		return out, err
	}
	return out, nil
}

// ReadExternalCheck returns the original report, not a claim that it remains applicable.
func ReadExternalCheck(ctx context.Context, pool *pgxpool.Pool, id string) (ExternalCheckRecord, error) {
	var out ExternalCheckRecord
	if pool == nil || !externalText(id, 128, false) {
		return out, newDomainError(ErrorInvalidInput, "pool and check id are required")
	}
	err := withExternalRecordRead(ctx, pool, func(tx sqlTx) error {
		var err error
		out, err = scanExternalCheck(tx.queryRow(ctx, `SELECT check_id,body,recorded_at FROM external_check_records WHERE check_id=$1`, id))
		return err
	})
	return out, err
}

// ReadExternalChecks returns up to limit reports for exactly one proposal occurrence.
func ReadExternalChecks(ctx context.Context, pool *pgxpool.Pool, proposalID string, limit int) (ExternalCheckList, error) {
	out := ExternalCheckList{Records: []ExternalCheckRecord{}}
	if pool == nil || !externalText(proposalID, 1024, false) || limit < 1 || limit > 100 {
		return out, newDomainError(ErrorInvalidInput, "pool, proposal and limit 1..100 required")
	}
	err := withExternalRecordRead(ctx, pool, func(tx sqlTx) error {
		rows, err := tx.query(ctx, `SELECT check_id,body,recorded_at FROM external_check_records WHERE proposal_occurrence_id=$1 ORDER BY check_id COLLATE "C" LIMIT $2`, proposalID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanExternalCheck(rows)
			if err != nil {
				return err
			}
			out.Records = append(out.Records, r)
		}
		return rows.Err()
	})
	if err != nil {
		return ExternalCheckList{}, err
	}
	if len(out.Records) > limit {
		out.Truncated = true
		out.Records = out.Records[:limit]
	}
	return out, nil
}

// Hidden history must produce an error rather than a misleading empty inventory.
func withExternalRecordRead(ctx context.Context, pool *pgxpool.Pool, fn func(sqlTx) error) error {
	return withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
			return err
		}
		return fn(tx)
	})
}
