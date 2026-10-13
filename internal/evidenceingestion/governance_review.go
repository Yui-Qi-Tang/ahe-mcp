package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const GovernanceReviewV1 = "evidence-governance-review/v1"
const GovernanceEventV1 = "evidence-governance-event/v1"
const governanceEventMaxBytes = 1 << 20

// GovernanceRequest is an exact review selection. Source findings do not change
// admission or withdrawal. Corrections are navigational references only.
type GovernanceRequest struct {
	MigrationRef     string                    `json:"migration_ref,omitempty"`
	Kind             string                    `json:"kind"`
	TargetID         string                    `json:"target_id"`
	Operation        string                    `json:"operation"`
	ExpectedRevision int64                     `json:"expected_revision"`
	PreviousEventID  string                    `json:"previous_event_id,omitempty"`
	CorrectionIDs    []string                  `json:"correction_ids"`
	Checks           []evidenceuse.SourceCheck `json:"checks"`
	MaterialIDs      []string                  `json:"material_ids"`
	ResolvesEventIDs []string                  `json:"resolves_event_ids"`
	Method           string                    `json:"method,omitempty"`
	CheckerName      string                    `json:"checker_name,omitempty"`
	CheckerVersion   string                    `json:"checker_version,omitempty"`
	CheckedAt        string                    `json:"checked_at,omitempty"`
	Limitations      []string                  `json:"limitations"`
}

// GovernanceSource retains the original envelope and exact bytes. No verbatim
// intake preflight is imposed on withdrawal or retrospective source assessment.
type GovernanceSource struct {
	Snapshot      SourceSnapshot `json:"snapshot"`
	RetainedBytes []byte         `json:"retained_bytes"`
}
type GovernanceEventSummary struct {
	EventID          string                    `json:"event_id"`
	Revision         int64                     `json:"revision"`
	RequestID        string                    `json:"request_id"`
	Operation        string                    `json:"operation"`
	Reason           string                    `json:"reason"`
	ReviewerID       string                    `json:"reviewer_id"`
	Checks           []evidenceuse.SourceCheck `json:"checks"`
	ResolvesEventIDs []string                  `json:"resolves_event_ids"`
}
type GovernanceDisplay struct {
	Contract    string                   `json:"contract"`
	StoreID     string                   `json:"store_id"`
	Request     GovernanceRequest        `json:"request"`
	Claim       *CanonicalQueryResult    `json:"claim,omitempty"`
	Source      *GovernanceSource        `json:"source,omitempty"`
	Materials   []GovernanceSource       `json:"materials"`
	Corrections []CanonicalQueryResult   `json:"corrections"`
	History     []GovernanceEventSummary `json:"history"`
	Limitations []string                 `json:"limitations"`
}
type GovernanceReview struct {
	Subject string            `json:"subject"`
	Display GovernanceDisplay `json:"display"`
}
type GovernanceDecision struct {
	BatchSubject    string            `json:"-"`
	RequestID       string            `json:"request_id"`
	Review          GovernanceRequest `json:"review"`
	ExpectedSubject string            `json:"expected_subject"`
	Decision        string            `json:"decision"`
	Reason          string            `json:"reason"`
	ReviewerID      string            `json:"-"`
}
type governanceEventBody struct {
	BatchSubject string `json:"batch_subject,omitempty"`
	Contract     string `json:"contract"`
	RequestID    string `json:"request_id"`
	Subject      string `json:"subject"`
	Display      string `json:"display"`
	Decision     string `json:"decision"`
	Reason       string `json:"reason"`
	ReviewerID   string `json:"reviewer_id"`
}
type GovernanceReceipt struct {
	BatchSubject    string            `json:"batch_subject,omitempty"`
	EventID         string            `json:"event_id"`
	RequestID       string            `json:"request_id"`
	TargetID        string            `json:"target_id"`
	Revision        int64             `json:"revision"`
	PreviousEventID string            `json:"previous_event_id,omitempty"`
	RecordedAt      time.Time         `json:"recorded_at"`
	Subject         string            `json:"subject"`
	Display         GovernanceDisplay `json:"display"`
	Decision        string            `json:"decision"`
	Reason          string            `json:"reason"`
	ReviewerID      string            `json:"reviewer_id"`
	Replayed        bool              `json:"replayed"`
}

