package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
)

// This opt-in checks the built native executable, not a second Go test process.
// Every invocation must exit before starting Wails; a regression is terminated
// after ten seconds and may briefly create a window in its private workspace.
func TestNativeDesktopStartup(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native desktop startup checks require macOS")
	}
	executable, enabled := os.LookupEnv("DETECTIVE_DESKTOP_TEST_BINARY")
	if !enabled {
		t.Skip("set DETECTIVE_DESKTOP_TEST_BINARY to the built native executable")
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		t.Fatal("DETECTIVE_DESKTOP_TEST_BINARY must be a clean absolute executable path")
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("DETECTIVE_DESKTOP_TEST_BINARY must name an existing executable regular file")
	}

	// Refuse a stale artifact before exercising either failure path.
	if !t.Run("version", func(t *testing.T) {
		dir := nativeStartupDirectory(t)
		stdout, stderr, err := runNativeStartup(t, executable, dir, "-version")
		if err != nil || stdout != desktop.Version+"\n" || stderr != "" {
			t.Fatalf("native version mismatch: err=%v stdout=%q stderr=%q", err, stdout, stderr)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("version check changed its explicit workspace: entries=%v err=%v", entries, err)
		}
	}) {
		return
	}

	t.Run("occupied_workspace", func(t *testing.T) {
		dir := nativeStartupDirectory(t)
		owner, err := desktop.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(owner.Close)
		settings := owner.Snapshot().Settings
		settings.SourceID = "synthetic-native-startup"
		settings.Model = "synthetic-model-not-called"
		if _, err := owner.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		before := owner.Snapshot()
		if before.Settings.Mode != "demo" || len(before.Settings.Connections) != 0 || before.Settings.IntakeLauncher != "" || before.Settings.QueryLauncher != "" {
			t.Fatal("native startup fixture must remain offline without launchers")
		}
		settingsPath := filepath.Join(dir, "settings.json")
		beforeBytes, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		beforeInfo, err := os.Lstat(settingsPath)
		if err != nil || beforeInfo.Mode().Perm() != 0o600 {
			t.Fatalf("fixture settings are not private: %v", err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
			t.Fatalf("unexpected initial workspace entries: %v, %v", entries, err)
		}

		stdout, stderr, err := runNativeStartup(t, executable, dir)
		assertNativeStartupRejected(t, stdout, stderr, err, startupMessage(desktop.ErrWorkspaceInUse))
		if !reflect.DeepEqual(owner.Snapshot(), before) {
			t.Error("rejected native process changed the first owner's snapshot")
		}
		afterBytes, err := os.ReadFile(settingsPath)
		if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
			t.Errorf("rejected native process changed saved settings bytes: %v", err)
		}
		afterInfo, err := os.Lstat(settingsPath)
		if err != nil || afterInfo.Mode() != beforeInfo.Mode() || !os.SameFile(beforeInfo, afterInfo) {
			t.Errorf("rejected native process replaced settings or changed permissions: %v", err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
			t.Errorf("rejected native process changed workspace entries: %v, %v", entries, err)
		}
	})

	t.Run("invalid_directory_permissions", func(t *testing.T) {
		dir := nativeStartupDirectory(t)
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		stdout, stderr, err := runNativeStartup(t, executable, dir)
		assertNativeStartupRejected(t, stdout, stderr, err, startupMessage(errors.New("synthetic invalid directory")))
		after, err := os.Lstat(dir)
		if err != nil || after.Mode() != before.Mode() || !os.SameFile(before, after) {
			t.Errorf("rejected native process replaced the invalid directory or changed its permissions: %v", err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("rejected native process created files in the invalid directory: %v, %v", entries, err)
		}
	})
}

func nativeStartupDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runNativeStartup(t *testing.T, executable, dir string, extraArgs ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// This gate verifies process rejection without a GUI. Native alert appearance
	// is a separate manual check, not something to dismiss through automation here.
	args := append([]string{"-startup-dialog=false", "-data-dir", dir}, extraArgs...)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	// Do not forward credentials, model settings, or profile-related environment.
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("native startup failed to exit before its deadline: %v", ctx.Err())
	}
	return stdout.String(), stderr.String(), err
}

func assertNativeStartupRejected(t *testing.T, stdout, stderr string, err error, message string) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("native startup rejection exit = %v, want exit 1", err)
	}
	if stdout != "" || stderr != message+"\n" {
		t.Errorf("unexpected native startup diagnostic: stdout=%q stderr=%q", stdout, stderr)
	}
}
