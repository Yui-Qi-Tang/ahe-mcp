package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubmitCanonicalContradictionProposal persists one human-reviewable
// contradiction proposal over two already-admitted canonical nodes.
func SubmitCanonicalContradictionProposal(
	ctx context.Context,
	pool *pgxpool.Pool,
	input CanonicalContradictionProposalInput,
) (CanonicalContradictionProposalResult, error) {
	if pool == nil {
		return CanonicalContradictionProposalResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return submitCanonicalContradictionProposal(ctx, pgxDB{pool: pool}, input)
}

// GetCanonicalContradictionProposal returns one durable relation proposal with
// both grounded canonical endpoints and its optional terminal decision.
func GetCanonicalContradictionProposal(
	ctx context.Context,
	pool *pgxpool.Pool,
	proposalID string,
) (CanonicalContradictionQueryResult, error) {
	if pool == nil {
		return CanonicalContradictionQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return getCanonicalContradictionProposal(ctx, pgxDB{pool: pool}, proposalID)
}

// AdmitPendingCanonicalContradiction atomically admits one reviewed pending
// proposal and writes its symmetric canonical contradicts edge.
func AdmitPendingCanonicalContradiction(
	ctx context.Context,
	pool *pgxpool.Pool,
	input CanonicalContradictionAdmissionInput,
) (CanonicalContradictionDecisionResult, error) {
	if pool == nil {
		return CanonicalContradictionDecisionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return admitPendingCanonicalContradiction(ctx, pgxDB{pool: pool}, input)
}

// RecordPendingCanonicalContradictionDisposition records a rejected or
// audit-only terminal decision without creating a canonical edge.
func RecordPendingCanonicalContradictionDisposition(
	ctx context.Context,
	pool *pgxpool.Pool,
	input CanonicalContradictionDispositionInput,
) (CanonicalContradictionDecisionResult, error) {
	if pool == nil {
		return CanonicalContradictionDecisionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return recordPendingCanonicalContradictionDisposition(ctx, pgxDB{pool: pool}, input)
}

type preparedCanonicalContradictionProposal struct {
	input       CanonicalContradictionProposalInput
	proposalID  string
	fingerprint string
	payloadHash string
}

func submitCanonicalContradictionProposal(
	ctx context.Context,
	db sqlDB,
	input CanonicalContradictionProposalInput,
) (CanonicalContradictionProposalResult, error) {
	prepared, err := prepareCanonicalContradictionProposal(input)
	if err != nil {
		return CanonicalContradictionProposalResult{}, err
	}

	proposal := CanonicalContradictionProposal{
		ID:                  prepared.proposalID,
		RequestID:           prepared.input.RequestID,
		RequestPayloadHash:  prepared.payloadHash,
		ProposalFingerprint: prepared.fingerprint,
		NodeAID:             prepared.input.NodeAID,
		NodeBID:             prepared.input.NodeBID,
		Relation:            evidencegraph.CanonicalContradicts,
		Rationale:           prepared.input.Rationale,
		ProducerName:        prepared.input.ProducerName,
		ProducerVersion:     prepared.input.ProducerVersion,
		ProducerSessionRef:  prepared.input.ProducerSessionRef,
		AdmissionOutcome:    admissionOutcomePending,
	}
	result := CanonicalContradictionProposalResult{Proposal: proposal}
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := requireCanonicalContradictionEndpoints(ctx, tx, proposal.NodeAID, proposal.NodeBID); err != nil {
			return err
		}
		tag, err := tx.exec(ctx, `
			INSERT INTO canonical_contradiction_proposals (
				canonical_contradiction_proposal_id,
				request_id,
				request_payload_hash,
				proposal_fingerprint,
				node_a_id,
				node_b_id,
				relation,
				rationale,
				producer_name,
				producer_version,
				producer_session_ref,
				admission_outcome
			)
			VALUES ($1, $2, $3, $4, $5, $6, 'contradicts', $7, $8, $9, NULLIF($10, ''), 'pending')
			ON CONFLICT DO NOTHING
		`,
			proposal.ID,
			proposal.RequestID,
			proposal.RequestPayloadHash,
			proposal.ProposalFingerprint,
			proposal.NodeAID,
			proposal.NodeBID,
			proposal.Rationale,
			proposal.ProducerName,
			proposal.ProducerVersion,
			proposal.ProducerSessionRef,
		)
		if err != nil {
			return fmt.Errorf("inserting canonical contradiction proposal: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		existing, _, err := loadCanonicalContradictionProposalByIdentity(
			ctx,
			tx,
			proposal.RequestID,
			proposal.NodeAID,
			proposal.NodeBID,
			false,
		)
		if err != nil {
			return err
		}
		if existing.RequestPayloadHash != proposal.RequestPayloadHash {
			return newDomainError(
				ErrorOccurrenceConflict,
				"canonical contradiction proposal for request %q or nodes %s/%s already exists with different content",
				proposal.RequestID,
				proposal.NodeAID,
				proposal.NodeBID,
			)
		}
		result.Proposal = existing
		result.Replayed = true
		return nil
	})
	if err != nil {
		return CanonicalContradictionProposalResult{}, err
	}
	return result, nil
}

func prepareCanonicalContradictionProposal(
	input CanonicalContradictionProposalInput,
) (preparedCanonicalContradictionProposal, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.NodeAID = strings.TrimSpace(input.NodeAID)
	input.NodeBID = strings.TrimSpace(input.NodeBID)
	input.Rationale = strings.TrimSpace(input.Rationale)
	input.ProducerName = strings.TrimSpace(input.ProducerName)
	input.ProducerVersion = strings.TrimSpace(input.ProducerVersion)
	input.ProducerSessionRef = strings.TrimSpace(input.ProducerSessionRef)

	if err := validateCanonicalContradictionText("request_id", input.RequestID, CanonicalContradictionRequestIDMaxBytes, true); err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	if err := validateCanonicalContradictionNodeID("node_a_id", input.NodeAID); err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	if err := validateCanonicalContradictionNodeID("node_b_id", input.NodeBID); err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	if input.NodeAID == input.NodeBID {
		return preparedCanonicalContradictionProposal{}, newDomainError(ErrorInvalidInput, "a canonical node cannot contradict itself")
	}
	if input.NodeBID < input.NodeAID {
		input.NodeAID, input.NodeBID = input.NodeBID, input.NodeAID
	}
	if err := validateCanonicalContradictionText("rationale", input.Rationale, CanonicalContradictionRationaleMaxBytes, true); err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	if err := validateCanonicalContradictionText("producer_name", input.ProducerName, ExtractorDefinitionNameMaxBytes, true); err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	if err := validateCanonicalContradictionText("producer_version", input.ProducerVersion, ExtractorDefinitionVersionMaxBytes, true); err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	if err := validateCanonicalContradictionText("producer_session_ref", input.ProducerSessionRef, ProducerSessionRefMaxBytes, false); err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}

	identity := struct {
		FingerprintVersion string `json:"fingerprint_version"`
		NodeAID            string `json:"node_a_id"`
		NodeBID            string `json:"node_b_id"`
		Relation           string `json:"relation"`
	}{
		FingerprintVersion: CanonicalContradictionProposalFingerprintV1,
		NodeAID:            input.NodeAID,
		NodeBID:            input.NodeBID,
		Relation:           string(evidencegraph.CanonicalContradicts),
	}
	proposalID, err := stableID(CanonicalContradictionProposalIDPrefix, "canonical_contradiction_proposal", identity)
	if err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	fingerprintFields := struct {
		Identity        any    `json:"identity"`
		Rationale       string `json:"rationale"`
		ProducerName    string `json:"producer_name"`
		ProducerVersion string `json:"producer_version"`
	}{
		Identity:        identity,
		Rationale:       input.Rationale,
		ProducerName:    input.ProducerName,
		ProducerVersion: input.ProducerVersion,
	}
	fingerprint, err := stableID("contradiction-fp:", "canonical_contradiction_proposal_fingerprint", fingerprintFields)
	if err != nil {
		return preparedCanonicalContradictionProposal{}, err
	}
	payloadData, err := deterministicJSON(struct {
		FingerprintFields  any    `json:"proposal"`
		ProducerSessionRef string `json:"producer_session_ref,omitempty"`
	}{
		FingerprintFields:  fingerprintFields,
		ProducerSessionRef: input.ProducerSessionRef,
	})
	if err != nil {
		return preparedCanonicalContradictionProposal{}, fmt.Errorf("serializing canonical contradiction request: %w", err)
	}
	return preparedCanonicalContradictionProposal{
		input:       input,
		proposalID:  proposalID,
		fingerprint: fingerprint,
		payloadHash: contentHash(payloadData),
	}, nil
}

func getCanonicalContradictionProposal(
	ctx context.Context,
	db sqlDB,
	proposalID string,
) (CanonicalContradictionQueryResult, error) {
	proposalID = strings.TrimSpace(proposalID)
	if err := validateCanonicalContradictionProposalID(proposalID); err != nil {
		return CanonicalContradictionQueryResult{}, err
	}
	var result CanonicalContradictionQueryResult
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		var err error
		result, err = hydrateCanonicalContradictionProposal(ctx, tx, proposalID)
		return err
	})
	if err != nil {
		return CanonicalContradictionQueryResult{}, err
	}
	return result, nil
}

