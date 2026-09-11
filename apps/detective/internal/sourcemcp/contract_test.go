package sourcemcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func contractFixture(config Config) Tool {
	schema := `{"type":"object","properties":{"id":{"type":"string"},"limit":{"type":"integer","minimum":1,"default":2}},"required":["id"],"additionalProperties":false}`
	return Tool{Name: "read_status", Description: "Synthetic source", InputSchemaJSON: schema, SchemaSHA256: digest([]byte(schema)), ConfigSHA256: configDigest(config), InventorySHA256: strings.Repeat("a", 64)}
}

func TestToolValidationDoesNotInspectLauncherOrRewriteInput(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "operator-launcher")
	config := Config{ID: "source", Name: "Synthetic", Transport: "stdio", Command: launcher, AllowedTools: []string{"read_status", "read_details"}}
	tool := contractFixture(config)
	beforeConfig := config
	beforeConfig.AllowedTools = append([]string(nil), config.AllowedTools...)
	beforeTool := tool
	args := " {\"id\":\"original\"}\n"
	for _, mode := range []string{"missing", "not-executable"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "not-executable" {
				if err := os.WriteFile(launcher, []byte("must not execute"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Required arguments need not exist when validating only the contract.
			if err := ValidateToolContract(config, tool); err != nil {
				t.Fatal(err)
			}
			if err := ValidateToolArguments(config, tool, args); err != nil {
				t.Fatal(err)
			}
			if Validate(config) == nil {
				t.Fatal("runtime validation must still require a protected executable")
			}
			if !reflect.DeepEqual(config, beforeConfig) || tool != beforeTool || args != " {\"id\":\"original\"}\n" {
				t.Fatal("validation modified configuration, schema, or arguments")
			}
		})
	}
}

func TestToolValidationDoesNotContactSourceOrSchemaServer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	config := Config{ID: "source", Name: "Synthetic", Transport: "streamable-http", URL: server.URL + "/mcp", AllowedTools: []string{"read_status"}}
	tool := contractFixture(config)
	if err := ValidateToolContract(config, tool); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolArguments(config, tool, `{"id":"saved"}`); err != nil {
		t.Fatal(err)
	}
	tool.InputSchemaJSON = `{"type":"object","$ref":"` + server.URL + `/schema"}`
	tool.SchemaSHA256 = digest([]byte(tool.InputSchemaJSON))
	if ValidateToolContract(config, tool) == nil || ValidateToolArguments(config, tool, `{}`) == nil {
		t.Fatal("reference-bearing schema accepted")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("validation contacted a source or schema server %d times", got)
	}
}

