package desktop

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
	"golang.org/x/sys/unix"
)

// CodeCitation identifies bytes observed in a local file, not a Git commit,
// source authenticity, or an assertion that the index is exhaustive/current.
type CodeCitation struct {
	FilePath   string `json:"filePath"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	FileSHA256 string `json:"fileSHA256"`
	ExactQuote string `json:"exactQuote"`
}

type codeSnippet struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Source    string `json:"source"`
}

func codebaseCitation(config sourcemcp.Config, name, text string) (*CodeCitation, error) {
	if config.CodebaseCache == "" || name != "get_code_snippet" {
		return nil, nil
	}
	var snippet codeSnippet
	if sourcemcp.ValidateRecordedJSON(text) != nil || json.Unmarshal([]byte(text), &snippet) != nil {
		return nil, errors.New("invalid code snippet")
	}
	// Ambiguous/not-found responses contain no original and are not citations.
	if snippet.Source == "" && snippet.FilePath == "" {
		return nil, nil
	}
	rel := snippet.FilePath
	if filepath.IsAbs(rel) {
		var err error
		rel, err = filepath.Rel(config.Directory, rel)
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsLocal(rel) || filepath.Clean(rel) != rel || snippet.StartLine < 1 || snippet.EndLine < snippet.StartLine || snippet.Source == "" {
		return nil, errors.New("invalid code location")
	}
	root, err := os.OpenRoot(config.Directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.OpenFile(rel, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > 8<<20 {
		return nil, errors.New("code file is not bounded regular data")
	}
	body, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		return nil, errors.New("code file exceeds bound")
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("code file changed during observation")
	}
	lines := strings.SplitAfter(string(body), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if snippet.EndLine > len(lines) {
		return nil, errors.New("code lines exceed file")
	}
	quote := strings.Join(lines[snippet.StartLine-1:snippet.EndLine], "")
	// CBM may omit the final line delimiter; no words/indentation are normalized.
	if snippet.Source != quote && snippet.Source != strings.TrimSuffix(quote, "\n") {
		return nil, errors.New("code snippet differs from file")
	}
	return &CodeCitation{FilePath: rel, StartLine: snippet.StartLine, EndLine: snippet.EndLine, FileSHA256: sourceArtifact("", body).SHA256, ExactQuote: snippet.Source}, nil
}

func recordedCodeCitation(c *CodeCitation, config sourcemcp.Config, tool, text string) bool {
	if c == nil {
		return true
	}
	var snippet codeSnippet
	if config.CodebaseCache == "" || tool != "get_code_snippet" {
		return false
	}
	if json.Unmarshal([]byte(text), &snippet) != nil || !filepath.IsLocal(c.FilePath) || filepath.Clean(c.FilePath) != c.FilePath || !safeText(c.FilePath, 4096) || c.StartLine < 1 || c.EndLine < c.StartLine || len(c.FileSHA256) != 64 || strings.Trim(c.FileSHA256, "0123456789abcdef") != "" || c.ExactQuote == "" {
		return false
	}
	path := snippet.FilePath
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(config.Directory, path)
		if err != nil {
			return false
		}
	}
	return c.FilePath == path && c.StartLine == snippet.StartLine && c.EndLine == snippet.EndLine && c.ExactQuote == snippet.Source
}
