package cadical

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

func pinnedScript(t *testing.T, body string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture requires POSIX")
	}
	path := filepath.Join(t.TempDir(), "tool")
	raw := []byte("#!/bin/sh\n" + body + "\n")
	if err := os.WriteFile(path, raw, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	return path, hex.EncodeToString(hash[:])
}
func fixtureRunner(t *testing.T, solverBody, checkerBody string, timeout time.Duration) *Runner {
	t.Helper()
	solver, solverHash := pinnedScript(t, solverBody)
	checker, checkerHash := pinnedScript(t, checkerBody)
	r, err := New(Config{SolverPath: solver, SolverSHA256: solverHash, CheckerPath: checker, CheckerSHA256: checkerHash, ArtifactDir: filepath.Join(t.TempDir(), "evidence"), Limits: logicresolver.Limits{Timeout: timeout}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func tinyCNF() logicresolver.CNF {
	return logicresolver.CNF{Variables: []logicresolver.Variable{{ID: 1, Metadata: map[string]string{"kind": "state"}}}, Clauses: [][]int{{1}}}
}
func TestCheckedSATAndArtifactIsolation(t *testing.T) {
	r := fixtureRunner(t, "echo 's SATISFIABLE'; echo 'v 1 0'; exit 10", "exit 1", time.Second)
	result, err := r.Solve(context.Background(), "q0", tinyCNF())
	if err != nil || !(result.Model.Checked || result.Proof.Checked) || result.Status != "SAT" {
		t.Fatalf("%+v %v", result, err)
	}
	if len(result.Artifacts) < 5 {
		t.Fatal("missing evidence")
	}
	for _, a := range result.Artifacts {
		if filepath.IsAbs(a.ID) || strings.Contains(a.ID, "..") {
			t.Fatal(a.ID)
		}
	}
	if _, err = r.Solve(context.Background(), "q0", tinyCNF()); err == nil {
		t.Fatal("overwrote query")
	}
	if _, err = r.Solve(context.Background(), "../escape", tinyCNF()); err == nil {
		t.Fatal("accepted traversal")
	}
}
func TestRejectWrongAssignmentAndCheckerExit(t *testing.T) {
	r := fixtureRunner(t, "echo 's SATISFIABLE'; echo 'v -1 0'; exit 10", "exit 1", time.Second)
	result, err := r.Solve(context.Background(), "wrong", tinyCNF())
	if err == nil || (result.Model.Checked || result.Proof.Checked) {
		t.Fatal("accepted invalid assignment")
	}
	r = fixtureRunner(t, "touch proof.drat; echo 's UNSATISFIABLE'; exit 20", "echo 's VERIFIED'; exit 1", time.Second)
	result, err = r.Solve(context.Background(), "wrong", tinyCNF())
	if err == nil || (result.Model.Checked || result.Proof.Checked) {
		t.Fatal("accepted checker VERIFIED with exit 1")
	}
}
func TestTimeoutKillsToolGroupAndPreservesEvidence(t *testing.T) {
	r := fixtureRunner(t, "sleep 30; echo 's SATISFIABLE'; exit 10", "exit 1", 30*time.Millisecond)
	start := time.Now()
	result, err := r.Solve(context.Background(), "timeout", tinyCNF())
	if !errors.Is(err, context.DeadlineExceeded) || (result.Model.Checked || result.Proof.Checked) {
		t.Fatalf("%+v %v", result, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("process cancellation did not complete")
	}
	if len(result.Artifacts) < 5 {
		t.Fatal("missing timeout artifacts")
	}
}
func TestPinsAndPrivateCopy(t *testing.T) {
	solver, hash := pinnedScript(t, "echo 's SATISFIABLE'; echo 'v 1 0'; exit 10")
	checker, checkHash := pinnedScript(t, "exit 1")
	cfg := Config{SolverPath: solver, SolverSHA256: strings.Repeat("0", 64), CheckerPath: checker, CheckerSHA256: checkHash, ArtifactDir: filepath.Join(t.TempDir(), "evidence")}
	if _, err := New(cfg); err == nil {
		t.Fatal("accepted wrong hash")
	}
	cfg.SolverSHA256 = hash
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(solver, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := r.Solve(context.Background(), "private", tinyCNF())
	if err != nil || !(result.Model.Checked || result.Proof.Checked) {
		t.Fatalf("caller edit changed pinned copy: %v", err)
	}
}
func TestAssignmentSyntax(t *testing.T) {
	for _, s := range []string{"v 1 -1 0\n", "v 2 0\n", "v 1\n", "v 0 1\n", "v true 0\n"} {
		if _, err := assignment([]byte(s), 1); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestExactStatusLineHandlesCRWithoutAcceptingWhitespace(t *testing.T) {
	if !exactLine([]byte("\rc progress\n\rs VERIFIED\n"), "s VERIFIED") {
		t.Fatal("CR line separators rejected")
	}
	for _, text := range []string{" s VERIFIED\n", "s VERIFIED extra\n", "c s VERIFIED\n"} {
		if exactLine([]byte(text), "s VERIFIED") {
			t.Fatalf("accepted %q", text)
		}
	}
}
