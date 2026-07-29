package evidenceingestion

import "testing"

func TestValidateDocumentSectionCoverage(t *testing.T) {
	t.Parallel()

	attempt := attemptContext{
		manualSourceContext: manualSourceContext{
			SourceSnapshot: SourceSnapshot{SourceSystem: SourceSystemMCPReadDocument},
		},
		ExtractorDefinition: ExtractorDefinition{Config: map[string]string{
			"section_mode":                     "heading_sections_v1",
			"document_section_coverage_schema": DocumentSectionCoverageSchemaV1,
			"max_sections":                     "2",
			"max_proposals_per_section":        "1",
		}},
	}
	valid := FrozenExtractorOutput{
		Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "section-001-proposal-01",
		}},
		DocumentSectionCoverage: &DocumentSectionCoverage{
			SchemaVersion:            DocumentSectionCoverageSchemaV1,
			SegmentationContract:     "heading_sections_v1",
			OffsetBasis:              DocumentSectionCoverageOffsetAdapterSelectedUTF8,
			CoverageComplete:         true,
			NegativeInferenceAllowed: false,
			SelectedSourceCount:      1,
			SectionCount:             2,
			ModelCallCount:           2,
			MaxSections:              2,
			MaxProposalsPerSection:   1,
			Sections: []DocumentSectionCoverageItem{
				{
					SectionID:        "section-001",
					CandidateLocalID: "selected",
					StartByte:        0,
					EndByte:          10,
					ContentHash:      contentHash([]byte("section one")),
					ResponseHash:     contentHash([]byte(`{"proposals":[{}]}`)),
					ProposalLocalIDs: []string{"section-001-proposal-01"},
				},
				{
					SectionID:        "section-002",
					CandidateLocalID: "selected",
					StartByte:        10,
					EndByte:          20,
					ContentHash:      contentHash([]byte("section two")),
					ResponseHash:     contentHash([]byte(`{"proposals":[]}`)),
					ProposalLocalIDs: []string{},
					Abstained:        true,
				},
			},
		},
	}
	if err := validateDocumentSectionCoverage(attempt, valid); err != nil {
		t.Fatalf("validateDocumentSectionCoverage() error = %v", err)
	}

	tampered := valid
	coverage := *valid.DocumentSectionCoverage
	tampered.DocumentSectionCoverage = &coverage
	tampered.DocumentSectionCoverage.NegativeInferenceAllowed = true
	if err := validateDocumentSectionCoverage(attempt, tampered); err == nil {
		t.Fatal("negative-inference section coverage unexpectedly accepted")
	}

	tampered = valid
	coverage = *valid.DocumentSectionCoverage
	coverage.Sections = append([]DocumentSectionCoverageItem(nil), coverage.Sections...)
	coverage.Sections[0].ProposalLocalIDs = []string{"unknown"}
	tampered.DocumentSectionCoverage = &coverage
	if err := validateDocumentSectionCoverage(attempt, tampered); err == nil {
		t.Fatal("unknown proposal section coverage unexpectedly accepted")
	}
}
