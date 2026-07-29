package detective

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// MCPReadDocumentResultContract identifies one normalized provider read page.
	MCPReadDocumentResultContract = "ahe-mcp-read-document-v1"

	// MCPReadCompletionComplete means the requested page is complete.
	MCPReadCompletionComplete = "complete"
	// MCPReadCompletionNextPage means another explicit page cursor is available.
	MCPReadCompletionNextPage = "next_page"
	// MCPReadCompletionProviderLimit means the provider stopped without a next cursor.
	MCPReadCompletionProviderLimit = "provider_limit"
	// MCPReadCompletionAdapterLimit means the adapter budget stopped the read.
	MCPReadCompletionAdapterLimit = "adapter_limit"

	mcpReadDeliveryContract            = "detective-mcp-read-delivery-v1"
	mcpReadSourceIdentityContract      = "mcp-read-source-v2"
	mcpReadProposalCandidateContract   = "ahe-mcp-proposal-candidates-v1"
	maxMCPReadStructuredResultBytes    = 700 << 10
	maxMCPReadArgumentsBytes           = 64 << 10
	maxMCPReadDocumentBytes            = 512 << 10
	maxMCPReadRawProviderResponseBytes = 128 << 10
	maxMCPReadDocumentNonEmptyLines    = 4096
	maxMCPReadLimitations              = 32
	maxMCPReadLimitationBytes          = 500
	maxMCPReadProposalCandidates       = 128
	maxMCPReadProposalCandidateIDBytes = 200
	maxMCPReadProposalSelectorBytes    = 1000
	maxMCPReadProposalStatementBytes   = 64 << 10
)

const (
	// MCPReadProposalSelectorLine selects one exact non-empty document line.
	MCPReadProposalSelectorLine = "line"
	// MCPReadProposalSelectorJSONPointerString selects one JSON string value.
	MCPReadProposalSelectorJSONPointerString = "json_pointer_string"
)

// MCPReadCollectionInput selects one pinned provider-neutral read adapter tool.
type MCPReadCollectionInput struct {
	RequestID                   string
	ConnectorID                 string
	Provider                    string
	LogicalCapability           string
	AdapterName                 string
	AdapterVersion              string
	ProviderToolName            string
	ProviderToolInputSchemaHash string
	Arguments                   json.RawMessage
	Command                     mcpstdio.CommandConfig
}

// MCPReadCollectionResult identifies the durable raw invocation receipt.
type MCPReadCollectionResult struct {
	Receipt        ConnectorDeliveryReceipt
	ObjectID       string
	Revision       string
	DocumentID     string
	ResultHash     string
	RequestHash    string
	Coverage       MCPReadCoverage
	NextPageCursor string
}

// MCPReadProcessingResult identifies one source projection from a durable MCP read receipt.
type MCPReadProcessingResult struct {
	ConnectorDeliveryID string
	Provider            string
	ObjectID            string
	Revision            string
	DocumentID          string
	Coverage            MCPReadCoverage
	Source              evidenceingestion.SourceIntakeResult
}

// MCPReadCoverage records the adapter's bounded page coverage without implying global absence.
type MCPReadCoverage struct {
	Complete         bool   `json:"complete"`
	Truncated        bool   `json:"truncated"`
	CompletionReason string `json:"completion_reason"`
}

// MCPReadDocument is one exact document body returned by a provider adapter.
type MCPReadDocument struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text"`
	ContentHash string `json:"content_hash"`
}

// MCPReadProposalCandidate is an adapter-declared, source-verifiable candidate.
// It is not an admitted claim or canonical evidence.
type MCPReadProposalCandidate struct {
	LocalID      string `json:"local_id"`
	SelectorKind string `json:"selector_kind"`
	Selector     string `json:"selector"`
}

