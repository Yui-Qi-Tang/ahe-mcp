package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const supersessionAdmissionSerializationAttempts = 3

// AdmitPendingSupersession admits one fresh source-backed claim and its exact
// outgoing new-to-old supersedes edge set in one PostgreSQL transaction.
func AdmitPendingSupersession(
	ctx context.Context,
	pool *pgxpool.Pool,
	input SupersessionAdmissionInput,
) (SupersessionAdmissionResult, error) {
	if pool == nil {
		return SupersessionAdmissionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return admitPendingSupersession(ctx, pgxDB{pool: pool}, input)
}

// GetCanonicalSupersessionHead returns the current writer compare-and-swap
// coordinate. It is an admission history coordinate, not proof of closure or
// currentness.
func GetCanonicalSupersessionHead(
	ctx context.Context,
	pool *pgxpool.Pool,
) (CanonicalSupersessionHead, error) {
	if pool == nil {
		return CanonicalSupersessionHead{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return loadCanonicalSupersessionHead(ctx, pgxDB{pool: pool}, false)
}

func admitPendingSupersession(
	ctx context.Context,
	db sqlDB,
	input SupersessionAdmissionInput,
) (SupersessionAdmissionResult, error) {
	normalized, basis, err := normalizeSupersessionAdmissionInput(input)
	if err != nil {
		return SupersessionAdmissionResult{}, err
	}
	input = normalized

	var result SupersessionAdmissionResult
	err = runSupersessionAdmissionAttempts(func() error {
		result = SupersessionAdmissionResult{}
		return withTx(ctx, db, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
				return fmt.Errorf("configuring supersession admission transaction: %w", err)
			}
			proposal, err := loadProposalForAdmission(ctx, tx, input.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			switch proposal.AdmissionOutcome {
			case admissionOutcomePending:
			case admissionOutcomeAdmitted:
				replay, err := replaySupersessionAdmission(ctx, tx, proposal, input, basis)
				if err != nil {
					return err
				}
				result = replay
				return nil
			case admissionOutcomeRejected, admissionOutcomeAuditOnly:
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s outcome is %s",
					proposal.ProposalOccurrenceID,
					proposal.AdmissionOutcome,
				)
			default:
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s has unknown admission outcome %q",
					proposal.ProposalOccurrenceID,
					proposal.AdmissionOutcome,
				)
			}

			if proposal.ExtractionAttemptStatus != attemptStatusSucceeded {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s attempt status is %s",
					proposal.ProposalOccurrenceID,
					proposal.ExtractionAttemptStatus,
				)
			}
			if proposal.ProposalKind != ProposalKindStatement {
				return newDomainError(
					ErrorUnsupportedAdmission,
					"proposal kind %q is not supported by supersession admission",
					proposal.ProposalKind,
				)
			}
			if err := validateProposalSupersessionBasis(proposal, basis); err != nil {
				return err
			}

			mutation, err := buildCanonicalAdmissionMutation(proposal, AdmissionInput{
				ProposalOccurrenceID: proposal.ProposalOccurrenceID,
				DecisionBy:           input.DecisionBy,
				DecisionReason:       input.DecisionReason,
			})
			if err != nil {
				return err
			}
			if len(mutation.nodes) == 0 || mutation.nodes[0].Kind != evidencegraph.CanonicalSourceClaim {
				return newDomainError(ErrorSupersessionInvariant, "replacement admission did not build a source_claim")
			}
			replacementNode := mutation.nodes[0]
			request, err := evidencesupersession.NewRequestPayloadV2(
				proposal.ProposalOccurrenceID,
				replacementNode.ID,
				basis,
				input.TargetNodeIDs,
				input.ExpectedRevision,
				input.ExpectedHeadEventID,
			)
			if err != nil {
				return supersessionInputError(err)
			}
			decisionPayload, err := evidencesupersession.NewDecisionPayloadV2(
				proposal.ProposalOccurrenceID,
				replacementNode.ID,
				input.DecisionBy,
				input.DecisionReason,
			)
			if err != nil {
				return supersessionInputError(err)
			}

			head, err := loadCanonicalSupersessionHead(ctx, tx, true)
			if err != nil {
				return err
			}
			if head.Revision != input.ExpectedRevision || head.HeadEventID != input.ExpectedHeadEventID {
				return newDomainError(
					ErrorSupersessionHeadConflict,
					"supersession head is revision=%d event=%q, expected revision=%d event=%q",
					head.Revision,
					head.HeadEventID,
					input.ExpectedRevision,
					input.ExpectedHeadEventID,
				)
			}
			if err := requireFreshSupersessionReplacement(ctx, tx, replacementNode.ID); err != nil {
				return err
			}
			if err := ensureSupersessionLineage(ctx, tx, request.LineageKey, basis); err != nil {
				return err
			}
			targets, err := loadSupersessionTargetsForUpdate(ctx, tx, input.TargetNodeIDs)
			if err != nil {
				return err
			}
			bootstrappedTargetIDs, err := validateSupersessionTargets(targets, input.TargetNodeIDs, request.LineageKey, basis)
			if err != nil {
				return err
			}

			event, err := evidencesupersession.NewAtomicReplacementEventV2(
				request,
				decisionPayload,
				mutation.decision.ID,
				bootstrappedTargetIDs,
			)
			if err != nil {
				return fmt.Errorf("building supersession admission event: %w", err)
			}
			eventID, err := event.ID()
			if err != nil {
				return fmt.Errorf("identifying supersession admission event: %w", err)
			}

			mutation.nodes[0].Provenance.ReviewRef = mutation.decision.ID
			for index := range mutation.edges {
				mutation.edges[index].Provenance.ReviewRef = mutation.decision.ID
			}
			supersedesEdges := buildSupersedesEdges(proposal, replacementNode.ID, input.TargetNodeIDs, mutation.decision.ID, eventID)
			supersedesEdgeIDs := make([]string, 0, len(supersedesEdges))
			for _, edge := range supersedesEdges {
				supersedesEdgeIDs = append(supersedesEdgeIDs, edge.ID)
			}
			mutation.edges = append(mutation.edges, supersedesEdges...)
			mutation.result.CanonicalEdgeIDs = append(mutation.result.CanonicalEdgeIDs, supersedesEdgeIDs...)
			mutation.decision.CanonicalEdgeIDs = append(mutation.decision.CanonicalEdgeIDs, supersedesEdgeIDs...)

			if err := insertSupersessionCanonicalMutation(ctx, tx, mutation, len(supersedesEdges)); err != nil {
				return err
			}
			if err := insertAdmissionDecision(ctx, tx, mutation.decision, admissionDecisionMetadata{
				DecisionBy:     input.DecisionBy,
				DecisionReason: input.DecisionReason,
			}); err != nil {
				return err
			}
			if err := insertSupersessionAdmissionEvent(ctx, tx, eventID, event); err != nil {
				return err
			}
			if err := insertSupersessionMember(ctx, tx, replacementNode.ID, request.LineageKey, eventID, false); err != nil {
				return err
			}
			for _, targetID := range bootstrappedTargetIDs {
				if err := insertSupersessionMember(ctx, tx, targetID, request.LineageKey, eventID, true); err != nil {
					return err
				}
			}
			if err := insertSupersessionReplacementTargets(
				ctx,
				tx,
				eventID,
				request.LineageKey,
				replacementNode.ID,
				input.TargetNodeIDs,
				supersedesEdgeIDs,
			); err != nil {
				return err
			}
			if err := advanceCanonicalSupersessionHead(ctx, tx, head, eventID, event.Revision); err != nil {
				return err
			}
			if err := markProposalAdmitted(ctx, tx, proposal.ProposalOccurrenceID, replacementNode.ID); err != nil {
				return err
			}

			result = supersessionAdmissionResult(
				mutation.result,
				request.LineageKey,
				input.TargetNodeIDs,
				bootstrappedTargetIDs,
				supersedesEdgeIDs,
				eventID,
				event,
				input.DecisionBy,
				input.DecisionReason,
				false,
			)
			return nil
		})
	})
	if err != nil {
		return SupersessionAdmissionResult{}, err
	}
	return result, nil
}

