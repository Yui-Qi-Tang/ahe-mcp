package logicresolver_test

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/internal/acceptance"
)

func TestDIMACSPreservesEveryClause(t *testing.T) {
	for name, c := range acceptance.Cases() {
		var raw bytes.Buffer
		if err := c.WriteDIMACS(&raw); err != nil {
			t.Fatal(err)
		}
		var marker, kind string
		var vars, clauses int
		if _, err := fmt.Fscan(&raw, &marker, &kind, &vars, &clauses); err != nil {
			t.Fatal(err)
		}
		if marker != "p" || kind != "cnf" || vars != len(c.Variables) || clauses != len(c.Clauses) {
			t.Fatalf("%s: header", name)
		}
		for i := 0; i < clauses; i++ {
			got := []int{}
			for {
				var lit int
				if _, err := fmt.Fscan(&raw, &lit); err != nil {
					t.Fatal(err)
				}
				if lit == 0 {
					break
				}
				got = append(got, lit)
			}
			if !reflect.DeepEqual(got, c.Clauses[i]) {
				t.Fatalf("%s: lost clause %d", name, i)
			}
		}
		if len(bytes.TrimSpace(raw.Bytes())) != 0 {
			t.Fatalf("%s: trailing formula", name)
		}
	}
}

func TestMalformedInputAndIncompleteAssignments(t *testing.T) {
	for _, c := range []logicresolver.CNF{
		{Variables: []logicresolver.Variable{{ID: 2}}},
		{Clauses: [][]int{{0}}},
		{Variables: []logicresolver.Variable{{ID: 1}}, Clauses: [][]int{{-2}}},
		{Clauses: [][]int{{}}, Origins: [][]string{}},
	} {
		if !errors.Is(c.CheckShape(), logicresolver.ErrInput) {
			t.Fatal("accepted malformed formula")
		}
	}
	c := acceptance.CNF(2, [][]int{{1}})
	for _, a := range []map[int]bool{{1: true}, {1: true, 3: true}, {1: false, 2: true}} {
		if c.CheckAssignment(a) == nil {
			t.Fatalf("accepted %v", a)
		}
	}
	if err := c.CheckAssignment(map[int]bool{1: true, 2: false}); err != nil {
		t.Fatal(err)
	}
}

func TestConsumerGuarantees(t *testing.T) {
	want := logicresolver.Requirements{SATModelChecked: true, UNSATProofChecked: true}
	for _, r := range []logicresolver.Result{
		{Status: logicresolver.Unknown},
		{Status: logicresolver.SAT},
		{Status: logicresolver.UNSAT},
		{Status: logicresolver.SAT, InputSHA256: "original", Model: logicresolver.Validation{Present: true, Decoded: true, Checked: true, TargetSHA256: "other"}},
	} {
		if !errors.Is(r.Require(want), logicresolver.ErrGuarantee) {
			t.Fatal("accepted insufficient guarantee")
		}
	}
}
