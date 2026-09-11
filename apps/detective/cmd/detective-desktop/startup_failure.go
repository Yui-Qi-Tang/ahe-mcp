package main

import (
	"fmt"
	"io"
)

// Keep the same bounded classification at both output boundaries. Raw startup
// errors can include private paths or upstream diagnostics, so never forward them.
func reportStartupFailure(err error, dialog bool, stderr io.Writer, notify func(string) error) {
	message := startupMessage(err)
	// Startup already failed; a diagnostic write failure cannot be recovered here.
	_, _ = fmt.Fprintln(stderr, message)
	if !dialog {
		return
	}
	if err := notify(message); err != nil {
		// Do not expose errors from the native alert mechanism either.
		_, _ = fmt.Fprintln(stderr, "detective desktop: 無法顯示啟動提示；請查看上方訊息。")
	}
}
