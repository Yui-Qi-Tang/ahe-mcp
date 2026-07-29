package mcpstdio

import (
	"runtime"
	"strings"
	"testing"
)

func TestMacOSLoopbackOnlyCommandWrapsExactProcess(t *testing.T) {
	command, err := MacOSLoopbackOnlyCommand(CommandConfig{
		Path:        "/fixture/provider",
		Args:        []string{"serve", "--stdio"},
		Directory:   "/fixture/work",
		Environment: []string{"HOME=/fixture/home"},
	})
	if runtime.GOOS != "darwin" {
		if err == nil {
			t.Fatal("MacOSLoopbackOnlyCommand() error = nil on non-macOS")
		}
		return
	}
	if err != nil {
		t.Fatalf("MacOSLoopbackOnlyCommand() error = %v", err)
	}
	if command.Path != macOSSandboxExecutable ||
		len(command.Args) != 5 ||
		command.Args[0] != "-p" ||
		command.Args[2] != "/fixture/provider" ||
		command.Args[3] != "serve" ||
		command.Args[4] != "--stdio" {
		t.Fatalf("wrapped command = %+v", command)
	}
	if !strings.Contains(command.Args[1], "(deny network*)") ||
		!strings.Contains(command.Args[1], `(remote ip "localhost:*")`) {
		t.Fatalf("sandbox profile = %q", command.Args[1])
	}
	if !IsMacOSLoopbackOnlyCommand(command) {
		t.Fatalf("IsMacOSLoopbackOnlyCommand(%+v) = false", command)
	}
}