func hydrateCanonicalContradictionProposal(
	ctx context.Context,
	db sqlQueryer,
	proposalID string,
) (CanonicalContradictionQueryResult, error) {
	proposal, decision, err := loadCanonicalContradictionProposalByID(ctx, db, proposalID, false)
	if err != nil {
		return CanonicalContradictionQueryResult{}, err
	}
	nodeA, err := getCanonicalEvidenceByID(ctx, db, proposal.NodeAID)
	if err != nil {
		return CanonicalContradictionQueryResult{}, err
	}
	nodeB, err := getCanonicalEvidenceByID(ctx, db, proposal.NodeBID)
	if err != nil {
		return CanonicalContradictionQueryResult{}, err
	}
	return CanonicalContradictionQueryResult{
		Proposal: proposal,
		NodeA:    nodeA,
		NodeB:    nodeB,
		Decision: decision,
	}, nil
}

func admitPendingCanonicalContradiction(
	ctx context.Context,
	db sqlDB,
	input CanonicalContradictionAdmissionInput,
) (CanonicalContradictionDecisionResult, error) {
	input.ProposalID = strings.TrimSpace(input.ProposalID)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	if err := validateCanonicalContradictionDecisionInput(input.ProposalID, input.DecisionBy, input.DecisionReason); err != nil {
		return CanonicalContradictionDecisionResult{}, err
	}

	var result CanonicalContradictionDecisionResult
	err := withTx(ctx, db, func(tx sqlTx) error {
		proposal, decision, err := loadCanonicalContradictionProposalByID(ctx, tx, input.ProposalID, true)
		if err != nil {
			return err
		}
		switch proposal.AdmissionOutcome {
		case admissionOutcomePending:
		case admissionOutcomeAdmitted:
			if decision == nil || decision.Outcome != admissionOutcomeAdmitted || decision.CanonicalEdgeID == "" {
				return newDomainError(ErrorAdmissionStateConflict, "canonical contradiction proposal %s has inconsistent admitted state", proposal.ID)
			}
			if decision.DecisionBy != input.DecisionBy || decision.DecisionReason != input.DecisionReason {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"canonical contradiction proposal %s is already admitted with different audit metadata",
					proposal.ID,
				)
			}
			result = CanonicalContradictionDecisionResult{Decision: *decision, Replayed: true}
			return nil
		default:
			return newDomainError(ErrorAdmissionStateConflict, "canonical contradiction proposal %s outcome is %s", proposal.ID, proposal.AdmissionOutcome)
		}
		if err := requireCanonicalContradictionEndpoints(ctx, tx, proposal.NodeAID, proposal.NodeBID); err != nil {
			return err
		}
		decisionID, err := canonicalContradictionDecisionID(proposal.ID, admissionOutcomeAdmitted)
		if err != nil {
			return err
		}
		edge := buildCanonicalContradictionEdge(proposal, decisionID)
		if err := insertCanonicalContradictionEdge(ctx, tx, edge); err != nil {
			return err
		}
		decision = &CanonicalContradictionDecision{
			ID:              decisionID,
			ProposalID:      proposal.ID,
			Outcome:         admissionOutcomeAdmitted,
			CanonicalEdgeID: edge.ID,
			DecisionBy:      input.DecisionBy,
			DecisionReason:  input.DecisionReason,
		}
		if err := insertCanonicalContradictionDecision(ctx, tx, *decision); err != nil {
			return err
		}
		if err := markCanonicalContradictionProposalDecided(ctx, tx, proposal.ID, admissionOutcomeAdmitted, edge.ID); err != nil {
			return err
		}
		result = CanonicalContradictionDecisionResult{Decision: *decision}
		return nil
	})
	if err != nil {
		return CanonicalContradictionDecisionResult{}, err
	}
	return result, nil
}