// MCPReadDocumentResult is the strict normalized result returned by a provider adapter tool.
type MCPReadDocumentResult struct {
	Contract                  string                     `json:"contract"`
	ObjectID                  string                     `json:"object_id"`
	Revision                  string                     `json:"revision"`
	SourceLocation            string                     `json:"source_location"`
	PageCursor                string                     `json:"page_cursor,omitempty"`
	NextPageCursor            string                     `json:"next_page_cursor,omitempty"`
	Coverage                  MCPReadCoverage            `json:"coverage"`
	Document                  MCPReadDocument            `json:"document"`
	ProposalCandidates        []MCPReadProposalCandidate `json:"proposal_candidates,omitempty"`
	RawProviderResponseBase64 string                     `json:"raw_provider_response_base64"`
	RawProviderResponseHash   string                     `json:"raw_provider_response_hash"`
	Limitations               []string                   `json:"limitations"`
}

type preparedMCPReadCollection struct {
	input       MCPReadCollectionInput
	requestHash string
}

type mcpReadDeliveryEnvelope struct {
	Contract                    string          `json:"contract"`
	Provider                    string          `json:"provider"`
	LogicalCapability           string          `json:"logical_capability"`
	AdapterName                 string          `json:"adapter_name"`
	AdapterVersion              string          `json:"adapter_version"`
	ProviderToolName            string          `json:"provider_tool_name"`
	ProviderToolInputSchemaHash string          `json:"provider_tool_input_schema_hash"`
	RequestHash                 string          `json:"request_hash"`
	Arguments                   json.RawMessage `json:"arguments"`
	ProtocolVersion             string          `json:"protocol_version"`
	ServerName                  string          `json:"server_name"`
	ServerVersion               string          `json:"server_version"`
	ReadOnlyHint                *bool           `json:"read_only_hint,omitempty"`
	DestructiveHint             *bool           `json:"destructive_hint,omitempty"`
	StructuredResultHash        string          `json:"structured_result_hash"`
	StructuredResultBase64      string          `json:"structured_result_base64"`
}

