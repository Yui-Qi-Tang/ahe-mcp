package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubmitCanonicalSupersessionProposal persists one human-reviewable directed
// proposal over two already-admitted canonical nodes. The caller supplies the
// semantics; AHE validates shape, identity, and admitted endpoints only.
func SubmitCanonicalSupersessionProposal(
	ctx context.Context,
	pool *pgxpool.Pool,
	input CanonicalSupersessionProposalInput,
) (CanonicalSupersessionProposalResult, error) {
	if pool == nil {
		return CanonicalSupersessionProposalResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return submitCanonicalSupersessionProposal(ctx, pgxDB{pool: pool}, input)
}

// GetCanonicalSupersessionProposal returns one durable relation proposal with
// both grounded canonical endpoints and its optional terminal decision.
func GetCanonicalSupersessionProposal(
	ctx context.Context,
	pool *pgxpool.Pool,
	proposalID string,
) (CanonicalSupersessionQueryResult, error) {
	if pool == nil {
		return CanonicalSupersessionQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return getCanonicalSupersessionProposal(ctx, pgxDB{pool: pool}, proposalID)
}

// AdmitPendingCanonicalSupersession atomically admits one reviewed pending
// proposal as from-current to-replaced. It serializes supersession admissions
// and rejects additions that would create a directed cycle.
func AdmitPendingCanonicalSupersession(
	ctx context.Context,
	pool *pgxpool.Pool,
	input CanonicalSupersessionAdmissionInput,
) (CanonicalSupersessionDecisionResult, error) {
	if pool == nil {
		return CanonicalSupersessionDecisionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return admitPendingCanonicalSupersession(ctx, pgxDB{pool: pool}, input)
}

// RecordPendingCanonicalSupersessionDisposition records a rejected or
// audit-only terminal decision without creating a canonical edge.
func RecordPendingCanonicalSupersessionDisposition(
	ctx context.Context,
	pool *pgxpool.Pool,
	input CanonicalSupersessionDispositionInput,
) (CanonicalSupersessionDecisionResult, error) {
	if pool == nil {
		return CanonicalSupersessionDecisionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return recordPendingCanonicalSupersessionDisposition(ctx, pgxDB{pool: pool}, input)
}

type preparedCanonicalSupersessionProposal struct {
	input       CanonicalSupersessionProposalInput
	proposalID  string
	fingerprint string
	payloadHash string
}

func submitCanonicalSupersessionProposal(
	ctx context.Context,
	db sqlDB,
	input CanonicalSupersessionProposalInput,
) (CanonicalSupersessionProposalResult, error) {
	prepared, err := prepareCanonicalSupersessionProposal(input)
	if err != nil {
		return CanonicalSupersessionProposalResult{}, err
	}
	proposal := CanonicalSupersessionProposal{
		ID:                  prepared.proposalID,
		RequestID:           prepared.input.RequestID,
		RequestPayloadHash:  prepared.payloadHash,
		ProposalFingerprint: prepared.fingerprint,
		FromNodeID:          prepared.input.FromNodeID,
		ToNodeID:            prepared.input.ToNodeID,
		Relation:            evidencegraph.CanonicalSupersedes,
		ProposalSentence:    prepared.input.ProposalSentence,
		Rationale:           prepared.input.Rationale,
		VersionDifference:   prepared.input.VersionDifference,
		Limitations:         append([]string(nil), prepared.input.Limitations...),
		ProducerName:        prepared.input.ProducerName,
		ProducerVersion:     prepared.input.ProducerVersion,
		ProducerSessionRef:  prepared.input.ProducerSessionRef,
		AdmissionOutcome:    admissionOutcomePending,
	}
	limitations, err := jsonBytes(proposal.Limitations)
	if err != nil {
		return CanonicalSupersessionProposalResult{}, err
	}
	result := CanonicalSupersessionProposalResult{Proposal: proposal}
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := requireCanonicalSupersessionEndpoints(ctx, tx, proposal.FromNodeID, proposal.ToNodeID); err != nil {
			return err
		}
		tag, err := tx.exec(ctx, `
			INSERT INTO canonical_supersession_proposals (
				canonical_supersession_proposal_id,
				request_id,
				request_payload_hash,
				proposal_fingerprint,
				from_node_id,
				to_node_id,
				relation,
				proposal_sentence,
				rationale,
				version_difference,
				limitations,
				producer_name,
				producer_version,
				producer_session_ref,
				admission_outcome
			)
			VALUES ($1, $2, $3, $4, $5, $6, 'supersedes', $7, $8, $9, $10::jsonb, $11, $12, NULLIF($13, ''), 'pending')
			ON CONFLICT DO NOTHING
		`,
			proposal.ID,
			proposal.RequestID,
			proposal.RequestPayloadHash,
			proposal.ProposalFingerprint,
			proposal.FromNodeID,
			proposal.ToNodeID,
			proposal.ProposalSentence,
			proposal.Rationale,
			proposal.VersionDifference,
			string(limitations),
			proposal.ProducerName,
			proposal.ProducerVersion,
			proposal.ProducerSessionRef,
		)
		if err != nil {
			return fmt.Errorf("inserting canonical supersession proposal: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		existing, _, err := loadCanonicalSupersessionProposalByIdentity(
			ctx,
			tx,
			proposal.RequestID,
			proposal.FromNodeID,
			proposal.ToNodeID,
			false,
		)
		if err != nil {
			return err
		}
		if existing.RequestPayloadHash != proposal.RequestPayloadHash {
			return newDomainError(
				ErrorOccurrenceConflict,
				"canonical supersession proposal for request %q or directed nodes %s -> %s already exists with different content",
				proposal.RequestID,
				proposal.FromNodeID,
				proposal.ToNodeID,
			)
		}
		result.Proposal = existing
		result.Replayed = true
		return nil
	})
	if err != nil {
		return CanonicalSupersessionProposalResult{}, err
	}
	return result, nil
}

func prepareCanonicalSupersessionProposal(
	input CanonicalSupersessionProposalInput,
) (preparedCanonicalSupersessionProposal, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.FromNodeID = strings.TrimSpace(input.FromNodeID)
	input.ToNodeID = strings.TrimSpace(input.ToNodeID)
	input.ProposalSentence = strings.TrimSpace(input.ProposalSentence)
	input.Rationale = strings.TrimSpace(input.Rationale)
	input.VersionDifference = strings.TrimSpace(input.VersionDifference)
	input.ProducerName = strings.TrimSpace(input.ProducerName)
	input.ProducerVersion = strings.TrimSpace(input.ProducerVersion)
	input.ProducerSessionRef = strings.TrimSpace(input.ProducerSessionRef)
	input.Limitations = append([]string(nil), input.Limitations...)
	for index := range input.Limitations {
		input.Limitations[index] = strings.TrimSpace(input.Limitations[index])
	}

	if err := validateCanonicalSupersessionText("request_id", input.RequestID, CanonicalSupersessionRequestIDMaxBytes, true); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if err := validateCanonicalSupersessionNodeID("from_node_id", input.FromNodeID); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if err := validateCanonicalSupersessionNodeID("to_node_id", input.ToNodeID); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if input.FromNodeID == input.ToNodeID {
		return preparedCanonicalSupersessionProposal{}, newDomainError(ErrorInvalidInput, "a canonical node cannot supersede itself")
	}
	if err := validateCanonicalSupersessionText("proposal_sentence", input.ProposalSentence, CanonicalSupersessionProposalSentenceMaxBytes, true); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if err := validateCanonicalSupersessionText("rationale", input.Rationale, CanonicalSupersessionRationaleMaxBytes, true); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if err := validateCanonicalSupersessionText("version_difference", input.VersionDifference, CanonicalSupersessionVersionDifferenceMaxBytes, true); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if len(input.Limitations) > CanonicalSupersessionLimitationsMaxEntries {
		return preparedCanonicalSupersessionProposal{}, newDomainError(
			ErrorInvalidInput,
			"limitations contains %d entries, maximum is %d",
			len(input.Limitations),
			CanonicalSupersessionLimitationsMaxEntries,
		)
	}
	seenLimitations := make(map[string]struct{}, len(input.Limitations))
	for index, limitation := range input.Limitations {
		if err := validateCanonicalSupersessionText(
			fmt.Sprintf("limitations[%d]", index),
			limitation,
			CanonicalSupersessionLimitationMaxBytes,
			true,
		); err != nil {
			return preparedCanonicalSupersessionProposal{}, err
		}
		if _, exists := seenLimitations[limitation]; exists {
			return preparedCanonicalSupersessionProposal{}, newDomainError(ErrorInvalidInput, "limitations contains duplicate entry %q", limitation)
		}
		seenLimitations[limitation] = struct{}{}
	}
	if err := validateCanonicalSupersessionText("producer_name", input.ProducerName, ExtractorDefinitionNameMaxBytes, true); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if err := validateCanonicalSupersessionText("producer_version", input.ProducerVersion, ExtractorDefinitionVersionMaxBytes, true); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	if err := validateCanonicalSupersessionText("producer_session_ref", input.ProducerSessionRef, ProducerSessionRefMaxBytes, false); err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}

	identity := struct {
		FingerprintVersion string `json:"fingerprint_version"`
		FromNodeID         string `json:"from_node_id"`
		ToNodeID           string `json:"to_node_id"`
		Relation           string `json:"relation"`
	}{
		FingerprintVersion: CanonicalSupersessionProposalFingerprintV1,
		FromNodeID:         input.FromNodeID,
		ToNodeID:           input.ToNodeID,
		Relation:           string(evidencegraph.CanonicalSupersedes),
	}
	proposalID, err := stableID(CanonicalSupersessionProposalIDPrefix, "canonical_supersession_proposal", identity)
	if err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	fingerprintFields := struct {
		Identity          any      `json:"identity"`
		ProposalSentence  string   `json:"proposal_sentence"`
		Rationale         string   `json:"rationale"`
		VersionDifference string   `json:"version_difference"`
		Limitations       []string `json:"limitations"`
		ProducerName      string   `json:"producer_name"`
		ProducerVersion   string   `json:"producer_version"`
	}{
		Identity:          identity,
		ProposalSentence:  input.ProposalSentence,
		Rationale:         input.Rationale,
		VersionDifference: input.VersionDifference,
		Limitations:       input.Limitations,
		ProducerName:      input.ProducerName,
		ProducerVersion:   input.ProducerVersion,
	}
	fingerprint, err := stableID("supersession-fp:", "canonical_supersession_proposal_fingerprint", fingerprintFields)
	if err != nil {
		return preparedCanonicalSupersessionProposal{}, err
	}
	payloadData, err := deterministicJSON(struct {
		FingerprintFields  any    `json:"proposal"`
		ProducerSessionRef string `json:"producer_session_ref,omitempty"`
	}{
		FingerprintFields:  fingerprintFields,
		ProducerSessionRef: input.ProducerSessionRef,
	})
	if err != nil {
		return preparedCanonicalSupersessionProposal{}, fmt.Errorf("serializing canonical supersession request: %w", err)
	}
	return preparedCanonicalSupersessionProposal{
		input:       input,
		proposalID:  proposalID,
		fingerprint: fingerprint,
		payloadHash: contentHash(payloadData),
	}, nil
}

func getCanonicalSupersessionProposal(
	ctx context.Context,
	db sqlDB,
	proposalID string,
) (CanonicalSupersessionQueryResult, error) {
	proposalID = strings.TrimSpace(proposalID)
	if err := validateCanonicalSupersessionProposalID(proposalID); err != nil {
		return CanonicalSupersessionQueryResult{}, err
	}
	var result CanonicalSupersessionQueryResult
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		var err error
		result, err = hydrateCanonicalSupersessionProposal(ctx, tx, proposalID)
		return err
	})
	if err != nil {
		return CanonicalSupersessionQueryResult{}, err
	}
	return result, nil
}

