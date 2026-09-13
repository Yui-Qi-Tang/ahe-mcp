package sourcemcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiteralProcessOptions(t *testing.T) {
	c := Config{Transport: "stdio", Command: "/operator/launcher", Directory: "/project", Args: []string{"literal; $(not-a-command)", "--option=two words"}}
	if err := ValidateProcessOptions(c); err != nil {
		t.Fatal(err)
	}
	cmd, cleanup, err := sourceProcess(context.Background(), c, c.Args)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if cmd.Dir != c.Directory || len(cmd.Args) != 3 || cmd.Args[1] != c.Args[0] {
		t.Fatal("argv was interpreted")
	}
	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "HOME=") || strings.Contains(env, "KEY=") {
			t.Fatal("inherited private environment")
		}
	}
	for _, c := range []Config{{Transport: "streamable-http", Args: []string{"x"}}, {Transport: "stdio", Directory: "relative"}, {Transport: "stdio", Args: []string{strings.Repeat("x", 4097)}}, {Transport: "stdio", CodebaseCache: "relative", Directory: "/project"}} {
		if ValidateProcessOptions(c) == nil {
			t.Fatal("invalid options accepted")
		}
	}
}

// Negative-control test: even an explicitly attempted TCP request must fail.
// This contacts only a local receiver, never an external IP or DNS endpoint.
func TestCodebaseSandboxLive(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("AHE_CODEBASE_SANDBOX_TEST") != "1" {
		t.Skip("explicit macOS sandbox smoke only")
	}
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if hits.Load() != 1 {
		t.Fatal("control receiver unavailable")
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	profile := codebaseSandboxProfile(repo)
	for _, args := range [][]string{{"/usr/bin/curl", "--silent", "--max-time", "2", server.URL}, {"/bin/sh", "-c", `exec /usr/bin/curl --silent --max-time 2 "$1"`, "guard-probe", server.URL}, {"/usr/bin/touch", filepath.Join(repo, "must-not-exist")}} {
		if err := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", append([]string{"-p", profile}, args...)...).Run(); err == nil {
			t.Fatal("sandbox permitted prohibited operation")
		}
	}
	if hits.Load() != 1 {
		t.Fatal("network request reached receiver")
	}
	if _, err := os.Stat(filepath.Join(repo, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("repository write was not blocked")
	}
	// A valid executable still runs, so the failures above are not bad startup.
	if err := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/usr/bin/true").Run(); err != nil {
		t.Fatal(err)
	}
}
