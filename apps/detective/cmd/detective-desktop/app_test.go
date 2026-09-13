package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
)

func TestNativeBridgeHasNoImplicitWriterOrShell(t *testing.T) {
	bridge := reflect.TypeOf(&App{})
	got := make([]string, 0, bridge.NumMethod())
	for i := 0; i < bridge.NumMethod(); i++ {
		got = append(got, bridge.Method(i).Name)
	}
	want := []string{"BeginAtlassianLogin", "DisconnectAtlassian", "ApplyBriefReview", "CallSourceTool", "Cancel", "ChooseBatch", "ChooseBriefSource", "ChooseBriefWork", "ChooseSource", "ChooseSourceReceipt", "ChooseTaskRecord", "DiscoverTools", "Extract", "ExtractBrief", "LoadDemo", "LoadEvidenceSearchDemo", "NewWork", "PrepareBriefCandidate", "PrepareBriefReview", "PrepareTask", "QueryBriefPending", "QueryPending", "RunTask", "SaveSettings", "SearchEvidence", "SendMessage", "Snapshot", "SubmitBriefPending", "SubmitPending", "SuggestSourceTool"}
	// The preset picker is draft-only; index is a separate explicit action.
	// Chat confirms a backend-held ID, never a frontend-supplied executable.
	want = append(want, "ChooseCodebaseRepository", "IndexCodebase", "StartSourceChat", "ConfirmSourceChat")
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("native authority surface changed: %v", got)
	}
}

func TestNativeBridgeNewWorkDoesNotLoadDemo(t *testing.T) {
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
	settings := app.Snapshot().Settings
	settings.Mode = "local"
	if _, err := app.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	state, err := app.NewWork()
	if err != nil || state.Busy || state.Source != nil || len(state.Messages) != 0 || len(state.Candidates) != 0 {
		t.Fatalf("new work did not remain empty: %v", err)
	}
	if !reflect.DeepEqual(state.Settings, settings) {
		t.Fatal("new work changed settings")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatal("new work created a saved demo or discarded settings")
	}
}

func TestNativeBridgeEvidenceSearchUsesContextAndFixedDemo(t *testing.T) {
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
	state, err := app.LoadEvidenceSearchDemo("mixed")
	if err != nil || state.Search == nil || !state.Search.Demo || state.Search.ReturnedMatches != 4 {
		t.Fatalf("bridge did not return the fixed demo: %v", err)
	}
	state, err = app.LoadEvidenceSearchDemo("unrecognized free question")
	if err == nil || state.Search != nil {
		t.Fatal("bridge accepted a free-question demo")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	app.startup(ctx)
	state, err = app.SearchEvidence(desktop.EvidenceSearchRequest{Question: "maintenance", LifecycleScope: "active", Limit: 20})
	if err == nil || state.Search != nil || state.Busy {
		t.Fatal("bridge failed to forward its cancelled context")
	}
	method, ok := reflect.TypeOf(app).MethodByName("SearchEvidence")
	if !ok || method.Type.NumIn() != 2 || method.Type.In(1) != reflect.TypeOf(desktop.EvidenceSearchRequest{}) {
		t.Fatal("native search accepts something other than a typed request")
	}
}
