// Package toolrun contains the process and artifact operations shared by the
// concrete backends. Adapted from Pouch; see ../../NOTICE.
package toolrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

func readTool(path, expected string) ([]byte, error) {
	hash, err := hex.DecodeString(expected)
	if err != nil || len(hash) != 32 || expected != hex.EncodeToString(hash) {
		return nil, fmt.Errorf("lowercase sha256 is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("executable unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil, fmt.Errorf("not an executable regular file")
	}
	if info.Size() > 64<<20 {
		return nil, fmt.Errorf("executable exceeds size limit")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(raw) > 64<<20 {
		return nil, fmt.Errorf("reading executable")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expected {
		return nil, fmt.Errorf("executable hash mismatch")
	}
	return raw, nil
}

// Execution records the process contract and termination cause.
type Execution struct {
	ExitCode   int      `json:"exit_code"`
	TimedOut   bool     `json:"timed_out"`
	Canceled   bool     `json:"canceled"`
	DurationMS int64    `json:"duration_ms"`
	Arguments  []string `json:"arguments"`
}

// Execute runs one process using the caller deadline and bounded output.
func Execute(ctx context.Context, dir, stem, executable string, maxOutput int64, args ...string) (Execution, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	record := Execution{ExitCode: -1, Arguments: append([]string{filepath.Base(executable)}, args...)}
	stdout, err := os.OpenFile(filepath.Join(dir, stem+".stdout"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return record, fmt.Errorf("creating tool output")
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(dir, stem+".stderr"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return record, fmt.Errorf("creating tool error output")
	}
	defer stderr.Close()
	out := &limitedWriter{w: stdout, remaining: maxOutput, cancel: cancel}
	errOut := &limitedWriter{w: stderr, remaining: maxOutput, cancel: cancel}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = errOut
	controlProcess(cmd)
	start := time.Now()
	runErr := cmd.Run()
	closeErr := errors.Join(stdout.Close(), stderr.Close())
	record.DurationMS = time.Since(start).Milliseconds()
	if cmd.ProcessState != nil {
		record.ExitCode = cmd.ProcessState.ExitCode()
	}
	record.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	record.Canceled = errors.Is(ctx.Err(), context.Canceled)
	if err := WriteJSON(filepath.Join(dir, stem+".json"), record); err != nil {
		return record, err
	}
	if closeErr != nil {
		return record, closeErr
	}
	if out.exceeded || errOut.exceeded {
		return record, fmt.Errorf("%w: tool output", logicresolver.ErrLimit)
	}
	if ctx.Err() != nil {
		return record, ctx.Err()
	}
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			return record, fmt.Errorf("tool failed to execute: %w", runErr)
		}
	}
	return record, nil
}

type limitedWriter struct {
	w         io.Writer
	remaining int64
	exceeded  bool
	cancel    context.CancelFunc
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		w.exceeded = true
		w.cancel()
		return 0, fmt.Errorf("tool output limit")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// WriteJSON preserves a record without overwriting existing artifacts.
func WriteJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding evidence: %w", err)
	}
	raw = append(raw, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("creating evidence file")
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if errors.Join(writeErr, closeErr) != nil {
		return fmt.Errorf("writing evidence file")
	}
	return nil
}

// Inventory hashes each regular file within a query directory.
func Inventory(dir, id string) ([]logicresolver.Artifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing evidence files")
	}
	artifacts := make([]logicresolver.Artifact, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return artifacts, fmt.Errorf("unexpected nonregular evidence")
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return artifacts, fmt.Errorf("opening evidence")
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if errors.Join(copyErr, closeErr) != nil {
			return artifacts, fmt.Errorf("hashing evidence")
		}
		artifacts = append(artifacts, logicresolver.Artifact{ID: id + "/" + entry.Name(), SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: n})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	return artifacts, nil
}
