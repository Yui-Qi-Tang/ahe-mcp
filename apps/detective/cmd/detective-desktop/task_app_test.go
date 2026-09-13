package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
)

func TestNativeTaskBridgeBindsExplicitInputAndForwardsCancellation(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := desktop.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	app := &App{ctx: t.Context(), service: service}
	method, ok := reflect.TypeOf(app).MethodByName("PrepareTask")
	if !ok || method.Type.NumIn() != 2 || method.Type.In(1) != reflect.TypeOf(desktop.TaskDraftRequest{}) {
		t.Fatal("task preparation must accept only its typed request")
	}
	if state, err := app.PrepareTask(desktop.TaskDraftRequest{Objective: "Find deployment conditions."}); err == nil || state.Task != nil {
		t.Fatal("native task bridge prepared without an explicit source")
	}
	sourcePath := filepath.Join(directory, "synthetic-ticket.txt")
	if err := os.WriteFile(sourcePath, []byte("The owner may deploy only after smoke tests pass.\n\nThe tests have not passed."), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := service.ImportSource(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	settings := state.Settings
	settings.Mode = "local"
	settings.Model = "synthetic-unavailable-model"
	settings.BaseURL = "http://127.0.0.1:1/v1"
	state, err = app.SaveSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	state, err = app.PrepareTask(desktop.TaskDraftRequest{
		Objective: "Find deployment conditions.", SourcePath: state.Source.Path, SourceSHA256: state.Source.SHA256,
	})
	if err != nil || state.Task == nil || state.Task.Status != "prepared" {
		t.Fatalf("native prepare did not expose the frozen task: %v", err)
	}
	inputID := state.Task.InputID
	if _, err := app.RunTask("stale-input-id"); err == nil {
		t.Fatal("native bridge accepted a stale input")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	app.startup(ctx)
	state, err = app.RunTask(inputID)
	if err == nil || state.Busy || state.Task == nil ||
		(state.Task.Record != nil && state.Task.Record.Result != nil) ||
		len(state.Candidates) != 0 || state.Extraction != nil || state.BatchPath != "" || state.BatchResult != nil {
		t.Fatal("cancelled task created a successful result or legacy handoff")
	}
}
