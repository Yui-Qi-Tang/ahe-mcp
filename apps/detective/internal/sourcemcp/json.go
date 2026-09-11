package sourcemcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

func decodeJSON(data []byte, output any) error {
	if len(data) == 0 || len(data) > maxRPCBytes || !utf8.Valid(data) || !pairedSurrogates(data) {
		return errors.New("invalid source MCP JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkValue(decoder, 0); err != nil {
		return errors.New("invalid source MCP JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("invalid source MCP JSON")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(output) != nil {
		return errors.New("invalid source MCP JSON")
	}
	return nil
}

// encoding/json replaces unmatched UTF-16 escapes with U+FFFD. Reject them
// before decoding so the displayed source cannot silently replace its bytes.
func pairedSurrogates(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func checkValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON depth exceeded")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	keys := make(map[string]bool)
	for d.More() {
		if delim == '{' {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || keys[key] {
				return errors.New("duplicate JSON key")
			}
			keys[key] = true
		}
		if err := checkValue(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func isObject(data []byte) bool {
	var object map[string]json.RawMessage
	return decodeJSON(data, &object) == nil && object != nil
}

func canonicalJSON(data []byte) []byte {
	var value any
	if decodeJSON(data, &value) != nil {
		return nil
	}
	return mustJSON(value)
}
