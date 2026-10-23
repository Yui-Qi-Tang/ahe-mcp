package evidenceingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InitialLineageRegistrationRequest selects native evidence, never a claimed
// winner, completeness flag, authority payload or supersession anchor.
type InitialLineageRegistrationRequest struct {
	ClaimID              string                     `json:"claim_id"`
	ObservationReceiptID string                     `json:"observation_receipt_id"`
	Basis                evidencesupersession.Basis `json:"basis"`
}

// InitialRegistrationGrounding retains exact original evidence and the cut at review.
type InitialRegistrationGrounding struct {
	RelatedGovernance  json.RawMessage                     `json:"related_governance"`
	GovernanceRevision int64                               `json:"governance_revision"`
	Node               CanonicalQueryResult                `json:"node"`
	Source             ReviewableSourceClaimReviewSnapshot `json:"source"`
}

// InitialRegistrationObservation exposes saved receipt metadata without
// recomputing its hash from PostgreSQL-rounded timestamps.
type InitialRegistrationObservation struct {
	RequestID   string    `json:"request_id"`
	SnapshotID  string    `json:"snapshot_id"`
	ViewID      string    `json:"view_id"`
	CollectorID string    `json:"collector_id"`
	ConnectorID string    `json:"connector_id"`
	ObservedAt  time.Time `json:"observed_at"`
	ReceivedAt  time.Time `json:"received_at"`
	SavedHash   string    `json:"saved_hash"`
}

// InitialLineageRegistrationReview displays the precise first-membership effect.
type InitialLineageRegistrationReview struct {
	Request     InitialLineageRegistrationRequest              `json:"request"`
	Subject     string                                         `json:"subject"`
	Binding     evidencesupersession.RegistrationReviewBinding `json:"binding"`
	Grounding   InitialRegistrationGrounding                   `json:"grounding"`
	Observation InitialRegistrationObservation                 `json:"observation"`
	Limitations string                                         `json:"limitations"`
}

// InitialLineageRegistrationDecision is an independently authorized exact approval.
type InitialLineageRegistrationDecision struct {
	RequestID       string                            `json:"request_id"`
	Review          InitialLineageRegistrationRequest `json:"review"`
	ExpectedSubject string                            `json:"expected_subject"`
	Decision        string                            `json:"decision"`
	Reason          string                            `json:"reason"`
	ReviewerID      string                            `json:"-"`
}

// InitialLineageRegistrationReceipt records historical authority, never current use.
type InitialLineageRegistrationReceipt struct {
	Record    evidencesupersession.RegistrationRecord `json:"record"`
	Grounding InitialRegistrationGrounding            `json:"grounding"`
	Replayed  bool                                    `json:"replayed"`
}

// LoadInitialLineageRegistrationReview derives all authority within one read-only cut.
func LoadInitialLineageRegistrationReview(ctx context.Context, pool *pgxpool.Pool, req InitialLineageRegistrationRequest) (InitialLineageRegistrationReview, error) {
	if pool == nil {
		return InitialLineageRegistrationReview{}, fmt.Errorf("postgres pool required")
	}
	var out InitialLineageRegistrationReview
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		out, err = loadInitialLineageRegistrationReview(ctx, tx, req)
		return err
	})
	return out, err
}

