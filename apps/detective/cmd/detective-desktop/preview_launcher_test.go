package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type previewLauncherFixture struct {
	base, script, executable, caller, home, homeAlias string
}

func newPreviewLauncherFixture(t *testing.T, exitCode int) previewLauncherFixture {
	t.Helper()
	script, err := os.ReadFile("../../scripts/preview/Start.command")
	if err != nil {
		t.Fatal(err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(base, "portable package with spaces")
	f := previewLauncherFixture{
		base:       base,
		executable: filepath.Join(packageDir, "AHE Detective.app/Contents/MacOS/AHE Detective"),
		caller:     filepath.Join(base, "unrelated working directory"),
		home:       filepath.Join(base, "operator home"),
		homeAlias:  filepath.Join(base, "home alias"),
	}
	for _, dir := range []string{filepath.Dir(f.executable), f.caller, f.home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTrialFixtureFile(t, filepath.Join(packageDir, "Start.command"), string(script), 0o700)
	packageAlias := filepath.Join(base, "package alias")
	if err := os.Symlink(packageDir, packageAlias); err != nil {
		t.Fatal(err)
	}
	f.script = filepath.Join(packageAlias, "Start.command")
	if err := os.Symlink(f.home, f.homeAlias); err != nil {
		t.Fatal(err)
	}
	writeTrialFixtureFile(t, filepath.Join(f.caller, "existing-source.md"), "existing private source", 0o600)
	writeTrialFixtureFile(t, f.executable, fmt.Sprintf(`#!/bin/sh
set -eu
printf 'fake-executable:%%s\n' "$0"
printf 'fake-argc:%%s\n' "$#"
printf 'fake-arg:%%s\n' "$@"
printf 'fake-cwd:%%s\n' "$(pwd -P)"
printf 'fake-home:%%s\n' "${HOME-unset}"
printf 'fake-umask:%%s\n' "$(umask)"
exit %d
`, exitCode), 0o700)
	return f
}

func (f previewLauncherFixture) run(t *testing.T, home *string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.script, args...)
	cmd.Dir = f.caller
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if home != nil {
		cmd.Env = append(cmd.Env, "HOME="+*home)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

type previewFileState struct {
	Mode     os.FileMode
	Modified int64
	Content  string
}

func previewFiles(t *testing.T, base string) map[string]previewFileState {
	t.Helper()
	files := make(map[string]previewFileState)
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := previewFileState{Mode: info.Mode(), Modified: info.ModTime().UnixNano()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			state.Content, err = os.Readlink(path)
		case info.Mode().IsRegular():
			var body []byte
			body, err = os.ReadFile(path)
			state.Content = string(body)
		}
		if err != nil {
			return err
		}
		files[path] = state
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestPreviewLauncherUsesSameWorkspaceWithoutWriting(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("existing=", existing), func(t *testing.T) {
			f := newPreviewLauncherFixture(t, 0)
			workspace := filepath.Join(f.home, "Library/Application Support/Detective Preview")
			daily := filepath.Join(f.home, "Library/Application Support/Detective Desktop")
			if err := os.MkdirAll(daily, 0o700); err != nil {
				t.Fatal(err)
			}
			writeTrialFixtureFile(t, filepath.Join(daily, "settings.json"), "daily settings stay private", 0o600)
			if existing {
				// Leave invalid permissions untouched; only the real app may reject
				// the workspace. The synthetic executable performs no validation.
				if err := os.Mkdir(workspace, 0o755); err != nil {
					t.Fatal(err)
				}
				writeTrialFixtureFile(t, filepath.Join(workspace, "settings.json"), "saved preview settings", 0o644)
			}
			before := previewFiles(t, f.base)
			for range 2 {
				output, err := f.run(t, &f.homeAlias)
				if err != nil {
					t.Fatalf("launch failed: %v\n%s", err, output)
				}
				for _, want := range []string{
					"Detective 預覽工作區：" + workspace + "\n",
					"不會自動連線；重開沿用已保存設定，未保存的聊天不會恢復。",
					"fake-executable:" + f.executable + "\n",
					"fake-argc:2\nfake-arg:-data-dir\nfake-arg:" + workspace + "\n",
					"fake-cwd:" + f.caller + "\n",
					"fake-home:" + f.homeAlias + "\n",
				} {
					if !strings.Contains(output, want) {
						t.Errorf("missing observation %q in %q", want, output)
					}
				}
				if !strings.Contains(output, "fake-umask:0077\n") && !strings.Contains(output, "fake-umask:077\n") {
					t.Fatal("launcher did not set a private child umask")
				}
				if !reflect.DeepEqual(before, previewFiles(t, f.base)) {
					t.Fatal("launcher created files, changed permissions, or modified existing markers")
				}
			}
		})
	}
}

func TestPreviewLauncherVersionDoesNotRequireHome(t *testing.T) {
	f := newPreviewLauncherFixture(t, 0)
	missing := filepath.Join(f.base, "home does not exist")
	for _, home := range []*string{nil, &missing} {
		before := previewFiles(t, f.base)
		output, err := f.run(t, home, "--version")
		if err != nil || !strings.Contains(output, "fake-argc:1\nfake-arg:-version\n") || strings.Contains(output, "預覽工作區") {
			t.Fatalf("version touched workspace setup or changed arguments: %v\n%s", err, output)
		}
		if !reflect.DeepEqual(before, previewFiles(t, f.base)) {
			t.Fatal("version lookup changed files")
		}
	}
}

func TestPreviewLauncherRejectsArgumentsBeforeStarting(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--"}, {""}, {"-version"}, {"--version", "extra"}, {"-data-dir", "/unused"}} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			f := newPreviewLauncherFixture(t, 0)
			before := previewFiles(t, f.base)
			output, err := f.run(t, nil, args...)
			assertPreviewFailure(t, f, before, output, err, 2)
		})
	}
}

