package detective

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

type resolvedMCPReadProposalCandidate struct {
	localID   string
	statement string
	line      int
	spanID    string
}

func validateMCPReadProposalCandidates(
	documentText string,
	candidates []MCPReadProposalCandidate,
) error {
	if len(candidates) > maxMCPReadProposalCandidates {
		return fmt.Errorf("candidate count exceeds %d", maxMCPReadProposalCandidates)
	}
	seenLocalIDs := make(map[string]struct{}, len(candidates))
	seenSelectors := make(map[string]struct{}, len(candidates))
	for index, candidate := range candidates {
		localID, err := normalizeBoundedText(
			candidate.LocalID,
			"proposal candidate local_id",
			maxMCPReadProposalCandidateIDBytes,
		)
		if err != nil || localID != candidate.LocalID {
			return fmt.Errorf("candidate %d has invalid local_id", index)
		}
		if _, exists := seenLocalIDs[candidate.LocalID]; exists {
			return fmt.Errorf("candidate %d duplicates local_id %q", index, candidate.LocalID)
		}
		seenLocalIDs[candidate.LocalID] = struct{}{}

		selector, err := normalizeBoundedText(
			candidate.Selector,
			"proposal candidate selector",
			maxMCPReadProposalSelectorBytes,
		)
		if err != nil || selector != candidate.Selector {
			return fmt.Errorf("candidate %d has invalid selector", index)
		}
		selectorIdentity := candidate.SelectorKind + "\x00" + candidate.Selector
		if _, exists := seenSelectors[selectorIdentity]; exists {
			return fmt.Errorf("candidate %d duplicates selector %q", index, candidate.Selector)
		}
		seenSelectors[selectorIdentity] = struct{}{}

		if _, _, err := resolveMCPReadProposalCandidateText(documentText, candidate); err != nil {
			return fmt.Errorf("candidate %d: %w", index, err)
		}
	}
	return nil
}

func resolveMCPReadProposalCandidates(
	input evidenceingestion.ExtractorInput,
	candidates []MCPReadProposalCandidate,
) ([]resolvedMCPReadProposalCandidate, error) {
	if err := validateMCPReadProposalCandidates(input.RenderedText, candidates); err != nil {
		return nil, err
	}
	spansByLine := make(map[int]evidenceingestion.ExtractorInputSpan, len(input.Spans))
	for _, span := range input.Spans {
		if _, exists := spansByLine[span.DisplayLine]; exists {
			return nil, fmt.Errorf("display line %d has multiple source spans", span.DisplayLine)
		}
		spansByLine[span.DisplayLine] = span
	}
	resolved := make([]resolvedMCPReadProposalCandidate, 0, len(candidates))
	for index, candidate := range candidates {
		statement, line, err := resolveMCPReadProposalCandidateText(input.RenderedText, candidate)
		if err != nil {
			return nil, fmt.Errorf("candidate %d: %w", index, err)
		}
		span, found := spansByLine[line]
		if !found {
			return nil, fmt.Errorf("candidate %d selects line %d without an exact source span", index, line)
		}
		resolved = append(resolved, resolvedMCPReadProposalCandidate{
			localID:   candidate.LocalID,
			statement: statement,
			line:      line,
			spanID:    span.SpanID,
		})
	}
	return resolved, nil
}

func resolveMCPReadProposalCandidateText(
	documentText string,
	candidate MCPReadProposalCandidate,
) (string, int, error) {
	switch candidate.SelectorKind {
	case MCPReadProposalSelectorLine:
		line, err := strconv.Atoi(candidate.Selector)
		if err != nil || line <= 0 || strconv.Itoa(line) != candidate.Selector {
			return "", 0, errors.New("line selector must be a canonical positive integer")
		}
		text, found := mcpReadDocumentLine(documentText, line)
		if !found || strings.TrimSpace(text) == "" {
			return "", 0, fmt.Errorf("line selector %d does not identify a non-empty source line", line)
		}
		if len(text) > maxMCPReadProposalStatementBytes {
			return "", 0, fmt.Errorf(
				"line selector %d exceeds %d statement bytes",
				line,
				maxMCPReadProposalStatementBytes,
			)
		}
		return text, line, nil
	case MCPReadProposalSelectorJSONPointerString:
		return resolveMCPReadJSONPointerString(documentText, candidate.Selector)
	default:
		return "", 0, fmt.Errorf("unsupported selector_kind %q", candidate.SelectorKind)
	}
}

