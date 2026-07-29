package detective

import (
	"reflect"
	"testing"
)

func TestSplitMCPReadHeadingSections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		text         string
		wantHeadings []string
		wantTexts    []string
	}{
		{
			name: "markdown atx headings",
			text: "# Project\n\nPurpose text.\n\n## Limits\n\nLimit text.\n",
			wantHeadings: []string{
				"Project",
				"Limits",
			},
			wantTexts: []string{
				"# Project\n\nPurpose text.\n\n",
				"## Limits\n\nLimit text.\n",
			},
		},
		{
			name: "provider rendered numbered headings",
			text: "1. Project\n\nPurpose text.\n\n  1. Limits\n\nLimit text.\n",
			wantHeadings: []string{
				"Project",
				"Limits",
			},
			wantTexts: []string{
				"1. Project\n\nPurpose text.\n\n",
				"  1. Limits\n\nLimit text.\n",
			},
		},
		{
			name:         "ordinary numbered list is not a heading",
			text:         "Instructions:\n1. Run tests.\n2. Inspect output.\n",
			wantHeadings: []string{""},
			wantTexts:    []string{"Instructions:\n1. Run tests.\n2. Inspect output.\n"},
		},
		{
			name:         "unstructured text remains one envelope",
			text:         "Refunds must be completed within 7 days.",
			wantHeadings: []string{""},
			wantTexts:    []string{"Refunds must be completed within 7 days."},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := splitMCPReadHeadingSections(test.text)
			headings := make([]string, 0, len(got))
			texts := make([]string, 0, len(got))
			for _, section := range got {
				headings = append(headings, section.heading)
				texts = append(texts, section.text)
				if section.text != test.text[section.startByte:section.endByte] {
					t.Fatalf("section bounds %d:%d do not reproduce text", section.startByte, section.endByte)
				}
			}
			if !reflect.DeepEqual(headings, test.wantHeadings) ||
				!reflect.DeepEqual(texts, test.wantTexts) {
				t.Fatalf("splitMCPReadHeadingSections() headings/texts = %#v/%#v, want %#v/%#v", headings, texts, test.wantHeadings, test.wantTexts)
			}
		})
	}
}

func TestValidateMCPReadProposalExtractionInputSectionBudget(t *testing.T) {
	t.Parallel()

	contract, err := validateMCPReadProposalExtractionInput(MCPReadProposalExtractionInput{
		MaxProposals: 1,
		SectionMode:  MCPReadProposalSectionModeHeadingV1,
		MaxSections:  16,
	})
	if err != nil {
		t.Fatalf("validateMCPReadProposalExtractionInput() error = %v", err)
	}
	if contract != MCPReadProposalSectionExtractionContract {
		t.Fatalf("section extraction contract = %q", contract)
	}
	if _, err := validateMCPReadProposalExtractionInput(MCPReadProposalExtractionInput{
		MaxProposals: 2,
		SectionMode:  MCPReadProposalSectionModeHeadingV1,
		MaxSections:  17,
	}); err == nil {
		t.Fatal("section proposal budget unexpectedly accepted")
	}
}