// CollectMCPReadDelivery calls one pinned read tool and stores the exact normalized result before projection.
func CollectMCPReadDelivery(
	ctx context.Context,
	pool *pgxpool.Pool,
	input MCPReadCollectionInput,
) (MCPReadCollectionResult, error) {
	if pool == nil {
		return MCPReadCollectionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareMCPReadCollection(input)
	if err != nil {
		return MCPReadCollectionResult{}, err
	}
	replay, found, err := replayMCPReadCollection(ctx, pool, prepared)
	if err != nil {
		return MCPReadCollectionResult{}, err
	}
	if found {
		return replay, nil
	}
	called, err := mcpstdio.CallTool(ctx, prepared.input.Command, mcpstdio.ToolCallRequest{
		Name:                    prepared.input.ProviderToolName,
		ExpectedInputSchemaHash: prepared.input.ProviderToolInputSchemaHash,
		Arguments:               prepared.input.Arguments,
	})
	if err != nil {
		if errors.Is(err, mcpstdio.ErrToolContract) {
			return MCPReadCollectionResult{}, newDomainError(ErrorMCPContractViolation, "%v", err)
		}
		return MCPReadCollectionResult{}, newDomainError(ErrorMCPTransportFailed, "%v", err)
	}
	schemaHash, err := mcpReadToolSchemaHash(called.Tool.InputSchema)
	if err != nil {
		return MCPReadCollectionResult{}, err
	}
	if schemaHash != prepared.input.ProviderToolInputSchemaHash {
		return MCPReadCollectionResult{}, newDomainError(
			ErrorMCPContractViolation,
			"provider tool input schema hash %s does not match pinned %s",
			schemaHash,
			prepared.input.ProviderToolInputSchemaHash,
		)
	}
	if len(called.StructuredContent) > maxMCPReadStructuredResultBytes {
		return MCPReadCollectionResult{}, newDomainError(
			ErrorMCPContractViolation,
			"structured result exceeds %d bytes",
			maxMCPReadStructuredResultBytes,
		)
	}
	document, err := decodeMCPReadDocumentResult(called.StructuredContent)
	if err != nil {
		return MCPReadCollectionResult{}, err
	}
	envelope := mcpReadDeliveryEnvelope{
		Contract:                    mcpReadDeliveryContract,
		Provider:                    prepared.input.Provider,
		LogicalCapability:           prepared.input.LogicalCapability,
		AdapterName:                 prepared.input.AdapterName,
		AdapterVersion:              prepared.input.AdapterVersion,
		ProviderToolName:            prepared.input.ProviderToolName,
		ProviderToolInputSchemaHash: schemaHash,
		RequestHash:                 prepared.requestHash,
		Arguments:                   prepared.input.Arguments,
		ProtocolVersion:             called.ProtocolVersion,
		ServerName:                  called.ServerName,
		ServerVersion:               called.ServerVersion,
		ReadOnlyHint:                called.Tool.Annotations.ReadOnlyHint,
		DestructiveHint:             called.Tool.Annotations.DestructiveHint,
		StructuredResultHash:        contentHash(called.StructuredContent),
		StructuredResultBase64:      base64.StdEncoding.EncodeToString(called.StructuredContent),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return MCPReadCollectionResult{}, fmt.Errorf("encoding mcp read delivery envelope: %w", err)
	}
	if len(payload) > maxConnectorDeliveryBytes {
		return MCPReadCollectionResult{}, newDomainError(
			ErrorMCPContractViolation,
			"mcp read delivery envelope exceeds %d bytes",
			maxConnectorDeliveryBytes,
		)
	}
	receipt, err := ReceiveConnectorDelivery(ctx, pool, ConnectorDeliveryInput{
		RequestID:          prepared.input.RequestID,
		ConnectorID:        prepared.input.ConnectorID,
		ExternalDeliveryID: mcpReadExternalDeliveryID(prepared.input.Provider, document),
		ContentType:        ConnectorDeliveryContentTypeJSON,
		Payload:            payload,
	})
	if err != nil {
		return MCPReadCollectionResult{}, err
	}
	return mcpReadCollectionResult(receipt, envelope, document), nil
}

// ProcessMCPReadDelivery projects one exact stored MCP document into existing source authority.
func ProcessMCPReadDelivery(
	ctx context.Context,
	pool *pgxpool.Pool,
	connectorDeliveryID string,
) (MCPReadProcessingResult, error) {
	if pool == nil {
		return MCPReadProcessingResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	connectorDeliveryID, err := normalizeConnectorDeliveryID(connectorDeliveryID)
	if err != nil {
		return MCPReadProcessingResult{}, err
	}
	delivery, found, err := readConnectorDelivery(ctx, pool, connectorDeliveryID, false)
	if err != nil {
		return MCPReadProcessingResult{}, err
	}
	if !found {
		return MCPReadProcessingResult{}, newDomainError(
			ErrorConnectorDeliveryNotFound,
			"connector delivery %s was not found",
			connectorDeliveryID,
		)
	}
	envelope, document, err := decodeMCPReadDelivery(delivery)
	if err != nil {
		return MCPReadProcessingResult{}, err
	}
	limitations, err := json.Marshal(document.Limitations)
	if err != nil {
		return MCPReadProcessingResult{}, fmt.Errorf("encoding mcp read limitations: %w", err)
	}
	proposalCandidates := document.ProposalCandidates
	if proposalCandidates == nil {
		proposalCandidates = []MCPReadProposalCandidate{}
	}
	proposalCandidatesJSON, err := json.Marshal(proposalCandidates)
	if err != nil {
		return MCPReadProcessingResult{}, fmt.Errorf("encoding mcp read proposal candidates: %w", err)
	}
	sourceID := mcpReadSourceID(
		envelope.Provider,
		envelope.AdapterName,
		envelope.AdapterVersion,
		document,
		proposalCandidatesJSON,
	)
	source, err := evidenceingestion.CaptureManualSource(ctx, pool, evidenceingestion.ManualTextInput{
		SourceSystem:  evidenceingestion.SourceSystemMCPReadDocument,
		SourceID:      sourceID,
		SourceVersion: document.Revision,
		Raw:           []byte(document.Document.Text),
		OriginMetadata: map[string]string{
			"connector_delivery_id":            delivery.receipt.ConnectorDeliveryID,
			"connector_id":                     delivery.receipt.ConnectorID,
			"connector_payload_hash":           delivery.receipt.PayloadHash,
			"external_delivery_id":             delivery.receipt.ExternalDeliveryID,
			"mcp_delivery_contract":            envelope.Contract,
			"mcp_provider":                     envelope.Provider,
			"mcp_logical_capability":           envelope.LogicalCapability,
			"mcp_adapter_name":                 envelope.AdapterName,
			"mcp_adapter_version":              envelope.AdapterVersion,
			"mcp_provider_tool_name":           envelope.ProviderToolName,
			"mcp_provider_tool_schema_hash":    envelope.ProviderToolInputSchemaHash,
			"mcp_request_hash":                 envelope.RequestHash,
			"mcp_protocol_version":             envelope.ProtocolVersion,
			"mcp_server_name":                  envelope.ServerName,
			"mcp_server_version":               envelope.ServerVersion,
			"mcp_structured_result_hash":       envelope.StructuredResultHash,
			"mcp_object_id":                    document.ObjectID,
			"mcp_revision":                     document.Revision,
			"mcp_source_location":              document.SourceLocation,
			"mcp_page_cursor":                  document.PageCursor,
			"mcp_next_page_cursor":             document.NextPageCursor,
			"mcp_coverage_complete":            fmt.Sprint(document.Coverage.Complete),
			"mcp_coverage_truncated":           fmt.Sprint(document.Coverage.Truncated),
			"mcp_coverage_completion_reason":   document.Coverage.CompletionReason,
			"mcp_raw_provider_response_hash":   document.RawProviderResponseHash,
			"mcp_document_id":                  document.Document.ID,
			"mcp_document_title":               document.Document.Title,
			"mcp_document_content_hash":        document.Document.ContentHash,
			"mcp_source_identity_contract":     mcpReadSourceIdentityContract,
			"mcp_proposal_candidate_contract":  mcpReadProposalCandidateContract,
			"mcp_proposal_candidates_json":     string(proposalCandidatesJSON),
			"mcp_limitations_json":             string(limitations),
			"global_absence_inference_allowed": "false",
		},
		RequestID: mcpReadSourceIntakeRequestID(delivery.receipt.ConnectorDeliveryID),
	})
	if err != nil {
		return MCPReadProcessingResult{}, err
	}
	return MCPReadProcessingResult{
		ConnectorDeliveryID: delivery.receipt.ConnectorDeliveryID,
		Provider:            envelope.Provider,
		ObjectID:            document.ObjectID,
		Revision:            document.Revision,
		DocumentID:          document.Document.ID,
		Coverage:            document.Coverage,
		Source:              source,
	}, nil
}

func prepareMCPReadCollection(input MCPReadCollectionInput) (preparedMCPReadCollection, error) {
	var err error
	input.RequestID, err = normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	input.ConnectorID, err = normalizeBoundedText(input.ConnectorID, "connector_id", 200)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	input.Provider, err = normalizeBoundedText(input.Provider, "provider", 100)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	input.LogicalCapability, err = normalizeBoundedText(input.LogicalCapability, "logical_capability", 100)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	input.AdapterName, err = normalizeBoundedText(input.AdapterName, "adapter_name", 100)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	input.AdapterVersion, err = normalizeBoundedText(input.AdapterVersion, "adapter_version", 100)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	input.ProviderToolName, err = normalizeBoundedText(input.ProviderToolName, "provider_tool_name", 200)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	input.ProviderToolInputSchemaHash, err = normalizeSHA256(
		input.ProviderToolInputSchemaHash,
		"provider_tool_input_schema_hash",
	)
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	if len(input.Arguments) == 0 {
		input.Arguments = json.RawMessage(`{}`)
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(input.Arguments, &arguments); err != nil || arguments == nil {
		return preparedMCPReadCollection{}, newDomainError(
			ErrorInvalidInput,
			"arguments must be a JSON object",
		)
	}
	input.Arguments, err = json.Marshal(arguments)
	if err != nil {
		return preparedMCPReadCollection{}, fmt.Errorf("canonicalizing mcp read arguments: %w", err)
	}
	if len(input.Arguments) > maxMCPReadArgumentsBytes {
		return preparedMCPReadCollection{}, newDomainError(
			ErrorInvalidInput,
			"arguments exceed %d bytes",
			maxMCPReadArgumentsBytes,
		)
	}
	requestHash, err := orchestrationPayloadHash(struct {
		Provider                    string          `json:"provider"`
		LogicalCapability           string          `json:"logical_capability"`
		AdapterName                 string          `json:"adapter_name"`
		AdapterVersion              string          `json:"adapter_version"`
		ProviderToolName            string          `json:"provider_tool_name"`
		ProviderToolInputSchemaHash string          `json:"provider_tool_input_schema_hash"`
		Arguments                   json.RawMessage `json:"arguments"`
	}{
		Provider:                    input.Provider,
		LogicalCapability:           input.LogicalCapability,
		AdapterName:                 input.AdapterName,
		AdapterVersion:              input.AdapterVersion,
		ProviderToolName:            input.ProviderToolName,
		ProviderToolInputSchemaHash: input.ProviderToolInputSchemaHash,
		Arguments:                   input.Arguments,
	})
	if err != nil {
		return preparedMCPReadCollection{}, err
	}
	return preparedMCPReadCollection{input: input, requestHash: requestHash}, nil
}

func replayMCPReadCollection(
	ctx context.Context,
	pool *pgxpool.Pool,
	prepared preparedMCPReadCollection,
) (MCPReadCollectionResult, bool, error) {
	request, found, err := readConnectorReceiveRequest(ctx, pool, prepared.input.RequestID)
	if err != nil {
		return MCPReadCollectionResult{}, false, err
	}
	if !found {
		return MCPReadCollectionResult{}, false, nil
	}
	if request.connectorID != prepared.input.ConnectorID {
		return MCPReadCollectionResult{}, false, newDomainError(
			ErrorIdempotencyKeyReused,
			"request_id %s already exists with a different connector",
			prepared.input.RequestID,
		)
	}
	delivery, found, err := readConnectorDelivery(ctx, pool, request.connectorDeliveryID, false)
	if err != nil {
		return MCPReadCollectionResult{}, false, err
	}
	if !found {
		return MCPReadCollectionResult{}, false, newDomainError(
			ErrorConnectorDeliveryConflict,
			"connector delivery request %s references missing delivery %s",
			prepared.input.RequestID,
			request.connectorDeliveryID,
		)
	}
	envelope, document, err := decodeMCPReadDelivery(delivery)
	if err != nil {
		return MCPReadCollectionResult{}, false, newDomainError(
			ErrorIdempotencyKeyReused,
			"request_id %s already exists outside the pinned MCP read contract",
			prepared.input.RequestID,
		)
	}
	if envelope.Provider != prepared.input.Provider ||
		envelope.LogicalCapability != prepared.input.LogicalCapability ||
		envelope.AdapterName != prepared.input.AdapterName ||
		envelope.AdapterVersion != prepared.input.AdapterVersion ||
		envelope.ProviderToolName != prepared.input.ProviderToolName ||
		envelope.ProviderToolInputSchemaHash != prepared.input.ProviderToolInputSchemaHash ||
		envelope.RequestHash != prepared.requestHash ||
		!bytes.Equal(envelope.Arguments, prepared.input.Arguments) {
		return MCPReadCollectionResult{}, false, newDomainError(
			ErrorIdempotencyKeyReused,
			"request_id %s already exists with a different MCP read payload",
			prepared.input.RequestID,
		)
	}
	receipt := delivery.receipt
	receipt.RequestID = prepared.input.RequestID
	receipt.DeliveryCreated = request.deliveryCreated
	receipt.Replayed = true
	return mcpReadCollectionResult(receipt, envelope, document), true, nil
}

func mcpReadCollectionResult(
	receipt ConnectorDeliveryReceipt,
	envelope mcpReadDeliveryEnvelope,
	document MCPReadDocumentResult,
) MCPReadCollectionResult {
	return MCPReadCollectionResult{
		Receipt:        receipt,
		ObjectID:       document.ObjectID,
		Revision:       document.Revision,
		DocumentID:     document.Document.ID,
		ResultHash:     envelope.StructuredResultHash,
		RequestHash:    envelope.RequestHash,
		Coverage:       document.Coverage,
		NextPageCursor: document.NextPageCursor,
	}
}

func decodeMCPReadDelivery(
	delivery persistedConnectorDelivery,
) (mcpReadDeliveryEnvelope, MCPReadDocumentResult, error) {
	if delivery.receipt.ContentType != ConnectorDeliveryContentTypeJSON {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"mcp read delivery has content_type %q",
			delivery.receipt.ContentType,
		)
	}
	var envelope mcpReadDeliveryEnvelope
	if err := decodeStrictMCPJSON(delivery.payload, &envelope); err != nil {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"invalid mcp read delivery envelope: %v",
			err,
		)
	}
	if err := validateMCPReadEnvelope(envelope); err != nil {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, err
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(envelope.StructuredResultBase64)
	if err != nil {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"structured result is not canonical base64",
		)
	}
	if base64.StdEncoding.EncodeToString(raw) != envelope.StructuredResultBase64 {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"structured result is not canonical base64",
		)
	}
	if contentHash(raw) != envelope.StructuredResultHash {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"structured result hash does not match exact bytes",
		)
	}
	document, err := decodeMCPReadDocumentResult(raw)
	if err != nil {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, err
	}
	if delivery.receipt.ExternalDeliveryID != mcpReadExternalDeliveryID(envelope.Provider, document) {
		return mcpReadDeliveryEnvelope{}, MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"mcp read external delivery identity disagrees with result",
		)
	}
	return envelope, document, nil
}

