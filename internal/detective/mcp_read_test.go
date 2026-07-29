package detective

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func TestDecodeMCPReadDocumentResultAcceptsExactCompleteDocument(t *testing.T) {
	raw := testMCPReadResult(t, "Refunds must be completed within 7 days.", MCPReadCoverage{
		Complete:         true,
		CompletionReason: MCPReadCompletionComplete,
	}, "", nil)
	result, err := decodeMCPReadDocumentResult(raw)
	if err != nil {
		t.Fatalf("decodeMCPReadDocumentResult() error = %v", err)
	}
	if result.ObjectID != "AHE-42" ||
		result.Document.ID != "AHE-42-description" ||
		!result.Coverage.Complete ||
		result.RawProviderResponseHash != contentHash([]byte(`{"id":"AHE-42"}`)) {
		t.Fatalf("decoded result = %+v", result)
	}
}

func TestDecodeMCPReadDocumentResultPreservesBoundedNextPage(t *testing.T) {
	raw := testMCPReadResult(t, "page one", MCPReadCoverage{
		Truncated:        true,
		CompletionReason: MCPReadCompletionNextPage,
	}, "cursor-2", []string{"one page returned"})
	result, err := decodeMCPReadDocumentResult(raw)
	if err != nil {
		t.Fatalf("decodeMCPReadDocumentResult() error = %v", err)
	}
	if result.Coverage.Complete || !result.Coverage.Truncated ||
		result.NextPageCursor != "cursor-2" ||
		len(result.Limitations) != 1 {
		t.Fatalf("partial result = %+v", result)
	}
}

func TestDecodeMCPReadDocumentResultValidatesProposalCandidateSelectors(t *testing.T) {
	document := "{\n" +
		`  "description": "Refunds must be completed within 7 days.",` + "\n" +
		`  "metadata": {"content": {"value": "Escalations require review."}},` + "\n" +
		`  "escaped": "use <tag> and \u00e9"` + "\n" +
		"}"
	raw := testMCPReadResultWithCandidates(
		t,
		document,
		MCPReadCoverage{
			Complete:         true,
			CompletionReason: MCPReadCompletionComplete,
		},
		"",
		nil,
		[]MCPReadProposalCandidate{
			{
				LocalID:      "description",
				SelectorKind: MCPReadProposalSelectorJSONPointerString,
				Selector:     "/description",
			},
			{
				LocalID:      "content-line",
				SelectorKind: MCPReadProposalSelectorLine,
				Selector:     "3",
			},
			{
				LocalID:      "content-value",
				SelectorKind: MCPReadProposalSelectorJSONPointerString,
				Selector:     "/metadata/content/value",
			},
			{
				LocalID:      "escaped-value",
				SelectorKind: MCPReadProposalSelectorJSONPointerString,
				Selector:     "/escaped",
			},
		},
	)
	result, err := decodeMCPReadDocumentResult(raw)
	if err != nil {
		t.Fatalf("decodeMCPReadDocumentResult() error = %v", err)
	}
	if len(result.ProposalCandidates) != 4 {
		t.Fatalf("proposal candidates = %#v", result.ProposalCandidates)
	}
}

func TestDecodeMCPReadDocumentResultRejectsAmbiguousOrInvalidProposalCandidates(t *testing.T) {
	tests := []struct {
		name       string
		document   string
		candidates []MCPReadProposalCandidate
	}{
		{
			name:     "ambiguous exact JSON string",
			document: `{"description":"same statement","summary":"same statement"}`,
			candidates: []MCPReadProposalCandidate{{
				LocalID:      "description",
				SelectorKind: MCPReadProposalSelectorJSONPointerString,
				Selector:     "/description",
			}},
		},
		{
			name:     "missing JSON pointer",
			document: `{"summary":"Refund policy"}`,
			candidates: []MCPReadProposalCandidate{{
				LocalID:      "description",
				SelectorKind: MCPReadProposalSelectorJSONPointerString,
				Selector:     "/description",
			}},
		},
		{
			name:     "duplicate JSON field",
			document: `{"description":"first","description":"second"}`,
			candidates: []MCPReadProposalCandidate{{
				LocalID:      "description",
				SelectorKind: MCPReadProposalSelectorJSONPointerString,
				Selector:     "/description",
			}},
		},
		{
			name:     "duplicate selector",
			document: "one statement",
			candidates: []MCPReadProposalCandidate{
				{
					LocalID:      "first",
					SelectorKind: MCPReadProposalSelectorLine,
					Selector:     "1",
				},
				{
					LocalID:      "second",
					SelectorKind: MCPReadProposalSelectorLine,
					Selector:     "1",
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := testMCPReadResultWithCandidates(
				t,
				test.document,
				MCPReadCoverage{
					Complete:         true,
					CompletionReason: MCPReadCompletionComplete,
				},
				"",
				nil,
				test.candidates,
			)
			_, err := decodeMCPReadDocumentResult(raw)
			assertDetectiveKind(t, err, ErrorMCPContractViolation)
		})
	}
}

func TestDecodeMCPReadDocumentResultRejectsMalformedContracts(t *testing.T) {
	valid := testMCPReadResult(t, "exact text", MCPReadCoverage{
		Complete:         true,
		CompletionReason: MCPReadCompletionComplete,
	}, "", nil)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "unknown field",
			mutate: func(value map[string]any) {
				value["answer"] = "invented"
			},
		},
		{
			name: "contradictory coverage",
			mutate: func(value map[string]any) {
				value["coverage"] = map[string]any{
					"complete": true, "truncated": true,
					"completion_reason": MCPReadCompletionComplete,
				}
			},
		},
		{
			name: "wrong document hash",
			mutate: func(value map[string]any) {
				document := value["document"].(map[string]any)
				document["content_hash"] = contentHash([]byte("different"))
			},
		},
		{
			name: "duplicate limitation",
			mutate: func(value map[string]any) {
				value["limitations"] = []any{"partial", "partial"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(valid, &value); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			test.mutate(value)
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			_, err = decodeMCPReadDocumentResult(raw)
			assertDetectiveKind(t, err, ErrorMCPContractViolation)
		})
	}
}

