package sourcemcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// sourceProcess keeps argv literal and credentials out of child environments.
// The Codebase preset uses a dedicated cache and a macOS network-denying sandbox.
// Existing daemons with a different cache fail CBM's admission check; we never
// stop or reconfigure those daemons to make a new connection succeed.
func sourceProcess(ctx context.Context, c Config, args []string) (*exec.Cmd, func(), error) {
	cleanup := func() {}
	command := c.Command
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "TZ=UTC"}
	if c.CodebaseCache != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, cleanup, err
		}
		// Only ephemeral IPC lives here: Darwin socket names cannot accommodate
		// deep workspace paths. Observations/config/index remain in the cache.
		runtimeDir, err := os.MkdirTemp("/private/tmp", "ahe-cbm-")
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = func() { _ = os.RemoveAll(runtimeDir) }
		profile := codebaseSandboxProfile(c.Directory)
		command = "/usr/bin/sandbox-exec"
		args = append([]string{"-p", profile, c.Command}, args...)
		env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+c.CodebaseCache, "CBM_CACHE_DIR="+c.CodebaseCache, "CBM_RUNTIME_DIR="+runtimeDir, "CBM_ALLOWED_ROOT="+c.Directory)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir, cmd.Env = c.Directory, env
	if c.CodebaseCache != "" && len(c.Args) == 0 && len(args) == 3 {
		// Explicit settings prevent connecting to a repository from implicitly
		// indexing or watching it. Both commands run under the same network guard.
		for _, key := range []string{"auto_index", "auto_watch"} {
			setupArgs := append(append([]string(nil), args...), "config", "set", key, "false")
			setup := exec.CommandContext(ctx, command, setupArgs...)
			setup.Dir, setup.Env = c.Directory, env
			setup.WaitDelay = 2 * time.Second
			if err := setup.Run(); err != nil {
				cleanup()
				return nil, func() {}, errors.New("offline codebase preparation failed; check cache conflicts or sandbox support")
			}
		}
		cmd.Args = append(cmd.Args, "--ui=false")
	}
	return cmd, cleanup, nil
}

func codebaseSandboxProfile(repository string) string {
	return `(version 1)(allow default)(deny network*)(allow network* (local unix-socket))(allow network* (remote unix-socket))(deny file-write* (subpath ` + strconv.Quote(repository) + `))`
}