func TestPreviewLauncherRequiresAdjacentExecutable(t *testing.T) {
	for _, mode := range []string{"missing", "not_executable", "directory"} {
		t.Run(mode, func(t *testing.T) {
			f := newPreviewLauncherFixture(t, 0)
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
			before := previewFiles(t, f.base)
			for _, args := range [][]string{nil, {"--version"}} {
				output, err := f.run(t, nil, args...)
				assertPreviewFailure(t, f, before, output, err, 1)
				if !strings.Contains(output, "找不到相鄰且可執行的 AHE Detective.app") {
					t.Fatal("missing executable was not rejected before home validation")
				}
			}
		})
	}
}

func TestPreviewLauncherRequiresExistingAbsoluteHome(t *testing.T) {
	f := newPreviewLauncherFixture(t, 0)
	empty, relative, missing := "", "relative home", filepath.Join(f.base, "missing home")
	for _, home := range []*string{nil, &empty, &relative, &missing, &f.executable} {
		before := previewFiles(t, f.base)
		output, err := f.run(t, home)
		assertPreviewFailure(t, f, before, output, err, 1)
	}
}

func TestPreviewLauncherPreservesChildExitCode(t *testing.T) {
	f := newPreviewLauncherFixture(t, 37)
	for _, args := range [][]string{nil, {"--version"}} {
		before := previewFiles(t, f.base)
		output, err := f.run(t, &f.homeAlias, args...)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 37 || !strings.Contains(output, "fake-executable:") {
			t.Fatalf("child exit code was not preserved: %v\n%s", err, output)
		}
		if !reflect.DeepEqual(before, previewFiles(t, f.base)) {
			t.Fatal("failed child invocation changed files")
		}
	}
}

func assertPreviewFailure(t *testing.T, f previewLauncherFixture, before map[string]previewFileState, output string, err error, code int) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != code || strings.Contains(output, "fake-") || strings.Contains(output, "預覽工作區") {
		t.Fatalf("rejected invocation started the app or used the wrong exit code: %v\n%s", err, output)
	}
	if !reflect.DeepEqual(before, previewFiles(t, f.base)) {
		t.Fatal("rejected invocation changed files")
	}
}
