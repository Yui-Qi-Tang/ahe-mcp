//go:build resolverintegration

package z3

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/internal/acceptance"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/linear"
)

func TestRealTools(t *testing.T) {
	for _, name := range []string{"AHE_LOGIC_Z3", "AHE_LOGIC_Z3_SHA256", "AHE_LOGIC_ARTIFACTS"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s required for explicit acceptance", name)
		}
	}
	r, err := New(Config{SolverPath: os.Getenv("AHE_LOGIC_Z3"), SolverSHA256: os.Getenv("AHE_LOGIC_Z3_SHA256"), ArtifactDir: filepath.Join(os.Getenv("AHE_LOGIC_ARTIFACTS"), "z3")})
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, name string, p linear.Problem, want logicresolver.Status) {
		t.Helper()
		got, err := r.Solve(context.Background(), name, p)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != want {
			t.Fatalf("want %s got %+v", want, got)
		}
		if got.Status == logicresolver.SAT {
			if err = got.Require(logicresolver.Requirements{SATModelChecked: true}); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(got.Require(logicresolver.Requirements{UNSATProofChecked: true}), logicresolver.ErrGuarantee) {
			t.Fatal("UNSAT must remain unchecked")
		}
	}
	// Same fixed 512 Boolean inputs and independent enumerating oracle as SAT.
	formulas := acceptance.Cases()
	names := []string{}
	for name := range formulas {
		if strings.HasPrefix(name, "matrix-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			c := formulas[name]
			check(t, name, linear.Problem{Booleans: c}, acceptance.Oracle(c))
		})
	}
	constraint := func(coefficient, rhs string, op linear.Relation) linear.Constraint {
		return linear.Constraint{Terms: []linear.Term{{Variable: 1, Coefficient: coefficient}}, Relation: op, RHS: rhs}
	}
	integer := []logicresolver.Variable{{ID: 1}}
	cases := []struct {
		p    linear.Problem
		want logicresolver.Status
	}{
		{linear.Problem{}, logicresolver.SAT},
		{linear.Problem{Booleans: acceptance.CNF(1, [][]int{{1}, {-1}})}, logicresolver.UNSAT},
		{linear.Problem{Integers: integer, Constraints: []linear.Constraint{constraint("1", "-2", linear.Equal)}}, logicresolver.SAT},
		{linear.Problem{Integers: integer, Constraints: []linear.Constraint{constraint("1", "3", linear.AtLeast), constraint("1", "5", linear.AtMost)}}, logicresolver.SAT},
		{linear.Problem{Integers: integer, Constraints: []linear.Constraint{constraint("1", "6", linear.AtLeast), constraint("1", "5", linear.AtMost)}}, logicresolver.UNSAT},
		{linear.Problem{Integers: integer, Constraints: []linear.Constraint{constraint("2", "1", linear.Equal)}}, logicresolver.UNSAT},
		{linear.Problem{Integers: []logicresolver.Variable{{ID: 1}, {ID: 2}}, Constraints: []linear.Constraint{
			{Terms: []linear.Term{{Variable: 1, Coefficient: "1"}, {Variable: 2, Coefficient: "1"}}, Relation: linear.Equal, RHS: "4"},
			{Terms: []linear.Term{{Variable: 1, Coefficient: "1"}, {Variable: 2, Coefficient: "-1"}}, Relation: linear.Equal, RHS: "2"},
		}}, logicresolver.SAT},
		{linear.Problem{Booleans: acceptance.CNF(1, [][]int{{1}}), Integers: integer, Constraints: []linear.Constraint{constraint("1", "0", linear.AtLeast), constraint("1", "0", linear.AtMost)}}, logicresolver.SAT},
	}
	for i, c := range cases {
		name := fmt.Sprintf("linear-%d", i)
		t.Run(name, func(t *testing.T) { check(t, name, c.p, c.want) })
	}
}
