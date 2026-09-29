package pouchvalidation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Strict JSON forbids null except the optional return certificate. All object
// keys are ASCII with exact casing; aliases, duplicates and unknown fields fail.
func decode(raw []byte, dest any) error {
	if len(raw) > MaxBytes || !utf8.Valid(raw) {
		return reject("JSON_BOUND_OR_UTF8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := unique(d, 0, ""); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return reject("JSON_TRAILING")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(dest); err != nil {
		return reject("JSON_SCHEMA")
	}
	var original, normalized any
	first := json.NewDecoder(bytes.NewReader(raw))
	first.UseNumber()
	if err := first.Decode(&original); err != nil {
		return reject("JSON_SCHEMA")
	}
	encoded, err := json.Marshal(dest)
	if err != nil {
		return reject("JSON_SCHEMA")
	}
	second := json.NewDecoder(bytes.NewReader(encoded))
	second.UseNumber()
	if err := second.Decode(&normalized); err != nil {
		return reject("JSON_SCHEMA")
	}
	if !reflect.DeepEqual(original, normalized) {
		return reject("JSON_FIELDS_OR_TYPES")
	}
	return nil
}
func unique(d *json.Decoder, depth int, key string) error {
	if depth > 32 {
		return reject("JSON_DEPTH")
	}
	token, err := d.Token()
	if err != nil {
		return reject("JSON_SCHEMA")
	}
	if token == nil && key != "return" {
		return reject("JSON_NULL")
	}
	delim, container := token.(json.Delim)
	if !container {
		if depth == 0 {
			return reject("JSON_OBJECT_REQUIRED")
		}
		return nil
	}
	if depth == 0 && delim != '{' {
		return reject("JSON_OBJECT_REQUIRED")
	}
	seen := map[string]bool{}
	for d.More() {
		child := ""
		if delim == '{' {
			token, err := d.Token()
			if err != nil {
				return reject("JSON_SCHEMA")
			}
			name, ok := token.(string)
			if !ok {
				return reject("JSON_SCHEMA")
			}
			for _, c := range name {
				if c >= utf8.RuneSelf {
					return reject("JSON_KEY")
				}
			}
			folded := strings.ToLower(name)
			if seen[folded] {
				return reject("JSON_DUPLICATE")
			}
			seen[folded] = true
			child = name
			// Upper-case coordinate and categorical names are permitted in maps. A
			// later structural roundtrip rejects aliases for actual struct members.
		}
		if err := unique(d, depth+1, child); err != nil {
			return err
		}
	}
	if _, err := d.Token(); err != nil {
		return reject("JSON_SCHEMA")
	}
	return nil
}
