package main

import (
	"context"
	"os"
	"testing"
)

func TestRunRejectsUnknownArgument(t *testing.T) {
	if err := run(context.Background(), []string{"unexpected"}); err == nil {
		t.Fatal("run() succeeded")
	}
}

func TestRunRequiresAbsoluteConfigPath(t *testing.T) {
	t.Setenv("AHE_MCP_CODEGRAPH_ADAPTER_CONFIG", "relative.json")
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("run() succeeded")
	}
}

func TestRunRejectsUnknownConfigField(t *testing.T) {
	path := t.TempDir() + "/config.json"
	if err := os.WriteFile(path, []byte(`{"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AHE_MCP_CODEGRAPH_ADAPTER_CONFIG", path)
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("run() succeeded")
	}
}