func hydrateCanonicalSupersessionProposal(
	ctx context.Context,
	db sqlQueryer,
	proposalID string,
) (CanonicalSupersessionQueryResult, error) {
	proposal, decision, err := loadCanonicalSupersessionProposalByID(ctx, db, proposalID, false)
	if err != nil {
		return CanonicalSupersessionQueryResult{}, err
	}
	from, err := getCanonicalEvidenceByID(ctx, db, proposal.FromNodeID)
	if err != nil {
		return CanonicalSupersessionQueryResult{}, err
	}
	to, err := getCanonicalEvidenceByID(ctx, db, proposal.ToNodeID)
	if err != nil {
		return CanonicalSupersessionQueryResult{}, err
	}
	return CanonicalSupersessionQueryResult{
		Proposal: proposal,
		From:     from,
		To:       to,
		Decision: decision,
	}, nil
}

func admitPendingCanonicalSupersession(
	ctx context.Context,
	db sqlDB,
	input CanonicalSupersessionAdmissionInput,
) (CanonicalSupersessionDecisionResult, error) {
	input.ProposalID = strings.TrimSpace(input.ProposalID)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	if err := validateCanonicalSupersessionDecisionInput(input.ProposalID, input.DecisionBy, input.DecisionReason); err != nil {
		return CanonicalSupersessionDecisionResult{}, err
	}

	var result CanonicalSupersessionDecisionResult
	err := withTx(ctx, db, func(tx sqlTx) error {
		proposal, decision, err := loadCanonicalSupersessionProposalByID(ctx, tx, input.ProposalID, true)
		if err != nil {
			return err
		}
		switch proposal.AdmissionOutcome {
		case admissionOutcomePending:
		case admissionOutcomeAdmitted:
			if decision == nil || decision.Outcome != admissionOutcomeAdmitted || decision.CanonicalEdgeID == "" {
				return newDomainError(ErrorAdmissionStateConflict, "canonical supersession proposal %s has inconsistent admitted state", proposal.ID)
			}
			if decision.DecisionBy != input.DecisionBy || decision.DecisionReason != input.DecisionReason {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"canonical supersession proposal %s is already admitted with different audit metadata",
					proposal.ID,
				)
			}
			result = CanonicalSupersessionDecisionResult{Decision: *decision, Replayed: true}
			return nil
		default:
			return newDomainError(ErrorAdmissionStateConflict, "canonical supersession proposal %s outcome is %s", proposal.ID, proposal.AdmissionOutcome)
		}
		if err := validateCanonicalSupersessionAdmissionInvariant(ctx, tx, proposal); err != nil {
			return err
		}
		decisionID, err := canonicalSupersessionDecisionID(proposal.ID, admissionOutcomeAdmitted)
		if err != nil {
			return err
		}
		edge := buildCanonicalSupersessionEdge(proposal, decisionID)
		if err := insertCanonicalSupersessionEdge(ctx, tx, edge); err != nil {
			return err
		}
		decision = &CanonicalSupersessionDecision{
			ID:              decisionID,
			ProposalID:      proposal.ID,
			Outcome:         admissionOutcomeAdmitted,
			CanonicalEdgeID: edge.ID,
			DecisionBy:      input.DecisionBy,
			DecisionReason:  input.DecisionReason,
		}
		if err := insertCanonicalSupersessionDecision(ctx, tx, *decision); err != nil {
			return err
		}
		if err := markCanonicalSupersessionProposalDecided(ctx, tx, proposal.ID, admissionOutcomeAdmitted, edge.ID); err != nil {
			return err
		}
		result = CanonicalSupersessionDecisionResult{Decision: *decision}
		return nil
	})
	if err != nil {
		return CanonicalSupersessionDecisionResult{}, err
	}
	return result, nil
}

