package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type trialLauncherFixture struct {
	script, executable, caller, log, probeDir string
	markers                                   []string
}

func newTrialLauncherFixture(t *testing.T, exitCode int) trialLauncherFixture {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("native trial launcher uses the macOS /private/tmp layout")
	}
	script, err := os.ReadFile("../../scripts/desktop-trial.sh")
	if err != nil {
		t.Fatal(err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "fake repository with spaces")
	f := trialLauncherFixture{
		executable: filepath.Join(repo, "build/bin/AHE Detective.app/Contents/MacOS/AHE Detective"),
		caller:     filepath.Join(base, "unrelated working directory"),
		log:        filepath.Join(base, "mktemp-calls"),
		probeDir:   filepath.Join(base, "probe commands"),
		markers:    []string{filepath.Join(repo, "settings.json"), filepath.Join(base, "unrelated working directory", "existing-source.md")},
	}
	for _, dir := range []string{filepath.Join(repo, "scripts"), filepath.Dir(f.executable), f.caller, f.probeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTrialFixtureFile(t, filepath.Join(repo, "scripts/desktop-trial.sh"), string(script), 0o600)
	// Resolve through a symlinked parent and a different cwd, as a caller may do.
	link := filepath.Join(base, "repository link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	f.script = filepath.Join(link, "scripts/desktop-trial.sh")
	for _, marker := range f.markers {
		writeTrialFixtureFile(t, marker, "existing private content", 0o600)
	}
	writeTrialFixtureFile(t, filepath.Join(f.probeDir, "mktemp"), `#!/bin/sh
set -eu
printf 'called\n' >> "$DETECTIVE_TRIAL_TEST_LOG"
[ "$#" -eq 2 ] && [ "$1" = '-d' ] && [ "$2" = '/private/tmp/detective-desktop-trial.XXXXXX' ] || exit 91
created=$(/usr/bin/mktemp "$@")
printf '%s\n' "$created" >> "$DETECTIVE_TRIAL_TEST_LOG"
printf '%s\n' "$created"
`, 0o700)
	writeTrialFixtureFile(t, f.executable, fmt.Sprintf(`#!/bin/sh
set -eu
printf 'fake-executable:%%s\n' "$0"
printf 'fake-argc:%%s\n' "$#"
printf 'fake-arg:%%s\n' "$@"
printf 'fake-cwd:%%s\n' "$(pwd -P)"
[ "$#" -eq 2 ] && [ "$1" = '-data-dir' ] || exit 92
for entry in "$2"/* "$2"/.[!.]* "$2"/..?*; do
    [ ! -e "$entry" ] && [ ! -L "$entry" ] || exit 93
done
printf 'fake-empty:yes\n'
printf 'trial child marker' > "$2/child-marker"
exit %d
`, exitCode), 0o700)
	return f
}

func writeTrialFixtureFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (f trialLauncherFixture) run(t *testing.T, args ...string) (string, error, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{f.script}, args...)...)
	cmd.Dir = f.caller
	cmd.Env = append(os.Environ(), "PATH="+f.probeDir+":/usr/bin:/bin", "DETECTIVE_TRIAL_TEST_LOG="+f.log)
	output, runErr := cmd.CombinedOutput()
	log, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return string(output), runErr, nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var created []string
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if line == "called" {
			continue
		}
		if filepath.Dir(line) != "/private/tmp" || !strings.HasPrefix(filepath.Base(line), "detective-desktop-trial.") {
			t.Fatalf("unexpected test mktemp output, refusing cleanup: %q", line)
		}
		created = append(created, line)
		// Only the wrapper's successful /usr/bin/mktemp outputs belong to this test.
		t.Cleanup(func() {
			if err := os.RemoveAll(line); err != nil {
				t.Errorf("remove exact test-created directory: %v", err)
			}
		})
	}
	return string(output), runErr, created
}

func TestTrialLauncherUsesFreshPrivateDirectoryAndExactBuiltApp(t *testing.T) {
	f := newTrialLauncherFixture(t, 0)
	var previous string
	for range 2 {
		output, err, created := f.run(t)
		if err != nil || len(created) == 0 {
			t.Fatalf("launch failed: %v\n%s", err, output)
		}
		dir := created[len(created)-1]
		if dir == previous {
			t.Fatal("launcher reused a trial directory")
		}
		previous = dir
		for _, want := range []string{
			"Desktop 隔離試用資料目錄：" + dir + "\n",
			"fake-executable:" + f.executable + "\n",
			"fake-argc:2\nfake-arg:-data-dir\nfake-arg:" + dir + "\n",
			"fake-cwd:" + f.caller + "\n", "fake-empty:yes\n",
		} {
			if !strings.Contains(output, want) {
				t.Errorf("missing launch observation %q in %q", want, output)
			}
		}
		for path, wantMode := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "child-marker"): 0o600} {
			info, err := os.Lstat(path)
			if err != nil || info.Mode().Perm() != wantMode {
				t.Fatalf("private mode for %s: info=%v err=%v", path, info, err)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "child-marker" {
			t.Fatalf("unexpected copied or removed trial content: %v, %v", entries, err)
		}
	}
	for _, marker := range f.markers {
		body, err := os.ReadFile(marker)
		if err != nil || string(body) != "existing private content" {
			t.Fatalf("existing marker changed: %s, %v", marker, err)
		}
	}
}

func TestTrialLauncherPreservesFailureCodeAndTrialDirectory(t *testing.T) {
	f := newTrialLauncherFixture(t, 37)
	output, err, created := f.run(t)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 37 || len(created) != 1 {
		t.Fatalf("exit code or created directory was lost: %v, %v\n%s", err, created, output)
	}
	body, err := os.ReadFile(filepath.Join(created[0], "child-marker"))
	if err != nil || string(body) != "trial child marker" {
		t.Fatalf("failed launch removed its trial directory: %v", err)
	}
}

func TestTrialLauncherRejectsArgumentsBeforeSideEffects(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--"}, {""}, {"-data-dir", "/private/tmp/unused"}, {"unexpected"}} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			f := newTrialLauncherFixture(t, 0)
			output, err, created := f.run(t, args...)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Fatalf("arguments accepted or wrong failure: %v\n%s", err, output)
			}
			assertTrialNotStarted(t, f, output, created)
		})
	}
}

func TestTrialLauncherRequiresExecutableBeforeCreatingDirectory(t *testing.T) {
	for _, mode := range []string{"missing", "not_executable", "directory"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrialLauncherFixture(t, 0)
			if err := os.Remove(f.executable); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "not_executable":
				writeTrialFixtureFile(t, f.executable, "not executable", 0o600)
			case "directory":
				if err := os.Mkdir(f.executable, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			output, err, created := f.run(t)
			if err == nil {
				t.Fatal("launcher accepted an unavailable executable")
			}
			assertTrialNotStarted(t, f, output, created)
		})
	}
}

func assertTrialNotStarted(t *testing.T, f trialLauncherFixture, output string, created []string) {
	t.Helper()
	if len(created) != 0 || strings.Contains(output, "Desktop 隔離試用資料目錄：") || strings.Contains(output, "fake-") {
		t.Fatalf("rejected invocation performed launch side effects: %q, %v", output, created)
	}
	if _, err := os.Lstat(f.log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected invocation reached mktemp: %v", err)
	}
	for _, marker := range f.markers {
		body, err := os.ReadFile(marker)
		if err != nil || string(body) != "existing private content" {
			t.Fatalf("rejected invocation altered existing marker: %s, %v", marker, err)
		}
	}
}
