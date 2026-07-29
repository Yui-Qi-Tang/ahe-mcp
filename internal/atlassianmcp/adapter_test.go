package atlassianmcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func TestBackendConvertsPinnedJiraIssue(t *testing.T) {
	backend := testBackend(t, "success")
	raw, err := backend.CallTool(
		context.Background(),
		ToolReadDocument,
		json.RawMessage(`{"product":"jira","object_id":"AHE-42"}`),
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var result readDocumentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if result.ObjectID != "jira-issue:10042" ||
		result.Revision != "updated:2026-07-26T01:02:03Z" ||
		result.Document.ID != "jira-issue:AHE-42" ||
		result.Document.Title != "Refund policy" ||
		len(result.ProposalCandidates) != 1 ||
		result.ProposalCandidates[0].Selector != "/description" ||
		!strings.Contains(result.Document.Text, "within 7 days") ||
		result.RawProviderResponseHash == result.Document.ContentHash {
		t.Fatalf("normalized Jira result = %+v", result)
	}
}

func TestBackendConvertsPinnedConfluencePage(t *testing.T) {
	backend := testBackend(t, "success")
	raw, err := backend.CallTool(
		context.Background(),
		ToolReadDocument,
		json.RawMessage(`{"product":"confluence","object_id":"4242"}`),
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var result readDocumentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if result.ObjectID != "confluence-page:4242" ||
		result.Revision != "version:7" ||
		result.Document.Title != "Refund operations" ||
		len(result.ProposalCandidates) != 1 ||
		result.ProposalCandidates[0].Selector != "/metadata/content/value" ||
		!result.Coverage.Complete ||
		result.Coverage.Truncated {
		t.Fatalf("normalized Confluence result = %+v", result)
	}
}

func TestBackendFailsClosedOnIdentityAndInputDrift(t *testing.T) {
	backend := testBackend(t, "wrong-identity")
	_, err := backend.CallTool(
		context.Background(),
		ToolReadDocument,
		json.RawMessage(`{"product":"jira","object_id":"AHE-42"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "matching key") {
		t.Fatalf("CallTool(wrong identity) error = %v", err)
	}
	_, err = backend.CallTool(
		context.Background(),
		ToolReadDocument,
		json.RawMessage(`{"product":"jira","object_id":"AHE-42","answer":"invented"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("CallTool(unknown field) error = %v", err)
	}
}

func TestNewBackendPinsAuditedRelease(t *testing.T) {
	_, err := NewBackend(Config{
		ProviderRelease: "latest",
		ProviderCommand: mcpstdio.CommandConfig{Path: "/provider", Directory: "/work"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("NewBackend() error = %v", err)
	}
}

func TestAtlassianProviderHelperProcess(t *testing.T) {
	mode := os.Getenv("AHE_ATLASSIAN_PROVIDER_HELPER")
	if mode == "" {
		return
	}
	server, err := mcpstdio.NewServer(providerServerName, "3.4.4", &providerBackend{mode: mode})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

type providerBackend struct {
	mode string
}

func (b *providerBackend) Tools() []mcpstdio.Tool {
	readOnly := true
	schema := providerTestSchema()
	return []mcpstdio.Tool{
		{
			Name:        "jira_get_issue",
			Description: "Reads one Jira issue.",
			InputSchema: schema,
			Annotations: mcpstdio.Annotations{ReadOnlyHint: &readOnly},
		},
		{
			Name:        "confluence_get_page",
			Description: "Reads one Confluence page.",
			InputSchema: schema,
			Annotations: mcpstdio.Annotations{ReadOnlyHint: &readOnly},
		},
	}
}

func (b *providerBackend) CallTool(
	_ context.Context,
	name string,
	_ json.RawMessage,
) (json.RawMessage, error) {
	var result string
	switch name {
	case "jira_get_issue":
		key := "AHE-42"
		if b.mode == "wrong-identity" {
			key = "AHE-99"
		}
		result = `{
  "id": "10042",
  "key": "` + key + `",
  "summary": "Refund policy",
  "description": "Refunds must be completed within 7 days.",
  "updated": "2026-07-26T01:02:03Z",
  "browse_url": "http://127.0.0.1:18080/browse/AHE-42"
}`
	case "confluence_get_page":
		result = `{
  "metadata": {
    "id": "4242",
    "title": "Refund operations",
    "url": "http://127.0.0.1:18080/wiki/pages/viewpage.action?pageId=4242",
    "updated": "2026-07-26T01:02:03Z",
    "version": 7,
    "content": {"value": "Refunds must be completed within 7 days.", "format": "markdown"}
  }
}`
	}
	return json.Marshal(map[string]string{"result": result})
}

func testBackend(t *testing.T, mode string) *Backend {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	hash, err := mcpstdio.ToolInputSchemaHash(providerTestSchema())
	if err != nil {
		t.Fatalf("ToolInputSchemaHash() error = %v", err)
	}
	return &Backend{
		config: Config{
			ProviderRelease: ProviderRelease,
			ProviderCommand: mcpstdio.CommandConfig{
				Path:        executable,
				Args:        []string{"-test.run=TestAtlassianProviderHelperProcess"},
				Directory:   t.TempDir(),
				Environment: []string{"AHE_ATLASSIAN_PROVIDER_HELPER=" + mode},
			},
		},
		jiraSchemaHash:       hash,
		confluenceSchemaHash: hash,
	}
}

func providerTestSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	}
}
