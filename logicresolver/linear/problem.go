// Package linear defines a deliberately small QF_LIA profile: Boolean CNF and
// a conjunction of linear integer comparisons. It accepts no SMT-LIB source,
// quantifiers, arrays, nonlinear terms or mixed Boolean/arithmetic disjunctions.
package linear

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

// Relation compares a linear sum with a constant integer.
type Relation string

const (
	// Equal requires equality.
	Equal Relation = "="
	// AtMost requires the sum to be at most the right-hand side.
	AtMost Relation = "<="
	// AtLeast requires the sum to be at least the right-hand side.
	AtLeast Relation = ">="
)

// Term is a constant integer coefficient times an integer variable ID.
type Term struct {
	Variable    int    `json:"variable"`
	Coefficient string `json:"coefficient"`
}

// Constraint compares a sum of Terms to RHS. An empty sum is zero.
// Origins retains every contributing condition ID without interpreting it.
type Constraint struct {
	Terms    []Term   `json:"terms"`
	Relation Relation `json:"relation"`
	RHS      string   `json:"rhs"`
	Origins  []string `json:"origins,omitempty"`
}

// Problem keeps Boolean and integer ID spaces distinct and one-based.
type Problem struct {
	Booleans    logicresolver.CNF        `json:"booleans"`
	Integers    []logicresolver.Variable `json:"integers"`
	Constraints []Constraint             `json:"constraints"`
}

// Assignment is a total interpretation, with exact decimal integer values.
type Assignment struct {
	Booleans map[int]bool   `json:"booleans"`
	Integers map[int]string `json:"integers"`
}

var decimal = regexp.MustCompile(`^(0|-[1-9][0-9]*|[1-9][0-9]*)$`)

// Integer parses a canonical decimal integer without machine-size overflow.
func Integer(s string) (*big.Int, error) {
	if !decimal.MatchString(s) {
		return nil, fmt.Errorf("%w: noncanonical integer", logicresolver.ErrInput)
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, logicresolver.ErrInput
	}
	return n, nil
}

// CheckShape validates IDs, sorts and the supported operators.
func (p Problem) CheckShape() error {
	if err := p.Booleans.CheckShape(); err != nil {
		return err
	}
	for i, v := range p.Integers {
		if v.ID != i+1 {
			return fmt.Errorf("%w: integer IDs", logicresolver.ErrInput)
		}
	}
	for _, c := range p.Constraints {
		if c.Relation != Equal && c.Relation != AtMost && c.Relation != AtLeast {
			return fmt.Errorf("%w: comparison %q", logicresolver.ErrUnsupported, c.Relation)
		}
		if _, err := Integer(c.RHS); err != nil {
			return err
		}
		for _, term := range c.Terms {
			if term.Variable < 1 || term.Variable > len(p.Integers) {
				return fmt.Errorf("%w: integer variable ID", logicresolver.ErrInput)
			}
			if _, err := Integer(term.Coefficient); err != nil {
				return err
			}
		}
		for _, id := range c.Origins {
			if id == "" {
				return fmt.Errorf("%w: empty origin", logicresolver.ErrInput)
			}
		}
	}
	return nil
}

// CheckLimits bounds dimensions and term counts before encoding.
func (p Problem) CheckLimits(l logicresolver.Limits) error {
	if len(p.Booleans.Variables) > l.MaxVariables || len(p.Integers) > l.MaxVariables-len(p.Booleans.Variables) {
		return logicresolver.ErrLimit
	}
	if len(p.Booleans.Clauses) > l.MaxClauses || len(p.Constraints) > l.MaxClauses-len(p.Booleans.Clauses) {
		return logicresolver.ErrLimit
	}
	remaining := l.MaxLiterals
	for _, clause := range p.Booleans.Clauses {
		if len(clause) > remaining {
			return logicresolver.ErrLimit
		}
		remaining -= len(clause)
	}
	for _, c := range p.Constraints {
		if len(c.Terms) > remaining {
			return logicresolver.ErrLimit
		}
		remaining -= len(c.Terms)
	}
	return p.CheckShape()
}

// SMTLIB encodes exactly this profile without check-sat or model commands.
func (p Problem) SMTLIB() (string, error) {
	if err := p.CheckShape(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("(set-logic QF_LIA)\n")
	for _, v := range p.Booleans.Variables {
		fmt.Fprintf(&b, "(declare-const b%d Bool)\n", v.ID)
	}
	for _, v := range p.Integers {
		fmt.Fprintf(&b, "(declare-const i%d Int)\n", v.ID)
	}
	for _, clause := range p.Booleans.Clauses {
		if len(clause) == 0 {
			b.WriteString("(assert false)\n")
			continue
		}
		b.WriteString("(assert (or")
		for _, lit := range clause {
			if lit < 0 {
				fmt.Fprintf(&b, " (not b%d)", -lit)
			} else {
				fmt.Fprintf(&b, " b%d", lit)
			}
		}
		b.WriteString("))\n")
	}
	for _, c := range p.Constraints {
		fmt.Fprintf(&b, "(assert (%s (+ 0", c.Relation)
		for _, term := range c.Terms {
			fmt.Fprintf(&b, " (* %s i%d)", numeral(term.Coefficient), term.Variable)
		}
		fmt.Fprintf(&b, ") %s))\n", numeral(c.RHS))
	}
	return b.String(), nil
}

func numeral(s string) string {
	if strings.HasPrefix(s, "-") {
		return "(- " + s[1:] + ")"
	}
	return s
}

// CheckAssignment checks all original constraints with exact integer arithmetic.
// This evaluates a witness; it cannot prove an UNSAT response.
func (p Problem) CheckAssignment(a Assignment) error {
	if err := p.CheckShape(); err != nil {
		return err
	}
	if err := p.Booleans.CheckAssignment(a.Booleans); err != nil {
		return err
	}
	if len(a.Integers) != len(p.Integers) {
		return fmt.Errorf("integer model is not total")
	}
	values := make(map[int]*big.Int, len(p.Integers))
	for _, v := range p.Integers {
		n, err := Integer(a.Integers[v.ID])
		if err != nil {
			return err
		}
		values[v.ID] = n
	}
	for i, c := range p.Constraints {
		sum := new(big.Int)
		for _, term := range c.Terms {
			coefficient, _ := Integer(term.Coefficient) // Checked by CheckShape.
			sum.Add(sum, new(big.Int).Mul(coefficient, values[term.Variable]))
		}
		rhs, _ := Integer(c.RHS) // Checked by CheckShape.
		cmp := sum.Cmp(rhs)
		if (c.Relation == Equal && cmp != 0) || (c.Relation == AtMost && cmp > 0) || (c.Relation == AtLeast && cmp < 0) {
			return fmt.Errorf("integer model violates constraint %d", i)
		}
	}
	return nil
}