func recordPendingCanonicalContradictionDisposition(
	ctx context.Context,
	db sqlDB,
	input CanonicalContradictionDispositionInput,
) (CanonicalContradictionDecisionResult, error) {
	input.ProposalID = strings.TrimSpace(input.ProposalID)
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	if err := validateCanonicalContradictionDecisionInput(input.ProposalID, input.DecisionBy, input.DecisionReason); err != nil {
		return CanonicalContradictionDecisionResult{}, err
	}
	switch input.Outcome {
	case admissionOutcomeRejected, admissionOutcomeAuditOnly:
	default:
		return CanonicalContradictionDecisionResult{}, newDomainError(ErrorInvalidInput, "outcome must be %s or %s", ProposalDispositionRejected, ProposalDispositionAuditOnly)
	}

	var result CanonicalContradictionDecisionResult
	err := withTx(ctx, db, func(tx sqlTx) error {
		proposal, decision, err := loadCanonicalContradictionProposalByID(ctx, tx, input.ProposalID, true)
		if err != nil {
			return err
		}
		switch proposal.AdmissionOutcome {
		case admissionOutcomePending:
		case input.Outcome:
			if decision == nil || decision.Outcome != input.Outcome || decision.CanonicalEdgeID != "" {
				return newDomainError(ErrorAdmissionStateConflict, "canonical contradiction proposal %s has inconsistent %s state", proposal.ID, input.Outcome)
			}
			if decision.DecisionBy != input.DecisionBy || decision.DecisionReason != input.DecisionReason {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"canonical contradiction proposal %s already has a %s disposition with different audit metadata",
					proposal.ID,
					input.Outcome,
				)
			}
			result = CanonicalContradictionDecisionResult{Decision: *decision, Replayed: true}
			return nil
		default:
			return newDomainError(ErrorAdmissionStateConflict, "canonical contradiction proposal %s outcome is %s", proposal.ID, proposal.AdmissionOutcome)
		}
		decisionID, err := canonicalContradictionDecisionID(proposal.ID, input.Outcome)
		if err != nil {
			return err
		}
		decision = &CanonicalContradictionDecision{
			ID:             decisionID,
			ProposalID:     proposal.ID,
			Outcome:        input.Outcome,
			DecisionBy:     input.DecisionBy,
			DecisionReason: input.DecisionReason,
		}
		if err := insertCanonicalContradictionDecision(ctx, tx, *decision); err != nil {
			return err
		}
		if err := markCanonicalContradictionProposalDecided(ctx, tx, proposal.ID, input.Outcome, ""); err != nil {
			return err
		}
		result = CanonicalContradictionDecisionResult{Decision: *decision}
		return nil
	})
	if err != nil {
		return CanonicalContradictionDecisionResult{}, err
	}
	return result, nil
}

