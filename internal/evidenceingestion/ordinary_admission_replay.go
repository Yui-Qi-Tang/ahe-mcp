package evidenceingestion

import (
	"errors"
	"slices"
)

func validateAdmissionReplay(
	proposal ProposalQueryResult,
	input AdmissionInput,
	replay AdmissionResult,
	metadata admissionDecisionMetadata,
) error {
	if metadata.DecisionBy != input.DecisionBy || metadata.DecisionReason != input.DecisionReason {
		return newDomainError(
			ErrorAdmissionReplayConflict,
			"repeated admission for proposal %s changes reviewer or decision reason",
			proposal.ProposalOccurrenceID,
		)
	}
	expected, err := buildCanonicalAdmissionMutation(proposal, input)
	if err != nil {
		return admissionReplayMutationBuildError(proposal.ProposalOccurrenceID, err)
	}
	want := expected.result
	if replay.ProposalOccurrenceID != want.ProposalOccurrenceID ||
		replay.AdmissionDecisionID != want.AdmissionDecisionID ||
		replay.AdmissionOutcome != want.AdmissionOutcome ||
		replay.CanonicalRef != want.CanonicalRef ||
		proposal.CanonicalRef != replay.CanonicalRef ||
		replay.DerivationID != want.DerivationID ||
		!slices.Equal(replay.RawEvidenceNodeIDs, want.RawEvidenceNodeIDs) ||
		!slices.Equal(replay.CanonicalEdgeIDs, want.CanonicalEdgeIDs) ||
		!slices.Equal(replay.ParentNodeIDs, want.ParentNodeIDs) {
		return newDomainError(
			ErrorAdmissionReplayConflict,
			"repeated admission for proposal %s does not match its admitted decision or graph mutation",
			proposal.ProposalOccurrenceID,
		)
	}
	if !admissionDerivationReplayMatches(expected, proposal.ProposalOccurrenceID, metadata) {
		return newDomainError(
			ErrorAdmissionReplayConflict,
			"repeated admission for proposal %s does not match its persisted derivation metadata or parent-edge bindings",
			proposal.ProposalOccurrenceID,
		)
	}
	return nil
}

func admissionReplayMutationBuildError(proposalOccurrenceID string, err error) error {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		switch domainErr.Kind {
		case ErrorInvalidInput, ErrorInvalidRecordID, ErrorUnsupportedAdmission:
			return newDomainError(
				ErrorAdmissionReplayConflict,
				"repeated admission for proposal %s does not reproduce its admitted mutation: %v",
				proposalOccurrenceID,
				err,
			)
		}
	}
	return err
}

func admissionDerivationReplayMatches(
	expected canonicalAdmissionMutation,
	proposalOccurrenceID string,
	metadata admissionDecisionMetadata,
) bool {
	if (expected.derivation == nil) != (metadata.Derivation == nil) {
		return false
	}
	if expected.derivation == nil {
		return metadata.DerivationOriginProposalOccurrenceID == "" &&
			len(metadata.DerivationParentEdges) == 0
	}
	got := metadata.Derivation
	want := expected.derivation
	if got.ID != want.ID ||
		got.NodeID != want.NodeID ||
		got.Method != want.Method ||
		got.Producer != want.Producer ||
		got.TraceRef != want.TraceRef ||
		got.ProvenanceRef != want.ProvenanceRef ||
		!slices.Equal(got.Parents, want.Parents) ||
		metadata.DerivationOriginProposalOccurrenceID != proposalOccurrenceID ||
		len(metadata.DerivationParentEdges) != len(expected.derivationParentEdges) {
		return false
	}
	for parentID, wantEdgeID := range expected.derivationParentEdges {
		if metadata.DerivationParentEdges[parentID] != wantEdgeID {
			return false
		}
	}
	return true
}