func recordPendingCanonicalSupersessionDisposition(
	ctx context.Context,
	db sqlDB,
	input CanonicalSupersessionDispositionInput,
) (CanonicalSupersessionDecisionResult, error) {
	input.ProposalID = strings.TrimSpace(input.ProposalID)
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	if err := validateCanonicalSupersessionDecisionInput(input.ProposalID, input.DecisionBy, input.DecisionReason); err != nil {
		return CanonicalSupersessionDecisionResult{}, err
	}
	switch input.Outcome {
	case admissionOutcomeRejected, admissionOutcomeAuditOnly:
	default:
		return CanonicalSupersessionDecisionResult{}, newDomainError(ErrorInvalidInput, "outcome must be %s or %s", ProposalDispositionRejected, ProposalDispositionAuditOnly)
	}

	var result CanonicalSupersessionDecisionResult
	err := withTx(ctx, db, func(tx sqlTx) error {
		proposal, decision, err := loadCanonicalSupersessionProposalByID(ctx, tx, input.ProposalID, true)
		if err != nil {
			return err
		}
		switch proposal.AdmissionOutcome {
		case admissionOutcomePending:
		case input.Outcome:
			if decision == nil || decision.Outcome != input.Outcome || decision.CanonicalEdgeID != "" {
				return newDomainError(ErrorAdmissionStateConflict, "canonical supersession proposal %s has inconsistent %s state", proposal.ID, input.Outcome)
			}
			if decision.DecisionBy != input.DecisionBy || decision.DecisionReason != input.DecisionReason {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"canonical supersession proposal %s already has a %s disposition with different audit metadata",
					proposal.ID,
					input.Outcome,
				)
			}
			result = CanonicalSupersessionDecisionResult{Decision: *decision, Replayed: true}
			return nil
		default:
			return newDomainError(ErrorAdmissionStateConflict, "canonical supersession proposal %s outcome is %s", proposal.ID, proposal.AdmissionOutcome)
		}
		decisionID, err := canonicalSupersessionDecisionID(proposal.ID, input.Outcome)
		if err != nil {
			return err
		}
		decision = &CanonicalSupersessionDecision{
			ID:             decisionID,
			ProposalID:     proposal.ID,
			Outcome:        input.Outcome,
			DecisionBy:     input.DecisionBy,
			DecisionReason: input.DecisionReason,
		}
		if err := insertCanonicalSupersessionDecision(ctx, tx, *decision); err != nil {
			return err
		}
		if err := markCanonicalSupersessionProposalDecided(ctx, tx, proposal.ID, input.Outcome, ""); err != nil {
			return err
		}
		result = CanonicalSupersessionDecisionResult{Decision: *decision}
		return nil
	})
	if err != nil {
		return CanonicalSupersessionDecisionResult{}, err
	}
	return result, nil
}

