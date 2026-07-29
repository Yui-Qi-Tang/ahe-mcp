package detective

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

const (
	// MCPReadProposalSectionModeHeadingV1 splits adapter-selected text at
	// Markdown ATX headings or provider-rendered numbered headings.
	MCPReadProposalSectionModeHeadingV1 = "heading_sections_v1"

	maxMCPReadProposalSectionCount = 32
)

type mcpReadProposalSection struct {
	id               string
	candidateLocalID string
	heading          string
	startByte        int
	endByte          int
	text             string
	input            evidenceingestion.ExtractorInput
}

type mcpReadHeadingSection struct {
	heading   string
	startByte int
	endByte   int
	text      string
}

type mcpReadTextLine struct {
	start int
	end   int
	text  string
}

func sectionBoundMCPReadProposalExtractorInputs(
	input evidenceingestion.ExtractorInput,
	resolved []resolvedMCPReadProposalCandidate,
	maxSections int,
) ([]mcpReadProposalSection, error) {
	spansByID := make(map[string]evidenceingestion.ExtractorInputSpan, len(input.Spans))
	for _, span := range input.Spans {
		spansByID[span.SpanID] = span
	}
	sections := make([]mcpReadProposalSection, 0, len(resolved))
	for _, candidate := range resolved {
		span, exists := spansByID[candidate.spanID]
		if !exists {
			return nil, newDomainError(
				ErrorMCPProposalConversionRejected,
				"proposal candidate span %s is absent",
				candidate.spanID,
			)
		}
		candidateSections := splitMCPReadHeadingSections(candidate.statement)
		for _, section := range candidateSections {
			if len(sections) == maxSections {
				return nil, newDomainError(
					ErrorMCPProposalConversionRejected,
					"adapter-selected text exceeds the %d section limit",
					maxSections,
				)
			}
			sectionID := fmt.Sprintf("section-%03d", len(sections)+1)
			sectionSpan := span
			sectionSpan.Text = section.text
			sectionSpan.QuotedTextHash = ""
			bounded := input
			bounded.RenderedText = ""
			bounded.Spans = []evidenceingestion.ExtractorInputSpan{sectionSpan}
			sections = append(sections, mcpReadProposalSection{
				id:               sectionID,
				candidateLocalID: candidate.localID,
				heading:          section.heading,
				startByte:        section.startByte,
				endByte:          section.endByte,
				text:             section.text,
				input:            bounded,
			})
		}
	}
	return sections, nil
}

func splitMCPReadHeadingSections(text string) []mcpReadHeadingSection {
	lines := mcpReadTextLines(text)
	type headingLine struct {
		lineIndex int
		heading   string
	}
	headings := make([]headingLine, 0)
	for index := range lines {
		heading, ok := mcpReadSectionHeading(lines, index)
		if !ok {
			continue
		}
		headings = append(headings, headingLine{lineIndex: index, heading: heading})
	}
	if len(headings) == 0 {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []mcpReadHeadingSection{{
			startByte: 0,
			endByte:   len(text),
			text:      text,
		}}
	}

	sections := make([]mcpReadHeadingSection, 0, len(headings)+1)
	firstStart := lines[headings[0].lineIndex].start
	if strings.TrimSpace(text[:firstStart]) != "" {
		sections = append(sections, mcpReadHeadingSection{
			startByte: 0,
			endByte:   firstStart,
			text:      text[:firstStart],
		})
	}
	for index, heading := range headings {
		start := lines[heading.lineIndex].start
		end := len(text)
		if index+1 < len(headings) {
			end = lines[headings[index+1].lineIndex].start
		}
		sections = append(sections, mcpReadHeadingSection{
			heading:   heading.heading,
			startByte: start,
			endByte:   end,
			text:      text[start:end],
		})
	}
	return sections
}

func mcpReadTextLines(text string) []mcpReadTextLine {
	if text == "" {
		return nil
	}
	lines := make([]mcpReadTextLine, 0, strings.Count(text, "\n")+1)
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start + 1
		}
		lineEnd := end
		if lineEnd > start && text[lineEnd-1] == '\n' {
			lineEnd--
		}
		if lineEnd > start && text[lineEnd-1] == '\r' {
			lineEnd--
		}
		lines = append(lines, mcpReadTextLine{
			start: start,
			end:   end,
			text:  text[start:lineEnd],
		})
		start = end
	}
	return lines
}

func mcpReadSectionHeading(lines []mcpReadTextLine, index int) (string, bool) {
	trimmed := strings.TrimSpace(lines[index].text)
	if heading, ok := mcpReadATXHeading(trimmed); ok {
		return heading, true
	}
	if index > 0 && strings.TrimSpace(lines[index-1].text) != "" {
		return "", false
	}
	if index+1 < len(lines) && strings.TrimSpace(lines[index+1].text) != "" {
		return "", false
	}
	return mcpReadProviderNumberedHeading(trimmed)
}

func mcpReadATXHeading(line string) (string, bool) {
	hashes := 0
	for hashes < len(line) && line[hashes] == '#' {
		hashes++
	}
	if hashes < 1 || hashes > 6 || hashes == len(line) || line[hashes] != ' ' {
		return "", false
	}
	heading := strings.TrimSpace(strings.TrimRight(line[hashes+1:], "#"))
	return heading, heading != ""
}

func mcpReadProviderNumberedHeading(line string) (string, bool) {
	dot := strings.IndexByte(line, '.')
	if dot < 1 || dot+2 > len(line) || line[dot+1] != ' ' {
		return "", false
	}
	for _, char := range line[:dot] {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	heading := strings.TrimSpace(line[dot+2:])
	if heading == "" || len([]byte(heading)) > 160 {
		return "", false
	}
	switch heading[len(heading)-1] {
	case '.', ':', ';', '?', '!':
		return "", false
	}
	return heading, true
}

func mcpReadSectionContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
