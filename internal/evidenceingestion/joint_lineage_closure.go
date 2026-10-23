package evidenceingestion

import (
	"context"
	"fmt"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GetJointLineageCurrentness reads both authority families in one read-only
// repeatable-read cut. It accepts only a lineage key, not caller-supplied proof.
func GetJointLineageCurrentness(ctx context.Context, pool *pgxpool.Pool, lineageKey string) (evidencesupersession.JointCurrentnessProjection, error) {
	var out evidencesupersession.JointCurrentnessProjection
	if pool == nil {
		return out, fmt.Errorf("postgres pool required")
	}
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		cut, err := loadJointLineageCut(ctx, tx, lineageKey)
		if err != nil {
			return err
		}
		out, err = evidencesupersession.ProjectJointCurrentness(cut)
		return err
	})
	return out, err
}

// The owning snapshot has already set row_security=off before this lookup.
func hasInitialLineageRegistrations(ctx context.Context, tx sqlTx) (bool, error) {
	var exists bool
	err := tx.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM canonical_initial_lineage_registrations)`).Scan(&exists)
	return exists, err
}

func loadJointLineageCut(ctx context.Context, tx sqlTx, lineageKey string) (evidencesupersession.JointCutInput, error) {
	if !validCanonicalSupersessionLineageKey(lineageKey) {
		return evidencesupersession.JointCutInput{}, fmt.Errorf("canonical lineage key required")
	}
	authority, err := loadJointLineageAuthority(ctx, tx)
	if err != nil {
		return evidencesupersession.JointCutInput{}, err
	}
	lineage, err := loadJointLineageMaterialization(ctx, tx, lineageKey)
	if err != nil {
		return evidencesupersession.JointCutInput{}, err
	}
	return evidencesupersession.JointCutInput{ContractVersion: evidencesupersession.JointCutContractV1, StoreID: authority.StoreID, Head: authority.Head, Events: authority.Events, Registrations: authority.Registrations,
		LineageKey: lineage.LineageKey, Basis: lineage.Basis, Members: lineage.Members, Edges: lineage.Edges, ObjectClaims: lineage.ObjectClaims}, nil
}

// The unselected frame stays within its owning native transaction. A complete
// assessment loads expensive immutable authority joins once for all lineages.
func loadJointLineageAuthority(ctx context.Context, tx sqlTx) (evidencesupersession.JointAuthorityInput, error) {
	var cut evidencesupersession.JointAuthorityInput
	if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return cut, err
	}
	if err := tx.queryRow(ctx, `SELECT store_id FROM evidence_governance_head WHERE singleton`).Scan(&cut.StoreID); err != nil {
		return cut, err
	}
	head, err := loadCanonicalSupersessionHead(ctx, tx, false)
	if err != nil {
		return cut, err
	}
	cut.Head = evidencesupersession.ClosureHead{ChainKey: head.ChainKey, Revision: head.Revision, HeadEventID: head.HeadEventID}
	cut.Events, err = loadCanonicalSupersessionClosureEvents(ctx, tx, evidencesupersession.MaxClosureEvents)
	if err != nil {
		return cut, err
	}
	var count, size int64
	if err := tx.queryRow(ctx, `SELECT count(*),2+coalesce(sum(octet_length('{"event_id":"'||event_id||'","payload":'||body||'}')),0)+greatest(count(*)-1,0) FROM canonical_initial_lineage_registrations`).Scan(&count, &size); err != nil {
		return cut, err
	}
	if err := evidencesupersession.ValidateRegistrationCatalogCapacity(count, size); err != nil {
		return cut, err
	}
	rows, err := tx.query(ctx, `SELECT request_id FROM canonical_initial_lineage_registrations ORDER BY event_id LIMIT $1`, evidencesupersession.MaxRegistrationEvents+1)
	if err != nil {
		return cut, err
	}
	requests := make([]string, 0, count)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return cut, err
		}
		requests = append(requests, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return cut, err
	}
	if int64(len(requests)) != count {
		return cut, fmt.Errorf("registration catalog count differs inside cut")
	}
	cut.Registrations = make([]evidencesupersession.RegistrationRecord, 0, count)
	for _, id := range requests {
		receipt, err := readInitialLineageRegistration(ctx, tx, id)
		if err != nil {
			return cut, err
		}
		b := receipt.Record.Payload.Binding
		// Authenticate immutable evidence joins independently of present eligibility.
		var matches bool
		if err := tx.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM canonical_graph_nodes n JOIN admission_decisions d ON d.proposal_occurrence_id=n.origin_proposal_occurrence_id
    JOIN external_source_intake_receipts r ON r.request_id=$4
    WHERE n.canonical_node_id=$1 AND n.node_kind='source_claim' AND n.origin_proposal_occurrence_id=$2 AND d.admission_decision_id=$3 AND d.outcome='admitted' AND d.canonical_ref=n.canonical_node_id
    AND r.source_snapshot_id=$5 AND r.extraction_view_id=$6 AND r.receipt_payload_hash=$7)`, b.ClaimID, b.OriginalOccurrenceID, b.OriginalAdmissionID, b.ObservationReceiptID, b.SnapshotID, b.ViewID, b.ObservationHash).Scan(&matches); err != nil {
			return cut, err
		}
		if !matches {
			return cut, fmt.Errorf("registration original admission/observation authority is missing")
		}
		cut.Registrations = append(cut.Registrations, receipt.Record)
	}
	return cut, nil
}