func runSupersessionAdmissionAttempts(run func() error) error {
	var err error
	for attempt := 1; attempt <= supersessionAdmissionSerializationAttempts; attempt++ {
		err = run()
		if err == nil || !isSupersessionSerializationFailure(err) {
			return err
		}
	}
	return fmt.Errorf(
		"supersession admission exceeded %d serialization attempts: %w",
		supersessionAdmissionSerializationAttempts,
		err,
	)
}

func isSupersessionSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

func normalizeSupersessionAdmissionInput(
	input SupersessionAdmissionInput,
) (SupersessionAdmissionInput, evidencesupersession.Basis, error) {
	input.ProposalOccurrenceID = strings.TrimSpace(input.ProposalOccurrenceID)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	input.ExpectedHeadEventID = strings.TrimSpace(input.ExpectedHeadEventID)
	basis := evidencesupersession.Basis{
		SourceSystem:    input.Basis.SourceSystem,
		SourceNamespace: input.Basis.SourceNamespace,
		ObjectType:      input.Basis.ObjectType,
		ObjectID:        input.Basis.ObjectID,
		SlotKind:        input.Basis.SlotKind,
		SlotID:          input.Basis.SlotID,
	}.Normalize()
	input.Basis = SupersessionLineageBasis{
		SourceSystem:    basis.SourceSystem,
		SourceNamespace: basis.SourceNamespace,
		ObjectType:      basis.ObjectType,
		ObjectID:        basis.ObjectID,
		SlotKind:        basis.SlotKind,
		SlotID:          basis.SlotID,
	}
	if input.ProposalOccurrenceID == "" {
		return SupersessionAdmissionInput{}, evidencesupersession.Basis{}, newDomainError(ErrorInvalidInput, "proposal_occurrence_id is required")
	}
	if !strings.HasPrefix(input.ProposalOccurrenceID, "occ:") {
		return SupersessionAdmissionInput{}, evidencesupersession.Basis{}, newDomainError(
			ErrorInvalidRecordID,
			"proposal_occurrence_id %q must start with occ:",
			input.ProposalOccurrenceID,
		)
	}
	if input.DecisionBy == "" || !utf8.ValidString(input.DecisionBy) || len(input.DecisionBy) > ProposalDispositionDecisionByMaxBytes {
		return SupersessionAdmissionInput{}, evidencesupersession.Basis{}, newDomainError(
			ErrorInvalidInput,
			"decision_by must contain 1 to %d bytes of valid UTF-8",
			ProposalDispositionDecisionByMaxBytes,
		)
	}
	if input.DecisionReason == "" || !utf8.ValidString(input.DecisionReason) || len(input.DecisionReason) > ProposalDispositionDecisionReasonMaxBytes {
		return SupersessionAdmissionInput{}, evidencesupersession.Basis{}, newDomainError(
			ErrorInvalidInput,
			"decision_reason must contain 1 to %d bytes of valid UTF-8",
			ProposalDispositionDecisionReasonMaxBytes,
		)
	}
	if err := basis.Validate(); err != nil {
		return SupersessionAdmissionInput{}, evidencesupersession.Basis{}, supersessionInputError(err)
	}
	targets, err := evidencesupersession.NormalizeTargets(input.TargetNodeIDs)
	if err != nil {
		return SupersessionAdmissionInput{}, evidencesupersession.Basis{}, supersessionInputError(err)
	}
	input.TargetNodeIDs = targets
	if input.ExpectedRevision < 0 ||
		(input.ExpectedRevision == 0 && input.ExpectedHeadEventID != "") ||
		(input.ExpectedRevision > 0 && input.ExpectedHeadEventID == "") {
		return SupersessionAdmissionInput{}, evidencesupersession.Basis{}, newDomainError(
			ErrorInvalidInput,
			"expected revision and head event do not form a valid supersession coordinate",
		)
	}
	return input, basis, nil
}