func governanceDigest(prefix string, b []byte) string {
	h := sha256.Sum256(b)
	return prefix + hex.EncodeToString(h[:])
}
func governanceTable(kind string) (string, error) {
	switch kind {
	case "withdrawal":
		return "canonical_withdrawal_events", nil
	case "source_assessment":
		return "source_assessment_events", nil
	}
	return "", fmt.Errorf("unsupported governance kind %q", kind)
}
func validateGovernanceRequest(r GovernanceRequest) error {
	if _, err := governanceTable(r.Kind); err != nil {
		return err
	}
	if !consistencyText(r.TargetID) || r.ExpectedRevision < 0 || len(r.CorrectionIDs) > 32 || len(r.Checks) > 32 || len(r.MaterialIDs) > 32 || len(r.ResolvesEventIDs) > 32 || len(r.Limitations) > 32 {
		return fmt.Errorf("invalid governance request bounds")
	}
	for _, ids := range [][]string{r.CorrectionIDs, r.MaterialIDs, r.ResolvesEventIDs} {
		seen := map[string]bool{}
		for _, id := range ids {
			if !consistencyText(id) || seen[id] {
				return fmt.Errorf("empty or duplicate governance reference")
			}
			seen[id] = true
		}
	}
	if r.Kind == "withdrawal" {
		if (r.Operation != "withdraw" && r.Operation != "restore") || len(r.Checks)+len(r.MaterialIDs)+len(r.ResolvesEventIDs) > 0 || r.Method != "" || r.CheckerName != "" || r.CheckerVersion != "" || r.CheckedAt != "" {
			return fmt.Errorf("withdrawal only permits withdraw/restore and correction references")
		}
	} else {
		if r.Operation != "assess" || len(r.Checks) == 0 || len(r.MaterialIDs) == 0 || len(r.CorrectionIDs) > 0 || !consistencyText(r.Method) || !consistencyText(r.CheckerName) || !consistencyText(r.CheckerVersion) || len(r.Limitations) == 0 {
			return fmt.Errorf("source assessment requires method, checker, materials, findings and limitations")
		}
		if _, err := time.Parse(time.RFC3339Nano, r.CheckedAt); err != nil {
			return fmt.Errorf("source check time required: %w", err)
		}
		criteria := map[string]bool{}
		for _, c := range r.Checks {
			if criteria[c.Criterion] {
				return fmt.Errorf("duplicate source finding criterion")
			}
			criteria[c.Criterion] = true
			if (c.AssessedField != "" && c.AssessedField != governanceAssessedField(c.Criterion)) || (c.CorrectedValue != "" && (!consistencyText(c.CorrectedValue) || (c.Criterion != evidenceuse.CorrespondenceV1 && c.Criterion != evidenceuse.CoverageV1) || c.Result != evidenceuse.CheckFail)) {
				return fmt.Errorf("source correction must identify a failed fidelity or coverage declaration")
			}
			if c.AssessmentID != "" || c.SourceSnapshotID != r.TargetID || !consistencyText(c.ContentHash) || c.Scope != evidenceuse.RetainedSnapshotScope || !slices.Contains(r.MaterialIDs, c.MaterialRef) {
				return fmt.Errorf("source check must bind exact target, full snapshot scope and comparison material")
			}
			if !slices.Contains([]string{evidenceuse.RetentionV1, evidenceuse.CorrespondenceV1, evidenceuse.CoverageV1, "statement-support/v1"}, c.Criterion) || !slices.Contains([]evidenceuse.CheckResult{evidenceuse.CheckPass, evidenceuse.CheckFail, evidenceuse.CheckInconclusive, evidenceuse.CheckUnknown}, c.Result) {
				return fmt.Errorf("unsupported source finding")
			}
		}
	}
	return nil
}

