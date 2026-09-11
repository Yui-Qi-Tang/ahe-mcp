package ahemcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

const (
	maxRPCBytes    = 4 << 20
	maxSessionTime = 2 * time.Minute
	shutdownGrace  = 2 * time.Second
)

// client owns one sequential, closed-profile stdio session. The selected launcher
// is trusted operator configuration, not a command supplied by source content.
type client struct {
	command        *exec.Cmd
	ctx            context.Context
	cancel         context.CancelFunc
	stdin          io.WriteCloser
	stdout         *os.File
	scanner        *bufio.Scanner
	waited         chan struct{}
	waitErr        error
	stopIO         func() bool
	ioDone         chan struct{}
	nextID         int
	ready          bool
	queryOnly      bool
	queryBriefOnly bool
	reviewerOnly   bool
	closed         bool
	closeErr       error
}

func start(ctx context.Context, command string) (*client, error) {
	if !filepath.IsAbs(command) || filepath.Clean(command) != command || strings.ContainsAny(command, "\x00\r\n") {
		return nil, errors.New("ahe MCP requires one absolute executable launcher path")
	}
	info, err := os.Stat(command)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("ahe MCP launcher is not an executable regular file")
	}
	ctx, cancel := context.WithTimeout(ctx, maxSessionTime)
	cmd := exec.CommandContext(ctx, command)
	cmd.Env = launcherEnvironment(os.Environ())
	cmd.WaitDelay = shutdownGrace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, errors.New("opening ahe MCP input failed")
	}
	stdout, writer, err := os.Pipe()
	if err != nil {
		_ = stdin.Close() // No child exists; release the already-created pipe.
		cancel()
		return nil, errors.New("opening ahe MCP output failed")
	}
	cmd.Stdout = writer
	// A nil Stderr connects directly to the null device: no credentials, raw
	// server diagnostics, or unbounded stderr buffers reach the caller.
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = writer.Close()
		cancel()
		return nil, errors.New("starting ahe MCP launcher failed")
	}
	_ = writer.Close() // Only the child retains the output writer after Start.
	c := &client{command: cmd, ctx: ctx, cancel: cancel, stdin: stdin, stdout: stdout,
		waited: make(chan struct{}), ioDone: make(chan struct{})}
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 64<<10), maxRPCBytes)
	// Closing both pipes also interrupts a blocked read or write if a broken
	// launcher leaves descendants holding inherited descriptors.
	c.stopIO = context.AfterFunc(ctx, func() {
		_ = stdin.Close()
		_ = stdout.Close()
		close(c.ioDone)
	})
	go func() {
		c.waitErr = cmd.Wait()
		close(c.waited)
	}()
	return c, nil
}

func launcherEnvironment(inherited []string) []string {
	environment := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	seen := make(map[string]bool)
	for _, entry := range inherited {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || seen[key] {
			continue
		}
		switch key {
		case "HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "TERM":
			environment = append(environment, entry)
			seen[key] = true
		}
	}
	return environment
}

// Close signals EOF first, then waits for normal process exit. A timed-out or
// nonzero exit is not a successful handoff, even if the final RPC was received.
func (c *client) Close() error {
	if c == nil || c.closed {
		if c == nil {
			return nil
		}
		return c.closeErr
	}
	c.closed = true
	_ = c.stdin.Close() // An already-closed input is equivalent to EOF.
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case <-c.waited:
	case <-timer.C:
		c.closeErr = errors.New("ahe MCP did not exit after input EOF")
		c.cancel()
		<-c.waited
	}
	if c.ctx.Err() != nil && c.closeErr == nil {
		c.closeErr = c.ctx.Err()
	}
	if c.waitErr != nil && c.closeErr == nil {
		c.closeErr = errors.New("ahe MCP exited unsuccessfully")
	}
	if !c.stopIO() {
		<-c.ioDone
	}
	c.cancel()
	_ = c.stdout.Close() // No reader runs after the sequential caller closes.
	return c.closeErr
}