func validateMCPReadEnvelope(envelope mcpReadDeliveryEnvelope) error {
	if envelope.Contract != mcpReadDeliveryContract {
		return newDomainError(ErrorMCPContractViolation, "unsupported mcp read delivery contract %q", envelope.Contract)
	}
	for field, value := range map[string]string{
		"provider":           envelope.Provider,
		"logical_capability": envelope.LogicalCapability,
		"adapter_name":       envelope.AdapterName,
		"adapter_version":    envelope.AdapterVersion,
		"provider_tool_name": envelope.ProviderToolName,
		"protocol_version":   envelope.ProtocolVersion,
		"server_name":        envelope.ServerName,
		"server_version":     envelope.ServerVersion,
	} {
		if normalized, err := normalizeBoundedText(value, field, 200); err != nil || normalized != value {
			return newDomainError(ErrorMCPContractViolation, "mcp read delivery has invalid %s", field)
		}
	}
	if normalized, err := normalizeSHA256(
		envelope.ProviderToolInputSchemaHash,
		"provider_tool_input_schema_hash",
	); err != nil || normalized != envelope.ProviderToolInputSchemaHash {
		return newDomainError(ErrorMCPContractViolation, "mcp read delivery has invalid provider tool schema hash")
	}
	if normalized, err := normalizeSHA256(envelope.RequestHash, "request_hash"); err != nil || normalized != envelope.RequestHash {
		return newDomainError(ErrorMCPContractViolation, "mcp read delivery has invalid request hash")
	}
	if normalized, err := normalizeSHA256(
		envelope.StructuredResultHash,
		"structured_result_hash",
	); err != nil || normalized != envelope.StructuredResultHash {
		return newDomainError(ErrorMCPContractViolation, "mcp read delivery has invalid structured result hash")
	}
	if envelope.ReadOnlyHint != nil && !*envelope.ReadOnlyHint {
		return newDomainError(ErrorMCPContractViolation, "mcp read tool declared read_only_hint=false")
	}
	if envelope.DestructiveHint != nil && *envelope.DestructiveHint {
		return newDomainError(ErrorMCPContractViolation, "mcp read tool declared destructive_hint=true")
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Arguments, &arguments); err != nil || arguments == nil {
		return newDomainError(ErrorMCPContractViolation, "mcp read arguments are not a JSON object")
	}
	requestHash, err := orchestrationPayloadHash(struct {
		Provider                    string          `json:"provider"`
		LogicalCapability           string          `json:"logical_capability"`
		AdapterName                 string          `json:"adapter_name"`
		AdapterVersion              string          `json:"adapter_version"`
		ProviderToolName            string          `json:"provider_tool_name"`
		ProviderToolInputSchemaHash string          `json:"provider_tool_input_schema_hash"`
		Arguments                   json.RawMessage `json:"arguments"`
	}{
		Provider:                    envelope.Provider,
		LogicalCapability:           envelope.LogicalCapability,
		AdapterName:                 envelope.AdapterName,
		AdapterVersion:              envelope.AdapterVersion,
		ProviderToolName:            envelope.ProviderToolName,
		ProviderToolInputSchemaHash: envelope.ProviderToolInputSchemaHash,
		Arguments:                   envelope.Arguments,
	})
	if err != nil {
		return err
	}
	if requestHash != envelope.RequestHash {
		return newDomainError(ErrorMCPContractViolation, "mcp read request hash does not match exact invocation")
	}
	return nil
}

