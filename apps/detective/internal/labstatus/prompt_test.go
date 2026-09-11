package labstatus

import (
	"strings"
	"testing"
)

func TestExtractionScopeInstructionSeparatesCapabilityFromVerification(t *testing.T) {
	for _, want := range []string{
		"Choose scope from what the candidate asserts",
		"Use runtime_core for an implemented evidence/admission/query runtime capability",
		"Use source_tree_verification only for an explicit test/build/check result",
		"never merely because a capability is documented or unreleased",
	} {
		if !strings.Contains(agentInstruction, want) {
			t.Errorf("scope instruction is missing %q", want)
		}
	}
}

func TestBuildPromptIncludesStatusMappingGuards(t *testing.T) {
	document := loadTestDocument(t, "## Status at a Glance\ntext\n")
	prompt := buildPrompt(document)

	for _, want := range []string{
		`Never emit "unreleased_lab_proven" unless the status cell contains "UNRELEASED LAB".`,
		`Never emit "unreleased_core_proven" unless it contains "UNRELEASED CORE".`,
		`When the status cell is IMPLEMENTED, EXPOSED, every record from that source line must use "implemented_exposed".`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("buildPrompt() does not contain %q", want)
		}
	}
}

func TestBuildPromptIncludesControllerStatusConstraint(t *testing.T) {
	document := loadTestDocument(t, "## Status at a Glance\ntext\n")
	prompt := buildPromptWithStatusConstraint(document, &statusConstraint{
		clause:  "LAB DOMAIN PROVEN",
		allowed: []string{"lab_proven"},
	})

	for _, want := range []string{
		`Controller-selected status clause: "LAB DOMAIN PROVEN"`,
		"every record status must be one of: lab_proven",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("buildPromptWithStatusConstraint() does not contain %q", want)
		}
	}
}
