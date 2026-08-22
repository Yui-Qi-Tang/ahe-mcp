package evidenceingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ExternalSourceEnvelopeSchemaV1 is the provider-neutral source intake contract.
	ExternalSourceEnvelopeSchemaV1 = "external-source-envelope-v1"

	// ExternalSourceContentFidelityVerbatim requires connector-observed content rather than a model summary.
	ExternalSourceContentFidelityVerbatim = "verbatim"

	// ExternalSourceCoverageFullDocument asserts that content covers the complete declared object projection.
	ExternalSourceCoverageFullDocument = "full_document"
	// ExternalSourceCoverageExactExcerpt identifies an intentional exact excerpt from a larger source object.
	ExternalSourceCoverageExactExcerpt = "exact_excerpt"
	// ExternalSourceCoverageTruncatedDocument identifies content cut short by a connector or transport limit.
	ExternalSourceCoverageTruncatedDocument = "truncated_document"

	// ExternalSourceContentFormatPlainText identifies exact plain-text source content.
	ExternalSourceContentFormatPlainText = "text/plain"
	// ExternalSourceContentFormatMarkdown identifies exact Markdown source content.
	ExternalSourceContentFormatMarkdown = "text/markdown"
	// ExternalSourceContentFormatJSON identifies exact JSON source content carried as a string.
	ExternalSourceContentFormatJSON = "application/json"

	// ExternalSourceContentMaxBytes caps one external source object before persistence.
	ExternalSourceContentMaxBytes = 512 << 10
	// ExternalSourceContentMaxLines caps deterministic line-span materialization.
	ExternalSourceContentMaxLines = 4096
	// ExternalSourceLimitationsMaxCount caps explicit incompleteness disclosures.
	ExternalSourceLimitationsMaxCount = 32
	// ExternalSourceLimitationMaxBytes caps one incompleteness disclosure.
	ExternalSourceLimitationMaxBytes = 500

	externalSourceSystemMaxBytes         = 64
	externalSourceNamespaceMaxBytes      = 200
	externalSourceObjectTypeMaxBytes     = 100
	externalSourceObjectIDMaxBytes       = 500
	externalSourceRevisionMaxBytes       = 500
	externalSourceLocationMaxBytes       = 2000
	externalSourceTitleMaxBytes          = 2000
	externalSourceActorIDMaxBytes        = 200
	externalSourceRequestIDMaxBytes      = 200
	externalSourceOriginSchemaKey        = "external_source_schema"
	externalSourceOriginSystemKey        = "external_source_system"
	externalSourceOriginNamespaceKey     = "external_source_namespace"
	externalSourceOriginObjectTypeKey    = "external_object_type"
	externalSourceOriginObjectIDKey      = "external_object_id"
	externalSourceOriginRevisionKey      = "external_revision"
	externalSourceOriginLocationKey      = "external_source_location"
	externalSourceOriginTitleKey         = "external_title"
	externalSourceOriginFormatKey        = "external_content_format"
	externalSourceOriginFidelityKey      = "external_content_fidelity"
	externalSourceOriginCoverageKey      = "external_coverage"
	externalSourceOriginLimitsKey        = "external_limitations_json"
	externalSourceOriginGlobalAbsenceKey = "global_absence_inference_allowed"
	externalSourceOriginCreatedAtKey     = "external_source_created_at"
	externalSourceOriginUpdatedAtKey     = "external_source_updated_at"
)

// ExternalSourceEnvelopeV1 carries one exact external source object into AHE.
// The caller owns collection; AHE owns validation, hashing, receipt time, and persistence.
type ExternalSourceEnvelopeV1 struct {
	SchemaVersion   string   `json:"schema_version"`
	RequestID       string   `json:"request_id"`
	SourceSystem    string   `json:"source_system"`
	SourceNamespace string   `json:"source_namespace"`
	ObjectType      string   `json:"object_type"`
	ObjectID        string   `json:"object_id"`
	Revision        string   `json:"revision"`
	SourceLocation  string   `json:"source_location"`
	Title           string   `json:"title,omitempty"`
	ContentFormat   string   `json:"content_format"`
	ContentFidelity string   `json:"content_fidelity"`
	Content         string   `json:"content"`
	Coverage        string   `json:"coverage"`
	Limitations     []string `json:"limitations,omitempty"`
	CollectorID     string   `json:"collector_id"`
	ConnectorID     string   `json:"connector_id"`
	ObservedAt      string   `json:"observed_at"`
	SourceCreatedAt string   `json:"source_created_at,omitempty"`
	SourceUpdatedAt string   `json:"source_updated_at,omitempty"`
}

// ExternalSourceIntakeResult reports the immutable source identity and AHE-controlled receipt time.
type ExternalSourceIntakeResult struct {
	SourceIntakeResult
	ExternalSourceSystem string
	SourceNamespace      string
	ObjectType           string
	ObjectID             string
	Revision             string
	Coverage             string
	ObservedAt           time.Time
	ReceivedAt           time.Time
}

