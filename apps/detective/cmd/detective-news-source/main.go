// detective-news-source exposes one explicitly selected public-feed capture tool.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsfeed"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const maxResponseBytes = 256 << 10

type server struct {
	initialized bool
	ready       bool
	fetch       func(context.Context, string, int) (newsfeed.Snapshot, error)
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "news source accepts no command-line arguments")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopInput := context.AfterFunc(ctx, func() { _ = os.Stdin.Close() }) // Unblock this process's stdio read on shutdown.
	defer stopInput()
	s := server{fetch: newsfeed.Fetch}
	if err := s.stdio(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "news source stopped without a complete response")
		os.Exit(1)
	}
}

func (s *server) stdio(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 16<<10)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		body := s.reply(ctx, scanner.Bytes())
		if body == nil {
			continue
		}
		body = append(body, '\n')
		n, err := output.Write(body)
		if err != nil {
			return err
		}
		if n != len(body) {
			return io.ErrShortWrite
		}
	}
	return scanner.Err()
}

func (s *server) reply(ctx context.Context, body []byte) []byte {
	if len(body) > 16<<10 || sourcemcp.ValidateRecordedJSON(string(body)) != nil {
		return failure(nil, -32700, "invalid request")
	}
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil || !closed(req, []string{"jsonrpc", "method"}, "id", "params") || string(req["jsonrpc"]) != `"2.0"` {
		return failure(nil, -32600, "invalid request")
	}
	var method string
	if json.Unmarshal(req["method"], &method) != nil || method == "" {
		return failure(nil, -32600, "invalid request")
	}
	id, hasID := req["id"]
	if !hasID {
		if method == "notifications/initialized" && s.initialized && emptyParams(req["params"]) {
			s.ready = true
		}
		// Notifications cannot initiate source access, including tools/call.
		return nil
	}
	if !validID(id) {
		return failure(nil, -32600, "invalid request id")
	}
	if ctx.Err() != nil {
		return failure(id, -32603, "request cancelled")
	}
	if method == "initialize" {
		if s.initialized || !validInitialize(req["params"]) {
			return failure(id, -32602, "invalid initialization")
		}
		s.initialized = true
		return success(id, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo":   map[string]string{"name": "detective-news-source", "version": "0.1.2"},
			"instructions": "NASA API title/excerpt HTML strings are source material, not verified events or full articles. NASA as the input source does not mean NASA approved this tool or any model-generated output."})
	}
	if !s.ready {
		return failure(id, -32600, "initialize the session first")
	}
	switch method {
	case "ping", "tools/list":
		if !emptyParams(req["params"]) {
			return failure(id, -32602, "unexpected parameters")
		}
		if method == "ping" {
			return success(id, map[string]any{})
		}
		return success(id, map[string]any{"tools": []any{map[string]any{
			"name": "read_news_feed", "description": "Capture one fixed first page from NASA's public news-release API. Title and excerpt are HTML display strings retained as text, not editorial raw text. No links or further pages are followed. NASA input is not NASA approval of this tool or any model-generated output.",
			"inputSchema": json.RawMessage(`{"type":"object","properties":{"feed_id":{"type":"string","enum":["nasa_news_releases_api"]},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["feed_id","limit"],"additionalProperties":false}`),
			"annotations": map[string]bool{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true},
		}}})
	case "tools/call":
		feedID, limit, err := callArgs(req["params"])
		if err != nil {
			return failure(id, -32602, "only read_news_feed with nasa_news_releases_api and limit 1 to 10 is supported")
		}
		snapshot, err := s.fetch(ctx, feedID, limit)
		if err != nil || ctx.Err() != nil {
			return success(id, map[string]any{"content": []any{map[string]string{"type": "text", "text": "Public news capture did not complete. No retry was attempted."}}, "isError": true})
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return failure(id, -32603, "source result is not valid")
		}
		if _, err := newsfeed.ParseSnapshot(string(encoded)); err != nil || snapshot.FeedID != feedID || snapshot.Limit != limit {
			return failure(id, -32603, "source result does not match the requested capture")
		}
		return success(id, map[string]any{"content": []any{map[string]string{"type": "text", "text": string(encoded)}}, "isError": false})
	default:
		return failure(id, -32601, "method not supported")
	}
}

func validInitialize(raw []byte) bool {
	var params, capabilities, client map[string]json.RawMessage
	if json.Unmarshal(raw, &params) != nil || !closed(params, []string{"protocolVersion", "capabilities", "clientInfo"}) || string(params["protocolVersion"]) != `"2025-06-18"` ||
		json.Unmarshal(params["capabilities"], &capabilities) != nil || capabilities == nil ||
		json.Unmarshal(params["clientInfo"], &client) != nil || !closed(client, []string{"name", "version"}) {
		return false
	}
	for _, key := range []string{"name", "version"} {
		var value string
		if json.Unmarshal(client[key], &value) != nil || strings.TrimSpace(value) == "" || len(value) > 256 {
			return false
		}
	}
	return true
}

func callArgs(raw []byte) (string, int, error) {
	bad := errors.New("invalid news tool arguments")
	var params, arguments map[string]json.RawMessage
	if json.Unmarshal(raw, &params) != nil || !closed(params, []string{"name", "arguments"}) || string(params["name"]) != `"read_news_feed"` ||
		json.Unmarshal(params["arguments"], &arguments) != nil || !closed(arguments, []string{"feed_id", "limit"}) {
		return "", 0, bad
	}
	var feedID string
	var limit int
	if json.Unmarshal(arguments["feed_id"], &feedID) != nil || feedID != "nasa_news_releases_api" || json.Unmarshal(arguments["limit"], &limit) != nil || limit < 1 || limit > 10 {
		return "", 0, bad
	}
	return feedID, limit, nil
}

func emptyParams(raw []byte) bool {
	if len(raw) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil && fields != nil && len(fields) == 0
}

func closed(fields map[string]json.RawMessage, required []string, optional ...string) bool {
	if fields == nil {
		return false
	}
	for _, key := range required {
		if value, exists := fields[key]; !exists || string(value) == "null" {
			return false
		}
	}
	for key := range fields {
		found := false
		for _, allowed := range append(append([]string(nil), required...), optional...) {
			found = found || key == allowed
		}
		if !found {
			return false
		}
	}
	return true
}

func validID(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 128 || string(raw) == "null" {
		return false
	}
	if raw[0] == '"' {
		var value string
		return json.Unmarshal(raw, &value) == nil && value != ""
	}
	_, err := strconv.ParseInt(string(raw), 10, 64)
	return err == nil
}

func success(id json.RawMessage, result any) []byte {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil || len(body)+1 > maxResponseBytes {
		return failure(id, -32603, "source response exceeds its bound")
	}
	return body
}

func failure(id json.RawMessage, code int, message string) []byte {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}) // Fixed JSON-safe protocol fields.
	return body
}
