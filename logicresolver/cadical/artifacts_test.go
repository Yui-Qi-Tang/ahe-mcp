package cadical

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

func TestArtifactArchivalBoundary(t *testing.T) {
	r := fixtureRunner(t, "echo 's SATISFIABLE'; echo 'v 1 0'; exit 10", "exit 1", time.Second)
	result, err := r.Solve(context.Background(), "archive", tinyCNF())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range result.Artifacts {
		if _, err := r.ReadArtifact(a); err != nil {
			t.Fatal(err)
		}
	}
	a := result.Artifacts[0]
	raw, err := os.ReadFile(filepath.Join(r.root, a.ID))
	if err != nil || len(raw) == 0 {
		t.Fatal(err)
	}
	raw[0] ^= 1
	if err := os.WriteFile(filepath.Join(r.root, a.ID), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadArtifact(a); err == nil {
		t.Fatal("accepted tampered artifact")
	}
	for _, id := range []string{"../escape", "/etc/hosts", "absent"} {
		if _, err := r.ReadArtifact(logicresolver.Artifact{ID: id, Bytes: 0}); err == nil {
			t.Fatal("accepted invalid path", id)
		}
	}
	if _, err := r.ReadArtifact(logicresolver.Artifact{ID: a.ID, Bytes: 1 << 60}); err == nil {
		t.Fatal("accepted unbounded read")
	}
	// Identity follows semantics and resource limits, independent of storage.
	cfg := r.config
	cfg.ArtifactDir = filepath.Join(t.TempDir(), "other")
	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Identity() != r.Identity() {
		t.Fatal("storage path changes execution identity")
	}
	cfg.Limits.Timeout *= 2
	cfg.ArtifactDir = filepath.Join(t.TempDir(), "third")
	third, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if third.Identity() == r.Identity() {
		t.Fatal("limits omitted from identity")
	}
}