func resolveMCPReadJSONPointerString(documentText, pointer string) (string, int, error) {
	decoder := json.NewDecoder(strings.NewReader(documentText))
	decoder.UseNumber()
	document, err := decodeMCPReadJSONValue(decoder)
	if err != nil {
		return "", 0, fmt.Errorf("document is not valid JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", 0, fmt.Errorf("document has trailing JSON data: %w", err)
	}
	tokens, err := decodeMCPReadJSONPointer(pointer)
	if err != nil {
		return "", 0, err
	}
	current := document
	for _, token := range tokens {
		switch typed := current.(type) {
		case map[string]any:
			next, found := typed[token]
			if !found {
				return "", 0, fmt.Errorf("JSON pointer field %q is absent", token)
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || strconv.Itoa(index) != token || index >= len(typed) {
				return "", 0, fmt.Errorf("JSON pointer array index %q is invalid", token)
			}
			current = typed[index]
		default:
			return "", 0, fmt.Errorf("JSON pointer cannot traverse %q", token)
		}
	}
	statement, ok := current.(string)
	if !ok {
		return "", 0, errors.New("JSON pointer must select a string")
	}
	if strings.TrimSpace(statement) == "" ||
		len(statement) > maxMCPReadProposalStatementBytes ||
		!utf8.ValidString(statement) {
		return "", 0, errors.New("selected JSON string is empty, oversized, or invalid UTF-8")
	}
	occurrences, err := mcpReadJSONStringOccurrences([]byte(documentText), statement)
	if err != nil {
		return "", 0, err
	}
	if len(occurrences) != 1 {
		return "", 0, fmt.Errorf(
			"selected JSON string has %d exact source occurrences, want 1",
			len(occurrences),
		)
	}
	documentBytes := []byte(documentText)
	start := occurrences[0].start
	line := bytes.Count(documentBytes[:start], []byte{'\n'}) + 1
	lineText, found := mcpReadDocumentLine(documentText, line)
	lineStart := bytes.LastIndex(documentBytes[:start], []byte{'\n'}) + 1
	if !found ||
		occurrences[0].end > lineStart+len(lineText) {
		return "", 0, errors.New("selected JSON string is not contained by one exact source line")
	}
	return statement, line, nil
}

type mcpReadJSONStringOccurrence struct {
	start int
	end   int
}

func mcpReadJSONStringOccurrences(
	document []byte,
	target string,
) ([]mcpReadJSONStringOccurrence, error) {
	occurrences := []mcpReadJSONStringOccurrence{}
	for offset := 0; offset < len(document); offset++ {
		if document[offset] != '"' {
			continue
		}
		start := offset
		offset++
	stringToken:
		for offset < len(document) {
			switch document[offset] {
			case '\\':
				offset += 2
				continue
			case '"':
				end := offset + 1
				var decoded string
				if err := json.Unmarshal(document[start:end], &decoded); err != nil {
					return nil, fmt.Errorf("decoding exact JSON string token: %w", err)
				}
				if decoded == target {
					occurrences = append(occurrences, mcpReadJSONStringOccurrence{
						start: start,
						end:   end,
					})
				}
				break stringToken
			default:
				offset++
				continue
			}
		}
		if offset >= len(document) {
			return nil, errors.New("JSON string token is not closed")
		}
	}
	return occurrences, nil
}

func decodeMCPReadJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("JSON object duplicates field %q", key)
			}
			value, err := decodeMCPReadJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("JSON object is not closed")
		}
		return object, nil
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := decodeMCPReadJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("JSON array is not closed")
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func decodeMCPReadJSONPointer(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, errors.New("JSON pointer must not select the document root")
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("JSON pointer must start with /")
	}
	encoded := strings.Split(pointer[1:], "/")
	tokens := make([]string, len(encoded))
	for index, token := range encoded {
		var decoded strings.Builder
		for offset := 0; offset < len(token); offset++ {
			if token[offset] != '~' {
				decoded.WriteByte(token[offset])
				continue
			}
			if offset+1 >= len(token) {
				return nil, errors.New("JSON pointer has an incomplete escape")
			}
			offset++
			switch token[offset] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, fmt.Errorf("JSON pointer has invalid escape ~%c", token[offset])
			}
		}
		tokens[index] = decoded.String()
	}
	return tokens, nil
}

func mcpReadDocumentLine(documentText string, displayLine int) (string, bool) {
	if displayLine <= 0 {
		return "", false
	}
	lines := strings.Split(documentText, "\n")
	if displayLine > len(lines) {
		return "", false
	}
	line := strings.TrimSuffix(lines[displayLine-1], "\r")
	return line, true
}
