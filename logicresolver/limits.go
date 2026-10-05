package logicresolver

import (
	"fmt"
	"time"
)

// Limits bounds one Solve call. Timeout covers all backend processes together;
// caller deadlines may be shorter. Input encoding/checking is synchronous and
// bounded by the size limits, with context checks at stage boundaries.
// Consumers additionally bound their total calls and simultaneous Solve calls.
type Limits struct {
	Timeout        time.Duration
	MaxVariables   int
	MaxClauses     int
	MaxLiterals    int
	MaxInputBytes  int
	MaxOutputBytes int64
}

// WithDefaults validates limits and fills zero values with conservative defaults.
func (l Limits) WithDefaults() (Limits, error) {
	if l.Timeout < 0 || l.MaxVariables < 0 || l.MaxClauses < 0 || l.MaxLiterals < 0 || l.MaxInputBytes < 0 || l.MaxOutputBytes < 0 {
		return l, fmt.Errorf("%w: negative limit", ErrInput)
	}
	if l.Timeout == 0 {
		l.Timeout = 30 * time.Second
	}
	if l.MaxVariables == 0 {
		l.MaxVariables = 100000
	}
	if l.MaxClauses == 0 {
		l.MaxClauses = 1000000
	}
	if l.MaxLiterals == 0 {
		l.MaxLiterals = 4000000
	}
	if l.MaxInputBytes == 0 {
		l.MaxInputBytes = 16 << 20
	}
	if l.MaxOutputBytes == 0 {
		l.MaxOutputBytes = 16 << 20
	}
	return l, nil
}

// CheckCNF enforces allocation bounds before encoding a CNF formula.
func (l Limits) CheckCNF(c CNF) error {
	if len(c.Variables) > l.MaxVariables || len(c.Clauses) > l.MaxClauses {
		return ErrLimit
	}
	remaining := l.MaxLiterals
	for _, clause := range c.Clauses {
		if len(clause) > remaining {
			return ErrLimit
		}
		remaining -= len(clause)
	}
	return c.CheckShape()
}
