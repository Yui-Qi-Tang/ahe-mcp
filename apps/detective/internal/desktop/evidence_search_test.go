package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

func searchRequest() EvidenceSearchRequest {
	return EvidenceSearchRequest{Question: "維護 影響？", SourceID: "synthetic-source", AdmissionOutcome: "audit_only", LifecycleScope: "active", Limit: 20}
}

func searchService(t *testing.T) *Service {
	t.Helper()
	s := newTestService(t)
	state, err := s.LoadDemo()
	if err != nil {
		t.Fatal(err)
	}
	settings := state.Settings
	settings.Mode = "local"
	settings.QueryLauncher = filepath.Join(s.dataDir, "unused-query-launcher")
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	// Existing pending and human-review projections must be orthogonal to search.
	s.state.BatchPath, s.state.BatchDigest = "synthetic-batch", "synthetic-digest"
	s.state.Brief = &BriefWork{Outcome: "audit_only", Decision: &BriefDecision{Reason: "synthetic existing decision"}}
	return s
}

func searchFixture() ahemcp.PracticalQueryResult {
	return ahemcp.PracticalQueryResult{
		Response:        json.RawMessage("{\n  \"unknown_number\": 9007199254740993, \"matches\": [], \"canary\": \"search-do-not-send-to-model\"\n}"),
		ReturnedMatches: 4, FallbackAttempted: true, Truncated: true,
		HanTermPolicy: &ahemcp.PracticalHanTermPolicy{Version: "han-auxiliary-anchor-v1", AuxiliaryTerms: []string{"影響"}, EligibleAnchorTerms: []string{"維護"}},
	}
}

func workspaceContents(t *testing.T, directory string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err == nil {
			files[path] = string(body)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func assertSearchPreservesWork(t *testing.T, before, after State) {
	t.Helper()
	if !reflect.DeepEqual(before.Source, after.Source) || !reflect.DeepEqual(before.Brief, after.Brief) || !reflect.DeepEqual(before.Candidates, after.Candidates) || !reflect.DeepEqual(before.Extraction, after.Extraction) || !reflect.DeepEqual(before.BatchResult, after.BatchResult) || before.BatchPath != after.BatchPath || before.BatchDigest != after.BatchDigest || !reflect.DeepEqual(before.Messages, after.Messages) {
		t.Fatal("search changed source, batch, review or conversation")
	}
}

func TestEvidenceSearchUsesOnlySavedQueryAndPreservesRawJSON(t *testing.T) {
	s := searchService(t)
	before, files := s.Snapshot(), workspaceContents(t, s.dataDir)
	request, fixture := searchRequest(), searchFixture()
	calls := 0
	state, err := s.searchEvidence(t.Context(), request, func(ctx context.Context, command string, input ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
		calls++
		if command != before.Settings.QueryLauncher || input != (ahemcp.PracticalQueryInput{Question: request.Question, SourceID: request.SourceID, AdmissionOutcome: request.AdmissionOutcome, LifecycleScope: request.LifecycleScope, Limit: request.Limit}) {
			t.Fatal("query did not use saved launcher and exact request")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > operationTimeout {
			t.Fatal("search lacks the bounded desktop deadline")
		}
		if running := s.Snapshot(); !running.Busy || running.Operation != "evidence_search" || running.Search != nil {
			t.Fatal("new search did not enter the existing busy lifecycle")
		}
		return fixture, nil
	})
	if err != nil || calls != 1 || state.Busy || state.Error != "" || state.Search == nil || state.Search.Demo || state.Search.Request != request || state.Search.RawJSON != string(fixture.Response) || state.Search.ReturnedMatches != 4 || !state.Search.FallbackAttempted || !state.Search.Truncated || !reflect.DeepEqual(state.Search.HanTermPolicy, fixture.HanTermPolicy) {
		t.Fatalf("search result or request changed: %v", err)
	}
	assertSearchPreservesWork(t, before, state)
	if !reflect.DeepEqual(files, workspaceContents(t, s.dataDir)) {
		t.Fatal("search saved or changed a workspace file")
	}
	input, err := chatInput(state)
	if err != nil || strings.Contains(input, "search-do-not-send-to-model") || strings.Contains(input, request.Question) {
		t.Fatal("search escaped the existing chat-context allowlist")
	}
	state.Search.HanTermPolicy.AuxiliaryTerms[0] = "changed-ui"
	state.Search.RawJSON = "changed-ui"
	fixture.HanTermPolicy.EligibleAnchorTerms[0] = "changed-adapter"
	fixture.Response[0] = '['
	live := s.Snapshot().Search
	if live.RawJSON[0] != '{' || live.HanTermPolicy.AuxiliaryTerms[0] != "影響" || live.HanTermPolicy.EligibleAnchorTerms[0] != "維護" {
		t.Fatal("search snapshot aliases UI or adapter memory")
	}
	for _, event := range state.Events {
		if strings.Contains(event.Detail, request.Question) || strings.Contains(event.Detail, "search-do-not-send-to-model") {
			t.Fatal("search data leaked to operational events")
		}
	}
}

func TestEvidenceSearchFailureClearsOldResultWithoutEmptySuccess(t *testing.T) {
	s := searchService(t)
	if _, err := s.LoadEvidenceSearchDemo("mixed"); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	calls := 0
	state, err := s.searchEvidence(t.Context(), searchRequest(), func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
		calls++
		if s.Snapshot().Search != nil {
			t.Fatal("previous result survived the new request start")
		}
		return ahemcp.PracticalQueryResult{}, errors.New("private raw upstream error")
	})
	if err == nil || calls != 1 || state.Search != nil || state.Busy || state.Error == "" || strings.Contains(state.Error, "private raw upstream error") || !strings.Contains(state.Error, "舊版需升級") {
		t.Fatal("failed search was presented as empty, retried or exposed diagnostics")
	}
	assertSearchPreservesWork(t, before, state)
	if state.Events[len(state.Events)-1].Status != "failed" {
		t.Fatal("failed search event is not explicit")
	}
}

func TestEvidenceSearchEmptySuccessRemainsInspectable(t *testing.T) {
	s := searchService(t)
	state, err := s.searchEvidence(t.Context(), searchRequest(), func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
		return ahemcp.PracticalQueryResult{Response: json.RawMessage(`{"matches":[],"unknown":9007199254740993}`)}, nil
	})
	if err != nil || state.Search == nil || state.Search.ReturnedMatches != 0 || state.Search.HanTermPolicy != nil || state.Error != "" || state.Search.Demo {
		t.Fatalf("empty success confused with failure or invented Han policy: %v", err)
	}
}

func TestEvidenceSearchRequiresSavedLocalSettings(t *testing.T) {
	for _, mode := range []string{"demo", "local"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestService(t)
			s.state.Settings.Mode = mode // Local case deliberately has no saved launcher.
			_, err := s.searchEvidence(t.Context(), searchRequest(), func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
				t.Fatal("unconfigured search reached adapter")
				return ahemcp.PracticalQueryResult{}, nil
			})
			if err == nil || s.Snapshot().Search != nil {
				t.Fatal("unconfigured search succeeded")
			}
		})
	}
}

