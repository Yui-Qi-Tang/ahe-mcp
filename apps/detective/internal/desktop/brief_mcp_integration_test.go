package desktop

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

// TestBriefDesktopNativeMCP is an opt-in synthetic operator-input witness, not
// authenticated human review or model-quality evaluation. Its explicit marker
// selects only the private socket lab created by scripts/brief-desktop-lab.sh.
func TestBriefDesktopNativeMCP(t *testing.T) {
	dir := os.Getenv("DETECTIVE_BRIEF_MCP_LAB")
	if dir == "" {
		t.Skip("requires an explicitly selected disposable Brief lab")
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !strings.HasPrefix(filepath.Base(dir), "brief-desktop-lab.") {
		t.Fatal("invalid explicit lab coordinate")
	}
	marker, err := os.ReadFile(filepath.Join(dir, "lab.marker"))
	if err != nil || string(marker) != "ahe-brief-desktop-lab/v1\n" {
		t.Fatal("missing lab marker")
	}
	var info struct {
		Database string `json:"database"`
		Schema   string `json:"schema"`
		Boundary string `json:"review_boundary"`
		Query    string `json:"query_launcher"`
		Intake   string `json:"intake_launcher"`
		Reviewer string `json:"review_launcher"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "lab-info.json"))
	if err != nil || json.Unmarshal(raw, &info) != nil || info.Database != "ahe_brief_lab" || info.Schema != "ahe_brief" || info.Boundary != "simulated_operator_fixture_only_not_authenticated_human_approval" {
		t.Fatal("wrong lab identity")
	}
	for _, path := range []string{info.Query, info.Intake, info.Reviewer} {
		if filepath.Dir(path) != dir {
			t.Fatal("launcher escaped selected lab")
		}
	}
	// No ambient profile is allowed. The protected launchers independently check
	// database, schema, LOGIN, role policy and exact live tool schemas.
	runDir, err := os.MkdirTemp(dir, "service-acceptance.")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic acceptance artifacts: %s", runDir)
	for _, outcome := range []string{"admit", "reject", "audit_only"} {
		t.Run(outcome, func(t *testing.T) {
			workspace := filepath.Join(runDir, outcome)
			s, err := New(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := sourcepilot.BriefSource{Version: sourcepilot.BriefSourceVersion, SourceID: "synthetic:desktop-brief:" + filepath.Base(runDir) + ":" + outcome, SourceRevision: "synthetic-v1", SourceURL: "https://example.invalid/brief-native-test", ObservedAt: "2026-09-11T00:00:00Z", Coverage: "full_document", Limitations: []string{}, Body: "The synthetic service reported increased errors.\nThe cause remains unconfirmed.\nOther services remain unaffected."}
			sourceRaw, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(runDir, outcome+"-source.json")
			if err := os.WriteFile(path, sourceRaw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ImportBriefSource(path); err != nil {
				t.Fatal(err)
			}
			statement := "The synthetic service reported increased errors; the cause remains unconfirmed."
			if outcome == "reject" {
				statement = "A deployment caused the synthetic service errors."
			}
			server := modelServer(t, func(w http.ResponseWriter, r *http.Request) { writeModelText(w, statement) })
			settings := s.Snapshot().Settings
			settings.Mode, settings.BaseURL, settings.IntakeLauncher, settings.QueryLauncher, settings.ReviewLauncher = "local", server.URL+"/v1", info.Intake, info.Query, info.Reviewer
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
			if err != nil {
				t.Fatalf("pending: %v", err)
			}
			if state.Brief.Outcome != "pending" || state.Brief.Decision != nil {
				t.Fatal("pending crossed review boundary")
			}
			state, err = s.PrepareBriefReview(t.Context(), state.Brief.Submission.Digest)
			if err != nil {
				t.Fatalf("prepare review: %v", err)
			}
			request := BriefReviewRequest{DisplayID: state.Brief.Review.Display.ID, Outcome: outcome, Reason: "Synthetic operator-input fixture only; not a human approval. "}
			switch outcome {
			case "admit":
				request.Reason += "The exact two source lines state increased errors and an unconfirmed cause."
			case "reject":
				request.Reason += "The source says the cause is unconfirmed; it does not attribute errors to a deployment."
			case "audit_only":
				request.Reason += "Retain this source-grounded test statement only for audit, not canonical evidence."
			}
			bad := request
			bad.DisplayID = "old-chat-admit"
			if _, err := s.ApplyBriefReview(t.Context(), bad); err == nil || s.Snapshot().Brief.Decision != nil {
				t.Fatal("wrong display caused a decision")
			}
			state, err = s.ApplyBriefReview(t.Context(), request)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if state.Brief.Outcome != outcome+"_verified" {
				t.Fatal("missing exact terminal verification")
			}
			if outcome != "admit" && state.Brief.Admission != nil {
				t.Fatal("noncanonical decision admitted")
			}
			// Reopen does not read the old source or acquire automatic authority.
			if _, err := s.OpenBriefWork(state.Brief.Path); err != nil {
				t.Fatal(err)
			}
			state, err = s.ApplyBriefReview(t.Context(), request)
			if err != nil {
				t.Fatalf("exact replay: %v", err)
			}
			changed := request
			changed.Reason += " Changed."
			if _, err := s.ApplyBriefReview(t.Context(), changed); err == nil {
				t.Fatal("saved decision reason changed")
			}
			state, err = s.QueryBriefPending(t.Context())
			if err != nil {
				t.Fatalf("query terminal: %v", err)
			}
			want := outcome
			if want == "admit" {
				want = "admitted"
			}
			if want == "reject" {
				want = "rejected"
			}
			if state.Brief.Outcome != want {
				t.Fatal("terminal query mismatch")
			}
			t.Logf("outcome=%s occurrence=%s work=%s", want, state.Brief.Handoff.ProposalOccurrenceID, state.Brief.Path)
		})
	}
}
