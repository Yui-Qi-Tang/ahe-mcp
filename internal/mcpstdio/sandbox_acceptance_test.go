//go:build acceptance

package mcpstdio

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAcceptanceMacOSLoopbackSandboxBlocksExternalSocket(t *testing.T) {
	if os.Getenv("AHE_MCP_NETWORK_CANARY") != "" {
		runNetworkCanary(t)
		return
	}
	if testing.Short() {
		t.Skip("network sandbox acceptance is disabled in short mode")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	command, err := MacOSLoopbackOnlyCommand(CommandConfig{
		Path:      executable,
		Args:      []string{"-test.run=TestAcceptanceMacOSLoopbackSandboxBlocksExternalSocket"},
		Directory: t.TempDir(),
		Environment: []string{
			"AHE_MCP_NETWORK_CANARY=1",
			"AHE_MCP_LOOPBACK_ADDRESS=" + address,
		},
	})
	if err != nil {
		t.Fatalf("MacOSLoopbackOnlyCommand() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Directory
	cmd.Env = command.Environment
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("network sandbox canary error = %v: %s", err, output)
	}
}

func runNetworkCanary(t *testing.T) {
	loopback := os.Getenv("AHE_MCP_LOOPBACK_ADDRESS")
	connection, err := net.DialTimeout("tcp", loopback, time.Second)
	if err != nil {
		t.Fatalf("loopback dial error = %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("loopback close error = %v", err)
	}
	external, err := net.DialTimeout("tcp", "1.1.1.1:443", time.Second)
	if err == nil {
		_ = external.Close()
		t.Fatal("external dial unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("external dial error = %v, want operation not permitted", err)
	}
	fmt.Fprintln(os.Stderr, "loopback allowed; external socket denied")
}