func (c *client) initialize() error {
	serverName := "ahe-ingest-mcp"
	if c.queryOnly {
		serverName = "ahe-query-mcp"
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := c.request("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "ahe-detective", "version": labstatus.ExtractorVersion},
	}, &initialized); err != nil {
		return err
	}
	if initialized.ProtocolVersion != protocolVersion || initialized.ServerInfo.Name != serverName ||
		strings.TrimSpace(initialized.ServerInfo.Version) == "" || initialized.Capabilities.Tools == nil || initialized.Capabilities.Tools.ListChanged {
		return c.fail(errors.New("ahe MCP endpoint identity or protocol mismatch"))
	}
	if err := c.write(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return err
	}
	var listed struct {
		Tools []struct {
			Name        string                     `json:"name"`
			Title       string                     `json:"title,omitempty"`
			Description string                     `json:"description"`
			InputSchema map[string]json.RawMessage `json:"inputSchema"`
			Annotations map[string]json.RawMessage `json:"annotations,omitempty"`
		} `json:"tools"`
	}
	if err := c.request("tools/list", nil, &listed); err != nil {
		return err
	}
	expected := map[string]bool{"submit_manual_evidence": true, "submit_text_source": true,
		"submit_external_source": true, "submit_extractor_output": true, "get_extractor_input": true}
	if c.queryOnly {
		expected = map[string]bool{
			"get_evidence_record": true, "list_evidence_records": true, "search_evidence_records": true,
			"get_grounded_evidence_brief": true, "list_evidence_neighbors": true, "get_relation_provenance": true,
			"get_mcp_read_source_states": true, "open_canonical_read_view": true, "find_canonical_path": true,
			"get_canonical_topology_diagnostics": true, "get_canonical_contradiction_proposal": true,
			"get_canonical_supersession_head": true, "get_canonical_supersession_currentness": true,
		}
	}
	if c.reviewerOnly {
		expected = map[string]bool{"get_source_claim_review": true, "admit_reviewed_source_claim": true, "record_reviewed_source_claim_disposition": true}
	}
	if len(listed.Tools) != len(expected) {
		return c.fail(errors.New("ahe MCP tool inventory does not match the selected endpoint"))
	}
	for _, tool := range listed.Tools {
		if !expected[tool.Name] || string(tool.InputSchema["type"]) != `"object"` {
			return c.fail(errors.New("ahe MCP tool inventory does not match the selected endpoint"))
		}
		if c.reviewerOnly && !validReviewerSchema(tool.Name, tool.InputSchema) {
			return c.fail(errors.New("ahe reviewer input schema does not match the exact review contract"))
		}
		if c.queryBriefOnly && tool.Name == "get_grounded_evidence_brief" && !supportsPracticalQuery(tool.InputSchema) {
			return c.fail(errors.New("ahe Query launcher requires practical_multisurface_lexical_v1 and grounded-evidence-brief-v7; upgrade the selected Query launcher, no fallback was used"))
		}
		delete(expected, tool.Name)
	}
	c.ready = true
	return nil
}

