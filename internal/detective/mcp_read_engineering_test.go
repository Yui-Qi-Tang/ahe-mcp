package detective

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestMCPReadEngineeringModesRemainExplicit(t *testing.T) {
	for _, identity := range []struct {
		provider string
		adapter  string
	}{
		{provider: "atlassian", adapter: "other-adapter"},
		{provider: "codegraph", adapter: "other-adapter"},
		{provider: "other", adapter: "ahe-mcp-atlassian-adapter"},
		{provider: "other", adapter: "ahe-mcp-codegraph-adapter"},
		{provider: " Atlassian ", adapter: "other-adapter"},
	} {
		t.Run(identity.provider+"/"+identity.adapter, func(t *testing.T) {
			config := mcpReadPeriodicRuntimeTestConfig("workspace:engineering-model-selected", 1)
			config.Sources[0].Config.Provider = identity.provider
			config.Sources[0].Config.AdapterName = identity.adapter
			calls := 0
			config.Sources[0].ProposalExtractor = &MCPReadProposalExtractorConfig{
				MaxProposals: 4,
				Runner: func(context.Context, evidenceingestion.ExtractorInput) ([]byte, error) {
					calls++
					return []byte(`{"proposals":[]}`), nil
				},
			}
			prepared, err := prepareMCPReadPeriodicRuntimeConfig(config)
			if err != nil || calls != 0 {
				t.Fatalf("engineering extraction error = %v, model calls = %d", err, calls)
			}
			if prepared.Sources[0].ProposalConversion || prepared.Sources[0].ProposalExtractor == nil {
				t.Fatalf("configured model selection was replaced: %+v", prepared.Sources[0])
			}
			config.Sources[0].ProposalExtractor = nil
			for _, conversion := range []bool{false, true} {
				config.Sources[0].ProposalConversion = conversion
				prepared, err := prepareMCPReadPeriodicRuntimeConfig(config)
				if err != nil {
					t.Fatalf("engineering source conversion=%t error = %v", conversion, err)
				}
				if prepared.Sources[0].ProposalConversion != conversion || prepared.Sources[0].ProposalExtractor != nil {
					t.Fatalf("configured collection/conversion choice was changed: %+v", prepared.Sources[0])
				}
			}
		})
	}
}

func TestMCPReadSelectedEngineeringValuesPreserveDetails(t *testing.T) {
	requirements := []string{
		"Requests must carry a request ID before processing.",
		"The service must not retry when validation fails.",
		"Unless the operator approves recovery, failed jobs remain pending.",
		"Release status: implemented in the lab; not deployed to production.",
		"逾時上限為 30 秒；例外：核准的離線工作可延長至 60 秒。",
	}
	for _, grouped := range []bool{false, true} {
		values := requirements
		if grouped {
			values = []string{strings.Join(requirements, "\n")}
		}
		document := make(map[string]string, len(values))
		candidates := make([]MCPReadProposalCandidate, 0, len(values))
		for index, value := range values {
			key := string(rune('a' + index))
			document[key] = value
			candidates = append(candidates, MCPReadProposalCandidate{
				LocalID: key, SelectorKind: MCPReadProposalSelectorJSONPointerString, Selector: "/" + key,
			})
		}
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		input := evidenceingestion.ExtractorInput{
			RenderedText: string(raw),
			Spans: []evidenceingestion.ExtractorInputSpan{{
				SpanID: "span:engineering", DisplayLine: 1, Text: string(raw),
			}},
		}
		resolved, err := resolveMCPReadProposalCandidates(input, candidates)
		if err != nil {
			t.Fatal(err)
		}
		if len(resolved) != len(values) {
			t.Fatalf("grouped=%t selected value count = %d, want %d", grouped, len(resolved), len(values))
		}
		for index, candidate := range resolved {
			if candidate.statement != values[index] || candidate.spanID != "span:engineering" {
				t.Fatalf("grouped=%t candidate %d did not preserve complete selected text and grounding: %+v", grouped, index, candidate)
			}
		}
	}
}

func TestMCPReadEngineeringConversionRejectsOversizedSelection(t *testing.T) {
	candidates := make([]MCPReadProposalCandidate, maxMCPReadProposalCandidates+1)
	if err := validateMCPReadProposalCandidates("requirement", candidates); err == nil {
		t.Fatal("oversized candidate batch accepted")
	}
	_, _, err := resolveMCPReadProposalCandidateText(strings.Repeat("x", maxMCPReadProposalStatementBytes+1), MCPReadProposalCandidate{
		LocalID: "oversized", SelectorKind: MCPReadProposalSelectorLine, Selector: "1",
	})
	if err == nil {
		t.Fatal("oversized source value accepted")
	}
}

func TestMCPReadControllerRevalidatesWholeSectionSelection(t *testing.T) {
	text := "# Recovery\n\nRetry is allowed.\nUnless validation fails, in which case leave the job pending.\n"
	input := evidenceingestion.ExtractorInput{Spans: []evidenceingestion.ExtractorInputSpan{{SpanID: "span:source", Text: text}}}
	sections, err := sectionBoundMCPReadProposalExtractorInputs(input, []resolvedMCPReadProposalCandidate{{
		localID: "candidate", spanID: "span:source", statement: text,
	}}, 1)
	if err != nil || len(sections) != 1 || sections[0].text != text {
		t.Fatalf("existing section boundary changed: sections=%+v, error=%v", sections, err)
	}
	definition := evidenceingestion.ExtractorDefinitionInput{
		Name: "synthetic-selector", Version: "v1",
		Config: map[string]string{"output_contract": evidenceingestion.OllamaExtractorPromptWholeSpanExactQuote},
	}
	fragment := []byte(`{"proposals":[{"proposal_local_id":"p1","statement_text":"Retry is allowed.","evidence_refs":["span:source"]}]}`)
	if _, err := decodeMCPReadProposalExtractionOutput(fragment, sections[0].input, 1, definition); err == nil {
		t.Fatal("controller accepted model substring after provider ignored selector schema")
	}
	// The legacy exact-substring contract remains distinct and compatible.
	if _, err := decodeMCPReadProposalExtractionOutput(fragment, sections[0].input, 1, evidenceingestion.ExtractorDefinitionInput{}); err != nil {
		t.Fatalf("legacy bounded quote contract changed: %v", err)
	}
	complete, err := json.Marshal(evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{
		ProposalLocalID: "p1", StatementText: text, EvidenceRefs: []string{"span:source"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMCPReadProposalExtractionOutput(complete, sections[0].input, 1, definition)
	if err != nil || len(decoded.Proposals) != 1 || decoded.Proposals[0].StatementText != text {
		t.Fatalf("controller changed complete selected section: %+v, error=%v", decoded, err)
	}
	provenance := mcpReadProposalExtractionDefinition(definition, MCPReadProposalSectionExtractionContract, MCPReadProposalSectionModeHeadingV1, 1, 1)
	if provenance.Config["grounding_contract"] != "exact-selected-span-substring-v1" || provenance.Config["coverage_semantics"] != "selection_only_not_fact_completeness" || provenance.Config["selection_unit"] != "whole_current_span" {
		t.Fatalf("controller provenance lost grounding or omission boundary: %+v", provenance)
	}
}
