package mcpquery

import (
	"regexp"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

// TestServerInstructionsNameRealToolsAndPolicies keeps the initialize text from
// naming a tool or policy that the authorized Query surface does not have.
func TestServerInstructionsNameRealToolsAndPolicies(t *testing.T) {
	text := evidencequerymcp.ServerInstructions
	for _, name := range regexp.MustCompile(`\b(?:get|check|list|search|open|find)_[a-z_]+\b`).FindAllString(text, -1) {
		if !slices.Contains(queryRuntimeToolNames, name) {
			t.Errorf("instructions name %s, which Query does not expose", name)
		}
	}
	for _, policy := range regexp.MustCompile(`\b[a-z-]+-action/v\d+\b`).FindAllString(text, -1) {
		if policy != string(evidenceuse.CurrentActionV1) && policy != string(evidenceuse.GroundedActionV1) {
			t.Errorf("instructions name unknown policy %s", policy)
		}
	}
}