func validateCanonicalContradictionText(name, value string, maxBytes int, required bool) error {
	if value == "" {
		if required {
			return newDomainError(ErrorInvalidInput, "%s is required", name)
		}
		return nil
	}
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return newDomainError(ErrorInvalidInput, "%s must contain at most %d bytes of valid UTF-8", name, maxBytes)
	}
	return nil
}

func validateCanonicalContradictionNodeID(name, id string) error {
	if !strings.HasPrefix(id, "canon-node:") {
		return newDomainError(ErrorInvalidRecordID, "%s %q must start with canon-node:", name, id)
	}
	return nil
}

func validateCanonicalContradictionProposalID(id string) error {
	if !strings.HasPrefix(id, CanonicalContradictionProposalIDPrefix) {
		return newDomainError(ErrorInvalidRecordID, "canonical_contradiction_proposal_id %q must start with %s", id, CanonicalContradictionProposalIDPrefix)
	}
	return nil
}

func validateCanonicalContradictionDecisionInput(proposalID, decisionBy, decisionReason string) error {
	if err := validateCanonicalContradictionProposalID(proposalID); err != nil {
		return err
	}
	if err := validateCanonicalContradictionText("decision_by", decisionBy, ProposalDispositionDecisionByMaxBytes, true); err != nil {
		return err
	}
	return validateCanonicalContradictionText("decision_reason", decisionReason, ProposalDispositionDecisionReasonMaxBytes, true)
}

