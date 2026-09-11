package sourcemcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type session struct {
	ctx    context.Context
	cancel context.CancelFunc
	stdio  *stdioSession
	http   *httpSession
	nextID int
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func startSession(parent context.Context, c Config) (*session, error) {
	ctx, cancel := context.WithTimeout(parent, maxSessionTime)
	s := &session{ctx: ctx, cancel: cancel}
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, err
	}
	if c.Transport == "stdio" {
		child, err := startStdio(ctx, c.Command)
		if err != nil {
			cancel()
			return nil, err
		}
		s.stdio = child
	} else {
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16 << 10}
		s.http = &httpSession{endpoint: c.URL, transport: transport, client: &http.Client{
			Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("source MCP redirects are not permitted")
			},
		}}
	}
	return s, nil
}

func (s *session) close() error {
	defer s.cancel()
	if s.stdio != nil {
		return s.stdio.close(s.ctx, s.cancel)
	}
	return s.http.close()
}

func (s *session) notify(method string) error {
	_, err := s.exchange(rpcRequest{JSONRPC: "2.0", Method: method}, true)
	return err
}

func (s *session) request(method string, params any) ([]byte, error) {
	s.nextID++
	raw, err := s.exchange(rpcRequest{JSONRPC: "2.0", ID: s.nextID, Method: method, Params: params}, false)
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if decodeJSON(raw, &envelope) != nil || envelope == nil {
		return nil, errors.New("source MCP returned malformed JSON-RPC")
	}
	if string(envelope["method"]) == `"notifications/tools/list_changed"` {
		return nil, ErrInventoryChanged
	}
	for key := range envelope {
		if key != "jsonrpc" && key != "id" && key != "result" && key != "error" {
			return nil, errors.New("source MCP returned an unexpected message")
		}
	}
	result, hasResult := envelope["result"]
	_, hasError := envelope["error"]
	if string(envelope["jsonrpc"]) != `"2.0"` || string(envelope["id"]) != strconv.Itoa(s.nextID) || hasResult == hasError {
		return nil, errors.New("source MCP response identity mismatch")
	}
	if hasError {
		return nil, errors.New("source MCP rejected the RPC request")
	}
	if !isObject(result) {
		return nil, errors.New("source MCP returned an invalid result object")
	}
	return result, nil
}

func (s *session) exchange(request rpcRequest, notification bool) ([]byte, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxRPCBytes {
		return nil, errors.New("source MCP request exceeds its bound")
	}
	var data []byte
	if s.stdio != nil {
		data, err = s.stdio.exchange(encoded, notification)
	} else {
		data, err = s.http.exchange(s.ctx, encoded, notification, request.Method == "initialize")
	}
	if ctxErr := s.ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return data, err
}

type stdioSession struct {
	stdin   io.WriteCloser
	stdout  *os.File
	scanner *bufio.Scanner
	waited  chan error
	stopIO  func() bool
	ioDone  chan struct{}
}

func startStdio(ctx context.Context, command string) (*stdioSession, error) {
	cmd := exec.CommandContext(ctx, command)
	// Nothing is inherited from the application environment, including HOME,
	// proxy settings, API keys, database credentials, or dynamic loader flags.
	// An operator launcher can set the exact environment its source needs.
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "TZ=UTC"}
	cmd.WaitDelay = shutdownGrace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("opening source MCP input failed")
	}
	stdout, writer, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, errors.New("opening source MCP output failed")
	}
	cmd.Stdout = writer
	// Nil stderr goes directly to the null device, never to UI/logs or buffers.
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = writer.Close()
		return nil, errors.New("starting source MCP launcher failed")
	}
	_ = writer.Close() // Only the child's descriptor must remain open.
	s := &stdioSession{stdin: stdin, stdout: stdout, waited: make(chan error, 1), ioDone: make(chan struct{})}
	s.scanner = bufio.NewScanner(stdout)
	s.scanner.Buffer(make([]byte, 64<<10), maxRPCBytes+1)
	s.stopIO = context.AfterFunc(ctx, func() {
		_ = stdin.Close()
		_ = stdout.Close()
		close(s.ioDone)
	})
	go func() { s.waited <- cmd.Wait() }()
	return s, nil
}

