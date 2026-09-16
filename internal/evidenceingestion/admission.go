package evidenceingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdmitPendingProposal admits one pending statement proposal into the canonical graph tables.
func AdmitPendingProposal(ctx context.Context, pool *pgxpool.Pool, input AdmissionInput) (AdmissionResult, error) {
	if pool == nil {
		return AdmissionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return admitPendingProposal(ctx, pgxDB{pool: pool}, input)
}

// GetCanonicalEvidenceByID returns one admitted canonical node and its proposal provenance.
func GetCanonicalEvidenceByID(ctx context.Context, pool *pgxpool.Pool, canonicalID string) (CanonicalQueryResult, error) {
	if pool == nil {
		return CanonicalQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return getCanonicalEvidenceByID(ctx, pgxDB{pool: pool}, canonicalID)
}

func admitPendingProposal(ctx context.Context, db sqlDB, input AdmissionInput) (AdmissionResult, error) {
	input = normalizeAdmissionInput(input)
	if input.ProposalOccurrenceID == "" {
		return AdmissionResult{}, newDomainError(ErrorInvalidInput, "proposal_occurrence_id is required")
	}
	if !strings.HasPrefix(input.ProposalOccurrenceID, "occ:") {
		return AdmissionResult{}, newDomainError(ErrorInvalidRecordID, "proposal_occurrence_id %q must start with occ:", input.ProposalOccurrenceID)
	}

	var result AdmissionResult
	err := withTx(ctx, db, func(tx sqlTx) error {
		proposal, err := loadProposalForAdmission(ctx, tx, input.ProposalOccurrenceID)
		if err != nil {
			return err
		}
		switch proposal.AdmissionOutcome {
		case admissionOutcomePending:
		case admissionOutcomeAdmitted:
			replay, metadata, err := loadAdmissionDecisionResult(ctx, tx, input.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			wasSupersessionAdmission, err := proposalHasSupersessionAdmissionEvent(
				ctx,
				tx,
				input.ProposalOccurrenceID,
			)
			if err != nil {
				return err
			}
			if wasSupersessionAdmission {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s was admitted through the supersession writer; use AdmitPendingSupersession for exact replay",
					input.ProposalOccurrenceID,
				)
			}
			if metadata.ReviewBindingContractVersion != "" {
				return newDomainError(ErrorAdmissionStateConflict, "proposal %s was admitted through the reviewed source-claim writer; use AdmitReviewedSourceClaim for exact replay", input.ProposalOccurrenceID)
			}
			wasReviewedSourceClaim, err := proposalHasSourceClaimReviewBinding(ctx, tx, input.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			if wasReviewedSourceClaim {
				return newDomainError(ErrorAdmissionStateConflict, "proposal %s was admitted through the reviewed source-claim writer; use AdmitReviewedSourceClaim for exact replay", input.ProposalOccurrenceID)
			}
			if err := validateAdmissionReplay(proposal, input, replay, metadata); err != nil {
				return err
			}
			if err := validatePersistedOrdinaryAdmissionMutation(ctx, tx, proposal, input); err != nil {
				return err
			}
			replay.Replayed = true
			result = replay
			return nil
		case admissionOutcomeRejected, admissionOutcomeAuditOnly:
			return newDomainError(ErrorAdmissionStateConflict, "proposal %s outcome is %s", input.ProposalOccurrenceID, proposal.AdmissionOutcome)
		default:
			return newDomainError(ErrorAdmissionStateConflict, "proposal %s has unknown admission outcome %q", input.ProposalOccurrenceID, proposal.AdmissionOutcome)
		}
		if proposal.ExtractionAttemptStatus != attemptStatusSucceeded {
			return newDomainError(ErrorAdmissionStateConflict, "proposal %s attempt status is %s", input.ProposalOccurrenceID, proposal.ExtractionAttemptStatus)
		}
		if proposal.ProposalKind != ProposalKindStatement {
			return newDomainError(ErrorUnsupportedAdmission, "proposal kind %q is not supported by Slice 9 admission", proposal.ProposalKind)
		}
		mutation, err := buildCanonicalAdmissionMutation(proposal, input)
		if err != nil {
			return err
		}
		if mutation.derivation != nil {
			if err := validateDerivedAdmissionInvariant(ctx, tx, mutation); err != nil {
				return err
			}
		}
		write, err := persistOrdinaryCanonicalMutation(ctx, tx, mutation)
		if err != nil {
			return err
		}
		if mutation.derivation != nil {
			if err := insertCanonicalDerivation(ctx, tx, *mutation.derivation, mutation.derivationParentEdges, proposal.ProposalOccurrenceID); err != nil {
				return err
			}
		}
		if err := insertAdmissionDecision(ctx, tx, mutation.decision, admissionDecisionMetadata{
			DecisionBy:     input.DecisionBy,
			DecisionReason: input.DecisionReason,
		}); err != nil {
			return err
		}
		if err := insertOrdinaryAdmissionAuthority(ctx, tx, mutation, write); err != nil {
			return err
		}
		if err := markProposalAdmitted(ctx, tx, proposal.ProposalOccurrenceID, mutation.result.CanonicalRef); err != nil {
			return err
		}
		result = mutation.result
		return nil
	})
	if err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func proposalHasSupersessionAdmissionEvent(
	ctx context.Context,
	tx sqlTx,
	proposalOccurrenceID string,
) (bool, error) {
	var found bool
	if err := tx.queryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM canonical_supersession_admission_events
			WHERE proposal_occurrence_id = $1
		)
	`, proposalOccurrenceID).Scan(&found); err != nil {
		return false, fmt.Errorf("checking admission replay authority: %w", err)
	}
	return found, nil
}

func getCanonicalEvidenceByID(ctx context.Context, db sqlQueryer, canonicalID string) (CanonicalQueryResult, error) {
	canonicalID = strings.TrimSpace(canonicalID)
	if canonicalID == "" {
		return CanonicalQueryResult{}, newDomainError(ErrorInvalidInput, "canonical_id is required")
	}
	if !strings.HasPrefix(canonicalID, "canon-node:") {
		return CanonicalQueryResult{}, newDomainError(ErrorInvalidRecordID, "canonical_id %q must start with canon-node:", canonicalID)
	}

	result, err := loadCanonicalNode(ctx, db, canonicalID)
	if err != nil {
		return CanonicalQueryResult{}, err
	}
	origin, err := traceProposalProvenance(ctx, db, result.OriginProposalOccurrenceID)
	if err != nil {
		return CanonicalQueryResult{}, err
	}
	result.OriginProposal = origin
	if result.CanonicalID == origin.CanonicalRef {
		result.EndpointAdmission, err = loadEndpointAdmissionReceipt(ctx, db, origin.ProposalOccurrenceID)
		if err != nil {
			return CanonicalQueryResult{}, err
		}
	}
	return result, nil
}

func normalizeAdmissionInput(input AdmissionInput) AdmissionInput {
	input.ProposalOccurrenceID = strings.TrimSpace(input.ProposalOccurrenceID)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	if input.DecisionBy == "" {
		input.DecisionBy = AdmissionProducerSlice9
	}
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	if input.Derivation != nil {
		derivation := *input.Derivation
		derivation.ParentNodeIDs = append([]string(nil), derivation.ParentNodeIDs...)
		derivation.Method = strings.TrimSpace(derivation.Method)
		derivation.Producer = strings.TrimSpace(derivation.Producer)
		derivation.TraceRef = strings.TrimSpace(derivation.TraceRef)
		input.Derivation = &derivation
	}
	if input.DecisionReason == "" && input.Derivation != nil {
		input.DecisionReason = "derived statement admitted with complete canonical parent set"
	} else if input.DecisionReason == "" {
		input.DecisionReason = "source-backed statement proposal admitted"
	}
	return input
}

type canonicalAdmissionMutation struct {
	result                AdmissionResult
	nodes                 []CanonicalGraphNode
	edges                 []CanonicalGraphEdge
	decision              admissionDecision
	derivation            *evidencegraph.DerivationRecord
	derivationParentEdges map[string]string
}

type admissionDecision struct {
	ID                 string
	ProposalOccurrence string
	Outcome            string
	CanonicalRef       string
	RawEvidenceNodeIDs []string
	CanonicalEdgeIDs   []string
}

type admissionDecisionMetadata struct {
	DecisionBy                           string
	DecisionReason                       string
	ReviewBindingContractVersion         string
	Derivation                           *evidencegraph.DerivationRecord
	DerivationParentEdges                map[string]string
	DerivationOriginProposalOccurrenceID string
}

func buildCanonicalAdmissionMutation(proposal ProposalQueryResult, input AdmissionInput) (canonicalAdmissionMutation, error) {
	if input.Derivation != nil {
		return buildDerivedAdmissionMutation(proposal, *input.Derivation)
	}
	if len(proposal.SourceRefs) == 0 {
		return canonicalAdmissionMutation{}, newDomainError(ErrorUnsupportedAdmission, "proposal %s has no source refs", proposal.ProposalOccurrenceID)
	}
	claimNode, err := buildClaimNode(proposal)
	if err != nil {
		return canonicalAdmissionMutation{}, err
	}

	nodes := []CanonicalGraphNode{claimNode}
	var edges []CanonicalGraphEdge
	rawNodeIDs := make([]string, 0, len(proposal.SourceRefs))
	edgeIDs := make([]string, 0, len(proposal.SourceRefs))
	for _, ref := range proposal.SourceRefs {
		rawNode, err := buildRawEvidenceNode(proposal, ref)
		if err != nil {
			return canonicalAdmissionMutation{}, err
		}
		edge, err := buildSupportsClaimEdge(proposal, rawNode.ID, claimNode.ID)
		if err != nil {
			return canonicalAdmissionMutation{}, err
		}
		nodes = append(nodes, rawNode)
		edges = append(edges, edge)
		rawNodeIDs = append(rawNodeIDs, rawNode.ID)
		edgeIDs = append(edgeIDs, edge.ID)
	}

	decisionID, err := stableID("adm:", "admission_decision", struct {
		ProposalOccurrenceID string `json:"proposal_occurrence_id"`
		Outcome              string `json:"outcome"`
		CanonicalRef         string `json:"canonical_ref"`
	}{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		Outcome:              admissionOutcomeAdmitted,
		CanonicalRef:         claimNode.ID,
	})
	if err != nil {
		return canonicalAdmissionMutation{}, err
	}
	return canonicalAdmissionMutation{
		result: AdmissionResult{
			ProposalOccurrenceID: proposal.ProposalOccurrenceID,
			AdmissionDecisionID:  decisionID,
			AdmissionOutcome:     admissionOutcomeAdmitted,
			CanonicalRef:         claimNode.ID,
			RawEvidenceNodeIDs:   rawNodeIDs,
			CanonicalEdgeIDs:     edgeIDs,
		},
		nodes: nodes,
		edges: edges,
		decision: admissionDecision{
			ID:                 decisionID,
			ProposalOccurrence: proposal.ProposalOccurrenceID,
			Outcome:            admissionOutcomeAdmitted,
			CanonicalRef:       claimNode.ID,
			RawEvidenceNodeIDs: rawNodeIDs,
			CanonicalEdgeIDs:   edgeIDs,
		},
	}, nil
}

const maxDerivationParents = 64

func buildDerivedAdmissionMutation(
	proposal ProposalQueryResult,
	input DerivationAdmissionInput,
) (canonicalAdmissionMutation, error) {
	parents, err := normalizeDerivationParents(input.ParentNodeIDs)
	if err != nil {
		return canonicalAdmissionMutation{}, err
	}
	if input.Method == "" || input.Producer == "" || input.TraceRef == "" {
		return canonicalAdmissionMutation{}, newDomainError(
			ErrorInvalidInput,
			"derived admission requires method, producer, and trace_ref",
		)
	}

	parentIdentity := strings.Join(parents, "\x00")
	nodeID := evidencegraph.StableCanonicalID(
		"canon-node",
		string(evidencegraph.CanonicalDerivedClaim),
		proposal.ProposalOccurrenceID,
		proposal.StatementText,
		parentIdentity,
		input.Method,
		input.Producer,
		input.TraceRef,
	)
	payload := evidencegraph.EvidencePayload{
		ID:         evidencegraph.StableCanonicalID("payload", nodeID),
		SourceType: "derived",
		Title:      "derived claim",
		Source:     "ahe:derivation",
		Claim:      proposal.StatementText,
	}
	provenance := evidencegraph.ProvenanceRecord{
		ID:            evidencegraph.StableCanonicalID("provenance", nodeID),
		OriginRefs:    append([]string(nil), parents...),
		OriginGroupID: evidencegraph.StableCanonicalID("origin-group", parentIdentity),
		Producer:      input.Producer,
		Method:        input.Method,
		MethodVersion: "v1",
		TraceRef:      input.TraceRef,
	}
	digest, err := evidencegraph.PayloadDigest(payload)
	if err != nil {
		return canonicalAdmissionMutation{}, fmt.Errorf("computing derived payload digest: %w", err)
	}
	node := CanonicalGraphNode{
		ID:         nodeID,
		Kind:       evidencegraph.CanonicalDerivedClaim,
		Payload:    payload,
		Provenance: provenance,
		Temporal: evidencegraph.TemporalRecord{
			ID:     evidencegraph.StableCanonicalID("temporal", nodeID),
			Status: evidencegraph.TemporalUnknown,
		},
		Integrity: evidencegraph.IntegrityRecord{
			ID:        evidencegraph.StableCanonicalID("integrity", payload.ID),
			Algorithm: "sha256",
			Digest:    digest,
		},
		OriginProposalOccurrenceID: proposal.ProposalOccurrenceID,
	}
	derivation := evidencegraph.DerivationRecord{
		ID:            evidencegraph.StableCanonicalID("derivation", nodeID, parentIdentity, input.Method, input.Producer, input.TraceRef),
		NodeID:        nodeID,
		Parents:       append([]string(nil), parents...),
		Method:        input.Method,
		Producer:      input.Producer,
		TraceRef:      input.TraceRef,
		ProvenanceRef: provenance.ID,
	}

	edges := make([]CanonicalGraphEdge, 0, len(parents))
	edgeIDs := make([]string, 0, len(parents))
	parentEdges := make(map[string]string, len(parents))
	for _, parent := range parents {
		edgeID := evidencegraph.StableCanonicalID("canon-edge", parent, nodeID, string(evidencegraph.CanonicalDerivedFrom))
		edgeProvenance := evidencegraph.ProvenanceRecord{
			ID:            evidencegraph.StableCanonicalID("provenance", edgeID),
			OriginRefs:    []string{parent, nodeID},
			OriginGroupID: provenance.OriginGroupID,
			Producer:      input.Producer,
			Method:        input.Method,
			MethodVersion: "v1",
			TraceRef:      input.TraceRef,
		}
		edges = append(edges, CanonicalGraphEdge{
			ID:                         edgeID,
			From:                       parent,
			To:                         nodeID,
			Relation:                   evidencegraph.CanonicalDerivedFrom,
			Provenance:                 edgeProvenance,
			OriginProposalOccurrenceID: proposal.ProposalOccurrenceID,
		})
		edgeIDs = append(edgeIDs, edgeID)
		parentEdges[parent] = edgeID
	}

	decisionID, err := stableID("adm:", "admission_decision", struct {
		ProposalOccurrenceID string `json:"proposal_occurrence_id"`
		Outcome              string `json:"outcome"`
		CanonicalRef         string `json:"canonical_ref"`
	}{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		Outcome:              admissionOutcomeAdmitted,
		CanonicalRef:         nodeID,
	})
	if err != nil {
		return canonicalAdmissionMutation{}, err
	}
	return canonicalAdmissionMutation{
		result: AdmissionResult{
			ProposalOccurrenceID: proposal.ProposalOccurrenceID,
			AdmissionDecisionID:  decisionID,
			AdmissionOutcome:     admissionOutcomeAdmitted,
			CanonicalRef:         nodeID,
			CanonicalEdgeIDs:     edgeIDs,
			DerivationID:         derivation.ID,
			ParentNodeIDs:        append([]string(nil), parents...),
		},
		nodes: []CanonicalGraphNode{node},
		edges: edges,
		decision: admissionDecision{
			ID:                 decisionID,
			ProposalOccurrence: proposal.ProposalOccurrenceID,
			Outcome:            admissionOutcomeAdmitted,
			CanonicalRef:       nodeID,
			CanonicalEdgeIDs:   edgeIDs,
		},
		derivation:            &derivation,
		derivationParentEdges: parentEdges,
	}, nil
}

func normalizeDerivationParents(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, newDomainError(ErrorInvalidInput, "derived admission requires at least one parent node")
	}
	if len(values) > maxDerivationParents {
		return nil, newDomainError(ErrorInvalidInput, "derived admission supports at most %d parent nodes", maxDerivationParents)
	}
	parents := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parent := strings.TrimSpace(value)
		if !strings.HasPrefix(parent, "canon-node:") {
			return nil, newDomainError(ErrorInvalidRecordID, "derivation parent %q must start with canon-node:", parent)
		}
		if _, exists := seen[parent]; exists {
			return nil, newDomainError(ErrorInvalidInput, "derivation parent %q is duplicated", parent)
		}
		seen[parent] = struct{}{}
		parents = append(parents, parent)
	}
	slices.Sort(parents)
	return parents, nil
}

func buildClaimNode(proposal ProposalQueryResult) (CanonicalGraphNode, error) {
	sourceIdentity, err := canonicalSourceRefsIdentity(proposal.SourceBindingKind, proposal.SourceRefs)
	if err != nil {
		return CanonicalGraphNode{}, err
	}
	nodeID := evidencegraph.StableCanonicalID("canon-node", string(evidencegraph.CanonicalSourceClaim), proposal.StatementText, sourceIdentity)
	payload := evidencegraph.EvidencePayload{
		ID:          evidencegraph.StableCanonicalID("payload", nodeID),
		SourceType:  proposalSourceType(proposal),
		Title:       "source claim",
		Source:      sourceLabel(proposal),
		Span:        joinedQuotedText(proposal.SourceRefs),
		SpanLocator: joinedSpanLocators(proposal.SourceRefs),
		Claim:       proposal.StatementText,
	}
	return canonicalNode(proposal, nodeID, evidencegraph.CanonicalSourceClaim, payload, evidencegraph.TemporalUnknown, "proposal_admission", proposal.ProposalFingerprint)
}

func buildRawEvidenceNode(proposal ProposalQueryResult, ref ResolvedSourceRef) (CanonicalGraphNode, error) {
	nodeID := rawEvidenceNodeID(proposal.SourceBindingKind, ref)
	payload := evidencegraph.EvidencePayload{
		ID:          evidencegraph.StableCanonicalID("payload", nodeID),
		SourceType:  proposalSourceType(proposal),
		Title:       "raw evidence span",
		Source:      sourceLabel(proposal),
		Span:        ref.QuotedText,
		SpanLocator: spanLocator(ref),
	}
	return canonicalNode(proposal, nodeID, evidencegraph.CanonicalRawEvidence, payload, evidencegraph.TemporalUnknown, "source_span_observation", ref.QuotedTextHash)
}

func canonicalNode(
	proposal ProposalQueryResult,
	nodeID string,
	kind evidencegraph.CanonicalNodeKind,
	payload evidencegraph.EvidencePayload,
	status evidencegraph.TemporalStatus,
	method string,
	traceRef string,
) (CanonicalGraphNode, error) {
	digest, err := evidencegraph.PayloadDigest(payload)
	if err != nil {
		return CanonicalGraphNode{}, fmt.Errorf("computing canonical payload digest: %w", err)
	}
	return CanonicalGraphNode{
		ID:      nodeID,
		Kind:    kind,
		Payload: payload,
		Provenance: evidencegraph.ProvenanceRecord{
			ID:            evidencegraph.StableCanonicalID("provenance", nodeID),
			OriginRefs:    canonicalOriginRefs(proposal),
			OriginGroupID: proposalOriginGroupID(proposal),
			Producer:      proposal.ExtractorName,
			Method:        method,
			MethodVersion: "v1",
			TraceRef:      traceRef,
		},
		Temporal: evidencegraph.TemporalRecord{
			ID:     evidencegraph.StableCanonicalID("temporal", nodeID),
			Status: status,
		},
		Integrity: evidencegraph.IntegrityRecord{
			ID:        evidencegraph.StableCanonicalID("integrity", payload.ID),
			Algorithm: "sha256",
			Digest:    digest,
		},
		OriginProposalOccurrenceID: proposal.ProposalOccurrenceID,
	}, nil
}

func buildSupportsClaimEdge(proposal ProposalQueryResult, from, to string) (CanonicalGraphEdge, error) {
	edgeID := evidencegraph.StableCanonicalID("canon-edge", from, to, string(evidencegraph.CanonicalSupportsClaim))
	return CanonicalGraphEdge{
		ID:       edgeID,
		From:     from,
		To:       to,
		Relation: evidencegraph.CanonicalSupportsClaim,
		Provenance: evidencegraph.ProvenanceRecord{
			ID:            evidencegraph.StableCanonicalID("provenance", edgeID),
			OriginRefs:    canonicalOriginRefs(proposal),
			OriginGroupID: proposalOriginGroupID(proposal),
			Producer:      proposal.ExtractorName,
			Method:        "proposal_admission_edge",
			MethodVersion: "v1",
			TraceRef:      proposal.ProposalFingerprint,
		},
		OriginProposalOccurrenceID: proposal.ProposalOccurrenceID,
	}, nil
}

func canonicalSourceRefsIdentity(bindingKind string, refs []ResolvedSourceRef) (string, error) {
	switch bindingKind {
	case ProposalSourceBindingSourceSnapshot:
		return canonicalExtractionViewSourceRefsIdentity(refs)
	case ProposalSourceBindingRepositorySnapshot:
		return canonicalRepositorySourceRefsIdentity(refs)
	default:
		return "", newDomainError(ErrorRepositorySnapshotIntegrity, "proposal source binding kind %q is not supported", bindingKind)
	}
}

func canonicalExtractionViewSourceRefsIdentity(refs []ResolvedSourceRef) (string, error) {
	type sourceRefIdentity struct {
		ExtractionViewID string `json:"extraction_view_id"`
		SpanID           string `json:"span_id"`
		StartByte        int    `json:"start_byte"`
		EndByte          int    `json:"end_byte"`
		QuotedTextHash   string `json:"quoted_text_hash"`
	}
	identities := make([]sourceRefIdentity, 0, len(refs))
	for _, ref := range refs {
		identities = append(identities, sourceRefIdentity{
			ExtractionViewID: ref.ExtractionViewID,
			SpanID:           ref.SpanID,
			StartByte:        ref.StartByte,
			EndByte:          ref.EndByte,
			QuotedTextHash:   ref.QuotedTextHash,
		})
	}
	data, err := deterministicJSON(identities)
	if err != nil {
		return "", fmt.Errorf("serializing canonical source refs identity: %w", err)
	}
	return string(data), nil
}

func canonicalRepositorySourceRefsIdentity(refs []ResolvedSourceRef) (string, error) {
	type sourceRefIdentity struct {
		TargetKind           string `json:"target_kind"`
		RepositorySnapshotID string `json:"repository_snapshot_id"`
		FileSnapshotID       string `json:"file_snapshot_id"`
		RepoID               string `json:"repo_id"`
		CommitSHA            string `json:"commit_sha"`
		Path                 string `json:"path"`
		SpanID               string `json:"span_id"`
		StartByte            int    `json:"start_byte"`
		EndByte              int    `json:"end_byte"`
		QuotedTextHash       string `json:"quoted_text_hash"`
	}
	identities := make([]sourceRefIdentity, 0, len(refs))
	for _, ref := range refs {
		identities = append(identities, sourceRefIdentity{
			TargetKind:           ref.TargetKind,
			RepositorySnapshotID: ref.RepositorySnapshotID,
			FileSnapshotID:       ref.FileSnapshotID,
			RepoID:               ref.RepoID,
			CommitSHA:            ref.CommitSHA,
			Path:                 ref.Path,
			SpanID:               ref.SpanID,
			StartByte:            ref.StartByte,
			EndByte:              ref.EndByte,
			QuotedTextHash:       ref.QuotedTextHash,
		})
	}
	data, err := deterministicJSON(identities)
	if err != nil {
		return "", fmt.Errorf("serializing canonical repository source refs identity: %w", err)
	}
	return string(data), nil
}

func rawEvidenceNodeID(bindingKind string, ref ResolvedSourceRef) string {
	if bindingKind == ProposalSourceBindingRepositorySnapshot {
		return evidencegraph.StableCanonicalID(
			"canon-node",
			string(evidencegraph.CanonicalRawEvidence),
			ref.RepositorySnapshotID,
			ref.FileSnapshotID,
			ref.Path,
			ref.SpanID,
			fmt.Sprintf("%d", ref.StartByte),
			fmt.Sprintf("%d", ref.EndByte),
			ref.QuotedTextHash,
		)
	}
	return evidencegraph.StableCanonicalID(
		"canon-node",
		string(evidencegraph.CanonicalRawEvidence),
		ref.ExtractionViewID,
		ref.SpanID,
		fmt.Sprintf("%d", ref.StartByte),
		fmt.Sprintf("%d", ref.EndByte),
		ref.QuotedTextHash,
	)
}

func proposalSourceType(proposal ProposalQueryResult) string {
	if proposal.SourceBindingKind == ProposalSourceBindingRepositorySnapshot {
		return SourceSystemCodeRepository
	}
	return proposal.SourceSystem
}

func proposalOriginGroupID(proposal ProposalQueryResult) string {
	if proposal.RepositorySnapshot != nil {
		return proposal.RepositorySnapshot.ID
	}
	return proposal.SourceSnapshotID
}

func sourceLabel(proposal ProposalQueryResult) string {
	if proposal.RepositorySnapshot != nil {
		return SourceSystemCodeRepository + ":" + proposal.RepositorySnapshot.RepoID + "@" + proposal.RepositorySnapshot.CommitSHA
	}
	return proposal.SourceSystem + ":" + proposal.SourceID + "@" + proposal.SourceVersion
}

func joinedQuotedText(refs []ResolvedSourceRef) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts = append(parts, ref.QuotedText)
	}
	return strings.Join(parts, "\n")
}

func joinedSpanLocators(refs []ResolvedSourceRef) string {
	locators := make([]string, 0, len(refs))
	for _, ref := range refs {
		locators = append(locators, spanLocator(ref))
	}
	return strings.Join(locators, "\n")
}

func spanLocator(ref ResolvedSourceRef) string {
	if ref.FileSnapshotID != "" {
		return fmt.Sprintf("%s:%s#%s[%d:%d]", ref.FileSnapshotID, ref.Path, ref.SpanID, ref.StartByte, ref.EndByte)
	}
	return fmt.Sprintf("%s#%s[%d:%d]", ref.ExtractionViewID, ref.SpanID, ref.StartByte, ref.EndByte)
}

func canonicalOriginRefs(proposal ProposalQueryResult) []string {
	if proposal.RepositorySnapshot != nil {
		refs := []string{proposal.RepositorySnapshot.ID}
		seenFiles := make(map[string]bool, len(proposal.SourceRefs))
		for _, ref := range proposal.SourceRefs {
			if !seenFiles[ref.FileSnapshotID] {
				refs = append(refs, ref.FileSnapshotID)
				seenFiles[ref.FileSnapshotID] = true
			}
			refs = append(refs, spanLocator(ref))
		}
		return refs
	}
	refs := []string{proposal.SourceSnapshotID, proposal.ExtractionViewID}
	for _, ref := range proposal.SourceRefs {
		refs = append(refs, spanLocator(ref))
	}
	return refs
}

func loadProposalForAdmission(ctx context.Context, tx sqlTx, occurrenceID string) (ProposalQueryResult, error) {
	// Acquire the proposal lock before loading its joined provenance. A waiter
	// then reads the completed predecessor's state in a fresh statement.
	var lockedID string
	err := tx.queryRow(ctx, `
		SELECT proposal_occurrence_id
		FROM proposal_occurrences
		WHERE proposal_occurrence_id = $1
		FOR UPDATE
	`, occurrenceID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProposalQueryResult{}, newDomainError(ErrorMissingSourceViewAttempt, "proposal occurrence %s not found", occurrenceID)
	}
	if err != nil {
		return ProposalQueryResult{}, fmt.Errorf("locking proposal occurrence: %w", err)
	}
	row := tx.queryRow(ctx, `
		SELECT
			po.proposal_occurrence_id,
			po.proposal_fingerprint,
			po.proposal_fingerprint_version,
			po.proposal_kind,
			po.statement_text,
			po.admission_outcome,
			po.canonical_ref,
			po.source_refs,
			po.proposed_payload,
			ea.extraction_attempt_id,
			ea.status,
			er.extraction_run_id,
			COALESCE(er.producer_session_ref, ''),
			ed.extractor_definition_id,
			ed.extractor_name,
			ed.extractor_version,
			ed.extractor_config_hash,
			COALESCE(ev.extraction_view_id, ''),
			COALESCE(ev.renderer_name, ''),
			COALESCE(ev.renderer_version, ''),
			COALESCE(ev.rendered_content_hash, ''),
			COALESCE(ss.source_snapshot_id, ''),
			COALESCE(ss.source_system, ''),
			COALESCE(ss.source_id, ''),
			COALESCE(ss.source_version, ''),
			COALESCE(ss.raw_content_hash, ''),
			COALESCE(ss.origin_metadata, '{}'::jsonb),
			COALESCE(rs.repository_snapshot_id, ''),
			COALESCE(rs.repo_id, ''),
			COALESCE(rs.commit_sha, ''),
			COALESCE(rs.manifest_hash, ''),
			COALESCE(rs.manifest_entry_count, 0),
			COALESCE(rs.revision_verification_method, ''),
			COALESCE(rs.manifest_contract, ''),
			COALESCE(rs.file_selection_contract, ''),
			COALESCE(rs.selected_file_count, 0),
			COALESCE(rsg.source_generation_id, ''),
			COALESCE(rsg.repo_id, ''),
			COALESCE(rsg.extractor_name, ''),
			COALESCE(rsg.extractor_definition_id, ''),
			COALESCE(rsg.generation_number, 0),
			COALESCE(rsg.repository_snapshot_id, ''),
			COALESCE(rsg.commit_sha, ''),
			COALESCE(rsg.proposal_batch_id, ''),
			COALESCE(rsg.extractor_output_hash, ''),
			COALESCE(rsg.proposal_count, 0),
			(rsh.active_generation_id IS NOT NULL),
			COALESCE(rgpr.source_generation_id, ''),
			COALESCE(rgpr.lifecycle_state, ''),
			COALESCE(rgr.identity_contract, ''),
			COALESCE(rgpr.proposal_identity, ''),
			COALESCE(rgpr.current_proposal_occurrence_id, ''),
			COALESCE(rgpr.previous_proposal_occurrence_id, '')
		FROM proposal_occurrences po
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = po.extraction_attempt_id
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		JOIN extractor_definitions ed ON ed.extractor_definition_id = er.extractor_definition_id
		LEFT JOIN extraction_views ev ON ev.extraction_view_id = er.extraction_view_id
		LEFT JOIN source_snapshots ss ON ss.source_snapshot_id = er.source_snapshot_id
		LEFT JOIN repository_snapshots rs ON rs.repository_snapshot_id = er.repository_snapshot_id
		LEFT JOIN repository_source_generations rsg ON rsg.proposal_batch_id = po.proposal_batch_id
		LEFT JOIN repository_source_heads rsh
			ON rsh.repo_id = rsg.repo_id
			AND rsh.extractor_name = rsg.extractor_name
			AND rsh.active_generation_id = rsg.source_generation_id
		LEFT JOIN repository_source_heads stream_head
			ON stream_head.repo_id = rsg.repo_id
			AND stream_head.extractor_name = rsg.extractor_name
		LEFT JOIN repository_generation_proposal_reconciliations rgpr
			ON rgpr.source_generation_id = stream_head.active_generation_id
			AND (
				rgpr.current_proposal_occurrence_id = po.proposal_occurrence_id
				OR rgpr.previous_proposal_occurrence_id = po.proposal_occurrence_id
			)
		LEFT JOIN repository_generation_reconciliations rgr
			ON rgr.source_generation_id = rgpr.source_generation_id
		WHERE po.proposal_occurrence_id = $1
	`, occurrenceID)
	return scanProposalQueryRow(row, occurrenceID)
}

func loadAdmissionDecisionResult(
	ctx context.Context,
	tx sqlTx,
	occurrenceID string,
) (AdmissionResult, admissionDecisionMetadata, error) {
	var result AdmissionResult
	var metadata admissionDecisionMetadata
	var canonicalRef sql.NullString
	var reviewBindingContractVersion sql.NullString
	var rawNodeIDsData, edgeIDsData []byte
	err := tx.queryRow(ctx, `
		SELECT
			admission_decision_id,
			outcome,
			canonical_ref,
			raw_evidence_node_ids,
			canonical_edge_ids,
			decision_by,
			decision_reason,
			review_binding_contract_version
		FROM admission_decisions
		WHERE proposal_occurrence_id = $1
	`, occurrenceID).Scan(
		&result.AdmissionDecisionID,
		&result.AdmissionOutcome,
		&canonicalRef,
		&rawNodeIDsData,
		&edgeIDsData,
		&metadata.DecisionBy,
		&metadata.DecisionReason,
		&reviewBindingContractVersion,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AdmissionResult{}, admissionDecisionMetadata{}, newDomainError(
				ErrorAdmissionStateConflict,
				"terminal proposal %s has no admission decision",
				occurrenceID,
			)
		}
		return AdmissionResult{}, admissionDecisionMetadata{}, fmt.Errorf("loading admission decision: %w", err)
	}
	result.ProposalOccurrenceID = occurrenceID
	if reviewBindingContractVersion.Valid {
		metadata.ReviewBindingContractVersion = reviewBindingContractVersion.String
	}
	if canonicalRef.Valid {
		result.CanonicalRef = canonicalRef.String
	}
	if err := json.Unmarshal(rawNodeIDsData, &result.RawEvidenceNodeIDs); err != nil {
		return AdmissionResult{}, admissionDecisionMetadata{}, fmt.Errorf("decoding raw evidence node IDs: %w", err)
	}
	if err := json.Unmarshal(edgeIDsData, &result.CanonicalEdgeIDs); err != nil {
		return AdmissionResult{}, admissionDecisionMetadata{}, fmt.Errorf("decoding canonical edge IDs: %w", err)
	}
	if err := loadAdmissionDerivationResult(ctx, tx, &result, &metadata); err != nil {
		return AdmissionResult{}, admissionDecisionMetadata{}, err
	}
	return result, metadata, nil
}

func loadAdmissionDerivationResult(
	ctx context.Context,
	tx sqlTx,
	result *AdmissionResult,
	metadata *admissionDecisionMetadata,
) error {
	if result.CanonicalRef == "" {
		return nil
	}
	var derivation evidencegraph.DerivationRecord
	var originProposalOccurrenceID string
	err := tx.queryRow(ctx, `
		SELECT
			derivation_id,
			node_id,
			method,
			producer,
			trace_ref,
			provenance_ref,
			origin_proposal_occurrence_id
		FROM canonical_derivations
		WHERE node_id = $1
	`, result.CanonicalRef).Scan(
		&derivation.ID,
		&derivation.NodeID,
		&derivation.Method,
		&derivation.Producer,
		&derivation.TraceRef,
		&derivation.ProvenanceRef,
		&originProposalOccurrenceID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("loading admission derivation: %w", err)
	}
	rows, err := tx.query(ctx, `
		SELECT parent_node_id, canonical_edge_id
		FROM canonical_derivation_parents
		WHERE derivation_id = $1
		ORDER BY parent_node_id
	`, derivation.ID)
	if err != nil {
		return fmt.Errorf("loading admission derivation parents: %w", err)
	}
	defer rows.Close()
	parentEdges := make(map[string]string)
	for rows.Next() {
		var parentID, edgeID string
		if err := rows.Scan(&parentID, &edgeID); err != nil {
			return fmt.Errorf("scanning admission derivation parent: %w", err)
		}
		derivation.Parents = append(derivation.Parents, parentID)
		parentEdges[parentID] = edgeID
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating admission derivation parents: %w", err)
	}
	if len(derivation.Parents) == 0 {
		return fmt.Errorf("persisted derivation %q has no parents", derivation.ID)
	}
	result.DerivationID = derivation.ID
	result.ParentNodeIDs = append(result.ParentNodeIDs, derivation.Parents...)
	metadata.Derivation = &derivation
	metadata.DerivationParentEdges = parentEdges
	metadata.DerivationOriginProposalOccurrenceID = originProposalOccurrenceID
	return nil
}

func loadCanonicalNode(ctx context.Context, db sqlQueryer, canonicalID string) (CanonicalQueryResult, error) {
	var result CanonicalQueryResult
	var nodeKind string
	var payloadData, provenanceData, temporalData, integrityData []byte
	err := db.queryRow(ctx, `
		SELECT
			canonical_node_id,
			node_kind,
			payload,
			provenance,
			temporal,
			integrity,
			origin_proposal_occurrence_id
		FROM canonical_graph_nodes
		WHERE canonical_node_id = $1
	`, canonicalID).Scan(
		&result.CanonicalID,
		&nodeKind,
		&payloadData,
		&provenanceData,
		&temporalData,
		&integrityData,
		&result.OriginProposalOccurrenceID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CanonicalQueryResult{}, newDomainError(ErrorMissingSourceViewAttempt, "canonical record %s not found", canonicalID)
		}
		return CanonicalQueryResult{}, fmt.Errorf("querying canonical record: %w", err)
	}
	result.NodeKind = evidencegraph.CanonicalNodeKind(nodeKind)
	if err := json.Unmarshal(payloadData, &result.Payload); err != nil {
		return CanonicalQueryResult{}, fmt.Errorf("decoding canonical payload: %w", err)
	}
	if err := json.Unmarshal(provenanceData, &result.Provenance); err != nil {
		return CanonicalQueryResult{}, fmt.Errorf("decoding canonical provenance: %w", err)
	}
	if err := json.Unmarshal(temporalData, &result.Temporal); err != nil {
		return CanonicalQueryResult{}, fmt.Errorf("decoding canonical temporal: %w", err)
	}
	if err := json.Unmarshal(integrityData, &result.Integrity); err != nil {
		return CanonicalQueryResult{}, fmt.Errorf("decoding canonical integrity: %w", err)
	}
	return result, nil
}

func scanProposalQueryRow(row sqlRow, occurrenceID string) (ProposalQueryResult, error) {
	result, err := scanProposalQueryResult(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProposalQueryResult{}, newDomainError(ErrorMissingSourceViewAttempt, "proposal occurrence %s not found", occurrenceID)
		}
		return ProposalQueryResult{}, fmt.Errorf("querying proposal occurrence: %w", err)
	}
	return result, nil
}

const derivationAdmissionLockKey int64 = 4704080862826080598

func validateDerivedAdmissionInvariant(ctx context.Context, tx sqlTx, mutation canonicalAdmissionMutation) error {
	return validateDerivedAdmissionWithLock(ctx, tx, mutation, false)
}

// reviewedLock uses the schema-pinned, lock-only definer companion. The original
// native path and its FOR KEY SHARE remain unchanged; no node UPDATE is granted.
func validateDerivedAdmissionWithLock(ctx context.Context, tx sqlTx, mutation canonicalAdmissionMutation, reviewedLock bool) error {
	if mutation.derivation == nil || len(mutation.nodes) != 1 {
		return newDomainError(ErrorDerivationInvariant, "derived admission must create exactly one derived node")
	}
	derivation := *mutation.derivation
	if mutation.nodes[0].ID != derivation.NodeID || mutation.nodes[0].Kind != evidencegraph.CanonicalDerivedClaim {
		return newDomainError(ErrorDerivationInvariant, "derivation target must be the new derived claim")
	}
	if derivation.ProvenanceRef != mutation.nodes[0].Provenance.ID {
		return newDomainError(ErrorDerivationInvariant, "derivation provenance must reference the derived claim provenance")
	}
	if len(mutation.edges) != len(derivation.Parents) || len(mutation.derivationParentEdges) != len(derivation.Parents) {
		return newDomainError(ErrorDerivationInvariant, "every derivation parent must have exactly one derived_from edge")
	}
	edges := make(map[string]CanonicalGraphEdge, len(mutation.edges))
	for _, edge := range mutation.edges {
		edges[edge.ID] = edge
	}
	for _, parent := range derivation.Parents {
		edgeID, exists := mutation.derivationParentEdges[parent]
		edge, edgeExists := edges[edgeID]
		if !exists || !edgeExists || edge.From != parent || edge.To != derivation.NodeID || edge.Relation != evidencegraph.CanonicalDerivedFrom {
			return newDomainError(ErrorDerivationInvariant, "derivation parent %q has no exact derived_from edge", parent)
		}
	}
	if _, err := tx.exec(ctx, `SELECT pg_advisory_xact_lock($1)`, derivationAdmissionLockKey); err != nil {
		return fmt.Errorf("locking canonical derivation admission: %w", err)
	}

	requested := append([]string{derivation.NodeID}, derivation.Parents...)
	nodeQuery := `
  SELECT canonical_node_id FROM canonical_graph_nodes
  WHERE canonical_node_id = ANY($1::text[]) FOR KEY SHARE
 `
	if reviewedLock {
		nodeQuery = "SELECT canonical_endpoint_lock_nodes_v1($1::text[])"
	}
	rows, err := tx.query(ctx, nodeQuery, requested)
	if err != nil {
		return fmt.Errorf("loading derivation admission nodes: %w", err)
	}
	existing := make(map[string]struct{}, len(requested))
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return fmt.Errorf("scanning derivation admission node: %w", err)
		}
		existing[nodeID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterating derivation admission nodes: %w", err)
	}
	rows.Close()
	if _, exists := existing[derivation.NodeID]; exists {
		return newDomainError(ErrorDerivationInvariant, "derived target %q already exists; admission may only append a new immutable node", derivation.NodeID)
	}
	for _, parent := range derivation.Parents {
		if parent == derivation.NodeID {
			return newDomainError(ErrorDerivationInvariant, "derived target %q cannot be its own parent", derivation.NodeID)
		}
		if _, exists := existing[parent]; !exists {
			return newDomainError(ErrorDerivationInvariant, "derivation parent %q is not an admitted canonical node", parent)
		}
	}

	var witness []string
	err = tx.queryRow(ctx, `
		WITH RECURSIVE descendants(node_id, path) AS (
			SELECT $1::text, ARRAY[$1::text]
			UNION ALL
			SELECT edge.to_node_id, descendants.path || edge.to_node_id
			FROM descendants
			JOIN canonical_graph_edges edge
				ON edge.from_node_id = descendants.node_id
				AND edge.relation = 'derived_from'
			WHERE NOT edge.to_node_id = ANY(descendants.path)
		)
		SELECT path
		FROM descendants
		WHERE node_id = ANY($2::text[])
			AND cardinality(path) > 1
		ORDER BY cardinality(path), path::text
		LIMIT 1
	`, derivation.NodeID, derivation.Parents).Scan(&witness)
	if err == nil {
		return newDomainError(ErrorDerivationInvariant, "derived admission would create a cycle: %s", strings.Join(witness, " -> "))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("checking derived admission cycle: %w", err)
	}
	return nil
}

func insertCanonicalNode(ctx context.Context, tx sqlTx, node CanonicalGraphNode, requireNew bool) error {
	payload, err := jsonBytes(node.Payload)
	if err != nil {
		return err
	}
	provenance, err := jsonBytes(node.Provenance)
	if err != nil {
		return err
	}
	temporal, err := jsonBytes(node.Temporal)
	if err != nil {
		return err
	}
	integrity, err := jsonBytes(node.Integrity)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO canonical_graph_nodes (
			canonical_node_id,
			node_kind,
			payload,
			provenance,
			temporal,
			integrity,
			origin_proposal_occurrence_id
		)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7)
	`
	if !requireNew {
		query += ` ON CONFLICT (canonical_node_id) DO NOTHING`
	}
	_, err = tx.exec(ctx, query,
		node.ID,
		string(node.Kind),
		string(payload),
		string(provenance),
		string(temporal),
		string(integrity),
		node.OriginProposalOccurrenceID,
	)
	if err != nil {
		return fmt.Errorf("upserting canonical graph node %s: %w", node.ID, err)
	}
	return nil
}

func insertCanonicalEdge(ctx context.Context, tx sqlTx, edge CanonicalGraphEdge, requireNew bool) error {
	provenance, err := jsonBytes(edge.Provenance)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO canonical_graph_edges (
			canonical_edge_id,
			from_node_id,
			to_node_id,
			relation,
			provenance,
			origin_proposal_occurrence_id
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
	`
	if !requireNew {
		query += ` ON CONFLICT (canonical_edge_id) DO NOTHING`
	}
	_, err = tx.exec(ctx, query,
		edge.ID,
		edge.From,
		edge.To,
		string(edge.Relation),
		string(provenance),
		edge.OriginProposalOccurrenceID,
	)
	if err != nil {
		return fmt.Errorf("upserting canonical graph edge %s: %w", edge.ID, err)
	}
	return nil
}

func insertCanonicalDerivation(
	ctx context.Context,
	tx sqlTx,
	derivation evidencegraph.DerivationRecord,
	parentEdges map[string]string,
	originProposalOccurrenceID string,
) error {
	if _, err := tx.exec(ctx, `
		INSERT INTO canonical_derivations (
			derivation_id,
			node_id,
			method,
			producer,
			trace_ref,
			provenance_ref,
			origin_proposal_occurrence_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		derivation.ID,
		derivation.NodeID,
		derivation.Method,
		derivation.Producer,
		derivation.TraceRef,
		derivation.ProvenanceRef,
		originProposalOccurrenceID,
	); err != nil {
		return fmt.Errorf("inserting canonical derivation %s: %w", derivation.ID, err)
	}
	for _, parent := range derivation.Parents {
		edgeID, ok := parentEdges[parent]
		if !ok {
			return newDomainError(ErrorDerivationInvariant, "derivation parent %q has no derived_from edge", parent)
		}
		if _, err := tx.exec(ctx, `
			INSERT INTO canonical_derivation_parents (
				derivation_id,
				parent_node_id,
				canonical_edge_id
			)
			VALUES ($1, $2, $3)
		`, derivation.ID, parent, edgeID); err != nil {
			return fmt.Errorf("inserting canonical derivation parent %s/%s: %w", derivation.ID, parent, err)
		}
	}
	return nil
}

func insertAdmissionDecision(
	ctx context.Context,
	tx sqlTx,
	decision admissionDecision,
	metadata admissionDecisionMetadata,
) error {
	rawEvidenceNodeIDs := decision.RawEvidenceNodeIDs
	if rawEvidenceNodeIDs == nil {
		rawEvidenceNodeIDs = []string{}
	}
	canonicalEdgeIDs := decision.CanonicalEdgeIDs
	if canonicalEdgeIDs == nil {
		canonicalEdgeIDs = []string{}
	}
	rawNodeIDs, err := jsonBytes(rawEvidenceNodeIDs)
	if err != nil {
		return err
	}
	edgeIDs, err := jsonBytes(canonicalEdgeIDs)
	if err != nil {
		return err
	}
	var canonicalRef any
	if decision.CanonicalRef != "" {
		canonicalRef = decision.CanonicalRef
	}
	var reviewBindingContractVersion any
	if metadata.ReviewBindingContractVersion != "" {
		reviewBindingContractVersion = metadata.ReviewBindingContractVersion
	}
	_, err = tx.exec(ctx, `
		INSERT INTO admission_decisions (
			admission_decision_id,
			proposal_occurrence_id,
			outcome,
			canonical_ref,
			raw_evidence_node_ids,
			canonical_edge_ids,
			decision_by,
			decision_reason,
			review_binding_contract_version
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9)
	`,
		decision.ID,
		decision.ProposalOccurrence,
		decision.Outcome,
		canonicalRef,
		string(rawNodeIDs),
		string(edgeIDs),
		metadata.DecisionBy,
		metadata.DecisionReason,
		reviewBindingContractVersion,
	)
	if err != nil {
		return fmt.Errorf("inserting admission decision %s: %w", decision.ID, err)
	}
	return nil
}

func markProposalAdmitted(ctx context.Context, tx sqlTx, occurrenceID, canonicalRef string) error {
	tag, err := tx.exec(ctx, `
		UPDATE proposal_occurrences
		SET admission_outcome = 'admitted',
			canonical_ref = $2
		WHERE proposal_occurrence_id = $1
			AND admission_outcome = 'pending'
	`, occurrenceID, canonicalRef)
	if err != nil {
		return fmt.Errorf("marking proposal admitted: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorAdmissionStateConflict, "proposal %s was not pending during admission", occurrenceID)
	}
	return nil
}
