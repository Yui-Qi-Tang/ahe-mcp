package evidenceingestion

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/cadical"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConsistencyAtom is an exact Boolean identity. Context is opaque: differing
// contexts do not imply disjoint time intervals or any other domain semantics.
type ConsistencyAtom struct {
	Subject  string `json:"subject"`
	Property string `json:"property"`
	Context  string `json:"context"`
}

type ConsistencyLiteral struct {
	Atom    ConsistencyAtom `json:"atom"`
	Negated bool            `json:"negated"`
}

// ConsistencyCondition is a conjunction of clauses, each a disjunction of
// literals. An empty clause means false. At least one clause is required.
// Evidence IDs must be canonical node IDs; rule IDs have their own namespace.
type ConsistencyCondition struct {
	ID      string                 `json:"id"`
	Clauses [][]ConsistencyLiteral `json:"clauses"`
}

// ConsistencyRequest requires a previously inspected view and exactly one
// condition per policy-selected member. Conditions are supplied normalization;
// this API never extracts them or infers them from graph relations.
type ConsistencyRequest struct {
	Scope          ConsistencyScope       `json:"scope"`
	ExpectedViewID string                 `json:"expected_view_id"`
	RuleVersion    string                 `json:"rule_version"`
	Rules          []ConsistencyCondition `json:"rules"`
	Evidence       []ConsistencyCondition `json:"evidence"`
}

type ConsistencyOutcome string

const (
	ConsistencyInconclusive ConsistencyOutcome = "inconclusive"
	ConsistencyCompatible   ConsistencyOutcome = "compatible_declarations"
	ConsistencyConflict     ConsistencyOutcome = "conflicting_declarations"
	ConsistencyInvalidRules ConsistencyOutcome = "inconsistent_rules"
)

// ConsistencyResult describes the entire checked set, not a minimal conflict
// subset or evidence blame. It is historical as of View.Snapshot; later writes
// require another check. Errors always leave Outcome inconclusive.
type ConsistencyResult struct {
	Outcome      ConsistencyOutcome `json:"outcome"`
	Reason       string             `json:"reason,omitempty"`
	View         ConsistencyView    `json:"view"`
	InputID      string             `json:"input_id,omitempty"`
	RuleVersion  string             `json:"rule_version,omitempty"`
	Rules        cadical.Result     `json:"rules"`
	Declarations cadical.Result     `json:"declarations"`
}

// CheckCanonicalConsistency rereads the complete scope before consuming a pinned
// SAT runner. queryID must be unique within that runner. Both calls share a
// 30-second deadline. Scope.Policy determines participation; this call does not
// write or infer causality. Inspect err AND Outcome, not just a raw solver answer.
func CheckCanonicalConsistency(ctx context.Context, pool *pgxpool.Pool, runner *cadical.Runner, queryID string, in ConsistencyRequest) (ConsistencyResult, error) {
	return checkConsistency(ctx, runner, queryID, in, func(ctx context.Context, scope ConsistencyScope) (ConsistencyView, error) {
		return ReadConsistencyScope(ctx, pool, scope)
	})
}

func checkConsistency(ctx context.Context, runner *cadical.Runner, queryID string, in ConsistencyRequest, read func(context.Context, ConsistencyScope) (ConsistencyView, error)) (ConsistencyResult, error) {
	out := ConsistencyResult{Outcome: ConsistencyInconclusive, Reason: "read_failed"}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	view, err := read(ctx, in.Scope)
	if err != nil {
		return out, err
	}
	out.View, out.RuleVersion = view, in.RuleVersion
	out.Reason = "invalid_input"
	if in.ExpectedViewID == "" || !consistencyText(in.RuleVersion) || runner == nil || !consistencyText(queryID) {
		return out, fmt.Errorf("%w: expected view, rule version, runner and query ID required", logicresolver.ErrInput)
	}
	if view.ID != in.ExpectedViewID {
		out.Reason = "stale_view"
		return out, nil
	}
	selected, complete := consistencySelectedView(view)
	if !complete {
		out.Reason = "unknown_currentness"
		return out, nil
	}
	if len(selected.Members) == 0 {
		out.Reason = "empty_scope"
		return out, nil
	}
	rules, declarations, err := compileConsistency(selected, in)
	if err != nil {
		return out, err
	}
	out.InputID, err = consistencyHash(struct {
		Profile, ViewID, Version string
		Rules, Declarations      logicresolver.CNF
	}{view.Profile, view.ID, in.RuleVersion, rules, declarations})
	if err != nil {
		return out, err
	}
	out.Reason = "resolver_failed"
	out.Rules, err = runner.Solve(ctx, queryID+"-rules", rules)
	if err != nil {
		return out, err
	}
	want := logicresolver.Requirements{SATModelChecked: true, UNSATProofChecked: true}
	if err := out.Rules.Require(want); err != nil {
		return out, err
	}
	if out.Rules.Status == logicresolver.UNSAT {
		out.Outcome, out.Reason = ConsistencyInvalidRules, ""
		return out, nil
	}
	out.Declarations, err = runner.Solve(ctx, queryID+"-declarations", declarations)
	if err != nil {
		return out, err
	}
	if err := out.Declarations.Require(want); err != nil {
		return out, err
	}
	out.Outcome, out.Reason = ConsistencyCompatible, ""
	if out.Declarations.Status == logicresolver.UNSAT {
		out.Outcome = ConsistencyConflict
	}
	return out, nil
}