func TestValidateToolContractRejectsInvalidMetadata(t *testing.T) {
	config := Config{ID: "source", Name: "Synthetic", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	tool := contractFixture(config)
	for _, mode := range []string{"config", "static-config", "schema-digest", "config-digest", "inventory-format", "denied-tool", "invalid-name", "description-size", "description-unicode", "description-nul"} {
		t.Run(mode, func(t *testing.T) {
			c, selected := config, tool
			switch mode {
			case "config":
				c.Name = "changed"
			case "static-config":
				c.URL = "http://localhost:1234/mcp"
				selected.ConfigSHA256 = configDigest(c)
			case "schema-digest":
				selected.InputSchemaJSON += " "
			case "config-digest":
				selected.ConfigSHA256 = strings.Repeat("b", 64)
			case "inventory-format":
				selected.InventorySHA256 = strings.Repeat("A", 64)
			case "denied-tool":
				selected.Name = "write_status"
			case "invalid-name":
				selected.Name = "read status"
				c.AllowedTools = []string{selected.Name}
				selected.ConfigSHA256 = configDigest(c)
			case "description-size":
				selected.Description = strings.Repeat("x", 16<<10+1)
			case "description-unicode":
				selected.Description = "invalid\xff"
			case "description-nul":
				selected.Description = "invalid\x00"
			}
			if ValidateToolContract(c, selected) == nil || ValidateToolArguments(c, selected, `{"id":"saved"}`) == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}

func TestValidateToolContractRejectsInvalidSchemasBeforeArguments(t *testing.T) {
	config := Config{ID: "source", Name: "Synthetic", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	for _, schema := range []string{
		`{}`, `null`, `true`, `[]`, `{"type":"array"}`, `{"Type":"object"}`,
		`{"type":"object","type":"object"}`, `{"type":"object"}{}`,
		`{"type":"object","title":"\ud800"}`,
		`{"type":"object","$ref":"#/properties/id"}`,
		`{"type":"object","properties":{"id":{"$dynamicRef":"http://127.0.0.1:1/schema"}}}`,
		`{"type":"object","$recursiveRef":"#"}`,
		`{"type":"object","required":1}`,
		`{"type":"object","properties":{"id":{"pattern":"["}}}`,
		`{"type":"object","minimum":1e1000}`,
		`{"type":"object","title":"` + strings.Repeat("x", maxArgsBytes) + `"}`,
	} {
		t.Run(digest([]byte(schema))[:8], func(t *testing.T) {
			tool := contractFixture(config)
			tool.InputSchemaJSON, tool.SchemaSHA256 = schema, digest([]byte(schema))
			if ValidateToolContract(config, tool) == nil {
				t.Fatal("invalid schema accepted before model input")
			}
			if ValidateToolArguments(config, tool, `{"id":"saved"}`) == nil {
				t.Fatal("invalid schema accepted with arguments")
			}
		})
	}
}

func TestValidateToolArgumentsRejectsInvalidParametersWithoutEcho(t *testing.T) {
	config := Config{ID: "source", Name: "Synthetic", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	tool := contractFixture(config)
	for _, args := range []string{
		`{}`, `null`, `[]`, `{} {}`,
		`{"id":1}`, `{"id":"private-marker","extra":true}`,
		`{"id":"private-marker","id":"duplicate"}`,
		`{"id":"\ud800"}`, "{\"id\":\"\xff\"}",
		`{"id":"saved","limit":1.2}`, `{"id":"saved","limit":0}`,
		`{"id":"saved","limit":9007199254740992}`,
		`{"id":"` + strings.Repeat("x", maxArgsBytes) + `"}`,
	} {
		t.Run(digest([]byte(args))[:8], func(t *testing.T) {
			err := ValidateToolArguments(config, tool, args)
			if err == nil {
				t.Fatal("invalid arguments accepted")
			}
			if strings.Contains(err.Error(), "private-marker") || strings.Contains(err.Error(), args) {
				t.Fatal("validation error exposed proposed arguments")
			}
		})
	}
}

func TestToolValidationRejectsCaseFoldedRequiredOverride(t *testing.T) {
	config := Config{ID: "source", Name: "Synthetic", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	tool := contractFixture(config)
	tool.InputSchemaJSON = `{"type":"object","required":["id"],"Required":[],"properties":{"id":{"type":"string"}},"additionalProperties":false}`
	tool.SchemaSHA256 = digest([]byte(tool.InputSchemaJSON))
	if err := ValidateToolArguments(config, tool, `{}`); err == nil {
		t.Fatal("missing required id accepted after case-folded Required override")
	}
}

func TestToolValidationRejectsSchemaKeywordAliases(t *testing.T) {
	config := Config{ID: "source", Name: "Synthetic", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	for _, schema := range []string{
		`{"type":"object","Type":"object"}`,
		`{"Type":"object","type":"object"}`,
		`{"type":"object","REQUIRED":[]}`,
		`{"type":"object","propertieſ":{}}`,
		`{"type":"object","required":["id"],"Required":[]}`,
		`{"type":"object","Required":[],"required":["id"]}`,
		`{"type":"object","properties":{"id":{"type":"string","Type":"number"}}}`,
		`{"type":"object","properties":{"id":{"type":"object","Required":[]}}}`,
		`{"type":"object","properties":{"id":{"type":"array","items":{"type":"string","MinLength":0}}}}`,
		`{"type":"object","$defs":{"Required":{"Type":"string"}}}`,
		`{"type":"object","definitions":{"Type":{"Required":[]}}}`,
		`{"type":"object","allOf":[{"Required":[]}]}`,
		`{"type":"object","anyOf":[{"Required":[]}]}`,
		`{"type":"object","oneOf":[{"Required":[]}]}`,
		`{"type":"object","prefixItems":[{"Type":"string"}]}`,
		`{"type":"object","items":[{"Type":"string"}]}`,
		`{"type":"object","dependentSchemas":{"Required":{"Type":"string"}}}`,
		`{"type":"object","dependencies":{"Required":{"Type":"string"}}}`,
		`{"type":"object","patternProperties":{"Required":{"Type":"string"}}}`,
		`{"type":"object","additionalProperties":{"Type":"string"}}`,
		`{"type":"object","not":{"Required":[]}}`,
		`{"type":"object","if":{"Required":[]}}`,
		`{"type":"object","then":{"Required":[]}}`,
		`{"type":"object","else":{"Required":[]}}`,
		`{"type":"object","contentSchema":{"Required":[]}}`,
	} {
		t.Run(digest([]byte(schema))[:8], func(t *testing.T) {
			tool := contractFixture(config)
			tool.InputSchemaJSON, tool.SchemaSHA256 = schema, digest([]byte(schema))
			if ValidateToolContract(config, tool) == nil || ValidateToolArguments(config, tool, `{}`) == nil {
				t.Fatal("case-folded schema keyword accepted")
			}
			// Runtime calls share the same resolver, not only the new static API.
			if validateArguments(schema, map[string]any{}) == nil {
				t.Fatal("runtime argument validation accepted a case-folded keyword")
			}
		})
	}
}

func TestToolValidationPreservesNonKeywordNamesAndValues(t *testing.T) {
	config := Config{ID: "source", Name: "Synthetic", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	for _, schema := range []string{
		`{"type":"object","required":["Required"],"properties":{"Required":{"type":"string"},"Type":{"type":"string"}},"additionalProperties":false}`,
		`{"type":"object","$defs":{"Required":{"type":"string"},"Type":{"type":"object"}}}`,
		`{"type":"object","definitions":{"Required":{"type":"string"}}}`,
		`{"type":"object","patternProperties":{"Required":{"type":"string"}}}`,
		`{"type":"object","dependentSchemas":{"Required":{"type":"object"}}}`,
		`{"type":"object","dependencies":{"Required":["Type"]}}`,
		`{"type":"object","dependentRequired":{"Required":["Type"]}}`,
		`{"type":"object","examples":[{"Required":[],"Type":"string"}],"default":{"Required":"text"},"x-annotation":{"Required":[]}}`,
		`{"type":"object","enum":[{"Required":"text","Type":"value"}],"const":{"Required":"text","Type":"value"}}`,
	} {
		t.Run(digest([]byte(schema))[:8], func(t *testing.T) {
			tool := contractFixture(config)
			tool.InputSchemaJSON, tool.SchemaSHA256 = schema, digest([]byte(schema))
			if err := ValidateToolArguments(config, tool, `{"Required":"text","Type":"value"}`); err != nil {
				t.Fatal(err)
			}
			if tool.InputSchemaJSON != schema {
				t.Fatal("schema was rewritten")
			}
		})
	}
}

func TestSchemaKeywordGuardCoversPinnedDecoderFields(t *testing.T) {
	known := map[string]bool{"type": true, "items": true, "dependencies": true}
	schemaType := reflect.TypeFor[jsonschema.Schema]()
	for i := range schemaType.NumField() {
		name := strings.Split(schemaType.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			known[name] = true
		}
	}
	if len(schemaKeywords) != len(known) {
		t.Fatal("keyword guard no longer matches the pinned decoder fields")
	}
	for _, name := range schemaKeywords {
		if !known[name] {
			t.Fatalf("unexpected keyword %q", name)
		}
		delete(known, name)
	}
	if len(known) != 0 {
		t.Fatal("decoder keyword is missing from the alias guard")
	}
}
