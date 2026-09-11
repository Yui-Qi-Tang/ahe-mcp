// Package sourcemcp provides bounded, manually selected source MCP calls.
// An allowlist is operator policy, not proof that a server is read-only. The
// caller owns human confirmation; tool descriptions and annotations confer no
// authority. This package never calls models, follows source links, or retries.
package sourcemcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	protocolVersion = "2025-06-18"
	maxRPCBytes     = 1 << 20
	maxArgsBytes    = 64 << 10
	maxPages        = 4
	maxTools        = 128
	maxSessionTime  = time.Minute
	shutdownGrace   = 2 * time.Second
)

// Config is trusted operator configuration, never model-generated authority.
// Transport is "stdio" or "streamable-http". HTTP supports only unauthenticated
// loopback literals; external services require a separately governed local gateway.
type Config struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	Command      string   `json:"command"`
	URL          string   `json:"url"`
	AllowedTools []string `json:"allowed_tools"`
}

// Tool is a discovered tool bound to its complete inventory and configuration.
// The controller must retain this value and obtain explicit human confirmation;
// accepting a replacement Tool from an untrusted frontend is not authorization.
type Tool struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	InputSchemaJSON string `json:"input_schema_json"`
	SchemaSHA256    string `json:"schema_sha256"`
	InventorySHA256 string `json:"inventory_sha256"`
	ConfigSHA256    string `json:"config_sha256"`
}

// Result preserves the exact tools/call result JSON and its SHA-256. Text is a
// display projection: multiple text blocks have explicit numbered separators.
// Neither the display text nor the digest proves semantic correctness.
type Result struct {
	Text    string `json:"text"`
	RawJSON string `json:"raw_json"`
	SHA256  string `json:"sha256"`
}

// ErrToolNotAllowed means no session was started for the requested tool.
var ErrToolNotAllowed = errors.New("source MCP tool is not allowlisted")

// ErrInventoryChanged requires fresh discovery and new human confirmation.
var ErrInventoryChanged = errors.New("source MCP inventory or configuration changed; rediscover and confirm")

// Validate checks the supported endpoint and closed operator allowlist without
// contacting a server. A trusted launcher remains responsible for its own files,
// credentials, child processes, network access, and enforcement of read-only scope.
func Validate(c Config) error {
	return validateConfig(c, true)
}

// Recorded call inspection checks syntax without touching historical launchers.
func validateConfig(c Config, inspectLauncher bool) error {
	if !ValidConnectionID(c.ID) || !validText(c.Name, 256) || strings.TrimSpace(c.Name) == "" || len(c.AllowedTools) == 0 || len(c.AllowedTools) > maxTools {
		return errors.New("invalid source MCP configuration")
	}
	seen := make(map[string]bool)
	for _, name := range c.AllowedTools {
		if !validName(name) || seen[name] {
			return errors.New("invalid source MCP tool allowlist")
		}
		seen[name] = true
	}
	switch c.Transport {
	case "stdio":
		if c.URL != "" || !filepath.IsAbs(c.Command) || filepath.Clean(c.Command) != c.Command || !validText(c.Command, 4096) {
			return errors.New("source MCP requires one absolute launcher path")
		}
		if !inspectLauncher {
			return nil
		}
		info, err := os.Lstat(c.Command)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("source MCP launcher must be a protected executable regular file")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(owner.Uid) != os.Getuid() {
			return errors.New("source MCP launcher must be owned by the current operator")
		}
	case "streamable-http":
		if c.Command != "" || !validText(c.URL, 2048) {
			return errors.New("invalid source MCP HTTP endpoint")
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
			return errors.New("source MCP HTTP requires a credential-free loopback literal URL")
		}
		ip := net.ParseIP(u.Hostname())
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 || ip == nil || !ip.IsLoopback() || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
			return errors.New("source MCP HTTP requires 127.0.0.1 or ::1 and an explicit port")
		}
	default:
		return errors.New("unsupported source MCP transport")
	}
	return nil
}