func TestEvidenceSearchCancellationBusyAndLateResult(t *testing.T) {
	s := searchService(t)
	before := s.Snapshot()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	finished := make(chan error, 1)
	go func() {
		_, err := s.searchEvidence(t.Context(), searchRequest(), func(ctx context.Context, _ string, _ ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
			close(started)
			<-ctx.Done()
			<-release
			return searchFixture(), nil // A late result cannot override cancellation.
		})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("search did not start")
	}
	if _, err := s.SearchEvidence(t.Context(), searchRequest()); err == nil {
		t.Fatal("busy service accepted another search")
	}
	if _, err := s.LoadEvidenceSearchDemo("mixed"); err == nil {
		t.Fatal("busy service accepted a demo")
	}
	if _, err := s.SaveSettings(before.Settings); err == nil {
		t.Fatal("busy service changed the query launcher")
	}
	state := s.Cancel()
	if !state.Busy || state.Search != nil || !strings.Contains(state.Events[len(state.Events)-1].Detail, "唯讀查詢") || strings.Contains(state.Events[len(state.Events)-1].Detail, "先前提交可能已生效") {
		t.Fatal("cancel claimed completion or a possible write")
	}
	unblock()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled query accepted a late result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled query did not finish")
	}
	state = s.Snapshot()
	if state.Busy || state.Search != nil || !strings.Contains(state.Error, "取消或逾時") {
		t.Fatal("cancelled search retained a result or busy state")
	}
	assertSearchPreservesWork(t, before, state)
}