func LoadGovernanceReview(ctx context.Context, pool *pgxpool.Pool, req GovernanceRequest) (GovernanceReview, error) {
	if pool == nil {
		return GovernanceReview{}, fmt.Errorf("postgres pool required")
	}
	if err := validateGovernanceRequest(req); err != nil {
		return GovernanceReview{}, err
	}
	var review GovernanceReview
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error { var err error; review, err = loadGovernanceReview(ctx, tx, req); return err })
	return review, err
}
func loadGovernanceSource(ctx context.Context, tx sqlQueryer, id string) (GovernanceSource, error) {
	var s GovernanceSource
	var meta []byte
	err := tx.queryRow(ctx, `SELECT s.source_snapshot_id,s.source_system,s.source_id,s.source_version,s.raw_content_hash,s.origin_metadata,CASE WHEN octet_length(b.raw_content)<=1048576 THEN b.raw_content END
 FROM source_snapshots s JOIN source_blobs b USING(raw_content_hash) WHERE source_snapshot_id=$1`, id).Scan(&s.Snapshot.ID, &s.Snapshot.SourceSystem, &s.Snapshot.SourceID, &s.Snapshot.SourceVersion, &s.Snapshot.RawContentHash, &meta, &s.RetainedBytes)
	if err != nil {
		return s, err
	}
	if s.RetainedBytes == nil {
		return s, fmt.Errorf("retained source exceeds review bound; no partial display is reviewable")
	}
	if contentHash(s.RetainedBytes) != s.Snapshot.RawContentHash {
		return s, fmt.Errorf("retained source integrity mismatch")
	}
	err = json.Unmarshal(meta, &s.Snapshot.OriginMetadata)
	return s, err
}
func loadGovernanceReview(ctx context.Context, tx sqlTx, req GovernanceRequest) (GovernanceReview, error) {
	req = normalizeGovernanceRequest(req)
	if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return GovernanceReview{}, err
	}
	d := GovernanceDisplay{Contract: GovernanceReviewV1, Request: req, Materials: []GovernanceSource{}, Corrections: []CanonicalQueryResult{}, History: []GovernanceEventSummary{}, Limitations: []string{"Retained bytes and original intake declarations are displayed as recorded; their fidelity is not presumed. Withdrawal does not decide truth or rewrite admission, parents, contradiction or supersession. Restoration clears withdrawal only.", "Source assessment is scoped to this exact snapshot. Missing historical provider revision prevents a conclusive historical correspondence or coverage finding. Statement support is independent of source provenance."}}
	if err := tx.queryRow(ctx, `SELECT store_id FROM evidence_governance_head WHERE singleton`).Scan(&d.StoreID); err != nil {
		return GovernanceReview{}, err
	}
	history, err := loadGovernanceEvents(ctx, tx, req.Kind, req.TargetID)
	if err != nil {
		return GovernanceReview{}, err
	}
	var rev int64
	var prev, op string
	for _, e := range history {
		d.History = append(d.History, GovernanceEventSummary{EventID: e.EventID, Revision: e.Revision, RequestID: e.RequestID, Operation: e.Display.Request.Operation, Reason: e.Reason, ReviewerID: e.ReviewerID, Checks: e.Display.Request.Checks, ResolvesEventIDs: e.Display.Request.ResolvesEventIDs})
		rev, prev, op = e.Revision, e.EventID, e.Display.Request.Operation
	}
	if req.ExpectedRevision != rev || req.PreviousEventID != prev {
		return GovernanceReview{}, fmt.Errorf("governance revision conflict: current revision %d, event %s", rev, prev)
	}
	if req.Kind == "withdrawal" {
		if req.Operation == "restore" && op != "withdraw" {
			return GovernanceReview{}, fmt.Errorf("restore requires an active withdrawal")
		}
		c, err := getCanonicalEvidenceByID(ctx, tx, req.TargetID)
		if err != nil {
			return GovernanceReview{}, err
		}
		if c.NodeKind != evidencegraph.CanonicalSourceClaim && c.NodeKind != evidencegraph.CanonicalDerivedClaim {
			return GovernanceReview{}, fmt.Errorf("only admitted claims can be withdrawn")
		}
		d.Claim = &c
		// Source and all AND leaves remain inspectable even for old condensed inputs.
		sources, err := governanceClaimSources(ctx, tx, req.TargetID)
		if err != nil {
			return GovernanceReview{}, err
		}
		for _, id := range sources {
			s, err := loadGovernanceSource(ctx, tx, id)
			if err != nil {
				return GovernanceReview{}, err
			}
			d.Materials = append(d.Materials, s)
		}
		for _, id := range req.CorrectionIDs {
			if id == req.TargetID {
				return GovernanceReview{}, fmt.Errorf("correction cannot reference its target")
			}
			c, err := getCanonicalEvidenceByID(ctx, tx, id)
			if err != nil {
				return GovernanceReview{}, err
			}
			if c.NodeKind != evidencegraph.CanonicalSourceClaim && c.NodeKind != evidencegraph.CanonicalDerivedClaim {
				return GovernanceReview{}, fmt.Errorf("correction must reference an admitted claim")
			}
			d.Corrections = append(d.Corrections, c)
		}
	} else {
		s, err := loadGovernanceSource(ctx, tx, req.TargetID)
		if err != nil {
			return GovernanceReview{}, err
		}
		d.Source = &s
		for _, id := range req.MaterialIDs {
			m, err := loadGovernanceSource(ctx, tx, id)
			if err != nil {
				return GovernanceReview{}, err
			}
			d.Materials = append(d.Materials, m)
		}
		for _, c := range req.Checks {
			if c.ContentHash != s.Snapshot.RawContentHash {
				return GovernanceReview{}, fmt.Errorf("source content hash changed")
			}
			if (c.Criterion == evidenceuse.CorrespondenceV1 || c.Criterion == evidenceuse.CoverageV1) && c.Result != evidenceuse.CheckInconclusive && c.Result != evidenceuse.CheckUnknown {
				if governanceProviderRevision(s.Snapshot) == "" || c.ComparedRevision != governanceProviderRevision(s.Snapshot) {
					return GovernanceReview{}, fmt.Errorf("historical finding requires the original provider revision")
				}
				for _, m := range d.Materials {
					if m.Snapshot.ID == c.MaterialRef && governanceProviderRevision(m.Snapshot) != c.ComparedRevision {
						return GovernanceReview{}, fmt.Errorf("comparison source revision does not match finding")
					}
				}
			}
		}
		for _, id := range req.ResolvesEventIDs {
			index := slices.IndexFunc(history, func(e GovernanceReceipt) bool { return e.EventID == id })
			if index < 0 {
				return GovernanceReview{}, fmt.Errorf("resolution references another snapshot or missing event")
			}
			for _, previous := range history[index].Display.Request.Checks {
				if !slices.ContainsFunc(req.Checks, func(c evidenceuse.SourceCheck) bool { return c.Criterion == previous.Criterion }) {
					return GovernanceReview{}, fmt.Errorf("resolution must reassess every criterion in the referenced event")
				}
			}
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return GovernanceReview{}, err
	}
	if err := validateGovernanceDisplaySize(raw); err != nil {
		return GovernanceReview{}, err
	}
	return GovernanceReview{Subject: governanceDigest("governance-review:sha256:", raw), Display: d}, nil
}

