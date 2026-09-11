package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
)

func TestStartupMessageReportsWorkspaceInUseWithoutPrivateDetails(t *testing.T) {
	for _, err := range []error{desktop.ErrWorkspaceInUse, fmt.Errorf("/private/synthetic-path: %w", desktop.ErrWorkspaceInUse)} {
		message := startupMessage(err)
		if !strings.Contains(message, "此工作區已由另一個 Detective 程序使用") || !strings.Contains(message, "未改寫設定") || strings.Contains(message, "synthetic-path") {
			t.Fatalf("workspace conflict message is missing or exposes private details: %q", message)
		}
	}
	message := startupMessage(errors.New("/private/synthetic-path synthetic-credential"))
	if message != "detective desktop: 無法啟動；請確認私有資料目錄權限與 Desktop 建置。" {
		t.Fatalf("unexpected startup diagnostic: %q", message)
	}
}

func TestStartupFailureReportsOnlyClassifiedMessage(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		dialog    bool
		notifyErr error
	}{
		{name: "workspace conflict", err: fmt.Errorf("/private/synthetic-secret: %w", desktop.ErrWorkspaceInUse), dialog: true},
		{name: "other startup failure", err: errors.New("/private/synthetic-secret"), dialog: true},
		{name: "script mode", err: desktop.ErrWorkspaceInUse},
		{name: "unavailable alert", err: desktop.ErrWorkspaceInUse, dialog: true, notifyErr: errors.New("synthetic-secret alert diagnostic")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			calls := 0
			reportStartupFailure(test.err, test.dialog, &stderr, func(message string) error {
				calls++
				if message != startupMessage(test.err) || strings.Contains(message, "synthetic-secret") {
					t.Fatal("unclassified error reached native alert")
				}
				return test.notifyErr
			})
			wantCalls := 0
			if test.dialog {
				wantCalls = 1
			}
			want := startupMessage(test.err) + "\n"
			if test.notifyErr != nil {
				want += "detective desktop: 無法顯示啟動提示；請查看上方訊息。\n"
			}
			if calls != wantCalls || stderr.String() != want {
				t.Fatalf("diagnostic: calls=%d stderr=%q", calls, stderr.String())
			}
		})
	}
}

func TestWindowTitleUsesWorkspaceIdentifier(t *testing.T) {
	for _, id := range []string{"0123456789ab", "abcdef012345"} {
		if got, want := windowTitle(id), "AHE Detective — 工作區 "+id; got != want {
			t.Errorf("windowTitle(%q) = %q, want %q", id, got, want)
		}
	}
}
