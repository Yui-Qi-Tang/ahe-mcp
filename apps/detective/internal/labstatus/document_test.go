package labstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDocument(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "STATUS.md")
	raw := []byte("first\r\nsecond\r\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	document, err := LoadDocument(path)
	if err != nil {
		t.Fatalf("LoadDocument() error = %v", err)
	}
	wantDigest := sha256.Sum256(raw)
	if got, want := document.Source().SHA256, hex.EncodeToString(wantDigest[:]); got != want {
		t.Fatalf("SHA256 = %q, want %q", got, want)
	}
	if got, want := document.Source().Lines, 2; got != want {
		t.Fatalf("Lines = %d, want %d", got, want)
	}
	if got, want := document.NumberedText(), "000001 | first\n000002 | second\n"; got != want {
		t.Fatalf("NumberedText() = %q, want %q", got, want)
	}
	if got, ok := document.ExactQuote(1, 2); !ok || got != "first\nsecond" {
		t.Fatalf("ExactQuote(1, 2) = %q, %v", got, ok)
	}
}

func TestSelectSectionsPreservesOriginalCoordinates(t *testing.T) {
	t.Parallel()
	document := loadTestDocument(t, "# Status\n## First\na\nb\n## Second\nc\n")
	view, err := document.SelectSections("Second", "First")
	if err != nil {
		t.Fatalf("SelectSections() error = %v", err)
	}
	if got, want := view.NumberedText(), "000002 | ## First\n000003 | a\n000004 | b\n000005 | ## Second\n000006 | c\n"; got != want {
		t.Fatalf("NumberedText() = %q, want %q", got, want)
	}
	sections := view.Source().SelectedSections
	if len(sections) != 2 || sections[0].StartLine != 2 || sections[0].EndLine != 4 || sections[1].StartLine != 5 || sections[1].EndLine != 6 {
		t.Fatalf("selected sections = %#v", sections)
	}
	if got, want := view.Source().SHA256, document.Source().SHA256; got != want {
		t.Fatalf("selected source hash = %q, want full source hash %q", got, want)
	}
}

func TestSelectSectionsFailsClosed(t *testing.T) {
	t.Parallel()
	document := loadTestDocument(t, "## Repeated\na\n## Repeated\nb\n")
	for _, headings := range [][]string{{"Missing"}, {"Repeated"}, {"Repeated", "Repeated"}, {""}} {
		if _, err := document.SelectSections(headings...); err == nil {
			t.Errorf("SelectSections(%q) error = nil", headings)
		}
	}
}

func TestLoadDocumentRejectsRelativePath(t *testing.T) {
	t.Parallel()
	if _, err := LoadDocument("STATUS.md"); err == nil {
		t.Fatal("LoadDocument() error = nil, want relative-path error")
	}
}

func TestLoadDocumentRejectsSymlink(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "target.md")
	link := filepath.Join(directory, "STATUS.md")
	if err := os.WriteFile(target, []byte("status\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocument(link); err == nil {
		t.Fatal("LoadDocument() error = nil, want symlink error")
	}
}

func TestLoadDocumentRejectsOversizeInput(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "STATUS.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxDocumentBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocument(path); err == nil {
		t.Fatal("LoadDocument() error = nil, want size error")
	}
}
