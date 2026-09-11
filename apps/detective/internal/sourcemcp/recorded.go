package sourcemcp

import "errors"

// ValidateRecordedJSON checks a bounded saved JSON object without losing Unicode
// or accepting duplicate keys. It confers no authenticity or execution authority.
func ValidateRecordedJSON(raw string) error {
	var object map[string]any
	if decodeJSON([]byte(raw), &object) != nil || object == nil {
		return errors.New("invalid recorded source JSON")
	}
	return nil
}

// ValidateToolContract checks a tool's static configuration, digest bindings,
// and bounded object schema with zero I/O. It does not inspect a launcher,
// authenticate an inventory digest, or authorize a tool call.
func ValidateToolContract(config Config, tool Tool) error {
	if validateConfig(config, false) != nil || !allowed(config, tool.Name) || !validName(tool.Name) || !validSourceText(tool.Description) || len(tool.Description) > 16<<10 || len(tool.InputSchemaJSON) > maxArgsBytes || !validDigest(tool.InventorySHA256) || tool.ConfigSHA256 != configDigest(config) || tool.SchemaSHA256 != digest([]byte(tool.InputSchemaJSON)) {
		return errors.New("source MCP tool contract does not match")
	}
	var schema map[string]any
	if decodeJSON([]byte(tool.InputSchemaJSON), &schema) != nil || schema["type"] != "object" {
		return errors.New("source MCP input schema is not an object schema")
	}
	if _, err := resolveInputSchema(tool.InputSchemaJSON); err != nil {
		return errors.New("source MCP input schema is unsupported")
	}
	return nil
}

// ValidateToolArguments checks exact proposed arguments against a static tool
// contract with zero I/O. No defaults, rewritten bytes, or call authority result.
func ValidateToolArguments(config Config, tool Tool, argsJSON string) error {
	if err := ValidateToolContract(config, tool); err != nil {
		return err
	}
	args, err := decodeArguments(argsJSON)
	if err != nil || validateArguments(tool.InputSchemaJSON, args) != nil {
		return errors.New("source MCP arguments do not match the tool contract")
	}
	return nil
}

// VerifyRecordedSource checks the saved call contract and exact result-to-text
// projection with zero I/O. Historical launcher existence and current tool
// inventory are deliberately irrelevant. Only the inventory digest's format can
// be checked; the complete historical inventory is not present in this record.
func VerifyRecordedSource(config Config, tool Tool, argsJSON, rawResult, text string) error {
	if ValidateToolArguments(config, tool, argsJSON) != nil {
		return errors.New("recorded source call contract or arguments do not match")
	}
	result, err := sourceResult([]byte(rawResult))
	if err != nil || result.Text == "" || result.Text != text {
		return errors.New("recorded source result and text do not match")
	}
	return nil
}