func validateCanonicalSupersessionText(name, value string, maxBytes int, required bool) error {
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

func validateCanonicalSupersessionNodeID(name, id string) error {
	if !strings.HasPrefix(id, "canon-node:") {
		return newDomainError(ErrorInvalidRecordID, "%s %q must start with canon-node:", name, id)
	}
	return nil
}

func validateCanonicalSupersessionProposalID(id string) error {
	if !strings.HasPrefix(id, CanonicalSupersessionProposalIDPrefix) {
		return newDomainError(ErrorInvalidRecordID, "canonical_supersession_proposal_id %q must start with %s", id, CanonicalSupersessionProposalIDPrefix)
	}
	return nil
}

func validateCanonicalSupersessionDecisionInput(proposalID, decisionBy, decisionReason string) error {
	if err := validateCanonicalSupersessionProposalID(proposalID); err != nil {
		return err
	}
	if err := validateCanonicalSupersessionText("decision_by", decisionBy, ProposalDispositionDecisionByMaxBytes, true); err != nil {
		return err
	}
	return validateCanonicalSupersessionText("decision_reason", decisionReason, ProposalDispositionDecisionReasonMaxBytes, true)
}

func requireCanonicalSupersessionEndpoints(ctx context.Context, tx sqlTx, fromNodeID, toNodeID string) error {
	rows, err := tx.query(ctx, `
		SELECT canonical_node_id
		FROM canonical_graph_nodes
		WHERE canonical_node_id = ANY($1::text[])
		FOR KEY SHARE
	`, []string{fromNodeID, toNodeID})
	if err != nil {
		return fmt.Errorf("loading canonical supersession endpoints: %w", err)
	}
	defer rows.Close()
	found := make(map[string]struct{}, 2)
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return fmt.Errorf("scanning canonical supersession endpoint: %w", err)
		}
		found[nodeID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating canonical supersession endpoints: %w", err)
	}
	for _, nodeID := range []string{fromNodeID, toNodeID} {
		if _, ok := found[nodeID]; !ok {
			return newDomainError(ErrorMissingSourceViewAttempt, "canonical supersession endpoint %s is not an admitted canonical node", nodeID)
		}
	}
	return nil
}

