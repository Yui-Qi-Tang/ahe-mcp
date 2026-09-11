package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The relocation must not silently build MCP programs from a second checkout.
// Both rejection paths happen before any lab, PostgreSQL process or data is
// created; no installed PostgreSQL tools are needed for this regression test.
func TestBriefLabLauncherRequiresEnclosingMCPCheckout(t *testing.T) {
	script, err := filepath.Abs("../../scripts/brief-desktop-lab.sh")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	wrongRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, root, want string
	}{
		{"other_checkout", wrongRoot, "Select the AHE MCP root that contains this Detective checkout."},
		{"enclosing_checkout", root, "Expected an existing non-symlink directory."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", script, "setup", tc.root, "/nonexistent-synthetic-postgres-install")
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil || err == nil || strings.TrimSpace(string(output)) != tc.want {
				t.Fatalf("lab path rejection: error=%v output=%q", err, output)
			}
		})
	}
}
