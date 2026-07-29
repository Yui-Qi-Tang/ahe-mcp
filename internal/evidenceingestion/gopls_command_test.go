package evidenceingestion

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestGoplsOfflineEnvironmentOverridesNetworkSettings(t *testing.T) {
	got := goplsOfflineEnvironment([]string{
		"PATH=/usr/bin",
		"GOPROXY=https://proxy.example.test",
		"GOSUMDB=sum.example.test",
		"GOTOOLCHAIN=auto",
		"GOTELEMETRY=on",
		"HTTP_PROXY=http://proxy.example.test",
		"all_proxy=socks5://proxy.example.test",
	})
	for _, want := range []string{
		"PATH=/usr/bin",
		"GOENV=off",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOTELEMETRY=off",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("environment = %v, missing %q", got, want)
		}
	}
	for _, item := range got {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy":
			t.Fatalf("environment retained proxy setting %q", item)
		}
	}
}

func TestNewOfflineGoplsCommand(t *testing.T) {
	cmd := newOfflineGoplsCommand(context.Background(), "/opt/gopls", "serve")
	if runtime.GOOS == "darwin" {
		if cmd.Path != "/usr/bin/sandbox-exec" {
			t.Fatalf("command path = %q, want sandbox-exec", cmd.Path)
		}
		want := []string{
			"/usr/bin/sandbox-exec",
			"-p",
			"(version 1) (allow default) (deny network*)",
			"/opt/gopls",
			"serve",
		}
		if !slices.Equal(cmd.Args, want) {
			t.Fatalf("command args = %v, want %v", cmd.Args, want)
		}
		return
	}
	if cmd.Path != "/opt/gopls" || !slices.Equal(cmd.Args, []string{"/opt/gopls", "serve"}) {
		t.Fatalf("command = %q %v, want direct gopls execution", cmd.Path, cmd.Args)
	}
}