func compileConsistency(view ConsistencyView, in ConsistencyRequest) (logicresolver.CNF, logicresolver.CNF, error) {
	var empty logicresolver.CNF
	if len(in.Evidence) != len(view.Members) || len(in.Rules) > 4096 {
		return empty, empty, fmt.Errorf("%w: exact evidence coverage or rule count", logicresolver.ErrInput)
	}
	members := make(map[string]bool, len(view.Members))
	for _, m := range view.Members {
		members[m.NodeID] = true
	}
	for _, c := range in.Evidence {
		if !members[c.ID] {
			return empty, empty, fmt.Errorf("%w: unknown or duplicate evidence %q", logicresolver.ErrInput, c.ID)
		}
		delete(members, c.ID)
	}
	// A global tuple map prevents per-condition variable collisions. No auxiliary
	// variables or integer IDs are accepted from normalization providers.
	atoms := map[ConsistencyAtom]int{}
	clauseCount, literalCount, textBytes := 0, 0, 0
	for _, group := range [][]ConsistencyCondition{in.Rules, in.Evidence} {
		seen := map[string]bool{}
		for _, c := range group {
			textBytes += len(c.ID)
			if !consistencyText(c.ID) || seen[c.ID] || len(c.Clauses) == 0 {
				return empty, empty, fmt.Errorf("%w: missing/duplicate condition or no clauses", logicresolver.ErrInput)
			}
			seen[c.ID] = true
			clauseCount += len(c.Clauses)
			if clauseCount > 100000 {
				return empty, empty, logicresolver.ErrLimit
			}
			for _, clause := range c.Clauses {
				literalCount += len(clause)
				if literalCount > 1000000 {
					return empty, empty, logicresolver.ErrLimit
				}
				for _, lit := range clause {
					textBytes += len(lit.Atom.Subject) + len(lit.Atom.Property) + len(lit.Atom.Context)
					if textBytes > 16<<20 {
						return empty, empty, logicresolver.ErrLimit
					}
					if !consistencyText(lit.Atom.Subject) || !consistencyText(lit.Atom.Property) || !consistencyText(lit.Atom.Context) {
						return empty, empty, fmt.Errorf("%w: complete atom identity required", logicresolver.ErrInput)
					}
					atoms[lit.Atom] = 0
					if len(atoms) > 100000 {
						return empty, empty, logicresolver.ErrLimit
					}
				}
			}
		}
	}
	keys := make([]ConsistencyAtom, 0, len(atoms))
	for atom := range atoms {
		keys = append(keys, atom)
	}
	slices.SortFunc(keys, func(a, b ConsistencyAtom) int {
		for _, pair := range [][2]string{{a.Subject, b.Subject}, {a.Property, b.Property}, {a.Context, b.Context}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	var base logicresolver.CNF
	for i, a := range keys {
		atoms[a] = i + 1
		base.Variables = append(base.Variables, logicresolver.Variable{ID: i + 1, Metadata: map[string]string{"subject": a.Subject, "property": a.Property, "context": a.Context}})
	}
	add := func(cnf *logicresolver.CNF, group []ConsistencyCondition, prefix string) {
		group = slices.Clone(group)
		slices.SortFunc(group, func(a, b ConsistencyCondition) int {
			if a.ID < b.ID {
				return -1
			}
			if a.ID > b.ID {
				return 1
			}
			return 0
		})
		for _, c := range group {
			for _, clause := range c.Clauses {
				encoded := make([]int, 0, len(clause))
				for _, lit := range clause {
					v := atoms[lit.Atom]
					if lit.Negated {
						v = -v
					}
					encoded = append(encoded, v)
				}
				slices.Sort(encoded)
				cnf.Clauses = append(cnf.Clauses, encoded)
				cnf.Origins = append(cnf.Origins, []string{prefix + c.ID})
			}
		}
	}
	rules := base
	add(&rules, in.Rules, "rule:")
	all := rules
	all.Clauses, all.Origins = slices.Clone(rules.Clauses), slices.Clone(rules.Origins)
	add(&all, in.Evidence, "node:")
	return rules, all, nil
}