func requireCanonicalContradictionEndpoints(ctx context.Context, tx sqlTx, nodeAID, nodeBID string) error {
	rows, err := tx.query(ctx, `
		SELECT canonical_node_id
		FROM canonical_graph_nodes
		WHERE canonical_node_id = ANY($1::text[])
		FOR KEY SHARE
	`, []string{nodeAID, nodeBID})
	if err != nil {
		return fmt.Errorf("loading canonical contradiction endpoints: %w", err)
	}
	defer rows.Close()
	found := make(map[string]struct{}, 2)
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return fmt.Errorf("scanning canonical contradiction endpoint: %w", err)
		}
		found[nodeID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating canonical contradiction endpoints: %w", err)
	}
	for _, nodeID := range []string{nodeAID, nodeBID} {
		if _, ok := found[nodeID]; !ok {
			return newDomainError(ErrorMissingSourceViewAttempt, "canonical contradiction endpoint %s is not an admitted canonical node", nodeID)
		}
	}
	return nil
}

func loadCanonicalContradictionProposalByID(
	ctx context.Context,
	db sqlQueryer,
	proposalID string,
	forUpdate bool,
) (CanonicalContradictionProposal, *CanonicalContradictionDecision, error) {
	if forUpdate {
		// Lock in a separate statement so a waiter gets a fresh READ COMMITTED
		// snapshot when it subsequently joins the terminal decision. A joined
		// SELECT FOR UPDATE can otherwise observe the updated proposal row while
		// retaining a pre-wait snapshot that cannot see the inserted decision.
		var lockedProposalID string
		err := db.queryRow(ctx, `
			SELECT canonical_contradiction_proposal_id
			FROM canonical_contradiction_proposals
			WHERE canonical_contradiction_proposal_id = $1
			FOR UPDATE
		`, proposalID).Scan(&lockedProposalID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return CanonicalContradictionProposal{}, nil, newDomainError(ErrorMissingSourceViewAttempt, "canonical contradiction proposal not found")
			}
			return CanonicalContradictionProposal{}, nil, fmt.Errorf("locking canonical contradiction proposal: %w", err)
		}
	}
	query := canonicalContradictionProposalSelect + ` WHERE p.canonical_contradiction_proposal_id = $1`
	return scanCanonicalContradictionProposal(db.queryRow(ctx, query, proposalID))
}