func loadInitialLineageRegistrationReview(ctx context.Context, tx sqlTx, req InitialLineageRegistrationRequest) (InitialLineageRegistrationReview, error) {
	var out InitialLineageRegistrationReview
	if !validateEndpointText(req.ClaimID, 200) || !validateEndpointText(req.ObservationReceiptID, 200) {
		return out, fmt.Errorf("claim and new observation receipt required")
	}
	key, err := req.Basis.Key()
	if err != nil {
		return out, err
	}
	if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return out, err
	}
	node, err := getCanonicalEvidenceByID(ctx, tx, req.ClaimID)
	if err != nil {
		return out, err
	}
	p := node.OriginProposal
	if node.NodeKind != evidencegraph.CanonicalSourceClaim || p.SourceBindingKind != ProposalSourceBindingSourceSnapshot || p.AdmissionOutcome != admissionOutcomeAdmitted || p.CanonicalRef != req.ClaimID || p.SourceSystem != SourceSystemExternalDocument {
		return out, fmt.Errorf("original admitted external source claim required")
	}
	if err := compareSupersessionExternalOrigin(p.OriginMetadata, req.Basis, req.ClaimID); err != nil {
		return out, err
	}
	if err := validateCanonicalNodeFirstMaterializerAuthority(ctx, tx, req.ClaimID, node.OriginProposalOccurrenceID); err != nil {
		return out, err
	}
	if err := validateCanonicalSourceBasisIntake(ctx, tx, p); err != nil {
		return out, err
	}
	if err := replayCanonicalSourceBasisAdmission(ctx, tx, p, nil); err != nil {
		return out, err
	}
	if len(p.SourceRefs) == 0 {
		return out, fmt.Errorf("complete source grounding required")
	}
	for _, ref := range p.SourceRefs {
		if ref.ExtractionViewID != p.ExtractionViewID || ref.RepositorySnapshotID != "" || ref.FileSnapshotID != "" {
			return out, fmt.Errorf("registration v1 requires one source view")
		}
	}
	var exists bool
	if err := tx.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM canonical_supersession_members WHERE canonical_node_id=$1) OR EXISTS(SELECT 1 FROM canonical_supersession_lineages WHERE lineage_key=$2)`, req.ClaimID, key).Scan(&exists); err != nil {
		return out, err
	}
	if exists {
		return out, fmt.Errorf("initial registration requires unclassified claim and fresh lineage")
	}
	claims, err := loadCanonicalSupersessionObjectClaims(ctx, tx, req.Basis, evidencesupersession.MaxClosureObjectClaims)
	if err != nil {
		return out, err
	}
	if len(claims) != 1 || claims[0].NodeID != req.ClaimID || claims[0].LineageKey != "" {
		return out, fmt.Errorf("initial registration requires complete singleton unclassified object manifest")
	}
	var storeID string
	var governanceRevision int64
	if err := tx.queryRow(ctx, `SELECT store_id,revision FROM evidence_governance_head WHERE singleton`).Scan(&storeID, &governanceRevision); err != nil {
		return out, err
	}
	withdrawals, err := loadGovernanceEvents(ctx, tx, "withdrawal", req.ClaimID)
	if err != nil {
		return out, err
	}
	if len(withdrawals) > 0 && withdrawals[len(withdrawals)-1].Display.Request.Operation == "withdraw" {
		return out, fmt.Errorf("withdrawn claim cannot register")
	}
	if err := tx.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM canonical_graph_edges WHERE relation='supersedes' AND to_node_id=$1)`, req.ClaimID).Scan(&exists); err != nil {
		return out, err
	}
	if exists {
		return out, fmt.Errorf("superseded claim cannot register")
	}
	sourceState, err := loadGovernanceSourceState(ctx, tx, p.SourceSnapshotID)
	if err != nil {
		return out, err
	}
	stateBytes, err := json.Marshal(struct {
		StoreID    string              `json:"store_id"`
		Revision   int64               `json:"revision"`
		Withdrawal []GovernanceReceipt `json:"withdrawal"`
		Source     evidenceuse.Source  `json:"source"`
	}{storeID, governanceRevision, withdrawals, sourceState})
	if err != nil {
		return out, err
	}
	digest := governanceDigest("sha256:", stateBytes)
	var admissionID string
	if err := tx.queryRow(ctx, `SELECT admission_decision_id FROM admission_decisions WHERE proposal_occurrence_id=$1 AND canonical_ref=$2 AND outcome='admitted'`, node.OriginProposalOccurrenceID, req.ClaimID).Scan(&admissionID); err != nil {
		return out, err
	}
	observation := InitialRegistrationObservation{RequestID: req.ObservationReceiptID}
	var envelope string
	if err := tx.queryRow(ctx, `SELECT source_snapshot_id,extraction_view_id,collector_id,connector_id,observed_at,created_at,receipt_payload_hash,envelope_schema_version FROM external_source_intake_receipts WHERE request_id=$1`, req.ObservationReceiptID).Scan(&observation.SnapshotID, &observation.ViewID, &observation.CollectorID, &observation.ConnectorID, &observation.ObservedAt, &observation.ReceivedAt, &observation.SavedHash, &envelope); err != nil {
		return out, err
	}
	if observation.SnapshotID != p.SourceSnapshotID || observation.ViewID != p.ExtractionViewID || envelope != ExternalSourceEnvelopeSchemaV1 {
		return out, fmt.Errorf("selected persisted observation must match original snapshot/view")
	}
	if err := endpointSourcePreflight(ctx, tx, p.ExtractionAttemptID); err != nil {
		return out, err
	}
	source, err := loadSourceClaimReviewSnapshotForLifecycleInTx(ctx, tx, p.ExtractionAttemptID, node.OriginProposalOccurrenceID, req.ClaimID)
	if err != nil {
		return out, err
	}
	grounding := InitialRegistrationGrounding{Node: node, Source: source, RelatedGovernance: stateBytes}
	if err := tx.queryRow(ctx, `SELECT revision FROM evidence_governance_head WHERE singleton`).Scan(&grounding.GovernanceRevision); err != nil {
		return out, err
	}
	raw, err := json.Marshal(grounding)
	if err != nil {
		return out, err
	}
	if len(raw) > endpointReviewMaxBytes {
		return out, fmt.Errorf("initial registration grounding exceeds one MiB")
	}
	head, err := loadCanonicalSupersessionHead(ctx, tx, false)
	if err != nil {
		return out, err
	}
	anchor := evidencesupersession.RegistrationAnchor{ChainKey: head.ChainKey, Revision: head.Revision}
	if head.Revision > 0 {
		id := head.HeadEventID
		anchor.HeadEventID = &id
	}
	manifest, err := evidencesupersession.RegistrationCreationManifestHash(req.Basis, req.ClaimID)
	if err != nil {
		return out, err
	}
	binding := evidencesupersession.RegistrationReviewBinding{StoreID: storeID, ClaimID: req.ClaimID, OriginalAdmissionID: admissionID, OriginalOccurrenceID: node.OriginProposalOccurrenceID,
		SnapshotID: p.SourceSnapshotID, ViewID: p.ExtractionViewID, GroundingHash: governanceDigest("sha256:", raw), ObservationReceiptID: req.ObservationReceiptID, ObservationHash: observation.SavedHash,
		Basis: req.Basis, LineageKey: key, Anchor: anchor, CreationObjectManifestHash: manifest, GovernanceDigest: digest}
	subject, err := binding.SubjectID()
	if err != nil {
		return out, err
	}
	return InitialLineageRegistrationReview{Request: req, Subject: subject, Binding: binding, Grounding: grounding, Observation: observation,
		Limitations: "The independent reviewer must establish a new observation from collector materials; receipt IDs or timestamps alone do not establish recontact. Independent approval grants first lineage membership and may make this claim current and change existing derived eligibility after full reassessment. Claim/source/admission remain unchanged. Receipt timestamps do not prove provider freshness. Withdrawal does not undo membership. No relocation or deregistration in v1."}, nil
}

