// Package atlassianmcp adapts a pinned mcp-atlassian read tool to the AHE
// provider-neutral MCP document contract.
package atlassianmcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

const (
	// AdapterVersion identifies the exact conversion implemented here.
	AdapterVersion = "mcp-atlassian-read-v2"
	// ProviderRelease is the source release audited for this adapter.
	ProviderRelease = "sooperset/mcp-atlassian@v0.23.0"

	// JiraGetIssueSchemaHashV0230 pins the v0.23.0 jira_get_issue input schema.
	JiraGetIssueSchemaHashV0230 = "sha256:f2af4d815a9de8fde23529e76ee20e4c00f4527e007a031c2e1f5cf1f4537f85"
	// ConfluenceGetPageSchemaHashV0230 pins the v0.23.0 confluence_get_page input schema.
	ConfluenceGetPageSchemaHashV0230 = "sha256:5dc0af2e634bcd0477bf56f1ae7f693dfd071d25a66fc7863aeed33a7213096e"

	ToolReadDocument     = "read_atlassian_document"
	resultContract       = "ahe-mcp-read-document-v1"
	completionComplete   = "complete"
	providerServerName   = "Atlassian MCP"
	maxProviderResult    = 512 << 10
	maxAtlassianObjectID = 300
)

// Config pins one local mcp-atlassian process.
type Config struct {
	ProviderCommand mcpstdio.CommandConfig `json:"provider_command"`
	ProviderRelease string                 `json:"provider_release"`
}

// Backend exposes one provider-neutral read tool over a pinned mcp-atlassian process.
type Backend struct {
	config               Config
	jiraSchemaHash       string
	confluenceSchemaHash string
}

// NewBackend validates the audited provider release boundary.
func NewBackend(config Config) (*Backend, error) {
	if config.ProviderRelease != ProviderRelease {
		return nil, fmt.Errorf("unsupported mcp-atlassian provider release %q", config.ProviderRelease)
	}
	if config.ProviderCommand.Path == "" || config.ProviderCommand.Directory == "" {
		return nil, errors.New("mcp-atlassian provider command path and directory are required")
	}
	if !mcpstdio.IsMacOSLoopbackOnlyCommand(config.ProviderCommand) {
		return nil, errors.New("mcp-atlassian provider command must use the exact macOS loopback-only sandbox")
	}
	environment := environmentMap(config.ProviderCommand.Environment)
	for name, expected := range map[string]string{
		"PYTHONHASHSEED":         "0",
		"TZ":                     "UTC",
		"READ_ONLY_MODE":         "true",
		"ENABLED_TOOLS":          "jira_get_issue,confluence_get_page",
		"TRANSPORT":              "stdio",
		"HTTP_PROXY":             "",
		"HTTPS_PROXY":            "",
		"ALL_PROXY":              "",
		"ATLASSIAN_OAUTH_ENABLE": "",
	} {
		if environment[name] != expected {
			return nil, fmt.Errorf("mcp-atlassian environment %s must equal %q", name, expected)
		}
	}
	return &Backend{
		config:               config,
		jiraSchemaHash:       JiraGetIssueSchemaHashV0230,
		confluenceSchemaHash: ConfluenceGetPageSchemaHashV0230,
	}, nil
}

func environmentMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if found {
			result[name] = value
		}
	}
	return result
}

// Tools returns the single AHE-normalized read capability.
func (b *Backend) Tools() []mcpstdio.Tool {
	readOnly := true
	destructive := false
	idempotent := true
	openWorld := true
	return []mcpstdio.Tool{{
		Name:        ToolReadDocument,
		Title:       "Read Atlassian Document",
		Description: "Reads one exact Jira issue or Confluence page through a pinned local mcp-atlassian provider.",
		InputSchema: ReadDocumentInputSchema(),
		Annotations: mcpstdio.Annotations{
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
			IdempotentHint:  &idempotent,
			OpenWorldHint:   &openWorld,
		},
	}}
}

// ReadDocumentInputSchema returns the stable adapter input schema.
func ReadDocumentInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"product": map[string]any{
				"type": "string",
				"enum": []string{"jira", "confluence"},
			},
			"object_id": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": maxAtlassianObjectID,
			},
		},
		"required": []string{"product", "object_id"},
	}
}

