// Package logicresolver provides experimental v0 formula and verification records.
// It grants no authority to admit, choose or withdraw application evidence.
package logicresolver

import (
	"fmt"
	"io"
)

// Variable binds a contiguous DIMACS ID to opaque consumer metadata.
// Metadata is retained verbatim; it is not interpreted as a logical condition.
type Variable struct {
	ID       int               `json:"id"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// CNF is a conjunction of clauses, each a disjunction of signed variable IDs.
// No clauses means true. An empty clause means false. Origins optionally maps
// each clause to all contributing consumer condition IDs (many-to-many).
// The caller must not mutate inputs while Solve is running.
type CNF struct {
	Variables []Variable `json:"variables"`
	Clauses   [][]int    `json:"clauses"`
	Origins   [][]string `json:"origins,omitempty"`
}

// CheckShape verifies DIMACS bounds before tools consume a formula.
func (c CNF) CheckShape() error {
	if c.Origins != nil && len(c.Origins) != len(c.Clauses) {
		return fmt.Errorf("%w: origins must cover every clause", ErrInput)
	}
	for _, origins := range c.Origins {
		for _, id := range origins {
			if id == "" {
				return fmt.Errorf("%w: empty origin ID", ErrInput)
			}
		}
	}

	for i, variable := range c.Variables {
		if variable.ID != i+1 {
			return fmt.Errorf("%w: variable IDs are not contiguous", ErrInput)
		}
	}
	for i, clause := range c.Clauses {
		for _, lit := range clause {
			if lit == 0 || lit > len(c.Variables) || lit < -len(c.Variables) {
				return fmt.Errorf("%w: invalid literal in clause %d", ErrInput, i)
			}
		}
	}
	return nil
}

// WriteDIMACS writes the exact formula checked by the external tools.
func (c CNF) WriteDIMACS(w io.Writer) error {
	if err := c.CheckShape(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "p cnf %d %d\n", len(c.Variables), len(c.Clauses)); err != nil {
		return err
	}
	for _, clause := range c.Clauses {
		for _, lit := range clause {
			if _, err := fmt.Fprintf(w, "%d ", lit); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, "0"); err != nil {
			return err
		}
	}
	return nil
}

// CheckAssignment requires a total assignment and checks every original clause.
func (c CNF) CheckAssignment(values map[int]bool) error {
	if err := c.CheckShape(); err != nil {
		return err
	}
	if len(values) != len(c.Variables) {
		return fmt.Errorf("assignment is not total")
	}
	for i := 1; i <= len(c.Variables); i++ {
		if _, ok := values[i]; !ok {
			return fmt.Errorf("assignment misses variable %d", i)
		}
	}
	for i, clause := range c.Clauses {
		satisfied := false
		for _, lit := range clause {
			id := lit
			if id < 0 {
				id = -id
			}
			if id < 1 || id > len(c.Variables) {
				return fmt.Errorf("%w: invalid literal in clause %d", ErrInput, i)
			}
			if values[id] == (lit > 0) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			return fmt.Errorf("assignment violates clause %d", i)
		}
	}
	return nil
}
