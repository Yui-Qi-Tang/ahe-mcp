package main

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"time"
)

const startupAlertTimeout = 35 * time.Second

const startupAlertScript = `on run argv
	display alert "AHE Detective 無法啟動" message (item 1 of argv) as warning buttons {"知道了"} default button 1 giving up after 30
end run
`

func showStartupAlert(message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), startupAlertTimeout)
	defer cancel()
	return startupAlertCommand(ctx, message).Run()
}

func startupAlertCommand(ctx context.Context, message string) *exec.Cmd {
	// Keep the classified message separate from executable script text. This is
	// only a startup notice; it must not inherit credentials or emit script output.
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-", message)
	cmd.Stdin = strings.NewReader(startupAlertScript)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	return cmd
}
