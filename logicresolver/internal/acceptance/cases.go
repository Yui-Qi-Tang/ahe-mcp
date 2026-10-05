// Package acceptance supplies frozen synthetic inputs to tool acceptance tests.
package acceptance

import (
	"fmt"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

// CNF creates a formula with the specified number of variables.
func CNF(n int, clauses [][]int) logicresolver.CNF {
	c := logicresolver.CNF{Variables: make([]logicresolver.Variable, n), Clauses: clauses}
	for i := range c.Variables {
		c.Variables[i].ID = i + 1
	}
	return c
}

// Cases returns 512 exhaustive two-variable formulas and eight frozen boundaries.
func Cases() map[string]logicresolver.CNF {
	clauses := [][]int{{}, {1}, {-1}, {2}, {-2}, {1, 2}, {1, -2}, {-1, 2}, {-1, -2}}
	out := map[string]logicresolver.CNF{}
	for mask := 0; mask < 512; mask++ {
		c := CNF(2, nil)
		for bit, clause := range clauses {
			if mask&(1<<bit) != 0 {
				c.Clauses = append(c.Clauses, clause)
			}
		}
		out[fmt.Sprintf("matrix-%03d", mask)] = c
	}
	edges := []logicresolver.CNF{CNF(0, nil), CNF(0, [][]int{{}}), CNF(1, [][]int{{1}}), CNF(1, [][]int{{1}, {-1}}), CNF(1, [][]int{{1}, {1}}), CNF(1, [][]int{{1, -1}}), CNF(3, [][]int{{1}, {-1, 2}, {-2, 3}, {-3}}), CNF(3, [][]int{{-1}, {-2, 1}, {-3, 2}, {3}})}
	for i, c := range edges {
		out[fmt.Sprintf("edge-%d", i)] = c
	}
	return out
}

// Oracle independently enumerates every interpretation of the small fixtures.
// It does not call the production assignment checker.
func Oracle(c logicresolver.CNF) logicresolver.Status {
	for mask := 0; mask < (1 << len(c.Variables)); mask++ {
		valid := true
		for _, clause := range c.Clauses {
			satisfied := false
			for _, lit := range clause {
				if lit > 0 {
					satisfied = satisfied || mask&(1<<(lit-1)) != 0
				} else {
					satisfied = satisfied || mask&(1<<(-lit-1)) == 0
				}
			}
			valid = valid && satisfied
		}
		if valid {
			return logicresolver.SAT
		}
	}
	return logicresolver.UNSAT
}