func loadJointLineageMaterialization(ctx context.Context, tx sqlTx, lineageKey string) (evidencesupersession.JointLineageInput, error) {
	var cut evidencesupersession.JointLineageInput
	if !validCanonicalSupersessionLineageKey(lineageKey) {
		return cut, fmt.Errorf("canonical lineage key required")
	}
	basis, err := loadCanonicalSupersessionClosureLineage(ctx, tx, lineageKey)
	if err != nil {
		return cut, err
	}
	cut.LineageKey = lineageKey
	cut.Basis = basis
	rows, err := tx.query(ctx, `SELECT canonical_node_id,lineage_key,coalesce(first_admission_event_id,''),was_bootstrapped,coalesce(initial_registration_event_id,'') FROM canonical_supersession_members WHERE lineage_key=$1 ORDER BY canonical_node_id LIMIT $2`, lineageKey, evidencesupersession.MaxClosureMembers+1)
	if err != nil {
		return cut, err
	}
	for rows.Next() {
		var m evidencesupersession.JointMemberRecord
		var first, registration string
		var bootstrap bool
		if err := rows.Scan(&m.NodeID, &m.LineageKey, &first, &bootstrap, &registration); err != nil {
			rows.Close()
			return cut, err
		}
		if len(cut.Members) >= evidencesupersession.MaxClosureMembers {
			rows.Close()
			return cut, fmt.Errorf("%w: joint member limit", evidencesupersession.ErrClosureLimitExceeded)
		}
		switch {
		case registration != "" && first == "" && !bootstrap:
			m.Origin = evidencesupersession.MembershipOrigin{Kind: evidencesupersession.MembershipRegistration, EventID: registration}
		case registration == "" && first != "":
			kind := evidencesupersession.MembershipReplacement
			if bootstrap {
				kind = evidencesupersession.MembershipBootstrap
			}
			m.Origin = evidencesupersession.MembershipOrigin{Kind: kind, EventID: first}
		default:
			rows.Close()
			return cut, fmt.Errorf("ambiguous joint membership authority")
		}
		cut.Members = append(cut.Members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return cut, err
	}
	cut.Edges, err = loadCanonicalSupersessionClosureEdges(ctx, tx, lineageKey, evidencesupersession.MaxClosureEdges)
	if err != nil {
		return cut, err
	}
	cut.ObjectClaims, err = loadCanonicalSupersessionObjectClaims(ctx, tx, cut.Basis, evidencesupersession.MaxClosureObjectClaims)
	return cut, err
}