const supersessionAdmissionLockKey int64 = 6008756669556170053

func validateCanonicalSupersessionAdmissionInvariant(
	ctx context.Context,
	tx sqlTx,
	proposal CanonicalSupersessionProposal,
) error {
	if proposal.Relation != evidencegraph.CanonicalSupersedes || proposal.FromNodeID == proposal.ToNodeID {
		return newDomainError(ErrorSupersessionInvariant, "canonical supersession proposal %s has invalid directed shape", proposal.ID)
	}
	if _, err := tx.exec(ctx, `SELECT pg_advisory_xact_lock($1)`, supersessionAdmissionLockKey); err != nil {
		return fmt.Errorf("locking canonical supersession admission: %w", err)
	}
	if err := requireCanonicalSupersessionEndpoints(ctx, tx, proposal.FromNodeID, proposal.ToNodeID); err != nil {
		return err
	}

	var witness []string
	err := tx.queryRow(ctx, `
		WITH RECURSIVE successors(node_id, path) AS (
			SELECT $1::text, ARRAY[$1::text]
			UNION ALL
			SELECT edge.to_node_id, successors.path || edge.to_node_id
			FROM successors
			JOIN canonical_graph_edges edge
				ON edge.from_node_id = successors.node_id
				AND edge.relation = 'supersedes'
			WHERE NOT edge.to_node_id = ANY(successors.path)
		)
		SELECT path
		FROM successors
		WHERE node_id = $2
			AND cardinality(path) > 1
		ORDER BY cardinality(path), path::text
		LIMIT 1
	`, proposal.ToNodeID, proposal.FromNodeID).Scan(&witness)
	if err == nil {
		cycle := append([]string{proposal.FromNodeID}, witness...)
		return newDomainError(ErrorSupersessionInvariant, "canonical supersession would create a cycle: %s", strings.Join(cycle, " -> "))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("checking canonical supersession cycle: %w", err)
	}
	return nil
}

