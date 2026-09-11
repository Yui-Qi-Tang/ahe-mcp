// detective-source-demo is an explicitly synthetic, read-only MCP fixture.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const sourceText = "# Synthetic Detective source\n\n## Status at a Glance\n\n| Area | State | Limits |\n| --- | --- | --- |\n| Preview search | **IMPLEMENTED, EXPOSED** | Synthetic rehearsal only; company deployment has not been verified |\n| Desktop review | **PLANNED** | No canonical writer is exposed by the desktop preview |\n"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func main() {
	address := flag.String("http", "", "optional literal loopback address for the synthetic HTTP MCP fixture")
	flag.Parse()
	var err error
	if *address == "" {
		err = stdio(os.Stdin, os.Stdout)
	} else {
		err = serveHTTP(*address)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "synthetic source fixture stopped with an error")
		os.Exit(1)
	}
}

func reply(body []byte) ([]byte, bool) {
	var req request
	if json.Unmarshal(body, &req) != nil || req.JSONRPC != "2.0" {
		return failure(nil, -32700, "invalid request"), false
	}
	if len(req.ID) == 0 {
		return nil, true
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "detective-synthetic-source", "version": "0.1.0"}, "instructions": "Synthetic read-only fixture, not company evidence."}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{"name": "read_status", "description": "Read an explicitly synthetic STATUS document. No network, file or DB access.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}}}}
	case "tools/call":
		var args struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &args) != nil || args.Name != "read_status" || len(args.Arguments) != 0 {
			return failure(req.ID, -32602, "only read_status with empty arguments is supported"), false
		}
		result = map[string]any{"content": []any{map[string]string{"type": "text", "text": sourceText}}, "isError": false}
	default:
		return failure(req.ID, -32601, "method not supported"), false
	}
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) // All fields are JSON-safe fixture data.
	return encoded, false
}

func failure(id json.RawMessage, code int, message string) []byte {
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}) // Fixed JSON-safe protocol error.
	return encoded
}

func stdio(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 128*1024)
	for scanner.Scan() {
		body, notification := reply(scanner.Bytes())
		if notification {
			continue
		}
		if _, err := fmt.Fprintln(output, string(body)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	// This fixture is for native clients only; browser-origin calls are refused.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins not accepted", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128*1024))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	response, notification := reply(body)
	if notification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(response) // Client disconnects need no retry or side effect.
}

func serveHTTP(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return errors.New("fixture requires literal loopback")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	fmt.Fprintf(os.Stderr, "synthetic MCP fixture only: http://%s/mcp\n", listener.Addr())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return err
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
