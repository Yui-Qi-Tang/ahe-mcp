package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadStatusIsSyntheticAndBounded(t *testing.T) {
	body, notification := reply([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_status","arguments":{}}}`))
	if notification || !json.Valid(body) || !bytes.Contains(body, []byte("Synthetic rehearsal only")) {
		t.Fatal("expected synthetic source result")
	}
	for _, input := range []string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"write","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_status","arguments":{"path":"/etc/passwd"}}}`,
	} {
		body, _ := reply([]byte(input))
		if !bytes.Contains(body, []byte(`"error"`)) {
			t.Fatal("unexpected tool accepted")
		}
	}
}

func TestStdioNotificationsDoNotEmitReplies(t *testing.T) {
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/list\"}\n")
	var output bytes.Buffer
	if err := stdio(input, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\n") != 1 || !strings.Contains(output.String(), "read_status") {
		t.Fatal("unexpected stdio protocol")
	}
}

func TestHTTPRejectsBrowserOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("browser origin allowed")
	}
}