func supersessionInputError(err error) error {
	return newDomainError(ErrorInvalidInput, "%v", err)
}

func validateProposalSupersessionBasis(proposal ProposalQueryResult, basis evidencesupersession.Basis) error {
	if proposal.SourceBindingKind != ProposalSourceBindingSourceSnapshot ||
		proposal.SourceSystem != SourceSystemExternalDocument {
		return newDomainError(
			ErrorUnsupportedAdmission,
			"supersession admission requires an external source-backed proposal",
		)
	}
	if proposal.OriginMetadata[externalSourceOriginSchemaKey] != ExternalSourceEnvelopeSchemaV1 ||
		proposal.OriginMetadata[externalSourceOriginFidelityKey] != ExternalSourceContentFidelityVerbatim {
		return newDomainError(
			ErrorSupersessionInvariant,
			"proposal %s lacks a verbatim external source envelope",
			proposal.ProposalOccurrenceID,
		)
	}
	return compareSupersessionExternalOrigin(proposal.OriginMetadata, basis, proposal.ProposalOccurrenceID)
}

func compareSupersessionExternalOrigin(
	origin map[string]string,
	basis evidencesupersession.Basis,
	recordID string,
) error {
	want := []struct {
		name  string
		key   string
		value string
	}{
		{name: "source_system", key: externalSourceOriginSystemKey, value: basis.SourceSystem},
		{name: "source_namespace", key: externalSourceOriginNamespaceKey, value: basis.SourceNamespace},
		{name: "object_type", key: externalSourceOriginObjectTypeKey, value: basis.ObjectType},
		{name: "object_id", key: externalSourceOriginObjectIDKey, value: basis.ObjectID},
	}
	for _, field := range want {
		if origin[field.key] != field.value {
			return newDomainError(
				ErrorSupersessionInvariant,
				"record %s %s %q does not match reviewed lineage basis %q",
				recordID,
				field.name,
				origin[field.key],
				field.value,
			)
		}
	}
	return nil
}