func decodeMCPReadDocumentResult(raw []byte) (MCPReadDocumentResult, error) {
	if len(raw) == 0 || len(raw) > maxMCPReadStructuredResultBytes {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"structured result must contain 1 to %d bytes",
			maxMCPReadStructuredResultBytes,
		)
	}
	var result MCPReadDocumentResult
	if err := decodeStrictMCPJSON(raw, &result); err != nil {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"invalid mcp read document result: %v",
			err,
		)
	}
	if result.Contract != MCPReadDocumentResultContract {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"unsupported mcp read document result contract %q",
			result.Contract,
		)
	}
	for field, value := range map[string]string{
		"object_id":       result.ObjectID,
		"revision":        result.Revision,
		"source_location": result.SourceLocation,
		"document_id":     result.Document.ID,
	} {
		normalized, normalizeErr := normalizeBoundedText(value, field, 1000)
		if normalizeErr != nil || normalized != value {
			return MCPReadDocumentResult{}, newDomainError(ErrorMCPContractViolation, "%s is invalid", field)
		}
	}
	if result.PageCursor != "" {
		normalized, normalizeErr := normalizeBoundedText(result.PageCursor, "page_cursor", 500)
		if normalizeErr != nil || normalized != result.PageCursor {
			return MCPReadDocumentResult{}, newDomainError(ErrorMCPContractViolation, "page_cursor is invalid")
		}
	}
	if result.NextPageCursor != "" {
		normalized, normalizeErr := normalizeBoundedText(result.NextPageCursor, "next_page_cursor", 500)
		if normalizeErr != nil || normalized != result.NextPageCursor {
			return MCPReadDocumentResult{}, newDomainError(ErrorMCPContractViolation, "next_page_cursor is invalid")
		}
	}
	if err := validateMCPReadCoverage(result.Coverage, result.NextPageCursor); err != nil {
		return MCPReadDocumentResult{}, err
	}
	if result.Document.Title != "" {
		normalized, normalizeErr := normalizeBoundedText(result.Document.Title, "document_title", 500)
		if normalizeErr != nil || normalized != result.Document.Title {
			return MCPReadDocumentResult{}, newDomainError(ErrorMCPContractViolation, "document title is invalid")
		}
	}
	documentBytes := []byte(result.Document.Text)
	if len(documentBytes) == 0 || len(documentBytes) > maxMCPReadDocumentBytes || !utf8.Valid(documentBytes) {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"document text must be valid UTF-8 containing 1 to %d bytes",
			maxMCPReadDocumentBytes,
		)
	}
	if connectorTextNonEmptyLineCount(documentBytes) > maxMCPReadDocumentNonEmptyLines {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"document text exceeds %d non-empty lines",
			maxMCPReadDocumentNonEmptyLines,
		)
	}
	if contentHash(documentBytes) != result.Document.ContentHash {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"document content hash does not match exact text",
		)
	}
	rawProvider, err := base64.StdEncoding.Strict().DecodeString(result.RawProviderResponseBase64)
	if err != nil || len(rawProvider) > maxMCPReadRawProviderResponseBytes {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"raw provider response must be canonical base64 of at most %d bytes",
			maxMCPReadRawProviderResponseBytes,
		)
	}
	if base64.StdEncoding.EncodeToString(rawProvider) != result.RawProviderResponseBase64 {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"raw provider response is not canonical base64",
		)
	}
	if contentHash(rawProvider) != result.RawProviderResponseHash {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"raw provider response hash does not match exact bytes",
		)
	}
	if len(result.Limitations) > maxMCPReadLimitations {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"limitations exceed %d entries",
			maxMCPReadLimitations,
		)
	}
	seenLimitations := make(map[string]struct{}, len(result.Limitations))
	for index, limitation := range result.Limitations {
		normalized, err := normalizeBoundedText(limitation, "limitation", maxMCPReadLimitationBytes)
		if err != nil || normalized != limitation {
			return MCPReadDocumentResult{}, newDomainError(
				ErrorMCPContractViolation,
				"limitation %d is invalid",
				index,
			)
		}
		if _, exists := seenLimitations[limitation]; exists {
			return MCPReadDocumentResult{}, newDomainError(
				ErrorMCPContractViolation,
				"limitation %d is duplicated",
				index,
			)
		}
		seenLimitations[limitation] = struct{}{}
	}
	if err := validateMCPReadProposalCandidates(result.Document.Text, result.ProposalCandidates); err != nil {
		return MCPReadDocumentResult{}, newDomainError(
			ErrorMCPContractViolation,
			"invalid proposal candidates: %v",
			err,
		)
	}
	return result, nil
}

