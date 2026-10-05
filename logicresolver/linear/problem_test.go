package linear

import (
	"errors"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

func TestExactIntegersAndUnsupportedInputs(t *testing.T) {
	p := Problem{Integers: []logicresolver.Variable{{ID: 1}}, Constraints: []Constraint{{Terms: []Term{{Variable: 1, Coefficient: "2"}}, Relation: Equal, RHS: "18446744073709551616"}}}
	if err := p.CheckAssignment(Assignment{Integers: map[int]string{1: "9223372036854775808"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckAssignment(Assignment{Integers: map[int]string{1: "9223372036854775807"}}); err == nil {
		t.Fatal("accepted off-by-one model")
	}
	p.Constraints[0].Relation = "forall"
	if !errors.Is(p.CheckShape(), logicresolver.ErrUnsupported) {
		t.Fatal("accepted unsupported operator")
	}
	p.Constraints[0].Relation = Equal
	p.Constraints[0].Terms[0].Variable = 2
	if !errors.Is(p.CheckShape(), logicresolver.ErrInput) {
		t.Fatal("accepted wrong variable sort/ID")
	}
	for _, s := range []string{"1.5", "(+ 1 2)", "-0", "01", "", "true"} {
		if _, err := Integer(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestTotalModelAndNegativeCoefficients(t *testing.T) {
	p := Problem{Integers: []logicresolver.Variable{{ID: 1}, {ID: 2}}, Constraints: []Constraint{{Terms: []Term{{Variable: 1, Coefficient: "-3"}, {Variable: 2, Coefficient: "2"}}, Relation: AtLeast, RHS: "8"}}}
	for _, a := range []Assignment{{Integers: map[int]string{1: "-2"}}, {Integers: map[int]string{1: "-2", 3: "1"}}, {Integers: map[int]string{1: "-1", 2: "1"}}} {
		if p.CheckAssignment(a) == nil {
			t.Fatal("accepted invalid model")
		}
	}
	if err := p.CheckAssignment(Assignment{Integers: map[int]string{1: "-2", 2: "1"}}); err != nil {
		t.Fatal(err)
	}
}
