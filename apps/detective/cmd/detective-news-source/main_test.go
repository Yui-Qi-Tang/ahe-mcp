package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsfeed"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
const initializedNotification = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
const callRequest = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_news_feed","arguments":{"feed_id":"nasa_news_releases_api","limit":1}}}`

func newTestServer(t *testing.T, calls *int) *server {
	t.Helper()
	raw, err := os.ReadFile("../../internal/newsfeed/testdata/nasa_news_releases_api.json")
	if err != nil {
		t.Fatal(err)
	}
	return &server{fetch: func(_ context.Context, id string, limit int) (newsfeed.Snapshot, error) {
		*calls++
		if id != "nasa_news_releases_api" || limit != 1 {
			t.Fatal("tool rewrote source arguments")
		}
		return newsfeed.NewAPISnapshot(id, raw, limit, time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC))
	}}
}

func readyServer(t *testing.T, calls *int) *server {
	t.Helper()
	s := newTestServer(t, calls)
	response := s.reply(context.Background(), []byte(initializeRequest))
	if bytes.Contains(response, []byte(`"error"`)) || !bytes.Contains(response, []byte(`"version":"0.1.2"`)) || !s.initialized {
		t.Fatal("initialization failed")
	}
	if response := s.reply(context.Background(), []byte(initializedNotification)); response != nil || !s.ready {
		t.Fatal("initialized notification produced a response or did not complete handshake")
	}
	return s
}

func TestDiscoveryAndNotificationsNeverFetch(t *testing.T) {
	calls := 0
	s := readyServer(t, &calls)
	response := s.reply(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	if !json.Valid(response) || !bytes.Contains(response, []byte(`"read_news_feed"`)) || !bytes.Contains(response, []byte(`"required":["feed_id","limit"]`)) || !bytes.Contains(response, []byte(`"enum":["nasa_news_releases_api"]`)) || bytes.Contains(response, []byte("bbc_world")) {
		t.Fatal("tool discovery contract is incomplete")
	}
	for _, request := range []string{
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"read_news_feed","arguments":{"feed_id":"nasa_news_releases_api","limit":1}}}`,
		`{"jsonrpc":"2.0","method":"anything"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{"extra":1}}`,
	} {
		if response := s.reply(context.Background(), []byte(request)); response != nil {
			t.Fatal("notification produced a reply")
		}
	}
	if calls != 0 {
		t.Fatal("initialization, discovery, or notification fetched source")
	}
}

func TestToolCallReturnsOneReparsableTextBlock(t *testing.T) {
	calls := 0
	s := readyServer(t, &calls)
	response := s.reply(context.Background(), []byte(callRequest))
	var envelope struct {
		Result struct {
			Content []struct{ Type, Text string } `json:"content"`
			IsError bool                          `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal(response, &envelope) != nil || envelope.Result.IsError || len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Type != "text" || len(response)+1 > maxResponseBytes || calls != 1 {
		t.Fatal("capture result was not a single bounded text block from one fetch")
	}
	snapshot, err := newsfeed.ParseSnapshot(envelope.Result.Content[0].Text)
	if err != nil || snapshot.FeedID != "nasa_news_releases_api" || len(snapshot.Items) != 1 {
		t.Fatal("tool result lost exact snapshot validation")
	}
}

func TestInvalidOrPrematureCallsNeverFetch(t *testing.T) {
	for _, request := range []string{
		`{`, `{}`,
		strings.Replace(callRequest, `"id":3`, `"id":3,"id":4`, 1),
		strings.Replace(callRequest, `"limit":1`, `"limit":1,"limit":2`, 1),
		strings.Replace(callRequest, `"limit":1`, `"Limit":1`, 1),
		strings.Replace(callRequest, `"limit":1`, `"limit":0`, 1),
		strings.Replace(callRequest, `"limit":1`, `"limit":11`, 1),
		strings.Replace(callRequest, `"limit":1`, `"limit":1.5`, 1),
		strings.Replace(callRequest, `,"limit":1`, ``, 1),
		strings.Replace(callRequest, `"feed_id":"nasa_news_releases_api",`, ``, 1),
		strings.Replace(callRequest, `"nasa_news_releases_api"`, `"unknown"`, 1),
		strings.Replace(callRequest, `"nasa_news_releases_api"`, `"bbc_world"`, 1),
		strings.Replace(callRequest, `"nasa_news_releases_api"`, `"nasa_news_releases"`, 1),
		strings.Replace(callRequest, `"nasa_news_releases_api"`, `"\ud800"`, 1),
		strings.Replace(callRequest, `"limit":1`, `"limit":1,"url":"https://example.invalid"`, 1),
		strings.Replace(callRequest, `"read_news_feed"`, `"other"`, 1),
		strings.Replace(callRequest, `"name":`, `"Name":`, 1),
		strings.Replace(callRequest, `"arguments":`, `"extra":true,"arguments":`, 1),
		strings.Replace(callRequest, `"params":`, `"extra":true,"params":`, 1),
		strings.Replace(callRequest, `"id":3`, `"id":null`, 1),
		strings.Replace(callRequest, `"method":"tools/call"`, `"method":"unknown"`, 1),
	} {
		calls := 0
		s := readyServer(t, &calls)
		response := s.reply(context.Background(), []byte(request))
		if !bytes.Contains(response, []byte(`"error"`)) || calls != 0 {
			t.Fatal("invalid request reached fetch or did not return a protocol error")
		}
	}
	for _, initialized := range []bool{false, true} {
		calls := 0
		s := newTestServer(t, &calls)
		if initialized {
			s.reply(context.Background(), []byte(initializeRequest))
		}
		if response := s.reply(context.Background(), []byte(callRequest)); !bytes.Contains(response, []byte(`"error"`)) || calls != 0 {
			t.Fatal("pre-handshake call fetched a source")
		}
	}
}