func validateMCPReadCoverage(coverage MCPReadCoverage, nextPageCursor string) error {
	switch coverage.CompletionReason {
	case MCPReadCompletionComplete:
		if !coverage.Complete || coverage.Truncated || nextPageCursor != "" {
			return newDomainError(ErrorMCPContractViolation, "complete coverage contradicts truncation or next page")
		}
	case MCPReadCompletionNextPage:
		if coverage.Complete || !coverage.Truncated || nextPageCursor == "" {
			return newDomainError(ErrorMCPContractViolation, "next-page coverage requires truncation and a cursor")
		}
	case MCPReadCompletionProviderLimit, MCPReadCompletionAdapterLimit:
		if coverage.Complete || !coverage.Truncated {
			return newDomainError(ErrorMCPContractViolation, "limited coverage must be incomplete and truncated")
		}
	default:
		return newDomainError(
			ErrorMCPContractViolation,
			"unsupported mcp read completion reason %q",
			coverage.CompletionReason,
		)
	}
	return nil
}

func decodeStrictMCPJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func normalizeSHA256(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return "", newDomainError(ErrorInvalidInput, "%s has invalid sha256 identity", field)
	}
	if _, err := hex.DecodeString(value[len(prefix):]); err != nil {
		return "", newDomainError(ErrorInvalidInput, "%s has invalid sha256 identity", field)
	}
	return value, nil
}