// Reserve the whole persisted envelope before offering a subject for approval.
// JSON can expand one permitted ASCII control byte to six bytes; checking only
// the display length would permit reviews that no valid decision could store.
func validateGovernanceDisplaySize(raw []byte) error {
	body, err := json.Marshal(governanceEventBody{
		Contract: GovernanceEventV1, Display: string(raw), Decision: "approved",
		Subject:   governanceDigest("governance-review:sha256:", raw),
		RequestID: strings.Repeat("\x01", 200), ReviewerID: strings.Repeat("\x01", 200),
		Reason: strings.Repeat("\x01", 2000), BatchSubject: strings.Repeat("\x01", 100),
	})
	if err != nil {
		return err
	}
	if len(body) > governanceEventMaxBytes {
		return fmt.Errorf("governance review exceeds event byte limit including decision envelope")
	}
	return nil
}
func governanceClaimSources(ctx context.Context, tx sqlQueryer, id string) ([]string, error) {
	rows, err := tx.query(ctx, `WITH RECURSIVE ancestry(id) AS (SELECT $1::text UNION SELECT p.parent_node_id FROM ancestry a JOIN canonical_derivations d ON d.node_id=a.id JOIN canonical_derivation_parents p USING(derivation_id))
 SELECT DISTINCT r.source_snapshot_id FROM ancestry a JOIN canonical_graph_nodes n ON n.canonical_node_id=a.id
 JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
 JOIN extraction_attempts e USING(extraction_attempt_id) JOIN extraction_runs r USING(extraction_run_id)
 WHERE r.source_snapshot_id IS NOT NULL ORDER BY r.source_snapshot_id LIMIT 10001`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) > 10000 {
		return nil, fmt.Errorf("source ancestry exceeds limit")
	}
	return ids, rows.Err()
}
func lockGovernance(ctx context.Context, tx sqlTx) error {
	_, err := tx.exec(ctx, `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)`)
	return err
}
func ApplyGovernanceDecision(ctx context.Context, pool *pgxpool.Pool, in GovernanceDecision) (GovernanceReceipt, error) {
	in.Review = normalizeGovernanceRequest(in.Review)
	if pool == nil {
		return GovernanceReceipt{}, fmt.Errorf("postgres pool required")
	}
	if err := validateGovernanceRequest(in.Review); err != nil {
		return GovernanceReceipt{}, err
	}
	if !consistencyText(in.RequestID) || len(in.RequestID) > 200 || in.Decision != "approved" || strings.TrimSpace(in.Reason) == "" || !utf8.ValidString(in.Reason) || strings.ContainsRune(in.Reason, 0) || len(in.Reason) > 2000 || !consistencyText(in.ReviewerID) || len(in.ReviewerID) > 200 || in.ExpectedSubject == "" {
		return GovernanceReceipt{}, fmt.Errorf("exact human decision, reason, request and launcher reviewer required")
	}
	if len(in.BatchSubject) > 100 {
		return GovernanceReceipt{}, fmt.Errorf("batch subject exceeds byte limit")
	}
	table, _ := governanceTable(in.Review.Kind)
	var out GovernanceReceipt
	err := runReviewedSourceClaimAdmissionAttempts(func() error {
		return withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
				return err
			}
			// Lock before request lookup: concurrent exact retries serialize, including
			// an initially absent request. Historical replay never becomes a use permit.
			if err := lockGovernance(ctx, tx); err != nil {
				return err
			}
			receipt, err := readGovernanceReceipt(ctx, tx, table, "request_id", in.RequestID)
			if err == nil {
				if receipt.BatchSubject != in.BatchSubject || receipt.Subject != in.ExpectedSubject || receipt.ReviewerID != in.ReviewerID || receipt.Reason != in.Reason || receipt.Decision != in.Decision || !reflect.DeepEqual(receipt.Display.Request, in.Review) {
					return fmt.Errorf("governance request ID reused with changed input")
				}
				receipt.Replayed = true
				out = receipt
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			review, err := loadGovernanceReview(ctx, tx, in.Review)
			if err != nil {
				return err
			}
			if review.Subject != in.ExpectedSubject {
				return fmt.Errorf("governance review subject changed; exact new review required")
			}
			raw, err := json.Marshal(review.Display)
			if err != nil {
				return err
			}
			body, err := json.Marshal(governanceEventBody{BatchSubject: in.BatchSubject, Contract: GovernanceEventV1, RequestID: in.RequestID, Subject: review.Subject, Display: string(raw), Decision: in.Decision, Reason: in.Reason, ReviewerID: in.ReviewerID})
			if err != nil {
				return err
			}
			if len(body) > governanceEventMaxBytes {
				return fmt.Errorf("governance event exceeds byte limit")
			}
			id := governanceDigest("governance:sha256:", body)
			_, err = tx.exec(ctx, `INSERT INTO `+table+`(event_id,request_id,target_id,revision,previous_event_id,body) VALUES($1,$2,$3,$4,NULLIF($5,''),$6)`, id, in.RequestID, in.Review.TargetID, in.Review.ExpectedRevision+1, in.Review.PreviousEventID, string(body))
			if err != nil {
				return err
			}
			out, err = readGovernanceReceipt(ctx, tx, table, "event_id", id)
			return err
		})
	})
	return out, err
}
func scanGovernanceReceipt(row sqlRow) (GovernanceReceipt, error) {
	var r GovernanceReceipt
	var body string
	if err := row.Scan(&r.EventID, &r.RequestID, &r.TargetID, &r.Revision, &r.PreviousEventID, &r.RecordedAt, &body); err != nil {
		return r, err
	}
	var b governanceEventBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return r, err
	}
	if b.Contract != GovernanceEventV1 || b.RequestID != r.RequestID || governanceDigest("governance:sha256:", []byte(body)) != r.EventID || governanceDigest("governance-review:sha256:", []byte(b.Display)) != b.Subject {
		return r, fmt.Errorf("invalid governance receipt")
	}
	if err := json.Unmarshal([]byte(b.Display), &r.Display); err != nil {
		return r, err
	}
	r.Subject, r.Decision, r.Reason, r.ReviewerID = b.Subject, b.Decision, b.Reason, b.ReviewerID
	r.BatchSubject = b.BatchSubject
	return r, nil
}
func readGovernanceReceipt(ctx context.Context, tx sqlQueryer, table, column, id string) (GovernanceReceipt, error) {
	return scanGovernanceReceipt(tx.queryRow(ctx, `SELECT event_id,request_id,target_id,revision,coalesce(previous_event_id,''),recorded_at,body FROM `+table+` WHERE `+column+`=$1`, id))
}
func loadGovernanceEvents(ctx context.Context, tx sqlQueryer, kind, target string) ([]GovernanceReceipt, error) {
	table, err := governanceTable(kind)
	if err != nil {
		return nil, err
	}
	rows, err := tx.query(ctx, `SELECT event_id,request_id,target_id,revision,coalesce(previous_event_id,''),recorded_at,body FROM `+table+` WHERE target_id=$1 ORDER BY revision LIMIT 10001`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GovernanceReceipt{}
	for rows.Next() {
		r, err := scanGovernanceReceipt(rows)
		if err != nil {
			return nil, err
		}
		if r.Revision != int64(len(out)+1) || (len(out) > 0 && r.PreviousEventID != out[len(out)-1].EventID) {
			return nil, fmt.Errorf("incomplete governance event chain")
		}
		out = append(out, r)
	}
	if len(out) > 10000 {
		return nil, fmt.Errorf("governance history exceeds limit")
	}
	return out, rows.Err()
}

// ReadGovernanceHistory is a complete bounded history, never a current-use permit.
func ReadGovernanceHistory(ctx context.Context, pool *pgxpool.Pool, kind, target string) ([]GovernanceReceipt, error) {
	if pool == nil || strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("pool and target required")
	}
	var out []GovernanceReceipt
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
			return err
		}
		var err error
		out, err = loadGovernanceEvents(ctx, tx, kind, target)
		return err
	})
	return out, err
}

