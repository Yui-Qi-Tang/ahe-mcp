package detectivehost

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/detective"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

const (
	// ConfigSchemaVersion identifies the standalone detective host configuration.
	ConfigSchemaVersion = "ahe-detective-host-v1"
	// ConfigSchemaVersionV2 adds the explicit bounded planner opt-in.
	ConfigSchemaVersionV2 = "ahe-detective-host-v2"
	// ConfigSchemaVersionV3 adds exact deterministic MCP read source bindings.
	ConfigSchemaVersionV3 = "ahe-detective-host-v3"
	// ConfigSchemaVersionV4 adds explicit MCP document proposal conversion and extraction.
	ConfigSchemaVersionV4 = "ahe-detective-host-v4"

	maxConfigBytes       = 1 << 20
	maxHostIDBytes       = 100
	maxRequestIDBytes    = 300
	maxWorkspaceSources  = 32
	minRuntimeInterval   = time.Millisecond
	maxRuntimeInterval   = 24 * time.Hour
	minGitSourceInterval = time.Second
	minMCPSourceInterval = time.Second
)

// Config is one validated, immutable standalone host configuration.
type Config struct {
	SchemaVersion         string
	HostID                string
	RegistrationRequestID string
	WorkspaceID           string
	WorkspaceRoot         string
	Sources               []detective.WorkspaceSourceRegistrationInput
	SourceInterval        time.Duration
	MaxSteps              int
	RepositoryMaintenance RepositoryMaintenanceConfig
	Planner               PlannerConfig
	plannerConfigured     bool
	MCPReadSources        []MCPReadSourceConfig
	mcpReadConfigured     bool
}

// RepositoryMaintenanceConfig controls the existing global bounded repository controllers.
type RepositoryMaintenanceConfig struct {
	Enabled    bool
	Interval   time.Duration
	BatchLimit int
}

// PlannerConfig controls the optional bounded local source-selection planner.
type PlannerConfig struct {
	Enabled    bool
	BaseURL    string
	Model      string
	Timeout    time.Duration
	NumPredict int
}

// MCPReadSourceConfig pins one remote object source and one local adapter invocation.
type MCPReadSourceConfig struct {
	SourceID                    string
	ConnectorID                 string
	Provider                    string
	LogicalCapability           string
	AdapterName                 string
	AdapterVersion              string
	ProviderToolName            string
	ProviderToolInputSchemaHash string
	Arguments                   json.RawMessage
	Command                     mcpstdio.CommandConfig
	ProposalConversion          bool
	ProposalExtraction          *MCPProposalExtractionConfig
}

// MCPProposalExtractionConfig controls one bounded loopback Ollama extractor.
type MCPProposalExtractionConfig struct {
	Enabled      bool
	BaseURL      string
	Model        string
	Timeout      time.Duration
	NumPredict   int
	MaxProposals int
	SectionMode  string
	MaxSections  int
}

type fileConfig struct {
	SchemaVersion         string                                       `json:"schema_version"`
	HostID                string                                       `json:"host_id"`
	RegistrationRequestID string                                       `json:"registration_request_id"`
	WorkspaceID           string                                       `json:"workspace_id"`
	WorkspaceRoot         string                                       `json:"workspace_root"`
	Sources               []detective.WorkspaceSourceRegistrationInput `json:"sources"`
	SourceInterval        string                                       `json:"source_interval"`
	MaxSteps              int                                          `json:"max_steps"`
	RepositoryMaintenance fileRepositoryMaintenanceConfig              `json:"repository_maintenance"`
	Planner               *filePlannerConfig                           `json:"planner,omitempty"`
	MCPReadSources        []fileMCPReadSourceConfig                    `json:"mcp_read_sources,omitempty"`
}

type fileRepositoryMaintenanceConfig struct {
	Enabled    bool   `json:"enabled"`
	Interval   string `json:"interval,omitempty"`
	BatchLimit int    `json:"batch_limit,omitempty"`
}

