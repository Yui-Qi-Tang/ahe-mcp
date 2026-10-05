package cadical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

func TestRejectedToolResponses(t *testing.T) {
	cases := []struct {
		name, solver, checker string
		want                  error
		status                logicresolver.Status
	}{
		{"wrong-exit", "echo 's SATISFIABLE'; echo 'v 1 0'; exit 0", "exit 0", logicresolver.ErrProtocol, logicresolver.Unknown},
		{"sat-and-unknown", "echo 's SATISFIABLE'; echo 's UNKNOWN'; echo 'v 1 0'; exit 10", "exit 0", logicresolver.ErrProtocol, logicresolver.Unknown},
		{"dual-status", "echo 's SATISFIABLE'; echo 's UNSATISFIABLE'; exit 10", "exit 0", logicresolver.ErrProtocol, logicresolver.Unknown},
		{"no-model", "echo 's SATISFIABLE'; exit 10", "exit 0", logicresolver.ErrProtocol, logicresolver.SAT},
		{"partial-model", "echo 's SATISFIABLE'; echo 'v 0'; exit 10", "exit 0", logicresolver.ErrProtocol, logicresolver.SAT},
		{"missing-proof", "echo 's UNSATISFIABLE'; exit 20", "exit 0", logicresolver.ErrProtocol, logicresolver.UNSAT},
		{"false-proof", "touch proof.drat; echo 's UNSATISFIABLE'; exit 20", "echo 's NOT VERIFIED'; exit 0", logicresolver.ErrValidation, logicresolver.UNSAT},
		{"incorrect-model", "echo 's SATISFIABLE'; echo 'v -1 0'; exit 10", "exit 0", logicresolver.ErrValidation, logicresolver.SAT},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := fixtureRunner(t, c.solver, c.checker, 5*time.Second)
			got, err := r.Solve(context.Background(), c.name, tinyCNF())
			if !errors.Is(err, c.want) || got.Status != c.status || got.Model.Checked || got.Proof.Checked {
				t.Fatalf("got %+v, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(r.root, c.name, "outcome.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLimitsCancellationAndUnknown(t *testing.T) {
	r := fixtureRunner(t, "echo 's UNKNOWN'; exit 0", "exit 1", time.Second)
	got, err := r.Solve(context.Background(), "unknown", tinyCNF())
	if err != nil || got.Status != logicresolver.Unknown || !errors.Is(got.Require(logicresolver.Requirements{}), logicresolver.ErrGuarantee) {
		t.Fatalf("%+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.Solve(ctx, "canceled", tinyCNF()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r.config.Limits.MaxVariables = 1
	c := tinyCNF()
	c.Variables = append(c.Variables, logicresolver.Variable{ID: 2})
	if _, err = r.Solve(context.Background(), "limit", c); !errors.Is(err, logicresolver.ErrLimit) {
		t.Fatal(err)
	}
	r = fixtureRunner(t, "while :; do echo lots-of-output; done", "exit 1", time.Second)
	r.config.Limits.MaxOutputBytes = 64
	if _, err = r.Solve(context.Background(), "output", tinyCNF()); !errors.Is(err, logicresolver.ErrLimit) {
		t.Fatal(err)
	}
	r = fixtureRunner(t, "printf 'proof-too-large' > proof.drat; echo 's UNSATISFIABLE'; exit 20", "exit 1", time.Second)
	r.config.MaxProofBytes = 2
	if _, err = r.Solve(context.Background(), "proof-limit", tinyCNF()); !errors.Is(err, logicresolver.ErrLimit) {
		t.Fatal(err)
	}
}

func TestConcurrentIsolationAndMapping(t *testing.T) {
	r := fixtureRunner(t, "echo 's SATISFIABLE'; echo 'v 1 0'; exit 10", "exit 1", time.Second)
	c := tinyCNF()
	c.Variables[0].Metadata = map[string]string{"kind": "action", "time": "0", "action": "rotate", "other_time": "2"}
	c.Origins = [][]string{{"rule-power-to-light", "fact-power"}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("parallel-%d", i)
			got, err := r.Solve(context.Background(), id, c)
			if err != nil || !got.Model.Checked {
				t.Errorf("%s: %v", id, err)
				return
			}
			raw, err := os.ReadFile(filepath.Join(r.root, id, "input.json"))
			if err != nil {
				t.Error(err)
				return
			}
			var saved logicresolver.CNF
			if err = json.Unmarshal(raw, &saved); err != nil || !reflect.DeepEqual(saved, c) {
				t.Errorf("mapping changed: %v", err)
			}
		}(i)
	}
	wg.Wait()
	// Simultaneous identical IDs must have exactly one owner.
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := r.Solve(context.Background(), "same", c); results <- err }()
	}
	e1, e2 := <-results, <-results
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("expected one winner: %v / %v", e1, e2)
	}
}
