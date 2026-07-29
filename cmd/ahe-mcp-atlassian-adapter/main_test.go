package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresAbsoluteConfig(t *testing.T) {
	t.Setenv("AHE_MCP_ATLASSIAN_ADAPTER_CONFIG", "")
	err := run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunRejectsUnknownConfigFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adapter.json")
	if err := os.WriteFile(path, []byte(`{"provider_release":"sooperset/mcp-atlassian@v0.23.0","provider_command":{},"unknown":true}`), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	t.Setenv("AHE_MCP_ATLASSIAN_ADAPTER_CONFIG", path)
	err := run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("run() error = %v", err)
	}
}