func (s *stdioSession) exchange(data []byte, notification bool) ([]byte, error) {
	if _, err := s.stdin.Write(append(data, '\n')); err != nil {
		return nil, errors.New("writing source MCP request failed")
	}
	if notification {
		return nil, nil
	}
	if !s.scanner.Scan() || len(s.scanner.Bytes()) > maxRPCBytes {
		return nil, errors.New("source MCP output ended or exceeded its bound")
	}
	return append([]byte(nil), s.scanner.Bytes()...), nil
}

func (s *stdioSession) close(ctx context.Context, cancel context.CancelFunc) error {
	_ = s.stdin.Close() // EOF requests normal shutdown; closed input is equivalent.
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	var err error
	select {
	case exitErr := <-s.waited:
		if exitErr != nil {
			err = errors.New("source MCP launcher exited unsuccessfully")
		}
	case <-timer.C:
		err = errors.New("source MCP launcher did not exit after EOF")
		cancel()
		<-s.waited // Join the killed process before returning; no detached reader.
	}
	if !s.stopIO() {
		<-s.ioDone
	}
	_ = s.stdout.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type httpSession struct {
	endpoint  string
	client    *http.Client
	transport *http.Transport
	sessionID string
}

func (h *httpSession) request(ctx context.Context, method string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, h.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("creating source MCP HTTP request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if h.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", h.sessionID)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, errors.New("source MCP HTTP transport failed; no retry was attempted")
	}
	return resp, nil
}

func (h *httpSession) exchange(ctx context.Context, body []byte, notification, initialize bool) ([]byte, error) {
	resp, err := h.request(ctx, http.MethodPost, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxRPCBytes {
		return nil, errors.New("source MCP HTTP body exceeds its bound")
	}
	id := resp.Header.Get("Mcp-Session-Id")
	if id != "" {
		if len(id) > 256 {
			return nil, errors.New("source MCP session ID exceeds its bound")
		}
		for _, r := range id {
			if r < 0x21 || r > 0x7e {
				return nil, errors.New("source MCP session ID is invalid")
			}
		}
		if initialize && h.sessionID == "" {
			h.sessionID = id
		} else if id != h.sessionID {
			return nil, errors.New("source MCP session identity changed")
		}
	}
	if notification {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRPCBytes+1))
		if readErr != nil || resp.StatusCode != http.StatusAccepted || len(data) != 0 {
			return nil, errors.New("source MCP notification was not accepted")
		}
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("source MCP HTTP request was rejected; no retry was attempted")
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, errors.New("source MCP HTTP content type is invalid")
	}
	switch mediaType {
	case "application/json":
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCBytes+1))
		if err != nil || len(data) > maxRPCBytes {
			return nil, errors.New("source MCP HTTP body ended or exceeded its bound")
		}
		return data, nil
	case "text/event-stream":
		return readSSE(resp.Body)
	default:
		return nil, errors.New("source MCP HTTP content type is unsupported")
	}
}

func (h *httpSession) close() error {
	defer h.transport.CloseIdleConnections()
	if h.sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	resp, err := h.request(ctx, http.MethodDelete, nil)
	if err != nil {
		return errors.New("closing source MCP HTTP session failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusMethodNotAllowed {
		return errors.New("source MCP HTTP session close was rejected")
	}
	return nil
}

func readSSE(input io.Reader) ([]byte, error) {
	limited := &io.LimitedReader{R: input, N: maxRPCBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxRPCBytes+1)
	var data []string
	for scanner.Scan() {
		if limited.N <= 0 {
			return nil, errors.New("source MCP SSE exceeded its bound")
		}
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				return []byte(strings.Join(data, "\n")), nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			if value != "message" {
				return nil, errors.New("source MCP SSE event is unsupported")
			}
		case "id", "retry": // Deliberately do not resume or retry this bounded session.
		default:
			return nil, errors.New("source MCP SSE field is unsupported")
		}
	}
	return nil, errors.New("source MCP SSE ended without a complete response")
}