func governanceProviderRevision(s SourceSnapshot) string {
	if s.SourceSystem == SourceSystemExternalDocument {
		return s.OriginMetadata["external_revision"]
	}
	return s.SourceVersion
}

func normalizeGovernanceRequest(r GovernanceRequest) GovernanceRequest {
	if r.CorrectionIDs == nil {
		r.CorrectionIDs = []string{}
	}
	if r.Checks == nil {
		r.Checks = []evidenceuse.SourceCheck{}
	}
	if r.MaterialIDs == nil {
		r.MaterialIDs = []string{}
	}
	if r.ResolvesEventIDs == nil {
		r.ResolvesEventIDs = []string{}
	}
	if r.Limitations == nil {
		r.Limitations = []string{}
	}
	r.Checks = slices.Clone(r.Checks)
	for i := range r.Checks {
		if r.Checks[i].AssessedField == "" {
			r.Checks[i].AssessedField = governanceAssessedField(r.Checks[i].Criterion)
		}
		if r.Checks[i].Limitations == nil {
			r.Checks[i].Limitations = []string{}
		}
	}
	return r
}

// Field names identify the original assessed dimension. Corrections annotate
// those declarations; they never overwrite the envelope or confer a pass.
func governanceAssessedField(criterion string) string {
	switch criterion {
	case evidenceuse.RetentionV1:
		return "retained_bytes"
	case evidenceuse.CorrespondenceV1:
		return "content_fidelity"
	case evidenceuse.CoverageV1:
		return "coverage"
	case "statement-support/v1":
		return "statement_support"
	default:
		return ""
	}
}