func loadCanonicalContradictionProposalByIdentity(
	ctx context.Context,
	db sqlQueryer,
	requestID, nodeAID, nodeBID string,
	forUpdate bool,
) (CanonicalContradictionProposal, *CanonicalContradictionDecision, error) {
	query := canonicalContradictionProposalSelect + `
		WHERE p.request_id = $1
			OR (p.node_a_id = $2 AND p.node_b_id = $3 AND p.relation = 'contradicts')
		ORDER BY (p.request_id = $1) DESC
		LIMIT 1
	`
	if forUpdate {
		query += ` FOR UPDATE OF p`
	}
	return scanCanonicalContradictionProposal(db.queryRow(ctx, query, requestID, nodeAID, nodeBID))
}

const canonicalContradictionProposalSelect = `
	SELECT
		p.canonical_contradiction_proposal_id,
		p.request_id,
		p.request_payload_hash,
		p.proposal_fingerprint,
		p.node_a_id,
		p.node_b_id,
		p.relation,
		p.rationale,
		p.producer_name,
		p.producer_version,
		COALESCE(p.producer_session_ref, ''),
		p.admission_outcome,
		COALESCE(p.canonical_edge_id, ''),
		COALESCE(d.canonical_contradiction_admission_decision_id, ''),
		COALESCE(d.outcome, ''),
		COALESCE(d.canonical_edge_id, ''),
		COALESCE(d.decision_by, ''),
		COALESCE(d.decision_reason, '')
	FROM canonical_contradiction_proposals p
	LEFT JOIN canonical_contradiction_admission_decisions d
		ON d.canonical_contradiction_proposal_id = p.canonical_contradiction_proposal_id
`

func scanCanonicalContradictionProposal(
	row sqlRow,
) (CanonicalContradictionProposal, *CanonicalContradictionDecision, error) {
	var proposal CanonicalContradictionProposal
	var relation string
	var decision CanonicalContradictionDecision
	err := row.Scan(
		&proposal.ID,
		&proposal.RequestID,
		&proposal.RequestPayloadHash,
		&proposal.ProposalFingerprint,
		&proposal.NodeAID,
		&proposal.NodeBID,
		&relation,
		&proposal.Rationale,
		&proposal.ProducerName,
		&proposal.ProducerVersion,
		&proposal.ProducerSessionRef,
		&proposal.AdmissionOutcome,
		&proposal.CanonicalEdgeID,
		&decision.ID,
		&decision.Outcome,
		&decision.CanonicalEdgeID,
		&decision.DecisionBy,
		&decision.DecisionReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CanonicalContradictionProposal{}, nil, newDomainError(ErrorMissingSourceViewAttempt, "canonical contradiction proposal not found")
		}
		return CanonicalContradictionProposal{}, nil, fmt.Errorf("querying canonical contradiction proposal: %w", err)
	}
	proposal.Relation = evidencegraph.CanonicalEdgeRelation(relation)
	if decision.ID == "" {
		return proposal, nil, nil
	}
	decision.ProposalID = proposal.ID
	return proposal, &decision, nil
}

func canonicalContradictionDecisionID(proposalID, outcome string) (string, error) {
	return stableID("contradiction-adm:", "canonical_contradiction_admission_decision", struct {
		ProposalID string `json:"proposal_id"`
		Outcome    string `json:"outcome"`
	}{ProposalID: proposalID, Outcome: outcome})
}