// ApplyInitialLineageRegistration writes only an independent registration
// command. A migration-owned trigger materializes its lineage and membership.
func ApplyInitialLineageRegistration(ctx context.Context, pool *pgxpool.Pool, in InitialLineageRegistrationDecision) (InitialLineageRegistrationReceipt, error) {
	var out InitialLineageRegistrationReceipt
	if pool == nil || in.Decision != "approved" || !validateEndpointText(in.RequestID, 200) || !validateEndpointText(in.ReviewerID, 200) || !validateEndpointText(in.Reason, 2000) || !validateEndpointText(in.ExpectedSubject, 200) {
		return out, fmt.Errorf("independent exact approval, reviewer, reason and request required")
	}
	err := runReviewedSourceClaimAdmissionAttempts(func() error {
		return withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
				return err
			}
			if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
				return err
			}
			if err := lockGovernance(ctx, tx); err != nil {
				return err
			}
			receipt, err := readInitialLineageRegistration(ctx, tx, in.RequestID)
			if err == nil {
				p := receipt.Record.Payload
				b := p.Binding
				if p.ReviewSubjectID != in.ExpectedSubject || p.Reviewer != in.ReviewerID || p.Reason != in.Reason || b.ClaimID != in.Review.ClaimID || b.ObservationReceiptID != in.Review.ObservationReceiptID || b.Basis != in.Review.Basis {
					return fmt.Errorf("registration request reused with changed decision")
				}
				receipt.Replayed = true
				out = receipt
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			review, err := loadInitialLineageRegistrationReview(ctx, tx, in.Review)
			if err != nil {
				return err
			}
			if review.Subject != in.ExpectedSubject {
				return fmt.Errorf("initial registration review changed; obtain a new exact review")
			}
			payload := evidencesupersession.InitialRegistrationPayload{ContractVersion: evidencesupersession.InitialRegistrationContractV1, Binding: review.Binding, ReviewSubjectID: review.Subject, RequestID: in.RequestID, Reviewer: in.ReviewerID, Reason: in.Reason}
			id, err := payload.ID()
			if err != nil {
				return err
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			binding, err := json.Marshal(struct {
				Contract string                                         `json:"contract_version"`
				Binding  evidencesupersession.RegistrationReviewBinding `json:"binding"`
			}{payload.ContractVersion, payload.Binding})
			if err != nil {
				return err
			}
			grounding, err := json.Marshal(review.Grounding)
			if err != nil {
				return err
			}
			if _, err := tx.exec(ctx, `INSERT INTO canonical_initial_lineage_registrations(event_id,request_id,claim_id,lineage_key,body,binding_body,grounding_body) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, in.RequestID, in.Review.ClaimID, review.Binding.LineageKey, string(body), string(binding), string(grounding)); err != nil {
				return err
			}
			out, err = readInitialLineageRegistration(ctx, tx, in.RequestID)
			return err
		})
	})
	return out, err
}

func readInitialLineageRegistration(ctx context.Context, tx sqlTx, requestID string) (InitialLineageRegistrationReceipt, error) {
	var out InitialLineageRegistrationReceipt
	var body, binding, grounding, claim, lineage string
	if err := tx.queryRow(ctx, `SELECT event_id,body,binding_body,grounding_body,claim_id,lineage_key FROM canonical_initial_lineage_registrations WHERE request_id=$1`, requestID).Scan(&out.Record.ID, &body, &binding, &grounding, &claim, &lineage); err != nil {
		return out, err
	}
	if err := decodeInitialRegistration(body, &out.Record.Payload); err != nil {
		return out, err
	}
	want, err := out.Record.Payload.ID()
	if err != nil {
		return out, err
	}
	p := out.Record.Payload
	if out.Record.ID != want || p.RequestID != requestID || p.Binding.ClaimID != claim || p.Binding.LineageKey != lineage || p.ReviewSubjectID != governanceDigest("initial-lineage-review:v1:sha256:", []byte(binding)) || p.Binding.GroundingHash != governanceDigest("sha256:", []byte(grounding)) {
		return out, fmt.Errorf("initial registration authority mirror mismatch")
	}
	var bound struct {
		Contract string                                         `json:"contract_version"`
		Binding  evidencesupersession.RegistrationReviewBinding `json:"binding"`
	}
	if err := decodeInitialRegistration(binding, &bound); err != nil {
		return out, err
	}
	if bound.Contract != p.ContractVersion || !reflect.DeepEqual(bound.Binding, p.Binding) {
		return out, fmt.Errorf("initial registration binding body mismatch")
	}
	if err := decodeInitialRegistration(grounding, &out.Grounding); err != nil {
		return out, err
	}
	if governanceDigest("sha256:", out.Grounding.RelatedGovernance) != p.Binding.GovernanceDigest {
		return out, fmt.Errorf("registration related governance binding mismatch")
	}
	// Rebuild immutable original evidence, not current eligibility or the original
	// singleton condition. Later withdrawal and new object claims remain valid history.
	node, err := getCanonicalEvidenceByID(ctx, tx, p.Binding.ClaimID)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(node, out.Grounding.Node) {
		return out, fmt.Errorf("registration original canonical grounding changed")
	}
	if err := validateCanonicalNodeFirstMaterializerAuthority(ctx, tx, node.CanonicalID, node.OriginProposalOccurrenceID); err != nil {
		return out, err
	}
	if err := validateCanonicalSourceBasisIntake(ctx, tx, node.OriginProposal); err != nil {
		return out, err
	}
	if err := replayCanonicalSourceBasisAdmission(ctx, tx, node.OriginProposal, nil); err != nil {
		return out, err
	}
	source, err := loadSourceClaimReviewSnapshotForLifecycleInTx(ctx, tx, node.OriginProposal.ExtractionAttemptID, node.OriginProposalOccurrenceID, node.CanonicalID)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(source, out.Grounding.Source) {
		return out, fmt.Errorf("registration original source review authority changed")
	}
	return out, nil
}

func decodeInitialRegistration(raw string, value any) error {
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing initial registration JSON")
	}
	return nil
}
