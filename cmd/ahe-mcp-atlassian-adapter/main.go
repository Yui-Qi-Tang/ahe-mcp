package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/atlassianmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

const version = atlassianmcp.AdapterVersion

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Fprintln(os.Stdout, "Usage: ahe-mcp-atlassian-adapter")
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "Environment:")
			fmt.Fprintln(os.Stdout, "  AHE_MCP_ATLASSIAN_ADAPTER_CONFIG  Absolute path to the pinned adapter JSON config")
			return nil
		}
		return fmt.Errorf("unknown argument %q", args[0])
	}
	configPath := os.Getenv("AHE_MCP_ATLASSIAN_ADAPTER_CONFIG")
	if !filepath.IsAbs(configPath) {
		return errors.New("AHE_MCP_ATLASSIAN_ADAPTER_CONFIG must be an absolute path")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading Atlassian adapter config: %w", err)
	}
	var config atlassianmcp.Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("decoding Atlassian adapter config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Atlassian adapter config must contain one JSON object")
	}
	backend, err := atlassianmcp.NewBackend(config)
	if err != nil {
		return err
	}
	server, err := mcpstdio.NewServer("ahe-mcp-atlassian-adapter", version, backend)
	if err != nil {
		return err
	}
	return server.Serve(ctx, os.Stdin, os.Stdout)
}