func TestFetchFailureAndCancellationDoNotExposeDetailsOrRetry(t *testing.T) {
	for _, mode := range []string{"upstream", "cancelled", "forged", "mismatched"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			s := readyServer(t, &calls)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			good := s.fetch
			s.fetch = func(ctx context.Context, id string, limit int) (newsfeed.Snapshot, error) {
				snapshot, err := good(ctx, id, limit)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "upstream":
					return newsfeed.Snapshot{}, errors.New("synthetic-private-upstream-error")
				case "cancelled":
					cancel()
				case "forged":
					snapshot.Items[0].Title = "synthetic-private-forged"
				case "mismatched":
					snapshot.Limit = 2
				}
				return snapshot, nil
			}
			response := s.reply(ctx, []byte(callRequest))
			if calls != 1 || bytes.Contains(response, []byte("synthetic-private")) || (!bytes.Contains(response, []byte(`"error"`)) && !bytes.Contains(response, []byte(`"isError":true`))) {
				t.Fatal("failed capture was retried, leaked content, or returned success")
			}
		})
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestStdioBoundsAndOutputFailures(t *testing.T) {
	calls := 0
	s := newTestServer(t, &calls)
	input := strings.NewReader(initializeRequest + "\n" + initializedNotification + "\n" + callRequest + "\n")
	var output bytes.Buffer
	if err := s.stdio(context.Background(), input, &output); err != nil || calls != 1 || strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("stdio result failed: %v", err)
	}
	s = newTestServer(t, &calls)
	if err := s.stdio(context.Background(), strings.NewReader(initializeRequest+"\n"), shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short output write was not reported")
	}
	s = newTestServer(t, &calls)
	if err := s.stdio(context.Background(), strings.NewReader(strings.Repeat("x", 16<<10)+"\n"), io.Discard); err == nil || calls != 1 {
		t.Fatal("oversized input reached fetch")
	}
	bounded := success(json.RawMessage("1"), strings.Repeat("x", maxResponseBytes))
	if len(bounded)+1 > maxResponseBytes || !bytes.Contains(bounded, []byte(`"error"`)) {
		t.Fatal("oversized response reached output")
	}
}

func TestExistingSourceClientInteroperatesWithNASAFixture(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "news-source-launcher")
	wirePath := filepath.Join(dir, "synthetic-wire.jsonl")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := fmt.Sprintf("#!/bin/sh\nexec %s -test.run=TestNewsSourceMCPHelperProcess -- news-source-mcp-fixture %s\n", quote(executable), quote(wirePath))
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := sourcemcp.Config{ID: "news-test", Name: "Synthetic interoperability test", Transport: "stdio", Command: launcher, AllowedTools: []string{"read_news_feed"}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tools, err := sourcemcp.Discover(ctx, config)
	if err != nil || len(tools) != 1 || tools[0].Name != "read_news_feed" {
		t.Fatalf("existing source client could not discover the real stdio server: %v", err)
	}
	result, err := sourcemcp.Call(ctx, config, tools[0], `{"feed_id":"bbc_world","limit":1}`)
	if err == nil || result != (sourcemcp.Result{}) {
		t.Fatal("disabled BBC source produced a successful capture")
	}
	result, err = sourcemcp.Call(ctx, config, tools[0], `{"feed_id":"nasa_news_releases_api","limit":1}`)
	if err != nil {
		t.Fatalf("existing source client rejected the synthetic NASA result: %v", err)
	}
	snapshot, err := newsfeed.ParseSnapshot(result.Text)
	if err != nil || snapshot.FeedID != "nasa_news_releases_api" || snapshot.Items[0].Title != "Synthetic mission briefing scheduled" {
		t.Fatalf("NASA fixture was not captured through the real client: %v", err)
	}
	wire, err := os.ReadFile(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	// The helper runs the real server protocol with synthetic input, not Fetch.
	// Invalid BBC arguments must not start another session or source operation.
	replies, captures := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(wire)), "\n") {
		replies++
		var response struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool                          `json:"isError"`
				Content []struct{ Type, Text string } `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &response) != nil || len(response.Error) != 0 {
			t.Fatal("real client handshake received a protocol error")
		}
		if response.Result.IsError {
			t.Fatal("synthetic NASA capture failed")
		}
		if len(response.Result.Content) != 0 {
			captures++
			if len(response.Result.Content) != 1 || response.Result.Content[0].Type != "text" || response.Result.Content[0].Text != result.Text {
				t.Fatal("server did not preserve its single snapshot text block")
			}
		}
	}
	if replies != 5 || captures != 1 {
		t.Fatal("unexpected requests/retries or extra source captures")
	}
}

func TestNewsSourceMCPHelperProcess(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "news-source-mcp-fixture" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	if marker+2 != len(os.Args) {
		os.Exit(20)
	}
	wire, err := os.OpenFile(os.Args[marker+1], os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(21)
	}
	// This test process runs the real server against a synthetic fixture; it
	// must never call the newly enabled public NASA Fetch during regression.
	calls := 0
	s := newTestServer(t, &calls)
	err = s.stdio(context.Background(), os.Stdin, io.MultiWriter(os.Stdout, wire))
	closeErr := wire.Close()
	if err != nil || closeErr != nil {
		os.Exit(22)
	}
	os.Exit(0) // Never append the Go test runner's PASS text to MCP stdout.
}
