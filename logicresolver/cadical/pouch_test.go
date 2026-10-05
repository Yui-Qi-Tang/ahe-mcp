package cadical

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

// These consumer-owned types mirror the fixed Pouch planning contract. They
// exercise a possible adapter; they are not a replacement planner or product API.
type pouchVariable struct {
	ID        int    `json:"id"`
	Kind      string `json:"kind"`
	Time      int    `json:"time"`
	OtherTime int    `json:"other_time,omitempty"`
	Field     string `json:"field,omitempty"`
	Value     string `json:"value,omitempty"`
	Action    string `json:"action,omitempty"`
}
type pouchCNF struct {
	Variables []pouchVariable
	Clauses   [][]int
}
type pouchResult struct {
	Status     string
	Assignment map[int]bool
	Verified   bool
	Artifacts  []logicresolver.Artifact
}
type pouchSolver interface {
	Solve(context.Context, string, pouchCNF) (pouchResult, error)
}
type pouchAdapter struct{ runner *Runner }

func adapt(c pouchCNF) (logicresolver.CNF, error) {
	out := logicresolver.CNF{Variables: make([]logicresolver.Variable, len(c.Variables)), Clauses: c.Clauses}
	for i, v := range c.Variables {
		raw, err := json.Marshal(v)
		if err != nil {
			return out, err
		}
		out.Variables[i] = logicresolver.Variable{ID: v.ID, Metadata: map[string]string{"pouch.variable": string(raw)}}
	}
	return out, nil
}

func (a pouchAdapter) Solve(ctx context.Context, id string, c pouchCNF) (pouchResult, error) {
	cnf, err := adapt(c)
	if err != nil {
		return pouchResult{}, err
	}
	r, err := a.runner.Solve(ctx, id, cnf)
	legacy := pouchResult{Status: string(r.Status), Assignment: r.Assignment, Artifacts: r.Artifacts}
	if err != nil {
		return legacy, err
	}
	if err = r.Require(logicresolver.Requirements{SATModelChecked: true, UNSATProofChecked: true}); err != nil {
		return legacy, err
	}
	legacy.Verified = true
	return legacy, nil
}

// Mirrors Search's context/budget -> Solve -> error -> verified/status order.
// Search's path enumeration, trace reconstruction and admissions are outside it.
type pouchConsumer struct {
	solver              pouchSolver
	maxQueries, queries int
}

var errQueries = errors.New("query budget exhausted")

func (c *pouchConsumer) query(ctx context.Context, cnf pouchCNF) (pouchResult, error) {
	if err := ctx.Err(); err != nil {
		return pouchResult{}, err
	}
	if c.queries >= c.maxQueries {
		return pouchResult{}, errQueries
	}
	id := fmt.Sprintf("q%06d", c.queries)
	c.queries++
	r, err := c.solver.Solve(ctx, id, cnf)
	if err != nil {
		return r, err
	}
	if !r.Verified || (r.Status != "SAT" && r.Status != "UNSAT") {
		return r, errors.New("solver result lacks verified status")
	}
	return r, nil
}

func pouchFixture(t *testing.T, name string) pouchCNF {
	t.Helper()
	dir := filepath.Join("testdata", "pouch", name)
	meta, err := os.ReadFile(filepath.Join(dir, "variables.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c pouchCNF
	if err = json.Unmarshal(meta, &c.Variables); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "input.cnf"))
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.NewReader(raw)
	var p, kind string
	var variables, clauses int
	if _, err = fmt.Fscan(input, &p, &kind, &variables, &clauses); err != nil {
		t.Fatal(err)
	}
	if p != "p" || kind != "cnf" || variables != len(c.Variables) {
		t.Fatal("invalid fixture header")
	}
	for range clauses {
		clause := []int{}
		for {
			var lit int
			if _, err = fmt.Fscan(input, &lit); err != nil {
				t.Fatal(err)
			}
			if lit == 0 {
				break
			}
			clause = append(clause, lit)
		}
		c.Clauses = append(c.Clauses, clause)
	}
	return c
}

func TestPouchMappingAndExactEncoding(t *testing.T) {
	for _, name := range []string{"sat", "unsat"} {
		t.Run(name, func(t *testing.T) {
			old := pouchFixture(t, name)
			mapped, err := adapt(old)
			if err != nil {
				t.Fatal(err)
			}
			for i, v := range mapped.Variables {
				var decoded pouchVariable
				if err = json.Unmarshal([]byte(v.Metadata["pouch.variable"]), &decoded); err != nil || !reflect.DeepEqual(decoded, old.Variables[i]) {
					t.Fatalf("variable %d changed: %v", i, err)
				}
			}
			var encoded bytes.Buffer
			if err = mapped.WriteDIMACS(&encoded); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(filepath.Join("testdata", "pouch", name, "input.cnf"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded.Bytes(), original) {
				t.Fatal("encoded formula differs from frozen Pouch input")
			}
		})
	}
}

func TestPouchConsumerErrorsLimitsAndLifecycle(t *testing.T) {
	cnf := pouchCNF{Variables: []pouchVariable{{ID: 1, Kind: "action", Time: 2, Action: "rotate"}}, Clauses: [][]int{{1}}}
	for _, c := range []struct {
		body string
		want error
	}{
		{"echo 's UNKNOWN'; exit 0", logicresolver.ErrGuarantee},
		{"echo 's SATISFIABLE'; echo 'v -1 0'; exit 10", logicresolver.ErrValidation},
		{"sleep 30", context.DeadlineExceeded},
	} {
		timeout := 5 * time.Second
		if errors.Is(c.want, context.DeadlineExceeded) {
			timeout = 50 * time.Millisecond
		}
		r := fixtureRunner(t, c.body, "exit 1", timeout)
		consumer := pouchConsumer{solver: pouchAdapter{runner: r}, maxQueries: 1}
		got, err := consumer.query(context.Background(), cnf)
		if !errors.Is(err, c.want) || got.Verified {
			t.Fatalf("wrong error/verification: %v %+v", err, got)
		}
		if _, err = consumer.query(context.Background(), cnf); !errors.Is(err, errQueries) {
			t.Fatal("consumer lost query budget")
		}
	}
	r := fixtureRunner(t, "echo 's SATISFIABLE'; echo 'v 1 0'; exit 10", "exit 1", time.Second)
	consumer := pouchConsumer{solver: pouchAdapter{runner: r}, maxQueries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := consumer.query(ctx, cnf); !errors.Is(err, context.Canceled) || consumer.queries != 0 {
		t.Fatal("context lost")
	}
	got, err := consumer.query(context.Background(), cnf)
	if err != nil || !got.Verified || !got.Assignment[1] {
		t.Fatalf("%+v %v", got, err)
	}
	// No Close is needed: Solve waits for processes; retained files belong to caller.
	if _, err = os.Stat(filepath.Join(r.root, "q000000", "outcome.json")); err != nil {
		t.Fatal(err)
	}
}
