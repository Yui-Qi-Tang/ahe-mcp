package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHelpDoesNotRequireConfigurationOrDatabase(t *testing.T) {
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"--help"}, &stdout, &bytes.Buffer{}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("run(--help) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "Usage: ahe-detective") ||
		!strings.Contains(stdout.String(), "ahe-detective-host-v1/v2/v3/v4") {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestParseCommandArgsRejectsMissingAndUnknownArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"--config"}, {"--unknown"}} {
		if _, err := parseCommandArgs(args); err == nil {
			t.Fatalf("parseCommandArgs(%q) error = nil", args)
		}
	}
}

func TestRunRequiresDatabaseDNSAfterValidConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "detective.json")
	data := `{
  "schema_version":"ahe-detective-host-v1",
  "host_id":"host",
  "registration_request_id":"register",
  "workspace_id":"workspace:test",
  "workspace_root":"/tmp/test",
  "source_interval":"1s",
  "max_steps":1,
  "sources":[{"capability_name":"local-prd-text","capability_version":"v1","source_id":"prd","relative_path":"docs"}],
  "repository_maintenance":{"enabled":false}
}`
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	err := run(
		context.Background(),
		[]string{"--config", configPath},
		&bytes.Buffer{},
		&bytes.Buffer{},
		func(string) string { return "" },
	)
	if err == nil || err.Error() != "DATABASE_DSN is required" {
		t.Fatalf("run() error = %v", err)
	}
}

func TestDiscoveryArguments(t *testing.T) {
	for _, args := range [][]string{{"--discover-tools"}, {"--discover-tools", " "}, {"--discover-tools", "command.json", "--migrate"}, {"--discover-tools", "command.json", "--config", "host.json"}} {
		if _, err := parseCommandArgs(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	parsed, err := parseCommandArgs([]string{"--discover-tools", "command.json"})
	if err != nil || parsed.discoveryPath != "command.json" {
		t.Fatalf("parsed = %+v, error = %v", parsed, err)
	}
}

func TestDiscoveryDoesNotReadDatabaseEnvironment(t *testing.T) {
	err := run(context.Background(), []string{"--discover-tools", filepath.Join(t.TempDir(), "missing.json")}, &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { t.Fatal("discovery accessed database environment"); return "" })
	if err == nil || !strings.Contains(err.Error(), "opening discovery command config") {
		t.Fatalf("error = %v", err)
	}
}
