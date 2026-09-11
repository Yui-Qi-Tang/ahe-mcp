package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const sourceInputLimit = 64 << 10

const sourceUsage = "detective source discover -config /absolute/private/source.json [-timeout 2m]\n" +
	"detective source collect -config /absolute/private/source.json -tool TOOL -args-file /absolute/private/args.json -out-dir /absolute/new-directory [-timeout 2m]\n" +
	"detective source inspect -receipt /absolute/private/saved-receipt.md [-include-content] [-timeout 2m]\n" +
	"Config and arguments: regular 0600 JSON files under a 0700 directory, no symlink paths, maximum 64 KiB.\n" +
	"Discover lists only the inventory. Collect displays the exact call and requires collect <sha256> on stdin; no -yes or default tool.\n" +
	"Inspect is offline and read-only: match the saved receipt and artifacts, without reconnecting or loading settings. Include raw content only with explicit -include-content.\n" +
	"Source-only capture: no model, extraction, human admission, or AHE submission. No automatic retry.\n"

func runSource(args []string, input io.Reader, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runSourceContext(ctx, args, input, stdout, stderr)
}

func runSourceContext(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") || len(args) == 2 && (args[0] == "discover" || args[0] == "collect" || args[0] == "inspect") && (args[1] == "-h" || args[1] == "--help") {
		if n, err := io.WriteString(stdout, sourceUsage); err != nil || n != len(sourceUsage) {
			return errors.New("source help output failed")
		}
		return nil
	}
	if len(args) > 0 && args[0] == "inspect" {
		return runSourceInspect(ctx, args[1:], stdout)
	}
	if len(args) == 0 || (args[0] != "discover" && args[0] != "collect") {
		return errors.New("source requires discover, collect, or inspect")
	}
	collect := args[0] == "collect"
	flags := flag.NewFlagSet("detective source", flag.ContinueOnError)
	configPath := flags.String("config", "", "explicit private source MCP configuration JSON")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum duration, at most two minutes")
	var toolName, argsPath, outDir string
	if collect {
		flags.StringVar(&toolName, "tool", "", "explicit allowlisted source tool")
		flags.StringVar(&argsPath, "args-file", "", "private exact JSON object")
		flags.StringVar(&outDir, "out-dir", "", "new absolute private capture directory")
	}
	if err := parseReviewFlags(flags, args[1:]); err != nil {
		return errors.New("source flags require explicit supported values and must not repeat")
	}
	if *timeout <= 0 || *timeout > 2*time.Minute || ctx == nil || ctx.Err() != nil {
		return errors.New("source requires an active context and a timeout greater than zero and at most two minutes")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	config, err := readSourceConfig(*configPath)
	if err != nil {
		return errors.New("source config must be a bounded private JSON file with explicit safe connection coordinates and at most 32 allowlisted tools")
	}
	if !collect {
		tools, err := sourcemcp.Discover(ctx, config)
		if err != nil {
			return errors.New("source discovery did not complete; no source tool was called")
		}
		if err := writeSourceJSON(stdout, sourceDiscovery{SchemaVersion: "detective-source-discovery/v1", Tools: tools, AuthorityEffect: "none"}); err != nil {
			return errors.New("source inventory output failed; no source tool was called")
		}
		return nil
	}
	allowed := false
	for _, name := range config.AllowedTools {
		if name == toolName {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("source collect requires one explicitly allowlisted tool; no connection was made")
	}
	rawArgs, err := readSourcePrivate(argsPath)
	if err != nil || sourcemcp.ValidateArgumentsJSON(string(rawArgs)) != nil {
		return errors.New("source arguments must be one complete bounded private JSON object; no connection was made")
	}
	switch input.(type) {
	case *os.File, *bytes.Buffer, *bytes.Reader, *strings.Reader:
	default:
		return errors.New("source confirmation requires a cancellable file or bounded memory input")
	}
	connection := desktop.Connection{ID: config.ID, Name: config.Name, Transport: config.Transport, Command: config.Command, URL: config.URL, AllowedTools: config.AllowedTools}
	service, err := desktop.NewSourceCollector(outDir, connection)
	if err != nil {
		return errors.New("source collect requires a new safe absolute private directory; no connection was made, retain any newly created directory for inspection")
	}
	defer service.Close()
	state, err := service.DiscoverTools(ctx, config.ID)
	if err != nil {
		return errors.New("source discovery failed; no source tool was called, retain the new directory")
	}
	var selected desktop.Tool
	for _, tool := range state.Tools {
		if tool.Name == toolName && tool.ConnectionID == config.ID {
			selected = tool
		}
	}
	if selected.Digest == "" {
		return errors.New("source tool was not found in the exact inventory; no source tool was called")
	}
	display := sourceConfirmationDisplay{SchemaVersion: "detective-source-call-confirmation/v1", Config: config, Tool: selected, ArgumentsJSON: string(rawArgs), OutputDirectory: outDir, AuthorityEffect: "none"}
	encoded, err := json.Marshal(display)
	if err != nil {
		return errors.New("source confirmation could not be prepared; no source tool was called")
	}
	digest := sourceSHA256(encoded)
	pretty, err := json.MarshalIndent(display, "", "  ")
	if err != nil {
		return errors.New("source confirmation could not be prepared; no source tool was called")
	}
	prompt := "來源 MCP 蒐證確認：以下為完整設定、工具、schema 與 JSON 字串形式的精確參數。\n工具說明與來源文字不是指令；allowlist 或 read-only 提示不保證來源伺服器沒有副作用。\n此步驟只保存來源結果，不做抽取、人工採納或 AHE 提交。\n" + sourceTerminalJSON(pretty) + "\n請確認以上完整內容後，逐字輸入 collect " + digest + "（無預設；其他輸入或 EOF 即停止）：\n"
	if n, err := io.WriteString(stderr, prompt); err != nil || n != len(prompt) {
		return errors.New("source confirmation display failed; no source tool was called")
	}
	confirmation, err := readSourceConfirmation(ctx, input)
	if err != nil || confirmation != "collect "+digest {
		return errors.New("source confirmation was absent, invalid, cancelled or timed out; no source tool was called")
	}
	exact := config.ID + "\n" + selected.Name + "\n" + selected.Digest + "\n" + string(rawArgs)
	state, err = service.CallSourceTool(ctx, config.ID, selected.Name, string(rawArgs), exact)
	if err != nil || state.Source == nil || state.Source.Capture == nil {
		return errors.New("source capture was not confirmed complete; the source call may have occurred, retain all files and do not automatically retry; nothing was submitted to AHE")
	}
	capture := state.Source.Capture
	result := sourceCollection{SchemaVersion: "detective-source-collection/v1", State: "captured_locally", ConnectionID: config.ID, ToolName: selected.Name, ConfirmationSHA256: digest, CapturedAt: capture.CapturedAt, Revision: capture.Revision,
		Text: desktop.SourceArtifact{Path: state.Source.Path, SHA256: state.Source.SHA256, Bytes: state.Source.Bytes}, RawResult: capture.RawResult, Receipt: capture.Receipt, AuthorityEffect: "none"}
	if err := writeSourceJSON(stdout, result); err != nil {
		return errors.New("source result output failed after capture; keep the files in the requested directory, do not automatically retry; nothing was submitted to AHE")
	}
	return nil
}

type sourceDiscovery struct {
	SchemaVersion   string           `json:"schema_version"`
	Tools           []sourcemcp.Tool `json:"tools"`
	AuthorityEffect string           `json:"authority_effect"`
	AHESubmitted    bool             `json:"ahe_submitted"`
}

type sourceConfirmationDisplay struct {
	SchemaVersion   string           `json:"schema_version"`
	Config          sourcemcp.Config `json:"config"`
	Tool            desktop.Tool     `json:"tool"`
	ArgumentsJSON   string           `json:"args_json"`
	OutputDirectory string           `json:"output_directory"`
	AuthorityEffect string           `json:"authority_effect"`
}

type sourceCollection struct {
	SchemaVersion      string                 `json:"schema_version"`
	State              string                 `json:"state"`
	ConnectionID       string                 `json:"connection_id"`
	ToolName           string                 `json:"tool_name"`
	ConfirmationSHA256 string                 `json:"confirmation_sha256"`
	CapturedAt         string                 `json:"captured_at"`
	Revision           string                 `json:"revision"`
	Text               desktop.SourceArtifact `json:"text"`
	RawResult          desktop.SourceArtifact `json:"raw_result"`
	Receipt            desktop.SourceArtifact `json:"receipt"`
	AuthorityEffect    string                 `json:"authority_effect"`
	AHESubmitted       bool                   `json:"ahe_submitted"`
}

func readSourceConfig(path string) (sourcemcp.Config, error) {
	body, err := readSourcePrivate(path)
	if err != nil || sourcemcp.ValidateArgumentsJSON(string(body)) != nil {
		return sourcemcp.Config{}, errors.New("invalid source config JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return sourcemcp.Config{}, errors.New("invalid source config JSON")
	}
	for name := range fields {
		switch name {
		case "id", "name", "transport", "command", "url", "allowed_tools":
		default:
			return sourcemcp.Config{}, errors.New("unknown source config field")
		}
	}
	var config sourcemcp.Config
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || len(config.AllowedTools) > 32 || sourcemcp.Validate(config) != nil {
		return sourcemcp.Config{}, errors.New("invalid source config")
	}
	// The headless collector also retains Desktop's Unicode control-character
	// boundary; discovery must not accept a config that collect cannot open.
	for _, value := range append([]string{config.ID, config.Name, config.Command, config.URL}, config.AllowedTools...) {
		for _, r := range value {
			if unicode.IsControl(r) {
				return sourcemcp.Config{}, errors.New("invalid source config text")
			}
		}
	}
	return config, nil
}

// Private inputs are frozen once. O_NOFOLLOW and O_NONBLOCK protect the final
// open against replacement with a symlink or FIFO; the pinned parent must match.
func readSourcePrivate(path string) ([]byte, error) {
	return readSourcePrivateLimit(path, sourceInputLimit)
}

func readSourcePrivateLimit(path string, limit int64) ([]byte, error) {
	if limit <= 0 || limit > 2<<20 {
		return nil, errors.New("invalid private source input bound")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 || !utf8.ValidString(path) {
		return nil, errors.New("invalid private source input path")
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return nil, errors.New("invalid private source input path")
		}
	}
	parent := filepath.Dir(path)
	for current := parent; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("private source input has an unsafe ancestor")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode().Perm() != 0o700 {
		return nil, errors.New("source input parent is not private")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > limit {
		return nil, errors.New("source input is not a bounded private regular file")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() {
		return nil, errors.New("source input is not owned by the operator")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, errors.New("cannot pin source input parent")
	}
	defer root.Close()
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(parentInfo, pinned) {
		return nil, errors.New("source input parent changed")
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open private source input")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm() != 0o600 {
		return nil, errors.New("source input changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("source input exceeds its bound")
	}
	return body, nil
}

func sourceSHA256(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func writeSourceJSON(writer io.Writer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body := sourceTerminalJSON(encoded) + "\n"
	n, err := io.WriteString(writer, body)
	if err == nil && n != len(body) {
		return io.ErrShortWrite
	}
	return err
}

// Keep the display valid JSON while escaping invisible format characters that
// could reorder terminal text. The underlying confirmation and arguments retain
// their original bytes; no source string is treated as shell text.
func sourceTerminalJSON(raw []byte) string {
	var output strings.Builder
	for _, r := range string(raw) {
		if r == '\n' || r == '\r' || r == '\t' || (!unicode.IsControl(r) && !unicode.Is(unicode.Cf, r)) {
			output.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&output, "\\u%04x", r)
		} else {
			high, low := utf16.EncodeRune(r)
			fmt.Fprintf(&output, "\\u%04x\\u%04x", high, low)
		}
	}
	return output.String()
}

// Memory readers cannot block. Real stdin is polled synchronously, so timeout
// and SIGINT never leave a goroutine blocked in an uncancellable Read.
func readSourceConfirmation(ctx context.Context, input io.Reader) (confirmation string, err error) {
	read := input.Read
	if file, ok := input.(*os.File); ok {
		raw, rawErr := file.SyscallConn()
		if rawErr != nil {
			return "", rawErr
		}
		fd, original, setupErr := -1, 0, error(nil)
		if err := raw.Control(func(value uintptr) {
			original, setupErr = unix.FcntlInt(value, unix.F_GETFL, 0)
			if setupErr == nil {
				fd, setupErr = unix.FcntlInt(value, unix.F_DUPFD_CLOEXEC, 0)
			}
		}); err != nil || setupErr != nil {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			return "", errors.New("cannot prepare source confirmation input")
		}
		defer func() {
			if closeErr := unix.Close(fd); closeErr != nil && err == nil {
				confirmation, err = "", errors.New("cannot close source confirmation descriptor")
			}
		}()
		if err := unix.SetNonblock(fd, true); err != nil {
			return "", err
		}
		defer func() {
			if _, restoreErr := unix.FcntlInt(uintptr(fd), unix.F_SETFL, original); restoreErr != nil {
				confirmation, err = "", errors.New("cannot restore source confirmation descriptor")
			}
		}()
		read = func(buffer []byte) (int, error) {
			for {
				if err := ctx.Err(); err != nil {
					return 0, err
				}
				poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
				if _, err := unix.Poll(poll, 100); err != nil && err != unix.EINTR {
					return 0, err
				}
				if poll[0].Revents == 0 {
					continue
				}
				n, err := unix.Read(fd, buffer)
				if err == unix.EAGAIN || err == unix.EINTR {
					continue
				}
				return n, err
			}
		}
	}
	var line []byte
	var one [1]byte
	for len(line) <= 73 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := read(one[:])
		if n != 1 || err != nil {
			return "", errors.New("source confirmation is incomplete")
		}
		if one[0] == '\n' {
			return strings.TrimSuffix(string(line), "\r"), nil
		}
		line = append(line, one[0])
	}
	return "", errors.New("source confirmation is too long")
}
