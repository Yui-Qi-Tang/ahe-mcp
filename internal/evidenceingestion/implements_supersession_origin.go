package evidenceingestion

import (
	"context"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

// replayImplementsSupersessionOrigin proves one immutable first materializer,
// not the current head or a global closure. Later replacement events must not
// change the authority of an already admitted leaf or its reviewed receipt.
func replayImplementsSupersessionOrigin(ctx context.Context, tx sqlTx, proposal ProposalQueryResult) (admissionDecisionMetadata, error) {
	if err := ctx.Err(); err != nil {
		return admissionDecisionMetadata{}, err
	}
	node, err := loadCanonicalNode(ctx, tx, proposal.CanonicalRef)
	if err != nil {
		return admissionDecisionMetadata{}, err
	}
	if node.NodeKind != evidencegraph.CanonicalSourceClaim ||
		proposal.SourceBindingKind != ProposalSourceBindingSourceSnapshot || proposal.SourceSystem != SourceSystemExternalDocument {
		return admissionDecisionMetadata{}, newDomainError(ErrorUnsupportedAdmission, "implements supersession origin requires an external-document source claim")
	}
	var bytes int64
	if err := tx.queryRow(ctx, `SELECT coalesce(sum(octet_length(to_jsonb(e)::text)+coalesce(octet_length(to_jsonb(l)::text),0)),0)
		FROM canonical_supersession_admission_events e LEFT JOIN canonical_supersession_lineages l USING(lineage_key)
		WHERE e.proposal_occurrence_id=$1`, proposal.ProposalOccurrenceID).Scan(&bytes); err != nil {
		return admissionDecisionMetadata{}, err
	}
	if bytes > 2<<20 {
		return admissionDecisionMetadata{}, newDomainError(ErrorInvalidInput, "implements supersession origin exceeds byte bound")
	}
	event, err := loadPersistedSupersessionEvent(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return admissionDecisionMetadata{}, err
	}
	basis := event.Payload.LineageBasis
	if err := validateProposalSupersessionBasis(proposal, basis); err != nil {
		return admissionDecisionMetadata{}, err
	}
	_, metadata, err := loadAdmissionDecisionResult(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return admissionDecisionMetadata{}, err
	}
	// The existing replay verifies exact decision/event hashes, original node
	// and edge bodies, single first-materializer authority, members and targets.
	// All input coordinates come from persisted authority in this transaction.
	_, err = replaySupersessionAdmission(ctx, tx, proposal, SupersessionAdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           metadata.DecisionBy,
		DecisionReason:       metadata.DecisionReason,
		Basis: SupersessionLineageBasis{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace,
			ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: basis.SlotID},
		TargetNodeIDs: event.Payload.TargetNodeIDs, ExpectedRevision: event.PreviousRevision, ExpectedHeadEventID: event.PreviousEventID,
	}, basis)
	if err != nil {
		return admissionDecisionMetadata{}, err
	}
	return metadata, nil
}
