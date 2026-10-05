package z3

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/linear"
)

func fixture(t *testing.T, body string) *Runner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	file := filepath.Join(t.TempDir(), "z3")
	raw := []byte("#!/bin/sh\n" + body + "\n")
	if err := os.WriteFile(file, raw, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{SolverPath: file, SolverSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), ArtifactDir: filepath.Join(t.TempDir(), "runs"), Limits: logicresolver.Limits{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUnknownAndUnverifiedUNSAT(t *testing.T) {
	for _, status := range []string{"unknown", "unsat"} {
		t.Run(status, func(t *testing.T) {
			r := fixture(t, "echo "+status)
			got, err := r.Solve(context.Background(), status, linear.Problem{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Proof.Present || got.Proof.Checked {
				t.Fatal("fabricated proof")
			}
			if !errors.Is(got.Require(logicresolver.Requirements{UNSATProofChecked: true}), logicresolver.ErrGuarantee) {
				t.Fatal("accepted missing guarantee")
			}
		})
	}
}

func TestBadModelAndProcessResponses(t *testing.T) {
	p := linear.Problem{Integers: []logicresolver.Variable{{ID: 1}}, Constraints: []linear.Constraint{{Terms: []linear.Term{{Variable: 1, Coefficient: "1"}}, Relation: linear.Equal, RHS: "1"}}}
	for _, c := range []struct {
		name, body string
		want       error
	}{
		{"no-model", "echo sat", logicresolver.ErrProtocol},
		{"wrong-model", "echo sat; if [ \"$2\" = model.smt2 ]; then echo '((i1 2))'; fi", logicresolver.ErrValidation},
		{"type-mismatch", "echo sat; if [ \"$2\" = model.smt2 ]; then echo '((i1 true))'; fi", logicresolver.ErrProtocol},
		{"bad-exit", "echo unsat; exit 1", logicresolver.ErrProtocol},
		{"timeout", "sleep 30", context.DeadlineExceeded},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := fixture(t, c.body)
			if c.name == "timeout" {
				r.config.Limits.Timeout = 50 * time.Millisecond
			}
			got, err := r.Solve(context.Background(), c.name, p)
			if !errors.Is(err, c.want) || got.Model.Checked {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestModelParserRejectsPartialAndExtraData(t *testing.T) {
	for _, s := range []string{"sat", "sat ((i1 2))", "sat ((b1 true)(b1 false))", "sat ((b1 1)(i1 2))", "sat ((b1 true)(i1 (/ 1 2)))", "sat ((b1 true)(i1 2)) extra"} {
		if _, err := parseAssignment([]byte(s), 1, 1); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	a, err := parseAssignment([]byte("sat\n((b1 false) (i1 (- 22)))"), 1, 1)
	if err != nil || a.Integers[1] != "-22" || a.Booleans[1] {
		t.Fatalf("%+v %v", a, err)
	}
}
