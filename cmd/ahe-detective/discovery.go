package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func discoverTools(ctx context.Context, path string, stdout io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening discovery command config: %w", err)
	}
	defer file.Close()
	// Separate from host config: tool names and pins are not known yet.
	var config struct {
		Path        string   `json:"path"`
		Args        []string `json:"args,omitempty"`
		Directory   string   `json:"directory"`
		Environment []string `json:"environment,omitempty"`
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("reading discovery command config: %w", err)
	}
	if len(data) > 1<<20 {
		return errors.New("discovery command config exceeds 1048576 bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("decoding discovery command config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("discovery command config must contain exactly one JSON object")
	}
	command, err := mcpstdio.MacOSLoopbackOnlyCommand(mcpstdio.CommandConfig{
		Path: config.Path, Args: config.Args, Directory: config.Directory, Environment: config.Environment,
	})
	if err != nil {
		return err
	}
	result, err := mcpstdio.DiscoverTools(ctx, command)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
