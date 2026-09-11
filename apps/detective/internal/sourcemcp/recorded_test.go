package sourcemcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyRecordedSourceDoesNotInspectHistoricalLauncher(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "historical-launcher")
	config := Config{ID: "recorded", Name: "Synthetic", Transport: "stdio", Command: launcher, AllowedTools: []string{"read_status"}}
	schema := `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`
	tool := Tool{Name: "read_status", Description: "Synthetic source", InputSchemaJSON: schema, SchemaSHA256: digest([]byte(schema)), ConfigSHA256: configDigest(config), InventorySHA256: strings.Repeat("a", 64)}
	raw := `{"content":[{"type":"text","text":"saved text"}],"isError":false}`
	for _, mode := range []string{"missing", "not-executable"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "not-executable" {
				if err := os.WriteFile(launcher, []byte("must not execute"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := VerifyRecordedSource(config, tool, " {\"id\":\"old\"}\n", raw, "saved text"); err != nil {
				t.Fatal(err)
			}
			if Validate(config) == nil {
				t.Fatal("runtime launcher validation was weakened")
			}
		})
	}
}

func TestVerifyRecordedSourceRejectsInconsistentContracts(t *testing.T) {
	config := Config{ID: "recorded", Name: "Synthetic", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	schema := `{"type":"object","properties":{},"additionalProperties":false}`
	tool := Tool{Name: "read_status", InputSchemaJSON: schema, SchemaSHA256: digest([]byte(schema)), ConfigSHA256: configDigest(config), InventorySHA256: strings.Repeat("a", 64)}
	raw := `{"content":[{"type":"text","text":"saved text"}],"isError":false}`
	for _, mode := range []string{"config", "schema", "inventory-format", "denied-tool", "args", "result-error", "text-mismatch", "schema-ref", "schema-type"} {
		t.Run(mode, func(t *testing.T) {
			c, recorded, args, result, text := config, tool, "{}", raw, "saved text"
			switch mode {
			case "config":
				c.Name = "different"
			case "schema":
				recorded.InputSchemaJSON = `{"type":"object"}`
			case "inventory-format":
				recorded.InventorySHA256 = "not-a-digest"
			case "denied-tool":
				recorded.Name = "write"
			case "args":
				args = `{"id":1}`
			case "result-error":
				result = strings.Replace(raw, "false", "true", 1)
			case "text-mismatch":
				text = "different text"
			case "schema-ref":
				recorded.InputSchemaJSON = `{"type":"object","$ref":"http://127.0.0.1:1/schema"}`
				recorded.SchemaSHA256 = digest([]byte(recorded.InputSchemaJSON))
			case "schema-type":
				recorded.InputSchemaJSON = `{}`
				recorded.SchemaSHA256 = digest([]byte(recorded.InputSchemaJSON))
			}
			if VerifyRecordedSource(c, recorded, args, result, text) == nil {
				t.Fatal("inconsistent saved source accepted")
			}
		})
	}
	// A different well-formed inventory digest cannot be authenticated offline.
	tool.InventorySHA256 = strings.Repeat("b", 64)
	if err := VerifyRecordedSource(config, tool, "{}", raw, "saved text"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRecordedJSONPreservesStrictCaptureParsing(t *testing.T) {
	large, err := json.Marshal(map[string]string{"record": strings.Repeat("x", 80<<10)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecordedJSON(string(large)); err != nil {
		t.Fatal("receipt may exceed argument-only 64 KiB bound")
	}
	for _, raw := range []string{"null", "[]", "{}{}", `{"id":1,"id":2}`, `{"nested":{"a":1,"a":2}}`, `{"text":"\ud800"}`, "{\"text\":\"\xff\"}"} {
		if ValidateRecordedJSON(raw) == nil {
			t.Fatal("invalid recorded JSON accepted")
		}
	}
}
