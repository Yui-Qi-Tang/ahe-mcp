package sourcemcp

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// validateArguments checks the actual arguments without applying defaults or
// rewriting any bytes. This preview deliberately rejects reference-bearing
// schemas, including local recursive references. There is no remote loader.
func validateArguments(schemaJSON string, arguments map[string]any) error {
	resolved, err := resolveInputSchema(schemaJSON)
	if err != nil {
		return err
	}
	instance, err := schemaNumbers(arguments)
	if err != nil {
		return err
	}
	if err := resolved.Validate(instance); err != nil {
		// Validator errors contain the actual argument values. Keep them private.
		return errors.New("source MCP arguments do not match the confirmed input schema")
	}
	return nil
}

// resolveInputSchema checks the schema independently of any proposed arguments,
// so a contract with required properties can be checked before model input.
func resolveInputSchema(schemaJSON string) (*jsonschema.Resolved, error) {
	var raw any
	if len(schemaJSON) > maxArgsBytes || decodeJSON([]byte(schemaJSON), &raw) != nil || hasSchemaReference(raw) {
		return nil, errors.New("source MCP input schema is invalid, reference-bearing, or exceeds the preview bound")
	}
	if hasSchemaKeywordAlias(raw) {
		return nil, errors.New("source MCP input schema contains a case-folded keyword alias")
	}
	if _, err := schemaNumbers(raw); err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if decodeJSON([]byte(schemaJSON), &schema) != nil {
		return nil, errors.New("source MCP input schema is unsupported")
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: nil})
	if err != nil {
		return nil, errors.New("source MCP input schema could not be resolved locally")
	}
	return resolved, nil
}

// These are the JSON keywords decoded by the pinned jsonschema-go Schema,
// including its custom type, items, and dependencies fields. JSON Schema keys
// are case-sensitive; encoding/json struct decoding also accepts case-folded
// aliases that can overwrite the exact constraint before validation.
var schemaKeywords = strings.Fields(`
	$id $schema $ref $comment $defs definitions dependencies
	$anchor $dynamicAnchor $dynamicRef $vocabulary
	title description default deprecated readOnly writeOnly examples
	type enum const multipleOf minimum maximum exclusiveMinimum exclusiveMaximum
	minLength maxLength pattern prefixItems items minItems maxItems additionalItems
	uniqueItems contains minContains maxContains unevaluatedItems
	minProperties maxProperties required dependentRequired properties
	patternProperties additionalProperties propertyNames unevaluatedProperties
	allOf anyOf oneOf not if then else dependentSchemas
	contentEncoding contentMediaType contentSchema format
`)

func hasSchemaKeywordAlias(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, child := range object {
		for _, keyword := range schemaKeywords {
			if key != keyword && strings.EqualFold(key, keyword) {
				return true
			}
		}
		// Only schema positions contain keywords. Names in property/definition
		// maps, and object values in annotations or enum/const, stay untouched.
		switch key {
		case "$defs", "definitions", "properties", "patternProperties", "dependentSchemas", "dependencies":
			children, _ := child.(map[string]any)
			for _, schema := range children {
				if hasSchemaKeywordAlias(schema) {
					return true
				}
			}
		case "allOf", "anyOf", "oneOf", "prefixItems":
			children, _ := child.([]any)
			for _, schema := range children {
				if hasSchemaKeywordAlias(schema) {
					return true
				}
			}
		case "items":
			if children, ok := child.([]any); ok {
				for _, schema := range children {
					if hasSchemaKeywordAlias(schema) {
						return true
					}
				}
			} else if hasSchemaKeywordAlias(child) {
				return true
			}
		case "additionalItems", "contains", "unevaluatedItems", "additionalProperties", "propertyNames", "unevaluatedProperties", "not", "if", "then", "else", "contentSchema":
			if hasSchemaKeywordAlias(child) {
				return true
			}
		}
	}
	return false
}

// The selected validator treats json.Number as a string for type checks. Make
// a separate numeric projection, preserving integer-vs-fraction distinctions.
// Limit numbers to the interoperable safe range; wire arguments stay untouched.
func schemaNumbers(value any) (any, error) {
	bad := errors.New("source MCP JSON number exceeds the preview validation range")
	switch v := value.(type) {
	case json.Number:
		number := strings.ToLower(v.String())
		if len(number) > 64 {
			return nil, bad
		}
		if _, exponent, ok := strings.Cut(number, "e"); ok {
			e, err := strconv.Atoi(exponent)
			if err != nil || e < -300 || e > 300 {
				return nil, bad
			}
		}
		f, err := v.Float64()
		if err != nil || math.IsInf(f, 0) || math.Abs(f) > 1<<53-1 {
			return nil, bad
		}
		r, ok := new(big.Rat).SetString(number)
		if !ok {
			return nil, bad
		}
		if r.IsInt() {
			return r.Num().Int64(), nil
		}
		if math.Trunc(f) == f {
			return nil, bad
		} // Do not turn a tiny fraction into an integer.
		return f, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			converted, err := schemaNumbers(child)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			converted, err := schemaNumbers(child)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	default:
		return value, nil
	}
}

func hasSchemaReference(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "$ref" || key == "$dynamicRef" || key == "$recursiveRef" {
				return true
			}
			if hasSchemaReference(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasSchemaReference(child) {
				return true
			}
		}
	}
	return false
}
