package evidenceingestion

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseGitTreeSelectsTrackedRegularGoFiles(t *testing.T) {
	oidA := strings.Repeat("a", 40)
	oidB := strings.Repeat("b", 40)
	oidC := strings.Repeat("c", 40)
	data := []byte(
		"100644 blob " + oidB + "\tpkg/worker.go\x00" +
			"100644 blob " + oidC + "\tREADME.md\x00" +
			"100755 blob " + oidA + "\tcmd/main.go\x00" +
			"120000 blob " + oidA + "\tlinked.go\x00",
	)

	entries, err := parseGitTree(data)
	if err != nil {
		t.Fatalf("parseGitTree() error = %v", err)
	}
	selected, err := selectTrackedGoFiles(entries)
	if err != nil {
		t.Fatalf("selectTrackedGoFiles() error = %v", err)
	}
	want := []gitTreeEntry{
		{mode: "100755", objectType: "blob", objectID: oidA, path: "cmd/main.go"},
		{mode: "100644", objectType: "blob", objectID: oidB, path: "pkg/worker.go"},
	}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected = %#v, want %#v", selected, want)
	}
}

func TestSelectTrackedGoFilesRejectsTraversal(t *testing.T) {
	_, err := selectTrackedGoFiles([]gitTreeEntry{{
		mode:       "100644",
		objectType: "blob",
		objectID:   strings.Repeat("a", 40),
		path:       "../escape.go",
	}})
	assertKind(t, err, ErrorInvalidInput)
}

func TestParseGitTreeRequiresNULTerminator(t *testing.T) {
	_, err := parseGitTree([]byte("100644 blob " + strings.Repeat("a", 40) + "\tmain.go"))
	assertKind(t, err, ErrorInvalidInput)
}