func TestEvidenceSearchDemoIsFixedOfflineAndPreservesWork(t *testing.T) {
	s := searchService(t)
	before, files := s.Snapshot(), workspaceContents(t, s.dataDir)
	for _, scenario := range []string{"mixed", "empty"} {
		state, err := s.LoadEvidenceSearchDemo(scenario)
		if err != nil || state.Search == nil || !state.Search.Demo || state.Search.Request.SourceID != "" || state.Settings.Mode != "local" {
			t.Fatalf("demo was not explicit and independent: %v", err)
		}
		want := 4
		if scenario == "empty" {
			want = 0
		}
		if state.Search.ReturnedMatches != want || state.Search.Request.LifecycleScope != "active" || !json.Valid([]byte(state.Search.RawJSON)) || !strings.Contains(state.Search.RawJSON, "固定合成") {
			t.Fatal("invalid fixed demo response")
		}
		if scenario == "mixed" {
			for _, outcome := range []string{"admitted", "rejected", "audit_only", "pending"} {
				if !strings.Contains(state.Search.RawJSON, `"admission_outcome":"`+outcome+`"`) {
					t.Fatal("mixed demo is missing an existing-record outcome")
				}
			}
			if !strings.Contains(state.Search.RawJSON, "span:demo-original-1") || !strings.Contains(state.Search.RawJSON, "span:demo-search-context") {
				t.Fatal("demo does not separate original quotes from search excerpts")
			}
		}
		assertSearchPreservesWork(t, before, state)
		if !reflect.DeepEqual(files, workspaceContents(t, s.dataDir)) {
			t.Fatal("fixed demo saved files or invoked configured launchers")
		}
	}
	if state, err := s.LoadEvidenceSearchDemo("my free question"); err == nil || state.Search != nil {
		t.Fatal("demo accepted an arbitrary question or retained an old result")
	}
}

func TestEvidenceSearchCloseRejectsLateResult(t *testing.T) {
	s := searchService(t)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	finished := make(chan error, 1)
	go func() {
		_, err := s.searchEvidence(t.Context(), searchRequest(), func(ctx context.Context, _ string, _ ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return searchFixture(), nil
		})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("search did not start")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not cancel owned search")
	}
	select {
	case <-closed:
		t.Fatal("close did not wait for search cleanup")
	default:
	}
	unblock()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("close accepted a late result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed search did not finish")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not complete cleanup")
	}
	if state := s.Snapshot(); state.Search != nil || state.Busy {
		t.Fatal("closed service published late search data")
	}
}

func TestEvidenceSearchCompletedCancelIsNoOp(t *testing.T) {
	s := searchService(t)
	state, err := s.searchEvidence(t.Context(), searchRequest(), func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
		return searchFixture(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := s.Cancel(); !reflect.DeepEqual(state, after) {
		t.Fatal("completed search accepted a misleading later cancellation")
	}
}

func TestEvidenceSearchRejectsInactiveContextAndInvalidRequestBeforeLauncher(t *testing.T) {
	s := searchService(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SearchEvidence(ctx, searchRequest()); err == nil {
		t.Fatal("search accepted an inactive context")
	}
	if _, err := s.SearchEvidence(nil, searchRequest()); err == nil {
		t.Fatal("search accepted a nil context")
	}
	request := searchRequest()
	request.Question = ""
	state, err := s.SearchEvidence(t.Context(), request)
	if err == nil || state.Search != nil || state.Busy {
		t.Fatal("invalid request became a successful query")
	}
}

func TestEvidenceSearchClearedBySettingsAndNewWork(t *testing.T) {
	for _, action := range []string{"settings", "new-work"} {
		t.Run(action, func(t *testing.T) {
			s := searchService(t)
			if _, err := s.LoadEvidenceSearchDemo("mixed"); err != nil {
				t.Fatal(err)
			}
			var state State
			var err error
			if action == "settings" {
				settings := s.Snapshot().Settings
				settings.QueryLauncher = filepath.Join(s.dataDir, "different-unused-query")
				state, err = s.SaveSettings(settings)
			} else {
				state, err = s.LoadDemo()
			}
			if err != nil || state.Search != nil {
				t.Fatalf("old query carried across %s: %v", action, err)
			}
		})
	}
}

func TestEvidenceSearchNativeRequestHasNoLauncherOrAuthorityFields(t *testing.T) {
	body, err := json.Marshal(searchRequest())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 5 {
		t.Fatal("search request surface grew")
	}
	for _, field := range []string{"question", "sourceID", "admissionOutcome", "lifecycleScope", "limit"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("missing exact camelCase request field %s", field)
		}
	}
}
