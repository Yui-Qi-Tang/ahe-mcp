package mcpquery

import (
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

func TestMultisurfaceAndV6AreExplicitInToolSchema(t *testing.T) {
	schema := groundedEvidenceBriefSchema()
	properties := schema["properties"].(map[string]any)
	mode := properties["query_mode"].(map[string]any)
	response := properties["response_schema"].(map[string]any)
	if !slices.Contains(mode["enum"].([]string), evidenceingestion.EvidenceQueryModeExperimentalMultisurfaceV1) ||
		!slices.Contains(response["enum"].([]string), evidencequerymcp.GroundedEvidenceBriefSchemaV6) {
		t.Fatal("explicit multisurface opt-in is missing")
	}
	if !strings.Contains(mode["description"].(string), "Defaults to deterministic_lexical_recovery") ||
		!strings.Contains(mode["description"].(string), "requires response_schema grounded-evidence-brief-v6") ||
		!strings.Contains(response["description"].(string), "v6 requires query_mode experimental_multisurface_lexical_v1") ||
		schema["additionalProperties"] != false || len(queryTools()) != 13 {
		t.Fatal("schema changed defaults, authority or lost explicit pairing")
	}
	if len(schema["allOf"].([]map[string]any)) != 4 {
		t.Fatal("schema does not enforce both directions of the opt-in pair")
	}
}

func TestPracticalV7IsAnAdditionalExplicitPair(t *testing.T) {
	schema := groundedEvidenceBriefSchema()
	props := schema["properties"].(map[string]any)
	if !slices.Contains(props["query_mode"].(map[string]any)["enum"].([]string), evidenceingestion.EvidenceQueryModePracticalMultisurfaceV1) ||
		!slices.Contains(props["response_schema"].(map[string]any)["enum"].([]string), evidencequerymcp.GroundedEvidenceBriefSchemaV7) {
		t.Fatal("practical opt-in missing")
	}
	for i, pair := range [][2]string{
		{evidenceingestion.EvidenceQueryModeExperimentalMultisurfaceV1, evidencequerymcp.GroundedEvidenceBriefSchemaV6},
		{evidenceingestion.EvidenceQueryModePracticalMultisurfaceV1, evidencequerymcp.GroundedEvidenceBriefSchemaV7},
	} {
		for direction := range 2 {
			clause := schema["allOf"].([]map[string]any)[i*2+direction]
			fields := []string{"query_mode", "response_schema"}
			for j, key := range []string{"if", "then"} {
				index := (j + direction) % 2
				condition := clause[key].(map[string]any)
				if !slices.Equal(condition["required"].([]string), []string{fields[index]}) ||
					condition["properties"].(map[string]any)[fields[index]].(map[string]any)["const"] != pair[index] {
					t.Fatal("mode/schema gate is not bidirectional")
				}
			}
		}
	}
}