func (c *client) call(name string, arguments, output any) error {
	if !c.ready || c.closed {
		return errors.New("ahe MCP session is not ready")
	}
	if c.reviewerOnly {
		if name != "get_source_claim_review" && name != "admit_reviewed_source_claim" && name != "record_reviewed_source_claim_disposition" {
			return c.fail(errors.New("tool is outside the detective explicit reviewer workflow"))
		}
	} else if c.queryBriefOnly {
		if name != "get_grounded_evidence_brief" {
			return c.fail(errors.New("tool is outside the detective practical evidence search"))
		}
	} else if c.queryOnly {
		if name != "get_evidence_record" {
			return c.fail(errors.New("tool is outside the detective exact readback"))
		}
	} else {
		switch name {
		case "submit_text_source", "get_extractor_input", "submit_extractor_output":
		default:
			return c.fail(errors.New("tool is outside the detective pending-only handoff"))
		}
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
		IsError           bool            `json:"isError"`
	}
	if err := c.request("tools/call", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		return err
	}
	if result.IsError {
		return c.fail(errors.New("ahe MCP tool rejected the request"))
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		return c.fail(errors.New("ahe MCP tool returned an invalid content envelope"))
	}
	data := []byte(result.Content[0].Text)
	if len(result.StructuredContent) > 0 && !sameJSON(data, result.StructuredContent) {
		return c.fail(errors.New("ahe MCP tool content disagrees with structured content"))
	}
	// Intake/Query projections permit additive domain fields. Exact reviewer
	// responses remain closed; duplicate keys are rejected in every profile.
	if err := decodeRPCJSON(data, output, c.reviewerOnly); err != nil {
		return c.fail(errors.New("ahe MCP tool returned invalid result JSON"))
	}
	if c.reviewerOnly {
		encoded, err := json.Marshal(output)
		if err != nil || !sameJSON(data, encoded) {
			return c.fail(errors.New("ahe reviewer returned incomplete or noncanonical result fields"))
		}
	}
	return nil
}

func (c *client) request(method string, params, output any) error {
	c.nextID++
	if err := c.write(rpcRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params}); err != nil {
		return err
	}
	if !c.scanner.Scan() {
		if err := c.ctx.Err(); err != nil {
			return c.fail(err)
		}
		return c.fail(errors.New("ahe MCP output ended or exceeded the response bound"))
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if decodeRPCJSON(c.scanner.Bytes(), &response, true) != nil || response.JSONRPC != "2.0" ||
		string(response.ID) != strconv.Itoa(c.nextID) || (len(response.Result) == 0) == (len(response.Error) == 0) {
		return c.fail(errors.New("ahe MCP returned an invalid JSON-RPC response"))
	}
	if len(response.Error) != 0 {
		return c.fail(errors.New("ahe MCP rejected the RPC request"))
	}
	if bytes.Equal(bytes.TrimSpace(response.Result), []byte("null")) || decodeRPCJSON(response.Result, output, true) != nil {
		return c.fail(errors.New("ahe MCP returned an invalid RPC result"))
	}
	return nil
}

func (c *client) write(request rpcRequest) error {
	if c.closed {
		return errors.New("ahe MCP session is closed")
	}
	if err := c.ctx.Err(); err != nil {
		return c.fail(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded)+1 >= maxRPCBytes {
		return c.fail(errors.New("ahe MCP request cannot be encoded within its bound"))
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		if err := c.ctx.Err(); err != nil {
			return c.fail(err)
		}
		return c.fail(errors.New("writing ahe MCP request failed"))
	}
	return nil
}

func (c *client) fail(err error) error {
	c.ready = false
	c.cancel()
	_ = c.Close() // The primary protocol error is retained; Close still joins the child.
	return err
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func decodeRPCJSON(data []byte, output any, closed bool) error {
	if len(data) == 0 || len(data) >= maxRPCBytes || !utf8.Valid(data) {
		return errors.New("invalid JSON size")
	}
	check := json.NewDecoder(bytes.NewReader(data))
	check.UseNumber()
	if err := checkJSONValue(check, 0); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if closed {
		decoder.DisallowUnknownFields()
	}
	return decoder.Decode(output)
}

func checkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	keys := make(map[string]bool)
	for decoder.More() {
		if delimiter == '{' {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || keys[key] {
				return errors.New("invalid or duplicate JSON key")
			}
			keys[key] = true
		}
		if err := checkJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func sameJSON(a, b []byte) bool {
	var left, right any
	if decodeRPCJSON(a, &left, false) != nil || decodeRPCJSON(b, &right, false) != nil {
		return false
	}
	// JSON number spelling is preserved when comparing parallel representations.
	decode := func(data []byte, out *any) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		_ = decoder.Decode(out) // Both documents were already validated above.
	}
	decode(a, &left)
	decode(b, &right)
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
