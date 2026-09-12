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
	want := []string{"BeginAtlassianLogin", "DisconnectAtlassian", "ApplyBriefReview", "CallSourceTool", "Cancel", "ChooseBatch", "ChooseBriefSource", "ChooseBriefWork", "ChooseSource", "ChooseSourceReceipt", "DiscoverTools", "Extract", "ExtractBrief", "LoadDemo", "LoadEvidenceSearchDemo", "PrepareBriefCandidate", "PrepareBriefReview", "QueryBriefPending", "QueryPending", "SaveSettings", "SearchEvidence", "SendMessage", "Snapshot", "SubmitBriefPending", "SubmitPending", "SuggestSourceTool"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("native authority surface changed: %v", got)
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
