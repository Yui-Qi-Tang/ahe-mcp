package mcprelations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const maxReviewToolRequestBytes = 1 << 20

// Exact-review requests reject ambiguous keys before encoding/json can
// silently replace an earlier decision or subject. Older ingestion decoders
// are deliberately unchanged by this shared, narrowly scoped boundary.
func decodeRequest(payload []byte, dest any) error {
	label := "relation review"
	fail := func(message string) error { return errors.New(message) }
	if len(payload) > maxReviewToolRequestBytes || !utf8.Valid(payload) {
		return fail(label + " request must be valid UTF-8 within 1 MiB")
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fail(label + " request must be one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := uniqueReviewJSONValue(decoder, 0, label); err != nil {
		return fail(err.Error())
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fail(label + " request must contain exactly one JSON object")
	}
	strict := json.NewDecoder(bytes.NewReader(payload))
	strict.DisallowUnknownFields()
	if err := strict.Decode(dest); err != nil {
		return fail(err.Error())
	}
	return nil
}

func uniqueReviewJSONValue(decoder *json.Decoder, depth int, label string) error {
	if depth > 32 {
		return fmt.Errorf("%s request JSON nesting exceeds 32", label)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("invalid %s JSON container", label)
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid %s JSON field", label)
			}
			// Non-ASCII aliases such as long-s fold onto ASCII field names in
			// encoding/json. All fields in these closed contracts are ASCII.
			for _, char := range name {
				if char >= utf8.RuneSelf {
					return fmt.Errorf("%s JSON field names must be ASCII", label)
				}
			}
			name = strings.ToLower(name)
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate %s JSON field", label)
			}
			seen[name] = struct{}{}
		}
		if err := uniqueReviewJSONValue(decoder, depth+1, label); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
