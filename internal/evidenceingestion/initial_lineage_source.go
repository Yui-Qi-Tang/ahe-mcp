package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"maps"
	"reflect"
	"time"
)

func replayCanonicalSourceBasisAdmission(ctx context.Context, tx sqlTx, proposal ProposalQueryResult, events []evidencesupersession.ClosureEventRecord) error {
	admission, metadata, err := loadAdmissionDecisionResult(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.ProposalOccurrence != proposal.ProposalOccurrenceID {
			continue
		}
		basis := event.Payload.LineageBasis
		_, err := replaySupersessionAdmission(ctx, tx, proposal, SupersessionAdmissionInput{
			ProposalOccurrenceID: proposal.ProposalOccurrenceID, DecisionBy: metadata.DecisionBy, DecisionReason: metadata.DecisionReason,
			Basis:         SupersessionLineageBasis{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: basis.SlotID},
			TargetNodeIDs: event.Payload.TargetNodeIDs, ExpectedRevision: event.PreviousRevision, ExpectedHeadEventID: event.PreviousEventID,
		}, basis)
		return err
	}
	input := AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID, DecisionBy: metadata.DecisionBy, DecisionReason: metadata.DecisionReason}
	if err := validateAdmissionReplay(proposal, input, admission, metadata); err != nil {
		return err
	}
	return validatePersistedOrdinaryAdmissionMutation(ctx, tx, proposal, input)
}

func validateCanonicalSourceBasisIntake(ctx context.Context, tx sqlQueryer, proposal ProposalQueryResult) error {
	source, err := loadManualSourceContext(ctx, tx, proposal.SourceSnapshotID, proposal.ExtractionViewID)
	if err != nil {
		return err
	}
	var raw []byte
	if err := tx.queryRow(ctx, `SELECT raw_content FROM source_blobs WHERE raw_content_hash=$1`, proposal.RawContentHash).Scan(&raw); err != nil {
		return fmt.Errorf("loading native source basis raw bytes: %w", err)
	}
	expectedSnapshot, expectedView, expectedSpans, err := buildManualSource(ManualTextInput{
		SourceSystem: proposal.SourceSystem, SourceID: proposal.SourceID, SourceVersion: proposal.SourceVersion, Raw: raw, OriginMetadata: proposal.OriginMetadata,
	})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expectedSnapshot, source.SourceSnapshot) || !reflect.DeepEqual(expectedView, source.ExtractionView) || !reflect.DeepEqual(expectedSpans, source.Spans) {
		return newDomainError(ErrorSupersessionInvariant, "native source basis source, view or complete span catalog differs from original bytes")
	}
	var receipt externalSourceReceipt
	var intakeSnapshot, intakeView, intakeHash string
	// Select one persisted intake witness, not an audit of all intake traffic.
	// Its source payload and snapshot/view association must reproduce exactly.
	if err := tx.queryRow(ctx, `SELECT r.request_id,r.source_snapshot_id,r.extraction_view_id,r.envelope_schema_version,r.collector_id,r.connector_id,r.observed_at,r.receipt_payload_hash,
		i.source_snapshot_id,i.extraction_view_id,i.request_payload_hash
		FROM external_source_intake_receipts r JOIN source_intake_requests i USING(request_id)
		WHERE r.source_snapshot_id=$1 AND r.extraction_view_id=$2 ORDER BY r.request_id LIMIT 1`, proposal.SourceSnapshotID, proposal.ExtractionViewID).Scan(
		&receipt.RequestID, &receipt.SourceSnapshotID, &receipt.ExtractionViewID, &receipt.EnvelopeSchemaVersion, &receipt.CollectorID, &receipt.ConnectorID, &receipt.ObservedAt, &receipt.ReceiptPayloadHash,
		&intakeSnapshot, &intakeView, &intakeHash,
	); err != nil {
		return fmt.Errorf("loading native external source intake mirror: %w", err)
	}
	origin := proposal.OriginMetadata
	var limitations []string
	if err := json.Unmarshal([]byte(origin[externalSourceOriginLimitsKey]), &limitations); err != nil || limitations == nil {
		return newDomainError(ErrorSupersessionInvariant, "native external source limitations require an explicit array")
	}
	prepared, err := prepareExternalSource(ExternalSourceEnvelopeV1{
		SchemaVersion: receipt.EnvelopeSchemaVersion, RequestID: receipt.RequestID,
		SourceSystem: origin[externalSourceOriginSystemKey], SourceNamespace: origin[externalSourceOriginNamespaceKey], ObjectType: origin[externalSourceOriginObjectTypeKey], ObjectID: origin[externalSourceOriginObjectIDKey],
		Revision: proposal.SourceVersion, SourceLocation: origin[externalSourceOriginLocationKey], Title: origin[externalSourceOriginTitleKey],
		ContentFormat: origin[externalSourceOriginFormatKey], ContentFidelity: origin[externalSourceOriginFidelityKey], Content: string(raw),
		Coverage: origin[externalSourceOriginCoverageKey], Limitations: limitations,
		CollectorID: receipt.CollectorID, ConnectorID: receipt.ConnectorID, ObservedAt: receipt.ObservedAt.UTC().Format(time.RFC3339Nano),
		SourceCreatedAt: origin[externalSourceOriginCreatedAtKey], SourceUpdatedAt: origin[externalSourceOriginUpdatedAtKey],
	})
	if err != nil {
		return canonicalSupersessionClosureInvariantError(err)
	}
	wantHash, err := sourceIntakePayloadHash(prepared.manual, expectedSnapshot, expectedView)
	if err != nil {
		return err
	}
	// observed_at is stored as timestamptz (microseconds), while the existing
	// audit receipt hashes the original RFC3339Nano value. Do not reinterpret
	// that lossy audit timestamp as source identity or claim to rehash it.
	if intakeSnapshot != proposal.SourceSnapshotID || intakeView != proposal.ExtractionViewID || prepared.manual.SourceID != proposal.SourceID || !maps.Equal(prepared.manual.OriginMetadata, origin) || wantHash != intakeHash {
		return newDomainError(ErrorSupersessionInvariant, "native external source intake does not reproduce its immutable authority")
	}
	return nil
}
