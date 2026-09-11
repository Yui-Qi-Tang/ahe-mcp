package desktop

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

// TestBriefRecoveryPrepareNative is an explicit synthetic-operator integration
// fixture. It leaves genuine Service checkpoints for a separate native UI run;
// it is not itself a Wails UI test or authenticated human review.
func TestBriefRecoveryPrepareNative(t *testing.T) {
	lab := os.Getenv("DETECTIVE_BRIEF_RECOVERY_LAB")
	if lab == "" {
		t.Skip("requires an explicitly selected new disposable recovery lab")
	}
	buildDir, err := briefRecoveryBuildDirectory()
	if err != nil || !briefRecoveryLabCoordinate(lab, buildDir) {
		t.Fatal("invalid recovery lab coordinate")
	}
	if info, err := os.Lstat(lab); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedSourceFile(info) {
		t.Fatal("recovery lab must be private and owned by this user")
	}
	marker, err := os.ReadFile(filepath.Join(lab, "lab.marker"))
	if err != nil || string(marker) != "ahe-brief-desktop-lab/v1\n" {
		t.Fatal("missing recovery lab marker")
	}
	var info struct {
		Database string `json:"database"`
		Schema   string `json:"schema"`
		Boundary string `json:"review_boundary"`
		Query    string `json:"query_launcher"`
		Intake   string `json:"intake_launcher"`
		Reviewer string `json:"review_launcher"`
	}
	raw, err := os.ReadFile(filepath.Join(lab, "lab-info.json"))
	if err != nil || json.Unmarshal(raw, &info) != nil || info.Database != "ahe_brief_lab" || info.Schema != "ahe_brief" || info.Boundary != "simulated_operator_fixture_only_not_authenticated_human_approval" {
		t.Fatal("wrong recovery lab identity")
	}
	for _, launcher := range []string{info.Query, info.Intake, info.Reviewer} {
		if filepath.Dir(launcher) != lab {
			t.Fatal("launcher escaped recovery lab")
		}
	}
	proxyBinary := filepath.Join(lab, "bin", "desktop-recovery.test")
	if info, err := os.Stat(proxyBinary); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("build the retained recovery proxy test binary first")
	}
	runDir, err := os.MkdirTemp(lab, "recovery-acceptance.")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native recovery fixture: %s", runDir)
	var cases []map[string]any
	for _, name := range []string{"admit", "reject", "audit_only", "pending-reject"} {
		t.Run(name, func(t *testing.T) {
			decision, fault := name, "after_write_once"
			if name == "pending-reject" {
				decision, fault = "reject", "before_write_once"
			}
			dir := filepath.Join(runDir, name)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			tool := "record_reviewed_source_claim_disposition"
			if decision == "admit" {
				tool = "admit_reviewed_source_claim"
			}
			writeRecoverySeedJSON(t, filepath.Join(dir, "proxy.json"), map[string]string{"upstream": info.Reviewer, "fault": fault, "target_tool": tool})
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			proxy := filepath.Join(dir, "review-launcher")
			script := "#!/bin/sh\nexport DETECTIVE_RECOVERY_PROXY_CASE=" + quote(dir) + "\nexec " + quote(proxyBinary) + " -test.run='^TestBriefRecoveryProxy$'\n"
			if err := os.WriteFile(proxy, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(dir, "workspace")
			s, err := New(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := sourcepilot.BriefSource{Version: sourcepilot.BriefSourceVersion,
				SourceID:       "synthetic:recovery:" + filepath.Base(runDir) + ":" + name,
				SourceRevision: "synthetic-v1", SourceURL: "https://example.invalid/recovery",
				ObservedAt: "2026-09-12T00:00:00Z", Coverage: "full_document", Limitations: []string{},
				Body: "The synthetic service reported increased errors.\nThe cause remains unconfirmed.\nOther services remain unaffected."}
			sourcePath := filepath.Join(dir, "source.json")
			writeRecoverySeedJSON(t, sourcePath, source)
			if _, err := s.ImportBriefSource(sourcePath); err != nil {
				t.Fatal(err)
			}
			statement := "The synthetic service reported increased errors; the cause remains unconfirmed."
			reason := "Synthetic operator-input fixture only; not a human approval. "
			switch decision {
			case "admit":
				reason += "The exact two source lines report increased errors and an unconfirmed cause."
			case "reject":
				statement = "A deployment caused the synthetic service errors."
				reason += "The source says the cause is unconfirmed and does not attribute it to a deployment."
			case "audit_only":
				reason += "Retain the source-grounded statement for audit only, not canonical evidence."
			}
			server := modelServer(t, func(w http.ResponseWriter, _ *http.Request) { writeModelText(w, statement) })
			settings := s.Snapshot().Settings
			settings.Mode, settings.BaseURL = "local", server.URL+"/v1"
			settings.IntakeLauncher, settings.QueryLauncher, settings.ReviewLauncher = info.Intake, info.Query, proxy
			if _, err := s.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			state, err := s.ExtractBrief(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			state, err = s.PrepareBriefCandidate(BriefCandidateRequest{Statement: statement, StartLine: 1, EndLine: 2, SourceSHA256: state.Brief.Report.BodySHA256})
			if err != nil {
				t.Fatal(err)
			}
			state, err = s.SubmitBriefPending(t.Context(), state.Brief.Submission.Digest)
			if err != nil || state.Brief.Outcome != "pending" {
				t.Fatalf("pending fixture: %v", err)
			}
			state, err = s.PrepareBriefReview(t.Context(), state.Brief.Submission.Digest)
			if err != nil {
				t.Fatalf("review fixture: %v", err)
			}
			request := BriefReviewRequest{DisplayID: state.Brief.Review.Display.ID, Outcome: decision, Reason: reason}
			writeRecoverySeedJSON(t, filepath.Join(dir, "simulated-decision.json"), request)
			state, err = s.ApplyBriefReview(t.Context(), request)
			if err == nil || state.Brief.Outcome != "decision_saved" || state.Brief.FailureStage != "review_write" || state.Brief.Admission != nil || state.Brief.Disposition != nil {
				t.Fatal("injected write failure did not preserve an unacknowledged decision")
			}
			before := *state.Brief.Decision
			preQuery := state.Brief.Path
			preBytes, err := os.ReadFile(preQuery)
			if err != nil {
				t.Fatal(err)
			}
			state, err = s.QueryBriefPending(t.Context())
			want := map[string]string{"admit": "admitted", "reject": "rejected", "audit_only": "audit_only", "pending-reject": "pending"}[name]
			if err != nil || state.Brief.Outcome != want || state.Brief.FailureStage != "" || !reflect.DeepEqual(*state.Brief.Decision, before) || state.Brief.Admission != nil || state.Brief.Disposition != nil {
				t.Fatalf("fault Query observation: %v", err)
			}
			after, err := os.ReadFile(preQuery)
			if err != nil || !bytes.Equal(preBytes, after) {
				t.Fatal("Query changed the immutable pre-call checkpoint")
			}
			settings.Mode, settings.BaseURL = "demo", "http://127.0.0.1:1/v1"
			if _, err := s.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			cases = append(cases, map[string]any{"name": name, "decision": request, "workspace": workspace,
				"workspace_id": state.WorkspaceID, "work_path": state.Brief.Path, "pre_query_path": preQuery,
				"occurrence_id": state.Brief.Handoff.ProposalOccurrenceID, "observed": want,
				"expected_verified": decision + "_verified", "expected_replayed": name != "pending-reject"})
		})
		if t.Failed() {
			t.Fatal("stop after first fixture failure; do not resume partial setup")
		}
	}
	writeRecoverySeedJSON(t, filepath.Join(runDir, "ui-cases.json"), cases)
}

func writeRecoverySeedJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(body)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("cannot preserve recovery fixture JSON")
	}
}