// CallTool converts one exact provider result without promoting it to graph truth.
func (b *Backend) CallTool(
	ctx context.Context,
	name string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	if name != ToolReadDocument {
		return nil, fmt.Errorf("unsupported atlassian adapter tool %q", name)
	}
	input, err := decodeReadDocumentInput(arguments)
	if err != nil {
		return nil, err
	}
	var providerTool, schemaHash string
	var providerArguments json.RawMessage
	switch input.Product {
	case "jira":
		providerTool = "jira_get_issue"
		schemaHash = b.jiraSchemaHash
		providerArguments, err = json.Marshal(map[string]any{
			"issue_key":      input.ObjectID,
			"fields":         "summary,description,status,issuetype,priority,assignee,reporter,labels,created,updated,versions",
			"comment_limit":  0,
			"update_history": false,
		})
	case "confluence":
		providerTool = "confluence_get_page"
		schemaHash = b.confluenceSchemaHash
		providerArguments, err = json.Marshal(map[string]any{
			"page_id":             input.ObjectID,
			"include_metadata":    true,
			"convert_to_markdown": true,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("encoding mcp-atlassian arguments: %w", err)
	}
	called, err := mcpstdio.CallTool(ctx, b.config.ProviderCommand, mcpstdio.ToolCallRequest{
		Name:                    providerTool,
		ExpectedInputSchemaHash: schemaHash,
		Arguments:               providerArguments,
	})
	if err != nil {
		return nil, fmt.Errorf("calling pinned mcp-atlassian %s: %w", providerTool, err)
	}
	if called.ServerName != providerServerName {
		return nil, fmt.Errorf("mcp-atlassian server name %q does not match pinned %q", called.ServerName, providerServerName)
	}
	providerText, err := decodeProviderText(called.StructuredContent)
	if err != nil {
		return nil, err
	}
	var result readDocumentResult
	switch input.Product {
	case "jira":
		result, err = jiraDocument(input.ObjectID, providerText, called.StructuredContent)
	case "confluence":
		result, err = confluenceDocument(input.ObjectID, providerText, called.StructuredContent)
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding normalized Atlassian document: %w", err)
	}
	return raw, nil
}

type readDocumentInput struct {
	Product  string `json:"product"`
	ObjectID string `json:"object_id"`
}

func decodeReadDocumentInput(raw []byte) (readDocumentInput, error) {
	var input readDocumentInput
	if err := decodeStrictJSON(raw, &input); err != nil {
		return readDocumentInput{}, fmt.Errorf("invalid atlassian read input: %w", err)
	}
	input.Product = strings.TrimSpace(input.Product)
	input.ObjectID = strings.TrimSpace(input.ObjectID)
	if input.Product != "jira" && input.Product != "confluence" {
		return readDocumentInput{}, fmt.Errorf("unsupported atlassian product %q", input.Product)
	}
	if input.ObjectID == "" || len(input.ObjectID) > maxAtlassianObjectID ||
		!utf8.ValidString(input.ObjectID) || strings.ContainsAny(input.ObjectID, "\r\n") {
		return readDocumentInput{}, errors.New("atlassian object_id is invalid")
	}
	return input, nil
}

func decodeProviderText(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxProviderResult {
		return nil, fmt.Errorf("mcp-atlassian structured result must contain 1 to %d bytes", maxProviderResult)
	}
	var wrapped struct {
		Result string `json:"result"`
	}
	if err := decodeStrictJSON(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("invalid mcp-atlassian structured result: %w", err)
	}
	text := []byte(wrapped.Result)
	if len(text) == 0 || len(text) > maxProviderResult || !utf8.Valid(text) {
		return nil, errors.New("mcp-atlassian result text is invalid")
	}
	if !json.Valid(text) {
		return nil, errors.New("mcp-atlassian result text is not JSON")
	}
	return text, nil
}

type readCoverage struct {
	Complete         bool   `json:"complete"`
	Truncated        bool   `json:"truncated"`
	CompletionReason string `json:"completion_reason"`
}

type readDocument struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text"`
	ContentHash string `json:"content_hash"`
}

type readProposalCandidate struct {
	LocalID      string `json:"local_id"`
	SelectorKind string `json:"selector_kind"`
	Selector     string `json:"selector"`
}

type readDocumentResult struct {
	Contract                  string                  `json:"contract"`
	ObjectID                  string                  `json:"object_id"`
	Revision                  string                  `json:"revision"`
	SourceLocation            string                  `json:"source_location"`
	Coverage                  readCoverage            `json:"coverage"`
	Document                  readDocument            `json:"document"`
	ProposalCandidates        []readProposalCandidate `json:"proposal_candidates,omitempty"`
	RawProviderResponseBase64 string                  `json:"raw_provider_response_base64"`
	RawProviderResponseHash   string                  `json:"raw_provider_response_hash"`
	Limitations               []string                `json:"limitations"`
}

func jiraDocument(requestedID string, providerText, rawProvider []byte) (readDocumentResult, error) {
	var issue struct {
		ID          string `json:"id"`
		Key         string `json:"key"`
		Summary     string `json:"summary"`
		Description string `json:"description"`
		Updated     string `json:"updated"`
		BrowseURL   string `json:"browse_url"`
	}
	if err := json.Unmarshal(providerText, &issue); err != nil {
		return readDocumentResult{}, fmt.Errorf("decoding mcp-atlassian Jira issue: %w", err)
	}
	if issue.ID == "" || issue.Key != requestedID || issue.Updated == "" {
		return readDocumentResult{}, errors.New("mcp-atlassian Jira issue omitted stable id, matching key, or updated revision")
	}
	if err := validateSourceURL(issue.BrowseURL); err != nil {
		return readDocumentResult{}, fmt.Errorf("invalid mcp-atlassian Jira browse_url: %w", err)
	}
	revision, err := normalizedJiraRevision(issue.Updated)
	if err != nil {
		return readDocumentResult{}, err
	}
	var candidates []readProposalCandidate
	if issue.Description != "" {
		candidates = []readProposalCandidate{{
			LocalID:      "jira-description",
			SelectorKind: "json_pointer_string",
			Selector:     "/description",
		}}
	}
	return normalizedDocument(
		"jira-issue:"+issue.ID,
		revision,
		issue.BrowseURL,
		"jira-issue:"+issue.Key,
		issue.Summary,
		providerText,
		rawProvider,
		candidates,
		[]string{
			"mcp-atlassian returns a simplified Jira projection, not the raw Jira REST entity",
			"adapter v2 fixes a bounded field allowlist and excludes comments, history, and properties",
		},
	), nil
}

func normalizedJiraRevision(value string) (string, error) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05 MST",
	} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return "updated:" + parsed.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("mcp-atlassian Jira updated revision %q is not a supported timestamp", value)
}

