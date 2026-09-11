package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
	"iter"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

type fakeModel struct{}

var numberedRowRe = regexp.MustCompile(`(?m)^\s*(\d{6})\s*\|\s*(.*)$`)

func (m *fakeModel) Name() string { return "fake-row-model" }

func (m *fakeModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	rowText, rowLine := parseRowContext(request)
	candidateSet := buildCandidateSet(rowText, rowLine)
	raw, _ := json.Marshal(candidateSet)

	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content:      genai.NewContentFromText(string(raw), genai.RoleModel),
			TurnComplete: true,
		}, nil)
	}
}

func parseRowContext(request *model.LLMRequest) (string, int) {
	rowText := ""
	candidates := make([]string, 0, 3)
	candidateLines := make([]int, 0, 3)
	candidateStatusLines := make([]int, 0, 3)
	candidateStatusRows := make([]string, 0, 3)

	for _, content := range request.Contents {
		if content == nil || len(content.Parts) == 0 {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.Text == "" {
				continue
			}
			for _, line := range strings.Split(part.Text, "\n") {
				_, parsedLineNo, cells := parseNumberedRow(line)
				if parsedLineNo == 0 || len(cells) < 3 {
					continue
				}

				rawRow := "| " + strings.Join(cells, " | ") + " |"
				if isHeaderRow(cells) || isDividerRow(cells) {
					continue
				}
				candidates = append(candidates, rawRow)
				candidateLines = append(candidateLines, parsedLineNo)
				if hasStatusIndicators(cells[1]) {
					candidateStatusLines = append(candidateStatusLines, parsedLineNo)
					candidateStatusRows = append(candidateStatusRows, rawRow)
				}
			}
		}
	}

	if len(candidateStatusRows) > 0 {
		return candidateStatusRows[0], candidateStatusLines[0]
	}
	if len(candidates) > 0 {
		return candidates[0], candidateLines[0]
	}

	if rowText == "" {
		rowText = "| Placeholder | LAB PROVEN | runtime-only |"
	}
	return rowText, 1
}

func parseNumberedRow(line string) (string, int, []string) {
	m := numberedRowRe.FindStringSubmatch(line)
	if len(m) != 3 {
		return "", 0, nil
	}

	lineNo, err := strconv.Atoi(m[1])
	if err != nil {
		return "", 0, nil
	}
	rowText := strings.TrimSpace(m[2])
	if !strings.HasSuffix(rowText, "|") {
		return "", 0, nil
	}
	cells := splitMarkdownRowRaw(rowText)
	return rowText, lineNo, cells
}

func isHeaderRow(cells []string) bool {
	if len(cells) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cells[0]), "Capability") &&
		strings.EqualFold(strings.TrimSpace(cells[1]), "Status")
}

func isDividerRow(cells []string) bool {
	for _, cell := range cells {
		trimmed := strings.Trim(strings.TrimSpace(cell), ":")
		if len(trimmed) < 3 {
			return false
		}
		for _, char := range trimmed {
			if char != '-' {
				return false
			}
		}
	}
	return true
}

func hasStatusIndicators(statusCell string) bool {
	normalized := strings.ToUpper(statusCell)
	return strings.Contains(normalized, "IMPLEMENTED") ||
		strings.Contains(normalized, "RELEASED") ||
		strings.Contains(normalized, "LAB") ||
		strings.Contains(normalized, "BACKLOG") ||
		strings.Contains(normalized, "EXPERIMENT READY") ||
		strings.Contains(normalized, "OPEN") ||
		strings.Contains(normalized, "OPTIONAL") ||
		strings.Contains(normalized, "NOT CLAIMED")
}

func splitMarkdownRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 {
		return []string{}
	}
	parts := strings.Split(trimmed[1:len(trimmed)-1], "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func buildCandidateSet(rowText string, rowLine int) labstatus.CandidateSet {
	cells := splitMarkdownRowRaw(rowText)
	if len(cells) < 3 {
		cells = []string{"unknown", "LAB PROVEN", "runtime"}
	}
	capability := strings.TrimSpace(cells[0])
	statusCell := strings.TrimSpace(cells[1])
	status := inferStatus(statusCell)

	set := labstatus.CandidateSet{
		Outcome: "extracted",
		Records: []labstatus.Record{
			{
				RecordType:       "capability_state",
				Subject:          sanitizeSubject(capability),
				Statement:        "Row-derived extraction from fixture input.",
				EpistemicClass:   "claim",
				Status:           status,
				Scope:            "runtime_core",
				SelectionState:   "unspecified",
				Citation:         labstatus.Citation{StartLine: rowLine, EndLine: rowLine},
				BlockedBy:        []string{},
				DoesNotEstablish: []string{},
				Qualifiers:       []string{},
			},
		},
		Abstentions:      []labstatus.Abstention{},
		Limitations:      []string{},
		AbstentionReason: "",
	}

	if strings.Contains(strings.ToUpper(statusCell), "CANONICAL") && strings.Contains(strings.ToUpper(capability), "IMPLEMENTS") {
		set.Records = append(set.Records, set.Records[0])
	}

	return set
}

func splitMarkdownRowRaw(line string) []string {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 || !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return []string{}
	}
	parts := strings.Split(trimmed[1:len(trimmed)-1], "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func inferStatus(statusCell string) string {
	normalized := strings.ToUpper(statusCell)
	normalized = strings.ReplaceAll(normalized, "`", "")
	normalized = strings.ReplaceAll(normalized, "*", "")
	if strings.Contains(normalized, "IMPLEMENTED") && strings.Contains(normalized, "EXPOSED") {
		return "implemented_exposed"
	}
	if strings.Contains(normalized, "IMPLEMENTED") {
		return "implemented"
	}
	if strings.Contains(normalized, "UNRELEASED LAB") && strings.Contains(normalized, "PROVEN") {
		return "unreleased_lab_proven"
	}
	if strings.Contains(normalized, "LAB DOMAIN PROVEN") || strings.Contains(normalized, "LAB PROVEN") {
		return "lab_proven"
	}
	if strings.Contains(normalized, "NOT CLAIMED") {
		return "not_claimed"
	}
	if strings.Contains(normalized, "BACKLOG") {
		return "backlog"
	}
	if strings.Contains(normalized, "EXPERIMENT READY") {
		return "experiment_ready"
	}
	if strings.Contains(normalized, "OPTIONAL") {
		return "optional_experimental"
	}
	if strings.Contains(normalized, "RELEASED") && !strings.Contains(normalized, "UNRELEASED") {
		return "released"
	}
	if strings.Contains(normalized, "OPEN") {
		return "open"
	}
	return "implemented"
}

func sanitizeSubject(value string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "`", "")
	value = strings.ReplaceAll(value, "`", "")
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' {
			out.WriteRune(r)
		}
	}
	v := strings.TrimSpace(out.String())
	v = strings.ReplaceAll(v, " ", "_")
	if v == "" {
		return "unknown"
	}
	return v
}

func main() {
	if len(os.Args) != 2 {
		_, _ = fmt.Fprintf(os.Stderr, "usage: fake-rowchunks <input>\n")
		os.Exit(2)
	}
	docPath := os.Args[1]
	doc, err := labstatus.LoadDocument(docPath)
	if err != nil {
		panic(err)
	}
	selected, err := doc.SelectSections("Status at a Glance")
	if err != nil {
		panic(err)
	}
	extractor, err := labstatus.NewExtractor(&fakeModel{})
	if err != nil {
		panic(err)
	}

	batch, err := extractor.ExtractTableRows(context.Background(), selected, "Status at a Glance")
	if err != nil {
		panic(err)
	}
	enc, err := json.MarshalIndent(batch, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(enc))
}
