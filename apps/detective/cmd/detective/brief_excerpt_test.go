package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func TestBriefExcerptCLISelectInspectVerifyWithoutModel(t *testing.T) {
	_, parent := briefCLIInput(t)
	prefix := strings.Repeat("Synthetic parent context.\r\n", 2800)
	selected := "  公告🙂 unresolved.\r\nThe named region remains affected.  "
	parent.Body = prefix + selected + "\r\nParent ending."
	input := briefPrivateJSON(t, parent)
	before, _ := os.ReadFile(input)
	out := filepath.Join(sourceCLIPrivateDir(t), "excerpt.json")
	args := []string{"select", "-input", input, "-start-byte", strconv.Itoa(len(prefix)), "-end-byte", strconv.Itoa(len(prefix) + len(selected)), "-reason", "Read the named incident.", "-out", out}
	calls := 0
	factory := func(context.Context, string, string) (*sourcepilot.BriefExtractor, error) {
		calls++
		return nil, errors.New("unexpected model")
	}
	var stdout bytes.Buffer
	if err := runBriefWithContext(t.Context(), args, &stdout, io.Discard, factory); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	source, err := sourcepilot.ParseBriefSource(raw)
	if err != nil || source.Body != selected || source.Excerpt.StartByte != len(prefix) || calls != 0 || strings.Contains(stdout.String(), input) || strings.Contains(stdout.String(), selected) {
		t.Fatalf("offline selection changed bytes or leaked source/path: %v", err)
	}
	for _, command := range [][]string{
		{"inspect", "-input", out},
		{"verify-excerpt", "-parent", input, "-input", out},
	} {
		if err := runBriefWithContext(t.Context(), command, io.Discard, io.Discard, factory); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatal("offline excerpt commands reached model")
	}
	if err := runBriefWithContext(t.Context(), args, io.Discard, io.Discard, factory); err == nil {
		t.Fatal("selection overwrote output")
	}
	after, _ := os.ReadFile(input)
	saved, _ := os.ReadFile(out)
	if !bytes.Equal(before, after) || !bytes.Equal(raw, saved) {
		t.Fatal("input or saved output was rewritten")
	}
	parent.Body += "changed outside excerpt"
	changed := briefPrivateJSON(t, parent)
	if err := runBriefWithContext(t.Context(), []string{"verify-excerpt", "-parent", changed, "-input", out}, io.Discard, io.Discard, factory); err == nil {
		t.Fatal("altered parent digest passed")
	}
}

func TestBriefCLIReportsSpecificLimitsBeforeModelOrOutput(t *testing.T) {
	_, base := briefCLIInput(t)
	for _, tc := range []struct {
		name     string
		body     string
		want     string
		observed int64
	}{
		{"body", strings.Repeat("x", sourcepilot.BriefBodyLimit+1), "body_bytes", sourcepilot.BriefBodyLimit + 1},
		{"segments", strings.Repeat("private-source-content\n", 65), "nonempty_segments", 65},
		{"JSON", strings.Repeat("private-source-content", 4000), "source_json_bytes", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := base
			source.Body = tc.body
			input := briefPrivateJSON(t, source)
			out := filepath.Join(sourceCLIPrivateDir(t), "report.json")
			calls := 0
			factory := func(context.Context, string, string) (*sourcepilot.BriefExtractor, error) {
				calls++
				return nil, errors.New("unexpected")
			}
			err := runBriefWithContext(t.Context(), briefCLIArgs(input, out), io.Discard, io.Discard, factory)
			var limit *sourcepilot.InputLimitError
			if !errors.As(err, &limit) || limit.Resource != tc.want || (tc.observed != 0 && limit.Observed != tc.observed) || calls != 0 || strings.Contains(err.Error(), "private-source-content") || strings.Contains(err.Error(), input) {
				t.Fatalf("specific safe bound was lost: %v", err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("invalid input reserved model output")
			}
		})
	}
}

func TestBriefExcerptCLIRejectsInvalidFlagsAndUnsafeFiles(t *testing.T) {
	input, _ := briefCLIInput(t)
	for _, name := range []string{"missing-range", "duplicate", "UTF8", "symlink", "permissions", "existing-output", "parent-limit"} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(sourceCLIPrivateDir(t), "excerpt.json")
			args := []string{"select", "-input", input, "-start-byte", "0", "-end-byte", "10", "-reason", "Explicit range.", "-out", out}
			switch name {
			case "missing-range":
				args[4] = "-1"
			case "duplicate":
				args = append(args, "-reason", "again")
			case "UTF8":
				_, source := briefCLIInput(t)
				source.Body = "公告🙂"
				args[2], args[6] = briefPrivateJSON(t, source), "1"
			case "symlink", "permissions", "parent-limit":
				path := filepath.Join(sourceCLIPrivateDir(t), "unsafe.json")
				if name == "symlink" {
					if err := os.Symlink(input, path); err != nil {
						t.Fatal(err)
					}
				} else {
					raw, _ := os.ReadFile(input)
					mode := os.FileMode(0o644)
					if name == "parent-limit" {
						raw, mode = bytes.Repeat([]byte(" "), sourcepilot.BriefParentJSONLimit+1), 0o600
					}
					if err := os.WriteFile(path, raw, mode); err != nil {
						t.Fatal(err)
					}
				}
				args[2] = path
			case "existing-output":
				if err := os.WriteFile(out, []byte("retain"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := runBriefWithContext(t.Context(), args, io.Discard, io.Discard, nil); err == nil {
				t.Fatal("invalid selection accepted")
			}
			if name != "existing-output" {
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("invalid selection created output")
				}
			}
		})
	}
}

func TestBriefExcerptCLILimitsEncodedOutputWithoutTruncation(t *testing.T) {
	_, parent := briefCLIInput(t)
	parent.Body = strings.Repeat("<", 12000)
	raw, _ := json.Marshal(parent)
	if len(raw) <= sourceInputLimit {
		t.Fatal("fixture must exceed CLI output bound after JSON encoding")
	}
	input := briefPrivateJSON(t, parent)
	out := filepath.Join(sourceCLIPrivateDir(t), "excerpt.json")
	err := runBriefWithContext(t.Context(), []string{"select", "-input", input, "-start-byte", "0", "-end-byte", "12000", "-reason", "Full selected range.", "-out", out}, io.Discard, io.Discard, nil)
	var limit *sourcepilot.InputLimitError
	if !errors.As(err, &limit) || limit.Resource != "source_json_bytes" {
		t.Fatal("JSON escaping silently made an unusable or truncated output", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("oversized output was reserved")
	}
}
