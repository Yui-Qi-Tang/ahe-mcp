package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWorkspaceOwnershipRejectsSecondHolderWithoutChanges(t *testing.T) {
	first := newTestService(t)
	settings := twoSourceSettings(first)
	settings.SourceID = "synthetic-owner-one"
	settings.Model = "synthetic-model-one-not-called"
	if _, err := first.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	before := first.Snapshot()
	path := filepath.Join(before.DataDir, "settings.json")
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		second, err := New(before.DataDir)
		if second != nil {
			second.Close()
			t.Fatal("second holder entered the active workspace")
		}
		if !errors.Is(err, ErrWorkspaceInUse) {
			t.Fatalf("second holder error = %v, want ErrWorkspaceInUse", err)
		}
	}
	if !reflect.DeepEqual(first.Snapshot(), before) {
		t.Fatal("rejected second holder changed the first owner's snapshot")
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatalf("rejected second holder changed saved settings: %v", err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil || afterInfo.Mode() != beforeInfo.Mode() || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("rejected second holder replaced settings or changed permissions: %v", err)
	}
	entries, err := os.ReadDir(before.DataDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("workspace ownership created an additional file: %v", err)
	}
}

func TestWorkspaceOwnershipAllowsDifferentDirectoriesAndReopen(t *testing.T) {
	first := newTestService(t)
	second := newTestService(t)
	firstState, secondState := first.Snapshot(), second.Snapshot()
	if firstState.DataDir == secondState.DataDir || firstState.WorkspaceID == secondState.WorkspaceID {
		t.Fatal("test workspaces are not distinct")
	}
	first.Close()
	reopened, err := New(firstState.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if got := reopened.Snapshot(); got.WorkspaceID != firstState.WorkspaceID || got.Settings.Mode != "demo" {
		t.Fatal("reopening changed identity or enabled external operations")
	}
	if !reflect.DeepEqual(second.Snapshot(), secondState) {
		t.Fatal("closing another workspace changed the second owner's state")
	}
}

func TestWorkspaceOwnershipFailedInitializationReleasesLock(t *testing.T) {
	dir := serviceDirectory(t)
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{invalid synthetic settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := New(dir); err == nil || s != nil {
		if s != nil {
			s.Close()
		}
		t.Fatal("invalid settings were accepted")
	} else if errors.Is(err, ErrWorkspaceInUse) || strings.Contains(err.Error(), dir) {
		t.Fatal("initialization returned an unexpected or unbounded error")
	}
	body, err := json.Marshal(initialSettings())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("failed initialization leaked its workspace lock: %v", err)
	}
	t.Cleanup(s.Close)
}

func TestWorkspaceOwnershipCloseRetainsLockUntilOperationCompletes(t *testing.T) {
	s := newTestService(t)
	deadline, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	operation, done, err := s.begin(deadline, "ownership-test")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	closed := make(chan struct{})
	go func() {
		s.Close()
		close(closed)
	}()
	select {
	case <-operation.Done():
	case <-deadline.Done():
		t.Fatal("Close did not cancel the owned operation")
	}
	second, err := New(s.dataDir)
	if second != nil {
		second.Close()
		t.Fatal("workspace lock was released before the operation completed")
	}
	if !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("incomplete Close did not keep the workspace locked: %v", err)
	}
	done()
	select {
	case <-closed:
	case <-deadline.Done():
		t.Fatal("Close did not finish after operation completion")
	}
	reopened, err := New(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
}

func TestWorkspaceOwnershipSubprocess(t *testing.T) {
	if os.Getenv("DETECTIVE_WORKSPACE_LOCK_HELPER") == "1" {
		s, err := New(os.Getenv("DETECTIVE_WORKSPACE_LOCK_DIR"))
		if err != nil {
			t.Fatal(err)
		}
		// Keep both handles alive; exit deliberately skips Close so the parent
		// verifies that process exit, not application cleanup, releases the lock.
		defer s.Close()
		if _, err := fmt.Fprintln(os.Stdout, "workspace-ready"); err != nil {
			t.Fatal(err)
		}
		if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil || line != "exit\n" {
			t.Fatal("invalid subprocess handshake")
		}
		os.Exit(0)
	}
	for _, mode := range []string{"exit", "kill"} {
		t.Run(mode, func(t *testing.T) {
			dir := serviceDirectory(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestWorkspaceOwnershipSubprocess$")
			cmd.Env = append(os.Environ(), "DETECTIVE_WORKSPACE_LOCK_HELPER=1", "DETECTIVE_WORKSPACE_LOCK_DIR="+dir)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					cancel()
					_ = cmd.Wait()
				}
			}()
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "workspace-ready\n" {
				t.Fatalf("test subprocess did not acquire its workspace: %v", err)
			}
			if s, err := New(dir); s != nil || !errors.Is(err, ErrWorkspaceInUse) {
				if s != nil {
					s.Close()
				}
				t.Fatalf("another process did not exclude this owner: %v", err)
			}
			if mode == "exit" {
				if _, err := fmt.Fprintln(stdin, "exit"); err != nil {
					t.Fatal(err)
				}
			} else if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if mode == "exit" && err != nil {
				t.Fatalf("test subprocess failed to exit normally: %v", err)
			}
			if mode == "kill" && err == nil {
				t.Fatal("killed subprocess unexpectedly reported success")
			}
			if ctx.Err() != nil {
				t.Fatalf("test subprocess timed out: %v", ctx.Err())
			}
			s, err := New(dir)
			if err != nil {
				t.Fatalf("terminated subprocess left the workspace locked: %v", err)
			}
			t.Cleanup(s.Close)
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("subprocess ownership created files: %v", err)
			}
		})
	}
}

func TestWorkspaceDirectoryFlock(t *testing.T) {
	dir := serviceDirectory(t)
	first, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := unix.Flock(int(first.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("directory exclusive flock is unavailable: %v", err)
	}
	if err := unix.Flock(int(second.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("second independent directory descriptor was not blocked: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(second.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("closing the first holder did not release the directory lock: %v", err)
	}
}
