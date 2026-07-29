package detective

import "github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

const (
	// SourceCapabilityCatalogV1 identifies the first closed detective source-capability catalog.
	SourceCapabilityCatalogV1 = "detective-source-capabilities-v1"

	// SourceCapabilityGitGoRepository identifies the bounded local Git and Go source adapter.
	SourceCapabilityGitGoRepository = "git-go-repository"
	// SourceCapabilityGitGoRepositoryVersion is the first Git and Go capability contract.
	SourceCapabilityGitGoRepositoryVersion = "v1"
	// SourceCapabilityLocalPRDText identifies bounded local PRD or text intake.
	SourceCapabilityLocalPRDText = "local-prd-text"
	// SourceCapabilityLocalPRDTextVersion is the first local PRD or text capability contract.
	SourceCapabilityLocalPRDTextVersion = "v1"
	// SourceCapabilityMCPReadDocument identifies one pinned remote document read.
	SourceCapabilityMCPReadDocument = "mcp-read-document"
	// SourceCapabilityMCPReadDocumentVersion is the first durable MCP polling contract.
	SourceCapabilityMCPReadDocumentVersion = "v1"
)

// SourceCapability is one compiled-in source capability available to bounded orchestration.
type SourceCapability struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	SourceSystem string `json:"source_system"`
}

var sourceCapabilities = []SourceCapability{
	{
		Name:         SourceCapabilityGitGoRepository,
		Version:      SourceCapabilityGitGoRepositoryVersion,
		SourceSystem: evidenceingestion.SourceSystemCodeRepository,
	},
	{
		Name:         SourceCapabilityLocalPRDText,
		Version:      SourceCapabilityLocalPRDTextVersion,
		SourceSystem: evidenceingestion.SourceSystemManualText,
	},
	{
		Name:         SourceCapabilityMCPReadDocument,
		Version:      SourceCapabilityMCPReadDocumentVersion,
		SourceSystem: evidenceingestion.SourceSystemMCPReadDocument,
	},
}

// ListSourceCapabilities returns a copy of the closed, versioned capability catalog.
func ListSourceCapabilities() []SourceCapability {
	return append([]SourceCapability(nil), sourceCapabilities...)
}

func lookupSourceCapability(name, version string) (SourceCapability, bool) {
	for _, capability := range sourceCapabilities {
		if capability.Name == name && capability.Version == version {
			return capability, true
		}
	}
	return SourceCapability{}, false
}