type filePlannerConfig struct {
	Enabled    bool   `json:"enabled"`
	BaseURL    string `json:"base_url,omitempty"`
	Model      string `json:"model,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
	NumPredict int    `json:"num_predict,omitempty"`
}

type fileMCPReadSourceConfig struct {
	SourceID                    string                           `json:"source_id"`
	ConnectorID                 string                           `json:"connector_id"`
	Provider                    string                           `json:"provider"`
	LogicalCapability           string                           `json:"logical_capability"`
	AdapterName                 string                           `json:"adapter_name"`
	AdapterVersion              string                           `json:"adapter_version"`
	ProviderToolName            string                           `json:"provider_tool_name"`
	ProviderToolInputSchemaHash string                           `json:"provider_tool_input_schema_hash"`
	Arguments                   json.RawMessage                  `json:"arguments"`
	Command                     fileCommandConfig                `json:"command"`
	ProposalConversion          bool                             `json:"proposal_conversion,omitempty"`
	ProposalExtraction          *fileMCPProposalExtractionConfig `json:"proposal_extraction,omitempty"`
}

type fileMCPProposalExtractionConfig struct {
	Enabled      bool   `json:"enabled"`
	BaseURL      string `json:"base_url,omitempty"`
	Model        string `json:"model,omitempty"`
	Timeout      string `json:"timeout,omitempty"`
	NumPredict   int    `json:"num_predict,omitempty"`
	MaxProposals int    `json:"max_proposals,omitempty"`
	SectionMode  string `json:"section_mode,omitempty"`
	MaxSections  int    `json:"max_sections,omitempty"`
}

type fileCommandConfig struct {
	Path        string   `json:"path"`
	Args        []string `json:"args,omitempty"`
	Directory   string   `json:"directory"`
	Environment []string `json:"environment,omitempty"`
}

// LoadConfig decodes one strict, bounded JSON host configuration.
func LoadConfig(path string) (Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Config{}, errors.New("detective host config path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("opening detective host config: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("reading detective host config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("detective host config exceeds %d bytes", maxConfigBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw fileConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decoding detective host config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("detective host config must contain exactly one JSON document")
	}

	sourceInterval, err := time.ParseDuration(strings.TrimSpace(raw.SourceInterval))
	if err != nil {
		return Config{}, fmt.Errorf("parsing source_interval: %w", err)
	}
	maintenanceInterval := time.Duration(0)
	if raw.RepositoryMaintenance.Enabled {
		maintenanceInterval, err = time.ParseDuration(strings.TrimSpace(raw.RepositoryMaintenance.Interval))
		if err != nil {
			return Config{}, fmt.Errorf("parsing repository_maintenance.interval: %w", err)
		}
	} else if strings.TrimSpace(raw.RepositoryMaintenance.Interval) != "" || raw.RepositoryMaintenance.BatchLimit != 0 {
		return Config{}, errors.New("disabled repository_maintenance must not set interval or batch_limit")
	}
	planner := PlannerConfig{}
	if raw.Planner != nil {
		if !raw.Planner.Enabled &&
			(strings.TrimSpace(raw.Planner.BaseURL) != "" ||
				strings.TrimSpace(raw.Planner.Model) != "" ||
				strings.TrimSpace(raw.Planner.Timeout) != "" ||
				raw.Planner.NumPredict != 0) {
			return Config{}, errors.New("disabled planner must not set base_url, model, timeout, or num_predict")
		}
		planner.Enabled = raw.Planner.Enabled
		planner.BaseURL = raw.Planner.BaseURL
		planner.Model = raw.Planner.Model
		planner.NumPredict = raw.Planner.NumPredict
		if strings.TrimSpace(raw.Planner.Timeout) != "" {
			planner.Timeout, err = time.ParseDuration(strings.TrimSpace(raw.Planner.Timeout))
			if err != nil {
				return Config{}, fmt.Errorf("parsing planner.timeout: %w", err)
			}
		}
	}

	config := Config{
		SchemaVersion:         raw.SchemaVersion,
		HostID:                raw.HostID,
		RegistrationRequestID: raw.RegistrationRequestID,
		WorkspaceID:           raw.WorkspaceID,
		WorkspaceRoot:         raw.WorkspaceRoot,
		Sources:               append([]detective.WorkspaceSourceRegistrationInput(nil), raw.Sources...),
		SourceInterval:        sourceInterval,
		MaxSteps:              raw.MaxSteps,
		RepositoryMaintenance: RepositoryMaintenanceConfig{
			Enabled:    raw.RepositoryMaintenance.Enabled,
			Interval:   maintenanceInterval,
			BatchLimit: raw.RepositoryMaintenance.BatchLimit,
		},
		Planner:           planner,
		plannerConfigured: raw.Planner != nil,
		MCPReadSources:    make([]MCPReadSourceConfig, len(raw.MCPReadSources)),
		mcpReadConfigured: raw.MCPReadSources != nil,
	}
	for index, source := range raw.MCPReadSources {
		command, err := mcpstdio.MacOSLoopbackOnlyCommand(mcpstdio.CommandConfig{
			Path:        source.Command.Path,
			Args:        append([]string(nil), source.Command.Args...),
			Directory:   source.Command.Directory,
			Environment: append([]string(nil), source.Command.Environment...),
		})
		if err != nil {
			return Config{}, fmt.Errorf("sandboxing mcp_read_sources[%d].command: %w", index, err)
		}
		config.MCPReadSources[index] = MCPReadSourceConfig{
			SourceID:                    source.SourceID,
			ConnectorID:                 source.ConnectorID,
			Provider:                    source.Provider,
			LogicalCapability:           source.LogicalCapability,
			AdapterName:                 source.AdapterName,
			AdapterVersion:              source.AdapterVersion,
			ProviderToolName:            source.ProviderToolName,
			ProviderToolInputSchemaHash: source.ProviderToolInputSchemaHash,
			Arguments:                   append(json.RawMessage(nil), source.Arguments...),
			Command:                     command,
			ProposalConversion:          source.ProposalConversion,
		}
		if source.ProposalExtraction != nil {
			extraction := &MCPProposalExtractionConfig{
				Enabled:      source.ProposalExtraction.Enabled,
				BaseURL:      source.ProposalExtraction.BaseURL,
				Model:        source.ProposalExtraction.Model,
				NumPredict:   source.ProposalExtraction.NumPredict,
				MaxProposals: source.ProposalExtraction.MaxProposals,
				SectionMode:  source.ProposalExtraction.SectionMode,
				MaxSections:  source.ProposalExtraction.MaxSections,
			}
			if strings.TrimSpace(source.ProposalExtraction.Timeout) != "" {
				extraction.Timeout, err = time.ParseDuration(strings.TrimSpace(source.ProposalExtraction.Timeout))
				if err != nil {
					return Config{}, fmt.Errorf("parsing mcp_read_sources[%d].proposal_extraction.timeout: %w", index, err)
				}
			}
			config.MCPReadSources[index].ProposalExtraction = extraction
		}
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c *Config) validate() error {
	if c == nil {
		return errors.New("detective host config is required")
	}
	c.SchemaVersion = strings.TrimSpace(c.SchemaVersion)
	if c.SchemaVersion != ConfigSchemaVersion &&
		c.SchemaVersion != ConfigSchemaVersionV2 &&
		c.SchemaVersion != ConfigSchemaVersionV3 &&
		c.SchemaVersion != ConfigSchemaVersionV4 {
		return fmt.Errorf(
			"detective host schema_version must be %s, %s, %s, or %s",
			ConfigSchemaVersion,
			ConfigSchemaVersionV2,
			ConfigSchemaVersionV3,
			ConfigSchemaVersionV4,
		)
	}
	if c.SchemaVersion == ConfigSchemaVersion &&
		(c.plannerConfigured || c.Planner != (PlannerConfig{})) {
		return fmt.Errorf("detective host schema_version %s does not support planner configuration", ConfigSchemaVersion)
	}
	if c.SchemaVersion != ConfigSchemaVersionV3 &&
		c.SchemaVersion != ConfigSchemaVersionV4 &&
		(c.mcpReadConfigured || len(c.MCPReadSources) != 0) {
		return fmt.Errorf(
			"detective host schema_version %s does not support mcp_read_sources",
			c.SchemaVersion,
		)
	}
	var err error
	c.HostID, err = normalizeConfigText(c.HostID, "host_id", maxHostIDBytes)
	if err != nil {
		return err
	}
	c.RegistrationRequestID, err = normalizeConfigText(c.RegistrationRequestID, "registration_request_id", maxRequestIDBytes)
	if err != nil {
		return err
	}
	c.WorkspaceID, err = normalizeConfigText(c.WorkspaceID, "workspace_id", maxRequestIDBytes)
	if err != nil {
		return err
	}
	c.WorkspaceRoot, err = normalizeConfigText(c.WorkspaceRoot, "workspace_root", maxConfigBytes)
	if err != nil {
		return err
	}
	if c.SourceInterval < minRuntimeInterval || c.SourceInterval > maxRuntimeInterval {
		return fmt.Errorf("source_interval must be between %s and %s", minRuntimeInterval, maxRuntimeInterval)
	}
	if c.MaxSteps < 1 || c.MaxSteps > 32 {
		return errors.New("max_steps must be between 1 and 32")
	}
	totalSources := len(c.Sources) + len(c.MCPReadSources)
	if totalSources < 1 || totalSources > maxWorkspaceSources {
		return fmt.Errorf("sources plus mcp_read_sources must contain between 1 and %d entries", maxWorkspaceSources)
	}

	capabilities := make(map[string]struct{})
	for _, capability := range detective.ListSourceCapabilities() {
		capabilities[capability.Name+"\x00"+capability.Version] = struct{}{}
	}
	hasGitSource := false
	for index := range c.Sources {
		source := &c.Sources[index]
		source.CapabilityName, err = normalizeConfigText(source.CapabilityName, fmt.Sprintf("sources[%d].capability_name", index), 100)
		if err != nil {
			return err
		}
		source.CapabilityVersion, err = normalizeConfigText(source.CapabilityVersion, fmt.Sprintf("sources[%d].capability_version", index), 100)
		if err != nil {
			return err
		}
		source.SourceID, err = normalizeConfigText(source.SourceID, fmt.Sprintf("sources[%d].source_id", index), 500)
		if err != nil {
			return err
		}
		source.RelativePath, err = normalizeConfigText(source.RelativePath, fmt.Sprintf("sources[%d].relative_path", index), 4096)
		if err != nil {
			return err
		}
		if _, ok := capabilities[source.CapabilityName+"\x00"+source.CapabilityVersion]; !ok {
			return fmt.Errorf(
				"sources[%d] uses unsupported capability %s/%s",
				index,
				source.CapabilityName,
				source.CapabilityVersion,
			)
		}
		if source.CapabilityName == detective.SourceCapabilityMCPReadDocument {
			return fmt.Errorf("sources[%d] must use mcp_read_sources for %s", index, detective.SourceCapabilityMCPReadDocument)
		}
		if source.CapabilityName == detective.SourceCapabilityGitGoRepository {
			hasGitSource = true
		}
	}
	mcpSourceIDs := make(map[string]struct{}, len(c.MCPReadSources))
	for index := range c.MCPReadSources {
		source := &c.MCPReadSources[index]
		source.SourceID, err = normalizeConfigText(source.SourceID, fmt.Sprintf("mcp_read_sources[%d].source_id", index), 500)
		if err != nil {
			return err
		}
		if _, exists := mcpSourceIDs[source.SourceID]; exists {
			return fmt.Errorf("mcp_read_sources duplicate source_id %q", source.SourceID)
		}
		mcpSourceIDs[source.SourceID] = struct{}{}
		fields := []struct {
			value    *string
			name     string
			maxBytes int
		}{
			{&source.ConnectorID, "connector_id", 200},
			{&source.Provider, "provider", 100},
			{&source.LogicalCapability, "logical_capability", 100},
			{&source.AdapterName, "adapter_name", 100},
			{&source.AdapterVersion, "adapter_version", 100},
			{&source.ProviderToolName, "provider_tool_name", 200},
		}
		for _, field := range fields {
			*field.value, err = normalizeConfigText(
				*field.value,
				fmt.Sprintf("mcp_read_sources[%d].%s", index, field.name),
				field.maxBytes,
			)
			if err != nil {
				return err
			}
		}
		source.ProviderToolInputSchemaHash = strings.TrimSpace(source.ProviderToolInputSchemaHash)
		if !validConfigSHA256(source.ProviderToolInputSchemaHash) {
			return fmt.Errorf("mcp_read_sources[%d].provider_tool_input_schema_hash is invalid", index)
		}
		if len(source.Arguments) == 0 {
			source.Arguments = json.RawMessage(`{}`)
		}
		var arguments map[string]json.RawMessage
		if err := json.Unmarshal(source.Arguments, &arguments); err != nil || arguments == nil {
			return fmt.Errorf("mcp_read_sources[%d].arguments must be a JSON object", index)
		}
		source.Arguments, err = json.Marshal(arguments)
		if err != nil {
			return fmt.Errorf("canonicalizing mcp_read_sources[%d].arguments: %w", index, err)
		}
		if len(source.Arguments) > 64<<10 {
			return fmt.Errorf("mcp_read_sources[%d].arguments exceed 65536 bytes", index)
		}
		source.Command, err = mcpstdio.NormalizeCommandConfig(source.Command)
		if err != nil {
			return fmt.Errorf("validating mcp_read_sources[%d].command: %w", index, err)
		}
		if !mcpstdio.IsMacOSLoopbackOnlyCommand(source.Command) {
			return fmt.Errorf("mcp_read_sources[%d].command must use the exact macOS loopback-only wrapper", index)
		}
		if source.ProposalConversion && c.SchemaVersion != ConfigSchemaVersionV4 {
			return fmt.Errorf(
				"mcp_read_sources[%d].proposal_conversion requires schema_version %s",
				index,
				ConfigSchemaVersionV4,
			)
		}
		if err := validateMCPProposalExtraction(
			source.ProposalExtraction,
			source.ProposalConversion,
			c.SchemaVersion,
			index,
		); err != nil {
			return err
		}
	}
	if len(c.MCPReadSources) != 0 && c.SourceInterval < minMCPSourceInterval {
		return fmt.Errorf("source_interval must be at least %s when an MCP read source is configured", minMCPSourceInterval)
	}
	if len(c.MCPReadSources) != 0 && c.Planner.Enabled {
		return errors.New("planner must be disabled when mcp_read_sources are configured")
	}

	maintenance := &c.RepositoryMaintenance
	if hasGitSource {
		if c.SourceInterval < minGitSourceInterval {
			return fmt.Errorf("source_interval must be at least %s when a Git source is configured", minGitSourceInterval)
		}
		if c.MaxSteps < 2 {
			return errors.New("max_steps must be at least 2 when a Git source is configured")
		}
		if !maintenance.Enabled {
			return errors.New("repository_maintenance.enabled must be true when a Git source is configured")
		}
	}
	if !maintenance.Enabled {
		if maintenance.Interval != 0 || maintenance.BatchLimit != 0 {
			return errors.New("disabled repository_maintenance must not set interval or batch_limit")
		}
		return c.validatePlanner()
	}
	if maintenance.Interval < minRuntimeInterval || maintenance.Interval > maxRuntimeInterval {
		return fmt.Errorf("repository_maintenance.interval must be between %s and %s", minRuntimeInterval, maxRuntimeInterval)
	}
	maxBatchLimit := evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit
	if evidenceingestion.RepositoryExtractionWorkRetryControllerTickMaxLimit < maxBatchLimit {
		maxBatchLimit = evidenceingestion.RepositoryExtractionWorkRetryControllerTickMaxLimit
	}
	if maintenance.BatchLimit < 1 || maintenance.BatchLimit > maxBatchLimit {
		return fmt.Errorf("repository_maintenance.batch_limit must be between 1 and %d", maxBatchLimit)
	}
	return c.validatePlanner()
}

func validateMCPProposalExtraction(
	extraction *MCPProposalExtractionConfig,
	proposalConversion bool,
	schemaVersion string,
	sourceIndex int,
) error {
	if extraction == nil {
		return nil
	}
	field := fmt.Sprintf("mcp_read_sources[%d].proposal_extraction", sourceIndex)
	if !extraction.Enabled {
		if extraction.BaseURL != "" ||
			extraction.Model != "" ||
			extraction.Timeout != 0 ||
			extraction.NumPredict != 0 ||
			extraction.MaxProposals != 0 ||
			extraction.SectionMode != "" ||
			extraction.MaxSections != 0 {
			return fmt.Errorf("disabled %s must not set model options", field)
		}
		return nil
	}
	if schemaVersion != ConfigSchemaVersionV4 {
		return fmt.Errorf("enabled %s requires schema_version %s", field, ConfigSchemaVersionV4)
	}
	if proposalConversion {
		return fmt.Errorf("%s cannot be enabled with proposal_conversion", field)
	}
	var err error
	extraction.Model, err = normalizeConfigText(extraction.Model, field+".model", 200)
	if err != nil {
		return err
	}
	extraction.BaseURL = strings.TrimSpace(extraction.BaseURL)
	if extraction.BaseURL == "" {
		extraction.BaseURL = evidenceingestion.DefaultOllamaExtractorURL
	}
	if err := validateLoopbackOllamaURL(extraction.BaseURL); err != nil {
		return fmt.Errorf("validating %s.base_url: %w", field, err)
	}
	if extraction.Timeout <= 0 || extraction.Timeout > 10*time.Minute {
		return fmt.Errorf("%s.timeout must be between 1ns and 10m", field)
	}
	if extraction.NumPredict < 1 || extraction.NumPredict > 8192 {
		return fmt.Errorf("%s.num_predict must be between 1 and 8192", field)
	}
	if extraction.MaxProposals < 1 || extraction.MaxProposals > 32 {
		return fmt.Errorf("%s.max_proposals must be between 1 and 32", field)
	}
	extraction.SectionMode = strings.TrimSpace(extraction.SectionMode)
	switch extraction.SectionMode {
	case "":
		if extraction.MaxSections != 0 {
			return fmt.Errorf("%s.max_sections requires section_mode", field)
		}
	case detective.MCPReadProposalSectionModeHeadingV1:
		if extraction.MaxSections < 1 || extraction.MaxSections > 32 {
			return fmt.Errorf("%s.max_sections must be between 1 and 32", field)
		}
		if extraction.MaxProposals > 32/extraction.MaxSections {
			return fmt.Errorf("%s section proposal budget must not exceed 32", field)
		}
	default:
		return fmt.Errorf("%s.section_mode %q is unsupported", field, extraction.SectionMode)
	}
	return nil
}

func validateLoopbackOllamaURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parsing URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must be an unauthenticated loopback HTTP endpoint")
	}
	host := parsed.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("URL host must be loopback")
		}
	}
	if parsed.Path != "" && parsed.Path != "/" && parsed.Path != "/api/generate" {
		return errors.New("URL path must be empty, /, or /api/generate")
	}
	return nil
}

func (c *Config) validatePlanner() error {
	planner := &c.Planner
	if !planner.Enabled {
		if *planner != (PlannerConfig{}) {
			return errors.New("disabled planner must not set base_url, model, timeout, or num_predict")
		}
		return nil
	}
	if c.SchemaVersion != ConfigSchemaVersionV2 &&
		c.SchemaVersion != ConfigSchemaVersionV3 &&
		c.SchemaVersion != ConfigSchemaVersionV4 {
		return fmt.Errorf(
			"enabled planner requires detective host schema_version %s, %s, or %s",
			ConfigSchemaVersionV2,
			ConfigSchemaVersionV3,
			ConfigSchemaVersionV4,
		)
	}
	var err error
	planner.Model, err = normalizeConfigText(planner.Model, "planner.model", 200)
	if err != nil {
		return err
	}
	planner.BaseURL = strings.TrimSpace(planner.BaseURL)
	if _, err := detective.NewOllamaPlannerRunner(detective.OllamaPlannerConfig{
		BaseURL:    planner.BaseURL,
		Model:      planner.Model,
		Timeout:    planner.Timeout,
		NumPredict: planner.NumPredict,
	}); err != nil {
		return fmt.Errorf("validating planner: %w", err)
	}
	return nil
}

func (c Config) workspaceSources() []detective.WorkspaceSourceRegistrationInput {
	sources := append([]detective.WorkspaceSourceRegistrationInput(nil), c.Sources...)
	for _, source := range c.MCPReadSources {
		sources = append(sources, detective.WorkspaceSourceRegistrationInput{
			CapabilityName:    detective.SourceCapabilityMCPReadDocument,
			CapabilityVersion: detective.SourceCapabilityMCPReadDocumentVersion,
			SourceID:          source.SourceID,
			RelativePath:      ".",
		})
	}
	return sources
}

func (c MCPReadSourceConfig) detectiveConfig() detective.MCPReadSourceConfig {
	return detective.MCPReadSourceConfig{
		ConnectorID:                 c.ConnectorID,
		Provider:                    c.Provider,
		LogicalCapability:           c.LogicalCapability,
		AdapterName:                 c.AdapterName,
		AdapterVersion:              c.AdapterVersion,
		ProviderToolName:            c.ProviderToolName,
		ProviderToolInputSchemaHash: c.ProviderToolInputSchemaHash,
		Arguments:                   append(json.RawMessage(nil), c.Arguments...),
		Command: mcpstdio.CommandConfig{
			Path:        c.Command.Path,
			Args:        append([]string(nil), c.Command.Args...),
			Directory:   c.Command.Directory,
			Environment: append([]string(nil), c.Command.Environment...),
		},
	}
}

func validConfigSHA256(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && len(decoded) == 32
}

func normalizeConfigText(value, field string, maxBytes int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return "", fmt.Errorf("%s must contain 1 to %d valid UTF-8 bytes", field, maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%s must not contain control characters", field)
		}
	}
	return value, nil
}
