package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode"
)

func TestSourceOutputEscapesTerminalControlsWithoutChangingJSON(t *testing.T) {
	want := map[string]string{"description": "來源\n\u0085\u009b\u202e\U000e0001\u001b[31m", "args_json": " {\n  \"id\": \"來源\"\n} \n"}
	var output bytes.Buffer
	if err := writeSourceJSON(&output, want); err != nil {
		t.Fatal(err)
	}
	for _, r := range output.String() {
		if (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') || unicode.Is(unicode.Cf, r) {
			t.Fatalf("unsafe raw terminal rune %U", r)
		}
	}
	for _, escaped := range []string{`\u0085`, `\u009b`, `\u202e`, `\udb40\udc01`} {
		if !strings.Contains(output.String(), escaped) {
			t.Errorf("missing escaped control %s", escaped)
		}
	}
	var got map[string]string
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || got["description"] != want["description"] || got["args_json"] != want["args_json"] {
		t.Fatal("safe output changed source or exact arguments")
	}
}

type sourceShortOutput struct{}

func (sourceShortOutput) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestSourceOutputDetectsShortWrites(t *testing.T) {
	if err := writeSourceJSON(sourceShortOutput{}, map[string]string{"state": "captured_locally"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want short write", err)
	}
}
