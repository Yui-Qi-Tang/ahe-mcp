package labstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const maxDocumentBytes = 256 * 1024

// Document contains controller-observed source metadata and canonical logical
// lines. Line endings are normalized to LF for citations; SHA256 covers the
// original bytes exactly.
type Document struct {
	source Source
	raw    string
	lines  []string
}

// LoadDocument loads one absolute, regular, non-symlink file.
func LoadDocument(path string) (*Document, error) {
	if path == "" {
		return nil, fmt.Errorf("input path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("input path must be absolute: %q", path)
	}

	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect input: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("input must not be a symlink: %q", path)
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("input must be a regular file: %q", path)
	}
	if pathInfo.Size() > maxDocumentBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxDocumentBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened input: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return nil, fmt.Errorf("input changed while opening: %q", path)
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(raw) > maxDocumentBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxDocumentBytes)
	}
	return RestoreDocument(path, string(raw))
}

// RestoreDocument reconstructs a document from previously captured exact bytes.
// path is an audit label, not a file to reopen. This validates the local snapshot
// format; it does not authenticate who captured or supplied those bytes.
func RestoreDocument(path, raw string) (*Document, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || !utf8.ValidString(path) {
		return nil, fmt.Errorf("snapshot source path must be an absolute UTF-8 path without NUL")
	}
	if len(raw) > maxDocumentBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxDocumentBytes)
	}
	if !utf8.ValidString(raw) {
		return nil, fmt.Errorf("input must be valid UTF-8")
	}
	if strings.IndexByte(raw, 0) >= 0 {
		return nil, fmt.Errorf("input must not contain NUL bytes")
	}

	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	} else if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	digest := sha256.Sum256([]byte(raw))
	selectedSections := []SourceSection{}
	if len(lines) > 0 {
		selectedSections = append(selectedSections, SourceSection{
			Heading:   "(entire document)",
			StartLine: 1,
			EndLine:   len(lines),
		})
	}
	return &Document{
		source: Source{
			Path:             path,
			SHA256:           hex.EncodeToString(digest[:]),
			Bytes:            len(raw),
			Lines:            len(lines),
			SelectedSections: selectedSections,
		},
		raw:   raw,
		lines: lines,
	}, nil
}

// Source returns the controller-observed source coordinate.
func (d *Document) Source() Source {
	source := d.source
	source.SelectedSections = append([]SourceSection(nil), d.source.SelectedSections...)
	return source
}

// RawText returns the exact UTF-8 bytes observed by LoadDocument as a Go string.
// It is for an evidence-store handoff; citations continue to use normalized lines.
func (d *Document) RawText() string {
	if d == nil {
		return ""
	}
	return d.raw
}

// SelectSections returns a document view containing the exact, uniquely named
// level-two Markdown sections. The source hash continues to cover the full file.
func (d *Document) SelectSections(headings ...string) (*Document, error) {
	if d == nil {
		return nil, fmt.Errorf("document is required")
	}
	if len(headings) == 0 {
		return nil, fmt.Errorf("at least one section heading is required")
	}

	type headingLocation struct {
		name string
		line int
	}
	locations := make([]headingLocation, 0)
	for i, line := range d.lines {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		locations = append(locations, headingLocation{name: name, line: i + 1})
	}

	requested := make(map[string]struct{}, len(headings))
	selected := make([]SourceSection, 0, len(headings))
	for _, rawHeading := range headings {
		heading := strings.TrimSpace(rawHeading)
		if heading == "" {
			return nil, fmt.Errorf("section heading must not be blank")
		}
		if _, exists := requested[heading]; exists {
			return nil, fmt.Errorf("section heading requested more than once: %q", heading)
		}
		requested[heading] = struct{}{}

		matches := make([]int, 0, 1)
		for i, location := range locations {
			if location.name == heading {
				matches = append(matches, i)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("section not found: %q", heading)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("section heading is ambiguous: %q", heading)
		}

		locationIndex := matches[0]
		endLine := len(d.lines)
		if locationIndex+1 < len(locations) {
			endLine = locations[locationIndex+1].line - 1
		}
		selected = append(selected, SourceSection{
			Heading:   heading,
			StartLine: locations[locationIndex].line,
			EndLine:   endLine,
		})
	}

	slices.SortFunc(selected, func(left, right SourceSection) int {
		return left.StartLine - right.StartLine
	})
	view := &Document{
		source: d.source,
		raw:    d.raw,
		lines:  d.lines,
	}
	view.source.SelectedSections = selected
	return view, nil
}

// NumberedText returns source lines with unambiguous one-based line numbers.
func (d *Document) NumberedText() string {
	var text strings.Builder
	for _, section := range d.source.SelectedSections {
		for lineNumber := section.StartLine; lineNumber <= section.EndLine; lineNumber++ {
			fmt.Fprintf(&text, "%06d | %s\n", lineNumber, d.lines[lineNumber-1])
		}
	}
	return text.String()
}

// ExactQuote returns the canonical text for an inclusive one-based line range.
func (d *Document) ExactQuote(startLine, endLine int) (string, bool) {
	if startLine < 1 || endLine < startLine || endLine > len(d.lines) {
		return "", false
	}
	return strings.Join(d.lines[startLine-1:endLine], "\n"), true
}

func (d *Document) containsSelectedRange(startLine, endLine int) bool {
	for _, section := range d.source.SelectedSections {
		if startLine >= section.StartLine && endLine <= section.EndLine {
			return true
		}
	}
	return false
}
