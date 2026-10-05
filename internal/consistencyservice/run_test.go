package consistencyservice

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerRejectsArgumentsAndInvalidIntervalBeforeExecution(t *testing.T) {
	for _, args := range [][]string{nil, {"no-such-operation"}, {"run", "--arbitrary-command"}} {
		err := Run(context.Background(), args, func(string) string { t.Fatal("invalid arguments accessed environment"); return "" }, &bytes.Buffer{})
		if err == nil {
			t.Fatal("invalid operation accepted")
		}
	}
	for _, value := range []string{"0", "-1s", "1ms", "2h", "malformed"} {
		err := Run(context.Background(), []string{"run"}, func(k string) string {
			if k == "AHE_CONSISTENCY_INTERVAL" {
				return value
			}
			t.Fatal("invalid interval started initialization")
			return ""
		}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("invalid interval accepted")
		}
	}
}

func TestWorkerInitializationRejectsMissingPinsAndSharedArtifactDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(root, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AHE_CONSISTENCY_ARTIFACT_DIR": root, "AHE_CONSISTENCY_CADICAL": tool, "AHE_CONSISTENCY_DRAT": tool}
	lookup := func(k string) string { return env[k] }
	if _, _, err := newRunner(lookup); err == nil {
		t.Fatal("missing pins accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("failed startup leaked session")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newRunner(lookup); err == nil {
		t.Fatal("shared artifact directory accepted")
	}
}

func TestWorkerHelpNeedsNoCredentials(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"--help"}, func(string) string { t.Fatal("help accessed environment"); return "" }, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "identity | once | run") {
		t.Fatal("incomplete help")
	}
}