func loadCanonicalSupersessionProposalByID(
	ctx context.Context,
	db sqlQueryer,
	proposalID string,
	forUpdate bool,
) (CanonicalSupersessionProposal, *CanonicalSupersessionDecision, error) {
	query := canonicalSupersessionProposalSelect + ` WHERE p.canonical_supersession_proposal_id = $1`
	if forUpdate {
		query += ` FOR UPDATE OF p`
	}
	return scanCanonicalSupersessionProposal(db.queryRow(ctx, query, proposalID))
}

func loadCanonicalSupersessionProposalByIdentity(
	ctx context.Context,
	db sqlQueryer,
	requestID, fromNodeID, toNodeID string,
	forUpdate bool,
) (CanonicalSupersessionProposal, *CanonicalSupersessionDecision, error) {
	query := canonicalSupersessionProposalSelect + `
		WHERE p.request_id = $1
			OR (p.from_node_id = $2 AND p.to_node_id = $3 AND p.relation = 'supersedes')
		ORDER BY (p.request_id = $1) DESC
		LIMIT 1
	`
	if forUpdate {
		query += ` FOR UPDATE OF p`
	}
	return scanCanonicalSupersessionProposal(db.queryRow(ctx, query, requestID, fromNodeID, toNodeID))
}

const canonicalSupersessionProposalSelect = `
	SELECT
		p.canonical_supersession_proposal_id,
		p.request_id,
		p.request_payload_hash,
		p.proposal_fingerprint,
		p.from_node_id,
		p.to_node_id,
		p.relation,
		p.proposal_sentence,
		p.rationale,
		p.version_difference,
		p.limitations,
		p.producer_name,
		p.producer_version,
		COALESCE(p.producer_session_ref, ''),
		p.admission_outcome,
		COALESCE(p.canonical_edge_id, ''),
		COALESCE(d.canonical_supersession_admission_decision_id, ''),
		COALESCE(d.outcome, ''),
		COALESCE(d.canonical_edge_id, ''),
		COALESCE(d.decision_by, ''),
		COALESCE(d.decision_reason, '')
	FROM canonical_supersession_proposals p
	LEFT JOIN canonical_supersession_admission_decisions d
		ON d.canonical_supersession_proposal_id = p.canonical_supersession_proposal_id
`

func scanCanonicalSupersessionProposal(
	row sqlRow,
) (CanonicalSupersessionProposal, *CanonicalSupersessionDecision, error) {
	var proposal CanonicalSupersessionProposal
	var relation string
	var limitationsData []byte
	var decision CanonicalSupersessionDecision
	err := row.Scan(
		&proposal.ID,
		&proposal.RequestID,
		&proposal.RequestPayloadHash,
		&proposal.ProposalFingerprint,
		&proposal.FromNodeID,
		&proposal.ToNodeID,
		&relation,
		&proposal.ProposalSentence,
		&proposal.Rationale,
		&proposal.VersionDifference,
		&limitationsData,
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
			return CanonicalSupersessionProposal{}, nil, newDomainError(ErrorMissingSourceViewAttempt, "canonical supersession proposal not found")
		}
		return CanonicalSupersessionProposal{}, nil, fmt.Errorf("querying canonical supersession proposal: %w", err)
	}
	proposal.Relation = evidencegraph.CanonicalEdgeRelation(relation)
	if err := json.Unmarshal(limitationsData, &proposal.Limitations); err != nil {
		return CanonicalSupersessionProposal{}, nil, fmt.Errorf("decoding canonical supersession limitations: %w", err)
	}
	if decision.ID == "" {
		return proposal, nil, nil
	}
	decision.ProposalID = proposal.ID
	return proposal, &decision, nil
}

