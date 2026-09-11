package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStartupAlertCommandKeepsMessageOutOfScript(t *testing.T) {
	t.Setenv("DETECTIVE_SYNTHETIC_SECRET", "must-not-be-forwarded")
	message := "synthetic message \"\n do shell script \"must-not-execute\"\n 臺灣測試"
	cmd := startupAlertCommand(t.Context(), message)
	if cmd.Path != "/usr/bin/osascript" || !reflect.DeepEqual(cmd.Args, []string{"/usr/bin/osascript", "-", message}) {
		t.Fatalf("unexpected startup alert command: path=%q args=%q", cmd.Path, cmd.Args)
	}
	script, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(script) != startupAlertScript || strings.Contains(string(script), message) || strings.Contains(string(script), "must-not-execute") {
		t.Fatalf("message changed the fixed startup alert script: %q", script)
	}
	if !reflect.DeepEqual(cmd.Environ(), []string{"PATH=/usr/bin:/bin"}) {
		t.Fatal("startup alert inherited process environment")
	}
	if cmd.Stdout != io.Discard || cmd.Stderr != io.Discard {
		t.Fatal("startup alert output must be discarded")
	}
	if cmd.WaitDelay != time.Second || startupAlertTimeout != 35*time.Second {
		t.Fatal("startup alert time bounds changed")
	}
}

func TestStartupAlertCommandDoesNotStartAfterCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{name: "canceled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx, cancel
		}, want: context.Canceled},
		{name: "deadline exceeded", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		}, want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			cmd := startupAlertCommand(ctx, "synthetic notice that must not be displayed")
			if err := cmd.Run(); !errors.Is(err, test.want) {
				t.Fatalf("startup alert returned %v, want %v", err, test.want)
			}
			if cmd.Process != nil {
				t.Fatal("startup alert started despite a completed context")
			}
		})
	}
}

func TestStartupAlertScriptCompilesWithoutExecuting(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	compiled := filepath.Join(t.TempDir(), "startup-alert.scpt")
	// osacompile only compiles this fixed source; it never executes the alert.
	cmd := exec.CommandContext(ctx, "/usr/bin/osacompile", "-o", compiled)
	cmd.Stdin = strings.NewReader(startupAlertScript)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile fixed startup alert: %v; %s", err, output)
	}
	if info, err := os.Stat(compiled); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("startup alert compiled artifact is missing or empty: %v", err)
	}
}