func mcpReadToolSchemaHash(schema map[string]any) (string, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return "", newDomainError(ErrorMCPContractViolation, "encoding provider tool input schema: %v", err)
	}
	return contentHash(raw), nil
}

func mcpReadExternalDeliveryID(provider string, result MCPReadDocumentResult) string {
	return "mcp-read-page:" + hashHex([]byte(
		mcpReadDeliveryContract+"\x00external\x00"+
			provider+"\x00"+result.ObjectID+"\x00"+result.Revision+"\x00"+
			result.PageCursor+"\x00"+result.Document.ID,
	))
}

func mcpReadSourceID(
	provider string,
	adapterName string,
	adapterVersion string,
	result MCPReadDocumentResult,
	proposalCandidatesJSON []byte,
) string {
	return "mcp-read-source:" + hashHex([]byte(
		mcpReadSourceIdentityContract+"\x00"+
			provider+"\x00"+result.ObjectID+"\x00"+
			result.PageCursor+"\x00"+result.Document.ID+"\x00"+
			adapterName+"\x00"+adapterVersion+"\x00"+
			string(proposalCandidatesJSON),
	))
}

func mcpReadSourceIntakeRequestID(connectorDeliveryID string) string {
	return "detective-mcp-read-intake:" + hashHex([]byte(
		mcpReadDeliveryContract+"\x00intake\x00"+connectorDeliveryID,
	))
}