func canonicalSupersessionDecisionID(proposalID, outcome string) (string, error) {
	return stableID("supersession-adm:", "canonical_supersession_admission_decision", struct {
		ProposalID string `json:"proposal_id"`
		Outcome    string `json:"outcome"`
	}{ProposalID: proposalID, Outcome: outcome})
}

func buildCanonicalSupersessionEdge(
	proposal CanonicalSupersessionProposal,
	decisionID string,
) CanonicalGraphEdge {
	edgeID := evidencegraph.StableCanonicalID(
		"canon-edge",
		proposal.FromNodeID,
		proposal.ToNodeID,
		string(evidencegraph.CanonicalSupersedes),
	)
	return CanonicalGraphEdge{
		ID:       edgeID,
		From:     proposal.FromNodeID,
		To:       proposal.ToNodeID,
		Relation: evidencegraph.CanonicalSupersedes,
		Provenance: evidencegraph.ProvenanceRecord{
			ID:            evidencegraph.StableCanonicalID("provenance", edgeID),
			OriginRefs:    []string{proposal.FromNodeID, proposal.ToNodeID},
			OriginGroupID: evidencegraph.StableCanonicalID("origin-group", proposal.FromNodeID, proposal.ToNodeID, string(evidencegraph.CanonicalSupersedes)),
			Producer:      proposal.ProducerName,
			Method:        "human_admitted_canonical_supersession",
			MethodVersion: "v1",
			TraceRef:      proposal.ID,
			ReviewRef:     decisionID,
		},
		OriginSupersessionProposalID: proposal.ID,
	}
}

func insertCanonicalSupersessionEdge(ctx context.Context, tx sqlTx, edge CanonicalGraphEdge) error {
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
			origin_canonical_contradiction_proposal_id,
			origin_canonical_supersession_proposal_id
		)
		VALUES ($1, $2, $3, 'supersedes', $4::jsonb, NULL, NULL, $5)
		ON CONFLICT DO NOTHING
	`, edge.ID, edge.From, edge.To, string(provenance), edge.OriginSupersessionProposalID)
	if err != nil {
		return fmt.Errorf("inserting canonical supersession edge %s: %w", edge.ID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var originProposalID string
	err = tx.queryRow(ctx, `
		SELECT COALESCE(origin_canonical_supersession_proposal_id, '')
		FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
	`, edge.ID).Scan(&originProposalID)
	if err != nil {
		return fmt.Errorf("reading existing canonical supersession edge %s: %w", edge.ID, err)
	}
	if originProposalID != edge.OriginSupersessionProposalID {
		return newDomainError(ErrorAdmissionStateConflict, "canonical supersession edge %s already exists with different provenance", edge.ID)
	}
	return nil
}

func insertCanonicalSupersessionDecision(
	ctx context.Context,
	tx sqlTx,
	decision CanonicalSupersessionDecision,
) error {
	_, err := tx.exec(ctx, `
		INSERT INTO canonical_supersession_admission_decisions (
			canonical_supersession_admission_decision_id,
			canonical_supersession_proposal_id,
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
		return fmt.Errorf("inserting canonical supersession decision: %w", err)
	}
	return nil
}

func markCanonicalSupersessionProposalDecided(
	ctx context.Context,
	tx sqlTx,
	proposalID, outcome, canonicalEdgeID string,
) error {
	tag, err := tx.exec(ctx, `
		UPDATE canonical_supersession_proposals
		SET admission_outcome = $2,
			canonical_edge_id = NULLIF($3, ''),
			decided_at = now()
		WHERE canonical_supersession_proposal_id = $1
			AND admission_outcome = 'pending'
	`, proposalID, outcome, canonicalEdgeID)
	if err != nil {
		return fmt.Errorf("marking canonical supersession proposal decided: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorAdmissionStateConflict, "canonical supersession proposal %s is no longer pending", proposalID)
	}
	return nil
}
