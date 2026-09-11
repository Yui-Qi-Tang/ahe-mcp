package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCommandHelpAndUnknownArguments(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		ok   bool
	}{
		{name: "help", args: []string{"--help"}, ok: true},
		{name: "unknown", args: []string{"--secret-marker"}},
		{name: "extra after help", args: []string{"--help", "secret-marker"}},
		{name: "missing config", args: []string{"--config"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			args := append([]string{"-test.run=^TestCommandHelper$", "--"}, test.args...)
			command := exec.CommandContext(ctx, os.Args[0], args...)
			command.Env = []string{"MCPLAUNCH_COMMAND_TEST=1"}
			output, err := command.CombinedOutput()
			if (err == nil) != test.ok || strings.Contains(string(output), "secret-marker") {
				t.Fatal("command status or safe output contract failed")
			}
			if test.ok && !strings.Contains(string(output), "Usage:") {
				t.Fatal("command help did not contain usage")
			}
		})
	}
}

func TestCommandHelper(t *testing.T) {
	if os.Getenv("MCPLAUNCH_COMMAND_TEST") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"ahe-mcp-launch"}, os.Args[index+1:]...)
			main()
			return
		}
	}
	t.Fatal("command test separator is missing")
}
