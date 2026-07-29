package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--help"}, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("run(--help) error = %v", err)
	}
	if !strings.Contains(out.String(), "Usage: ahe-migrate") {
		t.Fatalf("help output = %q, want usage", out.String())
	}
}

func TestRunRequiresDatabaseDNS(t *testing.T) {
	err := run(t.Context(), nil, func(string) string { return "" }, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_DNS is required") {
		t.Fatalf("run() error = %v, want DATABASE_DNS required", err)
	}
}

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	err := run(t.Context(), []string{"up"}, func(string) string { return "" }, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `unknown argument "up"`) {
		t.Fatalf("run(up) error = %v, want unknown argument", err)
	}
}
