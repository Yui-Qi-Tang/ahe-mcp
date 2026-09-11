package mcpquery

import (
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestHanModeIsExplicitInQuerySchema(t *testing.T) {
	properties := groundedEvidenceBriefSchema()["properties"].(map[string]any)
	mode := properties["query_mode"].(map[string]any)
	values := mode["enum"].([]string)
	if !slices.Contains(values, evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1) || !strings.Contains(mode["description"].(string), "Defaults to deterministic_lexical_recovery") {
		t.Fatalf("experimental opt-in or unchanged default missing: %+v", mode)
	}
	if len(queryTools()) != 13 {
		t.Fatal("experimental query mode changed the tool set")
	}
}