// Discover initializes one session, reads at most four inventory pages, and
// returns only allowlisted tools. It does not invoke any source tool.
func Discover(ctx context.Context, c Config) (result []Tool, err error) {
	if err := Validate(c); err != nil {
		return nil, err
	}
	s, err := startSession(ctx, c)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := s.close(); err == nil && closeErr != nil {
			result, err = nil, closeErr
		}
	}()
	return s.discover(c)
}

// Call revalidates a previously discovered Tool before one allowlisted call.
// Failure or cancellation does not prove that a remote operation was rolled
// back. No retry, fallback endpoint, or additional tool call is performed.
func Call(ctx context.Context, c Config, expected Tool, argsJSON string) (result Result, err error) {
	if !allowed(c, expected.Name) {
		return Result{}, ErrToolNotAllowed
	}
	if err := Validate(c); err != nil {
		return Result{}, err
	}
	if expected.ConfigSHA256 != configDigest(c) || expected.SchemaSHA256 != digest([]byte(expected.InputSchemaJSON)) || !validDigest(expected.InventorySHA256) {
		return Result{}, ErrInventoryChanged
	}
	args, err := decodeArguments(argsJSON)
	if err != nil {
		return Result{}, err
	}
	if err := validateArguments(expected.InputSchemaJSON, args); err != nil {
		return Result{}, err
	}
	s, err := startSession(ctx, c)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if closeErr := s.close(); err == nil && closeErr != nil {
			result, err = Result{}, closeErr
		}
	}()
	tools, err := s.discover(c)
	if err != nil {
		return Result{}, err
	}
	found := false
	for _, tool := range tools {
		if tool == expected {
			found = true
		}
	}
	if !found {
		return Result{}, ErrInventoryChanged
	}
	raw, err := s.request("tools/call", map[string]any{"name": expected.Name, "arguments": json.RawMessage(argsJSON)})
	if err != nil {
		return Result{}, err
	}
	return sourceResult(raw)
}

// ValidateArgumentsJSON checks a complete bounded JSON object without starting a
// session. It rejects duplicate keys and lossy Unicode decoding; it does not
// validate a tool schema or grant permission to call a tool.
func ValidateArgumentsJSON(raw string) error {
	_, err := decodeArguments(raw)
	return err
}

func decodeArguments(raw string) (map[string]any, error) {
	var args map[string]any
	if len(raw) > maxArgsBytes || decodeJSON([]byte(raw), &args) != nil || args == nil {
		return nil, errors.New("source MCP arguments must be a bounded JSON object")
	}
	return args, nil
}

func (s *session) discover(c Config) ([]Tool, error) {
	raw, err := s.request("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "detective-source-preview", "version": "0.1.0"},
	})
	if err != nil {
		return nil, err
	}
	var init struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      map[string]json.RawMessage `json:"serverInfo"`
	}
	if decodeJSON(raw, &init) != nil || init.ProtocolVersion != protocolVersion || !isObject(init.Capabilities["tools"]) || !isObject(mustJSON(init.ServerInfo)) {
		return nil, errors.New("source MCP initialization contract mismatch")
	}
	var serverName, serverVersion string
	if decodeJSON(init.ServerInfo["name"], &serverName) != nil || decodeJSON(init.ServerInfo["version"], &serverVersion) != nil || !validText(serverName, 256) || serverName == "" || !validText(serverVersion, 256) || serverVersion == "" {
		return nil, errors.New("source MCP server identity is incomplete")
	}
	// Server instructions, sampling, elicitation, roots, and resource requests are
	// not enabled by the empty client capability set and are never executed here.
	if err := s.notify("notifications/initialized"); err != nil {
		return nil, err
	}
	var tools []Tool
	var inventory []json.RawMessage
	seenNames, seenCursors := make(map[string]bool), make(map[string]bool)
	cursor := ""
	for page := 0; page < maxPages; page++ {
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := s.request("tools/list", params)
		if err != nil {
			return nil, err
		}
		var list struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if decodeJSON(raw, &list) != nil || list.Tools == nil || len(inventory)+len(list.Tools) > maxTools {
			return nil, errors.New("source MCP inventory is invalid or exceeds its bound")
		}
		for _, item := range list.Tools {
			var wire struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			}
			if decodeJSON(item, &wire) != nil || !validName(wire.Name) || seenNames[wire.Name] || len(wire.Description) > 16<<10 || !validSourceText(wire.Description) || !isObject(wire.InputSchema) {
				return nil, errors.New("source MCP tool definition is invalid")
			}
			var schema struct {
				Type string `json:"type"`
			}
			if decodeJSON(wire.InputSchema, &schema) != nil || schema.Type != "object" {
				return nil, errors.New("source MCP input schema must describe an object")
			}
			seenNames[wire.Name] = true
			inventory = append(inventory, canonicalJSON(item))
			if allowed(c, wire.Name) {
				schemaJSON := string(canonicalJSON(wire.InputSchema))
				tools = append(tools, Tool{Name: wire.Name, Description: wire.Description, InputSchemaJSON: schemaJSON, SchemaSHA256: digest([]byte(schemaJSON))})
			}
		}
		if list.NextCursor == "" {
			sort.Slice(inventory, func(i, j int) bool { return bytes.Compare(inventory[i], inventory[j]) < 0 })
			// Include server identity and negotiated capabilities as well as all
			// definitions, including tools outside the operator's allowlist.
			inventoryHash := digest(mustJSON(struct {
				ServerInfo   map[string]json.RawMessage `json:"server_info"`
				Capabilities map[string]json.RawMessage `json:"capabilities"`
				Tools        []json.RawMessage          `json:"tools"`
			}{init.ServerInfo, init.Capabilities, inventory}))
			for i := range tools {
				tools[i].InventorySHA256, tools[i].ConfigSHA256 = inventoryHash, configDigest(c)
			}
			sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
			return tools, nil
		}
		if !validText(list.NextCursor, 1024) || seenCursors[list.NextCursor] {
			return nil, errors.New("source MCP pagination cursor is invalid or repeated")
		}
		seenCursors[list.NextCursor], cursor = true, list.NextCursor
	}
	return nil, errors.New("source MCP inventory exceeds the page bound")
}