func buildCanonicalContradictionEdge(
	proposal CanonicalContradictionProposal,
	decisionID string,
) CanonicalGraphEdge {
	edgeID := evidencegraph.StableCanonicalID(
		"canon-edge",
		proposal.NodeAID,
		proposal.NodeBID,
		string(evidencegraph.CanonicalContradicts),
	)
	return CanonicalGraphEdge{
		ID:       edgeID,
		From:     proposal.NodeAID,
		To:       proposal.NodeBID,
		Relation: evidencegraph.CanonicalContradicts,
		Provenance: evidencegraph.ProvenanceRecord{
			ID:            evidencegraph.StableCanonicalID("provenance", edgeID),
			OriginRefs:    []string{proposal.NodeAID, proposal.NodeBID},
			OriginGroupID: evidencegraph.StableCanonicalID("origin-group", proposal.NodeAID, proposal.NodeBID),
			Producer:      proposal.ProducerName,
			Method:        "human_admitted_canonical_contradiction",
			MethodVersion: "v1",
			TraceRef:      proposal.ID,
			ReviewRef:     decisionID,
		},
		OriginContradictionProposalID: proposal.ID,
	}
}

func insertCanonicalContradictionEdge(ctx context.Context, tx sqlTx, edge CanonicalGraphEdge) error {
	provenance, err := jsonBytes(edge.Provenance)
	if err != nil {
		return err
	}
	tag, err := tx.exec(ctx, `
		INSERT INTO canonical_graph_edges (
			canonical_edge_id,
			from_node_id,
			to_node_id,
			relation,
			provenance,
			origin_proposal_occurrence_id,
			origin_canonical_contradiction_proposal_id
		)
		VALUES ($1, $2, $3, 'contradicts', $4::jsonb, NULL, $5)
		ON CONFLICT DO NOTHING
	`, edge.ID, edge.From, edge.To, string(provenance), edge.OriginContradictionProposalID)
	if err != nil {
		return fmt.Errorf("inserting canonical contradiction edge %s: %w", edge.ID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var originProposalID string
	err = tx.queryRow(ctx, `
		SELECT COALESCE(origin_canonical_contradiction_proposal_id, '')
		FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
	`, edge.ID).Scan(&originProposalID)
	if err != nil {
		return fmt.Errorf("reading existing canonical contradiction edge %s: %w", edge.ID, err)
	}
	if originProposalID != edge.OriginContradictionProposalID {
		return newDomainError(ErrorAdmissionStateConflict, "canonical contradiction edge %s already exists with different provenance", edge.ID)
	}
	return nil
}

func insertCanonicalContradictionDecision(
	ctx context.Context,
	tx sqlTx,
	decision CanonicalContradictionDecision,
) error {
	_, err := tx.exec(ctx, `
		INSERT INTO canonical_contradiction_admission_decisions (
			canonical_contradiction_admission_decision_id,
			canonical_contradiction_proposal_id,
			outcome,
			canonical_edge_id,
			decision_by,
			decision_reason
		)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6)
	`,
		decision.ID,
		decision.ProposalID,
		decision.Outcome,
		decision.CanonicalEdgeID,
		decision.DecisionBy,
		decision.DecisionReason,
	)
	if err != nil {
		return fmt.Errorf("inserting canonical contradiction decision: %w", err)
	}
	return nil
}

func markCanonicalContradictionProposalDecided(
	ctx context.Context,
	tx sqlTx,
	proposalID, outcome, canonicalEdgeID string,
) error {
	tag, err := tx.exec(ctx, `
		UPDATE canonical_contradiction_proposals
		SET admission_outcome = $2,
			canonical_edge_id = NULLIF($3, ''),
			decided_at = now()
		WHERE canonical_contradiction_proposal_id = $1
			AND admission_outcome = 'pending'
	`, proposalID, outcome, canonicalEdgeID)
	if err != nil {
		return fmt.Errorf("marking canonical contradiction proposal decided: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorAdmissionStateConflict, "canonical contradiction proposal %s is no longer pending", proposalID)
	}
	return nil
}