type preparedExternalSource struct {
	envelope ExternalSourceEnvelopeV1
	manual   ManualTextInput
	receipt  externalSourceReceipt
}

type externalSourceReceipt struct {
	RequestID             string
	SourceSnapshotID      string
	ExtractionViewID      string
	EnvelopeSchemaVersion string
	CollectorID           string
	ConnectorID           string
	ObservedAt            time.Time
	ReceiptPayloadHash    string
}

// CaptureExternalSource validates and persists one provider-neutral external source envelope.
// It creates source authority and deterministic spans only; it does not create or admit proposals.
func CaptureExternalSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ExternalSourceEnvelopeV1,
) (ExternalSourceIntakeResult, error) {
	if pool == nil {
		return ExternalSourceIntakeResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return captureExternalSource(ctx, pgxDB{pool: pool}, input)
}

func captureExternalSource(
	ctx context.Context,
	db sqlDB,
	input ExternalSourceEnvelopeV1,
) (ExternalSourceIntakeResult, error) {
	prepared, err := prepareExternalSource(input)
	if err != nil {
		return ExternalSourceIntakeResult{}, err
	}
	sourceCtx, err := buildManualSourceContext(prepared.manual)
	if err != nil {
		return ExternalSourceIntakeResult{}, err
	}
	prepared.receipt.SourceSnapshotID = sourceCtx.SourceSnapshot.ID
	prepared.receipt.ExtractionViewID = sourceCtx.ExtractionView.ID
	changed, err := persistExternalSource(ctx, db, sourceCtx, prepared.receipt)
	if err != nil {
		return ExternalSourceIntakeResult{}, err
	}
	receivedAt, err := externalSourceReceivedAt(ctx, db, prepared.envelope.RequestID)
	if err != nil {
		return ExternalSourceIntakeResult{}, err
	}
	return ExternalSourceIntakeResult{
		SourceIntakeResult:   sourceCtx.result(!changed),
		ExternalSourceSystem: prepared.envelope.SourceSystem,
		SourceNamespace:      prepared.envelope.SourceNamespace,
		ObjectType:           prepared.envelope.ObjectType,
		ObjectID:             prepared.envelope.ObjectID,
		Revision:             prepared.envelope.Revision,
		Coverage:             prepared.envelope.Coverage,
		ObservedAt:           prepared.receipt.ObservedAt,
		ReceivedAt:           receivedAt,
	}, nil
}

func prepareExternalSource(input ExternalSourceEnvelopeV1) (preparedExternalSource, error) {
	input.Limitations = append([]string(nil), input.Limitations...)
	input.SchemaVersion = strings.TrimSpace(input.SchemaVersion)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.SourceSystem = strings.TrimSpace(input.SourceSystem)
	input.SourceNamespace = strings.TrimSpace(input.SourceNamespace)
	input.ObjectType = strings.TrimSpace(input.ObjectType)
	input.ObjectID = strings.TrimSpace(input.ObjectID)
	input.Revision = strings.TrimSpace(input.Revision)
	input.SourceLocation = strings.TrimSpace(input.SourceLocation)
	input.Title = strings.TrimSpace(input.Title)
	input.ContentFormat = strings.TrimSpace(input.ContentFormat)
	input.ContentFidelity = strings.TrimSpace(input.ContentFidelity)
	input.Coverage = strings.TrimSpace(input.Coverage)
	input.CollectorID = strings.TrimSpace(input.CollectorID)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)

	if input.SchemaVersion != ExternalSourceEnvelopeSchemaV1 {
		return preparedExternalSource{}, newDomainError(
			ErrorInvalidInput,
			"schema_version must be %q",
			ExternalSourceEnvelopeSchemaV1,
		)
	}
	for _, field := range []struct {
		name     string
		value    string
		maxBytes int
	}{
		{name: "request_id", value: input.RequestID, maxBytes: externalSourceRequestIDMaxBytes},
		{name: "source_system", value: input.SourceSystem, maxBytes: externalSourceSystemMaxBytes},
		{name: "source_namespace", value: input.SourceNamespace, maxBytes: externalSourceNamespaceMaxBytes},
		{name: "object_type", value: input.ObjectType, maxBytes: externalSourceObjectTypeMaxBytes},
		{name: "object_id", value: input.ObjectID, maxBytes: externalSourceObjectIDMaxBytes},
		{name: "revision", value: input.Revision, maxBytes: externalSourceRevisionMaxBytes},
		{name: "source_location", value: input.SourceLocation, maxBytes: externalSourceLocationMaxBytes},
		{name: "collector_id", value: input.CollectorID, maxBytes: externalSourceActorIDMaxBytes},
		{name: "connector_id", value: input.ConnectorID, maxBytes: externalSourceActorIDMaxBytes},
	} {
		if err := validateExternalSourceString(field.name, field.value, field.maxBytes, true); err != nil {
			return preparedExternalSource{}, err
		}
	}
	if err := validateExternalSourceString("title", input.Title, externalSourceTitleMaxBytes, false); err != nil {
		return preparedExternalSource{}, err
	}
	if !isExternalSourceSystemToken(input.SourceSystem) {
		return preparedExternalSource{}, newDomainError(
			ErrorInvalidInput,
			"source_system must use lower-case letters, digits, dot, underscore, or hyphen",
		)
	}

	switch input.ContentFormat {
	case ExternalSourceContentFormatPlainText,
		ExternalSourceContentFormatMarkdown,
		ExternalSourceContentFormatJSON:
	default:
		return preparedExternalSource{}, newDomainError(ErrorInvalidInput, "content_format %q is not supported", input.ContentFormat)
	}
	if input.ContentFidelity != ExternalSourceContentFidelityVerbatim {
		return preparedExternalSource{}, newDomainError(
			ErrorInvalidInput,
			"content_fidelity must be %q; model summaries and paraphrases are not source authority",
			ExternalSourceContentFidelityVerbatim,
		)
	}
	if !utf8.ValidString(input.Content) {
		return preparedExternalSource{}, newDomainError(ErrorInvalidUTF8, "external source content is not valid UTF-8")
	}
	if input.Content == "" {
		return preparedExternalSource{}, newDomainError(ErrorInvalidInput, "content is required")
	}
	if input.ContentFormat == ExternalSourceContentFormatJSON && !json.Valid([]byte(input.Content)) {
		return preparedExternalSource{}, newDomainError(ErrorInvalidInput, "content is not valid JSON for application/json")
	}
	if len(input.Content) > ExternalSourceContentMaxBytes {
		return preparedExternalSource{}, newDomainError(
			ErrorInvalidInput,
			"content exceeds %d UTF-8 bytes",
			ExternalSourceContentMaxBytes,
		)
	}
	if lineCount := bytes.Count([]byte(input.Content), []byte{'\n'}) + 1; lineCount > ExternalSourceContentMaxLines {
		return preparedExternalSource{}, newDomainError(
			ErrorInvalidInput,
			"content has %d lines, maximum is %d",
			lineCount,
			ExternalSourceContentMaxLines,
		)
	}

	switch input.Coverage {
	case ExternalSourceCoverageFullDocument:
		if len(input.Limitations) != 0 {
			return preparedExternalSource{}, newDomainError(ErrorInvalidInput, "full_document coverage cannot declare limitations")
		}
	case ExternalSourceCoverageExactExcerpt, ExternalSourceCoverageTruncatedDocument:
		if len(input.Limitations) == 0 {
			return preparedExternalSource{}, newDomainError(ErrorInvalidInput, "%s coverage requires at least one limitation", input.Coverage)
		}
	default:
		return preparedExternalSource{}, newDomainError(ErrorInvalidInput, "coverage %q is not supported", input.Coverage)
	}
	if len(input.Limitations) > ExternalSourceLimitationsMaxCount {
		return preparedExternalSource{}, newDomainError(
			ErrorInvalidInput,
			"limitations contains %d entries, maximum is %d",
			len(input.Limitations),
			ExternalSourceLimitationsMaxCount,
		)
	}
	if input.Limitations == nil {
		input.Limitations = []string{}
	}
	for index, limitation := range input.Limitations {
		limitation = strings.TrimSpace(limitation)
		if err := validateExternalSourceString(
			fmt.Sprintf("limitations[%d]", index),
			limitation,
			ExternalSourceLimitationMaxBytes,
			true,
		); err != nil {
			return preparedExternalSource{}, err
		}
		input.Limitations[index] = limitation
	}

	var observedAt, sourceCreatedAt, sourceUpdatedAt time.Time
	var err error
	input.ObservedAt, observedAt, err = normalizeExternalSourceTime("observed_at", input.ObservedAt, true)
	if err != nil {
		return preparedExternalSource{}, err
	}
	input.SourceCreatedAt, sourceCreatedAt, err = normalizeExternalSourceTime("source_created_at", input.SourceCreatedAt, false)
	if err != nil {
		return preparedExternalSource{}, err
	}
	input.SourceUpdatedAt, sourceUpdatedAt, err = normalizeExternalSourceTime("source_updated_at", input.SourceUpdatedAt, false)
	if err != nil {
		return preparedExternalSource{}, err
	}
	if !sourceCreatedAt.IsZero() && !sourceUpdatedAt.IsZero() && sourceUpdatedAt.Before(sourceCreatedAt) {
		return preparedExternalSource{}, newDomainError(ErrorInvalidInput, "source_updated_at is earlier than source_created_at")
	}

	externalID, err := stableID("extsrc:", "external_source", struct {
		SourceSystem    string `json:"source_system"`
		SourceNamespace string `json:"source_namespace"`
		ObjectType      string `json:"object_type"`
		ObjectID        string `json:"object_id"`
	}{
		SourceSystem:    input.SourceSystem,
		SourceNamespace: input.SourceNamespace,
		ObjectType:      input.ObjectType,
		ObjectID:        input.ObjectID,
	})
	if err != nil {
		return preparedExternalSource{}, err
	}
	limitationsJSON, err := json.Marshal(input.Limitations)
	if err != nil {
		return preparedExternalSource{}, fmt.Errorf("encoding external source limitations: %w", err)
	}
	origin := map[string]string{
		externalSourceOriginSchemaKey:        input.SchemaVersion,
		externalSourceOriginSystemKey:        input.SourceSystem,
		externalSourceOriginNamespaceKey:     input.SourceNamespace,
		externalSourceOriginObjectTypeKey:    input.ObjectType,
		externalSourceOriginObjectIDKey:      input.ObjectID,
		externalSourceOriginRevisionKey:      input.Revision,
		externalSourceOriginLocationKey:      input.SourceLocation,
		externalSourceOriginFormatKey:        input.ContentFormat,
		externalSourceOriginFidelityKey:      input.ContentFidelity,
		externalSourceOriginCoverageKey:      input.Coverage,
		externalSourceOriginLimitsKey:        string(limitationsJSON),
		externalSourceOriginGlobalAbsenceKey: "false",
	}
	if input.Title != "" {
		origin[externalSourceOriginTitleKey] = input.Title
	}
	if input.SourceCreatedAt != "" {
		origin[externalSourceOriginCreatedAtKey] = input.SourceCreatedAt
	}
	if input.SourceUpdatedAt != "" {
		origin[externalSourceOriginUpdatedAtKey] = input.SourceUpdatedAt
	}
	receiptPayload, err := deterministicJSON(struct {
		RequestID             string `json:"request_id"`
		EnvelopeSchemaVersion string `json:"envelope_schema_version"`
		CollectorID           string `json:"collector_id"`
		ConnectorID           string `json:"connector_id"`
		ObservedAt            string `json:"observed_at"`
	}{
		RequestID:             input.RequestID,
		EnvelopeSchemaVersion: input.SchemaVersion,
		CollectorID:           input.CollectorID,
		ConnectorID:           input.ConnectorID,
		ObservedAt:            input.ObservedAt,
	})
	if err != nil {
		return preparedExternalSource{}, fmt.Errorf("serializing external source receipt identity: %w", err)
	}
	return preparedExternalSource{
		envelope: input,
		manual: ManualTextInput{
			SourceSystem:   SourceSystemExternalDocument,
			SourceID:       externalID,
			SourceVersion:  input.Revision,
			Raw:            []byte(input.Content),
			OriginMetadata: origin,
			RequestID:      input.RequestID,
		},
		receipt: externalSourceReceipt{
			RequestID:             input.RequestID,
			EnvelopeSchemaVersion: input.SchemaVersion,
			CollectorID:           input.CollectorID,
			ConnectorID:           input.ConnectorID,
			ObservedAt:            observedAt,
			ReceiptPayloadHash:    contentHash(receiptPayload),
		},
	}, nil
}