func TestPrepareMCPReadCollectionCanonicalizesAndPinsRequest(t *testing.T) {
	schemaHash, err := mcpReadToolSchemaHash(testMCPReadInputSchema())
	if err != nil {
		t.Fatalf("mcpReadToolSchemaHash() error = %v", err)
	}
	prepared, err := prepareMCPReadCollection(MCPReadCollectionInput{
		RequestID:                   " collect-ahe-42 ",
		ConnectorID:                 " jira-loopback ",
		Provider:                    " fixture ",
		LogicalCapability:           " read_issue ",
		AdapterName:                 " fixture-adapter ",
		AdapterVersion:              " v1 ",
		ProviderToolName:            " read_document ",
		ProviderToolInputSchemaHash: schemaHash,
		Arguments:                   json.RawMessage(`{"z":1,"object_id":"AHE-42"}`),
		Command:                     mcpstdio.CommandConfig{Path: "/fixture", Directory: "/tmp"},
	})
	if err != nil {
		t.Fatalf("prepareMCPReadCollection() error = %v", err)
	}
	if prepared.input.RequestID != "collect-ahe-42" ||
		string(prepared.input.Arguments) != `{"object_id":"AHE-42","z":1}` ||
		len(prepared.requestHash) != 71 {
		t.Fatalf("prepared collection = %+v", prepared)
	}
	second, err := prepareMCPReadCollection(prepared.input)
	if err != nil {
		t.Fatalf("second prepareMCPReadCollection() error = %v", err)
	}
	if second.requestHash != prepared.requestHash {
		t.Fatalf("request hashes differ: %s != %s", second.requestHash, prepared.requestHash)
	}
	input := prepared.input
	input.Arguments = json.RawMessage(`{"value":"` +
		strings.Repeat("x", maxMCPReadArgumentsBytes) +
		`"}`)
	_, err = prepareMCPReadCollection(input)
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func TestMCPReadSourceIDPinsAdapterAndCandidateContract(t *testing.T) {
	document := MCPReadDocumentResult{
		ObjectID: "AHE-42",
		Revision: "revision-42",
		Document: MCPReadDocument{ID: "AHE-42-description"},
	}
	first := mcpReadSourceID(
		"fixture",
		"fixture-adapter",
		"v1",
		document,
		[]byte(`[{"local_id":"description","selector_kind":"line","selector":"1"}]`),
	)
	replay := mcpReadSourceID(
		"fixture",
		"fixture-adapter",
		"v1",
		document,
		[]byte(`[{"local_id":"description","selector_kind":"line","selector":"1"}]`),
	)
	changed := mcpReadSourceID(
		"fixture",
		"fixture-adapter",
		"v2",
		document,
		[]byte(`[{"local_id":"description","selector_kind":"json_pointer_string","selector":"/description"}]`),
	)
	if first != replay || first == changed {
		t.Fatalf("source identities first=%q replay=%q changed=%q", first, replay, changed)
	}
}

func TestMCPReadEntryPointsRequirePool(t *testing.T) {
	_, err := CollectMCPReadDelivery(context.Background(), nil, MCPReadCollectionInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
	_, err = ProcessMCPReadDelivery(context.Background(), nil, "")
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func testMCPReadResult(
	t *testing.T,
	text string,
	coverage MCPReadCoverage,
	nextPageCursor string,
	limitations []string,
) []byte {
	return testMCPReadResultWithCandidates(
		t,
		text,
		coverage,
		nextPageCursor,
		limitations,
		nil,
	)
}

func testMCPReadResultWithCandidates(
	t *testing.T,
	text string,
	coverage MCPReadCoverage,
	nextPageCursor string,
	limitations []string,
	candidates []MCPReadProposalCandidate,
) []byte {
	t.Helper()
	rawProvider := []byte(`{"id":"AHE-42"}`)
	raw, err := json.Marshal(MCPReadDocumentResult{
		Contract:       MCPReadDocumentResultContract,
		ObjectID:       "AHE-42",
		Revision:       "revision-42",
		SourceLocation: "https://fixture.invalid/browse/AHE-42",
		NextPageCursor: nextPageCursor,
		Coverage:       coverage,
		Document: MCPReadDocument{
			ID:          "AHE-42-description",
			Title:       "AHE-42",
			Text:        text,
			ContentHash: contentHash([]byte(text)),
		},
		ProposalCandidates:        candidates,
		RawProviderResponseBase64: base64.StdEncoding.EncodeToString(rawProvider),
		RawProviderResponseHash:   contentHash(rawProvider),
		Limitations:               limitations,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}

func testMCPReadInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"object_id"},
		"properties": map[string]any{
			"object_id": map[string]any{"type": "string"},
		},
	}
}

func mustMCPReadSchemaHash(t *testing.T) string {
	t.Helper()
	hash, err := mcpReadToolSchemaHash(testMCPReadInputSchema())
	if err != nil {
		t.Fatalf("mcpReadToolSchemaHash() error = %v", err)
	}
	return hash
}