func sourceResult(raw []byte) (Result, error) {
	var fields map[string]json.RawMessage
	if decodeJSON(raw, &fields) != nil {
		return Result{}, errors.New("source MCP tool result is malformed")
	}
	if flag, present := fields["isError"]; present && string(flag) != "true" && string(flag) != "false" {
		return Result{}, errors.New("source MCP tool error status is invalid")
	}
	var wire struct {
		Content []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if decodeJSON(raw, &wire) != nil || wire.IsError || wire.Content == nil || len(wire.Content) > 32 || (len(wire.StructuredContent) != 0 && !isObject(wire.StructuredContent)) {
		return Result{}, errors.New("source MCP tool returned an error or unsupported result")
	}
	var blocks []string
	for i, block := range wire.Content {
		if block.Type != "text" || block.Text == nil || !validSourceText(*block.Text) {
			return Result{}, errors.New("source MCP preview accepts only bounded text content")
		}
		text := *block.Text
		if len(wire.Content) > 1 {
			text = fmt.Sprintf("[MCP text block %d]\n%s", i+1, text)
		}
		blocks = append(blocks, text)
	}
	if len(blocks) == 0 && len(wire.StructuredContent) == 0 {
		return Result{}, errors.New("source MCP result has no source content")
	}
	return Result{Text: strings.Join(blocks, "\n\n"), RawJSON: string(raw), SHA256: digest(raw)}, nil
}

func allowed(c Config, name string) bool {
	for _, item := range c.AllowedTools {
		if item == name {
			return true
		}
	}
	return false
}

// ValidConnectionID checks the shared connection ID rule without I/O: 1–128
// ASCII letters, digits, underscores, periods or hyphens. It does not authorize
// a connection or validate the other configuration fields.
func ValidConnectionID(id string) bool { return validName(id) }

func validName(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
			return false
		}
	}
	return true
}

func validText(s string, bound int) bool {
	if len(s) > bound || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validSourceText(s string) bool {
	return len(s) <= maxRPCBytes && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func validDigest(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == sha256.Size && s == strings.ToLower(s)
}

func configDigest(c Config) string {
	c.AllowedTools = append([]string(nil), c.AllowedTools...)
	sort.Strings(c.AllowedTools)
	return digest(mustJSON(c))
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v) // Only validated JSON and concrete serializable values reach this helper.
	return data
}