func externalSourceReceivedAt(ctx context.Context, db sqlQueryer, requestID string) (time.Time, error) {
	var receivedAt time.Time
	err := db.queryRow(ctx, `
		SELECT created_at
		FROM external_source_intake_receipts
		WHERE request_id = $1
	`, requestID).Scan(&receivedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading source intake receipt %s: %w", requestID, err)
	}
	return receivedAt.UTC(), nil
}

func normalizeExternalSourceTime(field, value string, required bool) (string, time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", time.Time{}, newDomainError(ErrorInvalidInput, "%s is required", field)
		}
		return "", time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", time.Time{}, newDomainError(ErrorInvalidInput, "%s must be an RFC3339 timestamp", field)
	}
	parsed = parsed.UTC()
	return parsed.Format(time.RFC3339Nano), parsed, nil
}

func validateExternalSourceString(field, value string, maxBytes int, required bool) error {
	if value == "" {
		if required {
			return newDomainError(ErrorInvalidInput, "%s is required", field)
		}
		return nil
	}
	if !utf8.ValidString(value) {
		return newDomainError(ErrorInvalidUTF8, "%s is not valid UTF-8", field)
	}
	if len(value) > maxBytes {
		return newDomainError(ErrorInvalidInput, "%s exceeds %d UTF-8 bytes", field, maxBytes)
	}
	return nil
}

func isExternalSourceSystemToken(value string) bool {
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return value != ""
}
