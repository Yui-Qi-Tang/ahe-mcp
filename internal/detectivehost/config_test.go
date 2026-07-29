package detectivehost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/detective"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func TestLoadConfigAcceptsBoundedLocalTextHost(t *testing.T) {
	path := writeHostConfig(t, `{
  "schema_version": "ahe-detective-host-v1",
  "host_id": "local-host",
  "registration_request_id": "register-local-host",
  "workspace_id": "workspace:local-host",
  "workspace_root": "/tmp/local-host",
  "source_interval": "30s",
  "max_steps": 1,
  "sources": [{
    "capability_name": "local-prd-text",
    "capability_version": "v1",
    "source_id": "prd",
    "relative_path": "docs"
  }],
  "repository_maintenance": {"enabled": false}
}`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.SchemaVersion != ConfigSchemaVersion || config.HostID != "local-host" ||
		config.SourceInterval != 30*time.Second || config.MaxSteps != 1 ||
		config.RepositoryMaintenance.Enabled || len(config.Sources) != 1 {
		t.Fatalf("LoadConfig() = %+v", config)
	}
	if config.Sources[0].CapabilityName != detective.SourceCapabilityLocalPRDText {
		t.Fatalf("source capability = %q", config.Sources[0].CapabilityName)
	}
}

func TestLoadConfigAcceptsGitHostWithGlobalMaintenance(t *testing.T) {
	path := writeHostConfig(t, `{
  "schema_version": "ahe-detective-host-v1",
  "host_id": "git-host",
  "registration_request_id": "register-git-host",
  "workspace_id": "workspace:git-host",
  "workspace_root": "/tmp/git-host",
  "source_interval": "1s",
  "max_steps": 3,
  "sources": [{
    "capability_name": "git-go-repository",
    "capability_version": "v1",
    "source_id": "repository",
    "relative_path": "."
  }],
  "repository_maintenance": {
    "enabled": true,
    "interval": "5s",
    "batch_limit": 25
  }
}`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !config.RepositoryMaintenance.Enabled ||
		config.RepositoryMaintenance.Interval != 5*time.Second ||
		config.RepositoryMaintenance.BatchLimit != 25 {
		t.Fatalf("repository maintenance = %+v", config.RepositoryMaintenance)
	}
}

func TestLoadConfigAcceptsExplicitBoundedPlannerOptIn(t *testing.T) {
	path := writeHostConfig(t, `{
  "schema_version": "ahe-detective-host-v2",
  "host_id": "planner-host",
  "registration_request_id": "register-planner-host",
  "workspace_id": "workspace:planner-host",
  "workspace_root": "/tmp/planner-host",
  "source_interval": "30s",
  "max_steps": 2,
  "sources": [{
    "capability_name": "local-prd-text",
    "capability_version": "v1",
    "source_id": "prd",
    "relative_path": "docs"
  }],
  "repository_maintenance": {"enabled": false},
  "planner": {
    "enabled": true,
    "base_url": "http://localhost:11434",
    "model": "gemma4:31b",
    "timeout": "20s",
    "num_predict": 192
  }
}`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.SchemaVersion != ConfigSchemaVersionV2 || !config.Planner.Enabled ||
		config.Planner.BaseURL != "http://localhost:11434" ||
		config.Planner.Model != "gemma4:31b" ||
		config.Planner.Timeout != 20*time.Second ||
		config.Planner.NumPredict != 192 {
		t.Fatalf("planner config = %+v", config)
	}
}

func TestLoadConfigAcceptsExactLoopbackMCPReadSource(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	command := mcpstdio.CommandConfig{
		Path:        "/fixture/ahe-mcp-atlassian-adapter",
		Args:        []string{"serve"},
		Directory:   "/fixture",
		Environment: []string{"AHE_MCP_ATLASSIAN_ADAPTER_CONFIG=/fixture/adapter.json"},
	}
	payload, err := json.Marshal(map[string]any{
		"schema_version":          ConfigSchemaVersionV3,
		"host_id":                 "mcp-host",
		"registration_request_id": "register-mcp-host",
		"workspace_id":            "workspace:mcp-host",
		"workspace_root":          "/tmp/mcp-host",
		"source_interval":         "30s",
		"max_steps":               1,
		"sources":                 []any{},
		"repository_maintenance":  map[string]any{"enabled": false},
		"mcp_read_sources": []any{map[string]any{
			"source_id":                       "atlassian:jira:AHE-42",
			"connector_id":                    "atlassian-read",
			"provider":                        "atlassian",
			"logical_capability":              "read_document",
			"adapter_name":                    "ahe-mcp-atlassian-adapter",
			"adapter_version":                 "v1",
			"provider_tool_name":              "read_atlassian_document",
			"provider_tool_input_schema_hash": "sha256:" + strings.Repeat("a", 64),
			"arguments":                       map[string]any{"product": "jira", "object_id": "AHE-42"},
			"command": map[string]any{
				"path": command.Path, "args": command.Args, "directory": command.Directory,
				"environment": command.Environment,
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal MCP host config: %v", err)
	}
	config, err := LoadConfig(writeHostConfig(t, string(payload)))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.SchemaVersion != ConfigSchemaVersionV3 ||
		len(config.Sources) != 0 ||
		len(config.MCPReadSources) != 1 ||
		config.MCPReadSources[0].SourceID != "atlassian:jira:AHE-42" ||
		!mcpstdio.IsMacOSLoopbackOnlyCommand(config.MCPReadSources[0].Command) {
		t.Fatalf("MCP host config = %+v", config)
	}
	workspaceSources := config.workspaceSources()
	if len(workspaceSources) != 1 ||
		workspaceSources[0].CapabilityName != detective.SourceCapabilityMCPReadDocument ||
		workspaceSources[0].RelativePath != "." {
		t.Fatalf("MCP workspace sources = %+v", workspaceSources)
	}
}

func TestLoadConfigRequiresV4ForMCPProposalConversion(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	raw := map[string]any{
		"schema_version":          ConfigSchemaVersionV3,
		"host_id":                 "mcp-conversion-host",
		"registration_request_id": "register-mcp-conversion-host",
		"workspace_id":            "workspace:mcp-conversion-host",
		"workspace_root":          "/tmp/mcp-conversion-host",
		"source_interval":         "30s",
		"max_steps":               1,
		"sources":                 []any{},
		"repository_maintenance":  map[string]any{"enabled": false},
		"mcp_read_sources": []any{map[string]any{
			"source_id":                       "atlassian:jira:AHE-52",
			"connector_id":                    "atlassian-read",
			"provider":                        "atlassian",
			"logical_capability":              "read_document",
			"adapter_name":                    "ahe-mcp-atlassian-adapter",
			"adapter_version":                 "v1",
			"provider_tool_name":              "read_atlassian_document",
			"provider_tool_input_schema_hash": "sha256:" + strings.Repeat("a", 64),
			"arguments":                       map[string]any{"product": "jira", "object_id": "AHE-52"},
			"proposal_conversion":             true,
			"command": map[string]any{
				"path":        "/fixture/ahe-mcp-atlassian-adapter",
				"directory":   "/fixture",
				"environment": []string{"AHE_MCP_ATLASSIAN_ADAPTER_CONFIG=/fixture/adapter.json"},
			},
		}},
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal MCP v3 conversion config: %v", err)
	}
	if _, err := LoadConfig(writeHostConfig(t, string(payload))); err == nil ||
		!strings.Contains(err.Error(), "requires schema_version "+ConfigSchemaVersionV4) {
		t.Fatalf("LoadConfig(v3 proposal conversion) error = %v", err)
	}

	raw["schema_version"] = ConfigSchemaVersionV4
	payload, err = json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal MCP v4 conversion config: %v", err)
	}
	config, err := LoadConfig(writeHostConfig(t, string(payload)))
	if err != nil {
		t.Fatalf("LoadConfig(v4 proposal conversion) error = %v", err)
	}
	if config.SchemaVersion != ConfigSchemaVersionV4 ||
		len(config.MCPReadSources) != 1 ||
		!config.MCPReadSources[0].ProposalConversion ||
		!mcpstdio.IsMacOSLoopbackOnlyCommand(config.MCPReadSources[0].Command) {
		t.Fatalf("MCP v4 proposal conversion config = %+v", config)
	}
}

func TestLoadConfigAcceptsBoundedLoopbackMCPProposalExtraction(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	raw := map[string]any{
		"schema_version":          ConfigSchemaVersionV4,
		"host_id":                 "mcp-extraction-host",
		"registration_request_id": "register-mcp-extraction-host",
		"workspace_id":            "workspace:mcp-extraction-host",
		"workspace_root":          "/tmp/mcp-extraction-host",
		"source_interval":         "30s",
		"max_steps":               1,
		"sources":                 []any{},
		"repository_maintenance":  map[string]any{"enabled": false},
		"mcp_read_sources": []any{map[string]any{
			"source_id":                       "atlassian:jira:AHE-56",
			"connector_id":                    "atlassian-read",
			"provider":                        "atlassian",
			"logical_capability":              "read_document",
			"adapter_name":                    "ahe-mcp-atlassian-adapter",
			"adapter_version":                 "v1",
			"provider_tool_name":              "read_atlassian_document",
			"provider_tool_input_schema_hash": "sha256:" + strings.Repeat("a", 64),
			"arguments":                       map[string]any{"product": "jira", "object_id": "AHE-56"},
			"proposal_extraction": map[string]any{
				"enabled":       true,
				"base_url":      "http://127.0.0.1:11434/api/generate",
				"model":         "gemma4:12b-mlx",
				"timeout":       "45s",
				"num_predict":   512,
				"max_proposals": 8,
				"section_mode":  detective.MCPReadProposalSectionModeHeadingV1,
				"max_sections":  4,
			},
			"command": map[string]any{
				"path":        "/fixture/ahe-mcp-atlassian-adapter",
				"directory":   "/fixture",
				"environment": []string{"AHE_MCP_ATLASSIAN_ADAPTER_CONFIG=/fixture/adapter.json"},
			},
		}},
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal MCP extraction config: %v", err)
	}
	config, err := LoadConfig(writeHostConfig(t, string(payload)))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	extraction := config.MCPReadSources[0].ProposalExtraction
	if extraction == nil ||
		!extraction.Enabled ||
		extraction.Model != "gemma4:12b-mlx" ||
		extraction.Timeout != 45*time.Second ||
		extraction.NumPredict != 512 ||
		extraction.MaxProposals != 8 ||
		extraction.SectionMode != detective.MCPReadProposalSectionModeHeadingV1 ||
		extraction.MaxSections != 4 {
		t.Fatalf("proposal extraction config = %+v", extraction)
	}

	source := raw["mcp_read_sources"].([]any)[0].(map[string]any)
	source["proposal_extraction"].(map[string]any)["base_url"] = "https://models.example.test"
	payload, _ = json.Marshal(raw)
	if _, err := LoadConfig(writeHostConfig(t, string(payload))); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Fatalf("LoadConfig(external model endpoint) error = %v", err)
	}

	source["proposal_extraction"].(map[string]any)["base_url"] = "http://127.0.0.1:11434"
	source["proposal_conversion"] = true
	payload, _ = json.Marshal(raw)
	if _, err := LoadConfig(writeHostConfig(t, string(payload))); err == nil ||
		!strings.Contains(err.Error(), "cannot be enabled with proposal_conversion") {
		t.Fatalf("LoadConfig(conversion plus extraction) error = %v", err)
	}
}

func TestConfigRejectsUnconfinedMCPReadSource(t *testing.T) {
	config := Config{
		SchemaVersion:         ConfigSchemaVersionV3,
		HostID:                "mcp-host",
		RegistrationRequestID: "register-mcp-host",
		WorkspaceID:           "workspace:mcp-host",
		WorkspaceRoot:         "/tmp/mcp-host",
		SourceInterval:        time.Second,
		MaxSteps:              1,
		MCPReadSources: []MCPReadSourceConfig{{
			SourceID:                    "atlassian:jira:AHE-42",
			ConnectorID:                 "atlassian-read",
			Provider:                    "atlassian",
			LogicalCapability:           "read_document",
			AdapterName:                 "adapter",
			AdapterVersion:              "v1",
			ProviderToolName:            "read_atlassian_document",
			ProviderToolInputSchemaHash: "sha256:" + strings.Repeat("a", 64),
			Arguments:                   json.RawMessage(`{"product":"jira","object_id":"AHE-42"}`),
			Command: mcpstdio.CommandConfig{
				Path:      "/fixture/adapter",
				Directory: "/fixture",
			},
		}},
	}
	if err := config.validate(); err == nil {
		t.Fatal("Config.validate() error = nil for unconfined MCP command")
	}
}

func TestLoadConfigRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	tests := map[string]string{
		"unknown field": `{
  "schema_version":"ahe-detective-host-v1","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false},"unexpected":true}`,
		"trailing document": `{
  "schema_version":"ahe-detective-host-v1","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false}} {}`,
		"unsupported capability": `{
  "schema_version":"ahe-detective-host-v1","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"jira","capability_version":"v1","source_id":"jira","relative_path":"."}],
  "repository_maintenance":{"enabled":false}}`,
		"git without maintenance": `{
  "schema_version":"ahe-detective-host-v1","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":2,
  "sources":[{"capability_name":"git-go-repository","capability_version":"v1","source_id":"repo","relative_path":"."}],
  "repository_maintenance":{"enabled":false}}`,
		"git interval below stability window": `{
  "schema_version":"ahe-detective-host-v1","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"500ms","max_steps":2,
  "sources":[{"capability_name":"git-go-repository","capability_version":"v1","source_id":"repo","relative_path":"."}],
  "repository_maintenance":{"enabled":true,"interval":"1s","batch_limit":1}}`,
		"disabled maintenance options": `{
  "schema_version":"ahe-detective-host-v1","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false,"interval":"1s","batch_limit":1}}`,
		"v1 planner": `{
  "schema_version":"ahe-detective-host-v1","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false},
  "planner":{"enabled":true,"model":"gemma4:31b"}}`,
		"disabled planner options": `{
  "schema_version":"ahe-detective-host-v2","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false},
  "planner":{"enabled":false,"model":"gemma4:31b"}}`,
		"planner missing model": `{
  "schema_version":"ahe-detective-host-v2","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false},
  "planner":{"enabled":true}}`,
		"planner non-loopback endpoint": `{
  "schema_version":"ahe-detective-host-v2","host_id":"host","registration_request_id":"register",
  "workspace_id":"workspace:test","workspace_root":"/tmp/test","source_interval":"1s","max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false},
  "planner":{"enabled":true,"model":"gemma4:31b","base_url":"https://example.com"}}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeHostConfig(t, data))
			if err == nil {
				t.Fatal("LoadConfig() error = nil")
			}
		})
	}
}

func TestMaintenanceRequestIDsAreBoundedAndUnique(t *testing.T) {
	controller := &maintenanceController{
		hostID: "host", bootID: strings.Repeat("a", 24), actorID: "ahe-detective-host:host", batchLimit: 10,
	}
	first := controller.nextRequestID("expired")
	second := controller.nextRequestID("retry")
	if first == second {
		t.Fatalf("request IDs are equal: %q", first)
	}
	if len(first) > maxRequestIDBytes || len(second) > maxRequestIDBytes {
		t.Fatalf("request ID lengths = %d, %d", len(first), len(second))
	}
}

func writeHostConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "detective.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
