package mcpstdio

import (
	"errors"
	"runtime"
)

const (
	macOSSandboxExecutable = "/usr/bin/sandbox-exec"
	macOSLoopbackProfile   = `(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*"))`
)

// MacOSLoopbackOnlyCommand wraps one command in the macOS process sandbox.
// The profile allows loopback network traffic and rejects every other socket.
func MacOSLoopbackOnlyCommand(command CommandConfig) (CommandConfig, error) {
	if runtime.GOOS != "darwin" {
		return CommandConfig{}, errors.New("macOS loopback sandbox is unavailable on this operating system")
	}
	prepared, err := prepareCommandConfig(command)
	if err != nil {
		return CommandConfig{}, err
	}
	args := make([]string, 0, len(prepared.Args)+3)
	args = append(args, "-p", macOSLoopbackProfile, prepared.Path)
	args = append(args, prepared.Args...)
	return prepareCommandConfig(CommandConfig{
		Path:        macOSSandboxExecutable,
		Args:        args,
		Directory:   prepared.Directory,
		Environment: prepared.Environment,
	})
}

// IsMacOSLoopbackOnlyCommand reports whether command has the exact AHE
// loopback-only wrapper produced by MacOSLoopbackOnlyCommand.
func IsMacOSLoopbackOnlyCommand(command CommandConfig) bool {
	prepared, err := prepareCommandConfig(command)
	if err != nil || prepared.Path != macOSSandboxExecutable || len(prepared.Args) < 3 {
		return false
	}
	return prepared.Args[0] == "-p" &&
		prepared.Args[1] == macOSLoopbackProfile &&
		prepared.Args[2] != macOSSandboxExecutable
}