func confluenceDocument(requestedID string, providerText, rawProvider []byte) (readDocumentResult, error) {
	var wrapped struct {
		Metadata struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			URL     string `json:"url"`
			Updated string `json:"updated"`
			Version int    `json:"version"`
			Content struct {
				Value string `json:"value"`
			} `json:"content"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(providerText, &wrapped); err != nil {
		return readDocumentResult{}, fmt.Errorf("decoding mcp-atlassian Confluence page: %w", err)
	}
	page := wrapped.Metadata
	if page.ID != requestedID || page.Version <= 0 {
		return readDocumentResult{}, errors.New("mcp-atlassian Confluence page omitted matching id or positive version")
	}
	if err := validateSourceURL(page.URL); err != nil {
		return readDocumentResult{}, fmt.Errorf("invalid mcp-atlassian Confluence URL: %w", err)
	}
	var candidates []readProposalCandidate
	if page.Content.Value != "" {
		candidates = []readProposalCandidate{{
			LocalID:      "confluence-content",
			SelectorKind: "json_pointer_string",
			Selector:     "/metadata/content/value",
		}}
	}
	return normalizedDocument(
		"confluence-page:"+page.ID,
		"version:"+strconv.Itoa(page.Version),
		page.URL,
		"confluence-page:"+page.ID,
		page.Title,
		providerText,
		rawProvider,
		candidates,
		[]string{
			"mcp-atlassian converts Confluence storage content to Markdown before this receipt",
			"adapter v2 captures one page and does not infer descendant or space coverage",
		},
	), nil
}

func normalizedDocument(
	objectID, revision, sourceLocation, documentID, title string,
	text, rawProvider []byte,
	proposalCandidates []readProposalCandidate,
	limitations []string,
) readDocumentResult {
	return readDocumentResult{
		Contract:       resultContract,
		ObjectID:       objectID,
		Revision:       revision,
		SourceLocation: sourceLocation,
		Coverage: readCoverage{
			Complete:         true,
			CompletionReason: completionComplete,
		},
		Document: readDocument{
			ID:          documentID,
			Title:       title,
			Text:        string(text),
			ContentHash: contentHash(text),
		},
		ProposalCandidates:        proposalCandidates,
		RawProviderResponseBase64: base64.StdEncoding.EncodeToString(rawProvider),
		RawProviderResponseHash:   contentHash(rawProvider),
		Limitations:               limitations,
	}
}

func validateSourceURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("absolute URL is required")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
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

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
