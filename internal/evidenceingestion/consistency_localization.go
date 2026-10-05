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

// ConsistencyConflictStep records one attempted whole-condition deletion.
// The runner's input artifact records the exact formula actually checked.
type ConsistencyConflictStep struct {
	Removed string         `json:"removed"`
	Result  cadical.Result `json:"result"`
	Error   string         `json:"error,omitempty"`
}

// ConsistencyLocalization is one conflicting subset, never an exhaustive list
// or a minimum-cardinality repair. Conditions use distinct node:/rule: prefixes.
// Minimal is true only after every remaining group has a checked SAT deletion.
type ConsistencyLocalization struct {
	Conditions []string                  `json:"conditions,omitempty"`
	Minimal    bool                      `json:"minimal"`
	Reason     string                    `json:"reason"`
	Witness    cadical.Result            `json:"witness"`
	Steps      []ConsistencyConflictStep `json:"steps,omitempty"`
}

// ConsistencyDiagnosis keeps global consistency and localization separately.
// A localization limit/failure does not erase the already checked global UNSAT.
type ConsistencyDiagnosis struct {
	ConsistencyResult
	Localization ConsistencyLocalization `json:"localization"`
}

// DiagnoseCanonicalConsistency checks the current view, then tries to locate
// one irreducible contradiction. All calls share 60 seconds; at most 64 extra
// calls are permitted (zero selects 64). No evidence or relation is modified.
func DiagnoseCanonicalConsistency(ctx context.Context, pool *pgxpool.Pool, runner *cadical.Runner, queryID string, in ConsistencyRequest, maxCalls int) (ConsistencyDiagnosis, error) {
	return diagnoseConsistency(ctx, runner, queryID, in, maxCalls, func(ctx context.Context, scope ConsistencyScope) (ConsistencyView, error) {
		return ReadConsistencyScope(ctx, pool, scope)
	})
}

func diagnoseConsistency(ctx context.Context, runner *cadical.Runner, queryID string, in ConsistencyRequest, maxCalls int, read func(context.Context, ConsistencyScope) (ConsistencyView, error)) (ConsistencyDiagnosis, error) {
	if maxCalls == 0 {
		maxCalls = 64
	}
	if maxCalls < 1 || maxCalls > 64 {
		return ConsistencyDiagnosis{}, fmt.Errorf("%w: localization budget", logicresolver.ErrInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	checked, err := checkConsistency(ctx, runner, queryID, in, read)
	out := ConsistencyDiagnosis{ConsistencyResult: checked, Localization: ConsistencyLocalization{Reason: "not_applicable"}}
	if err != nil || (checked.Outcome != ConsistencyConflict && checked.Outcome != ConsistencyInvalidRules) {
		return out, err
	}
	selected, _ := consistencySelectedView(checked.View)
	rules, all, err := compileConsistency(selected, in)
	if err != nil {
		return out, err
	}
	cnf, witness := all, checked.Declarations
	if checked.Outcome == ConsistencyInvalidRules {
		cnf, witness = rules, checked.Rules
	}
	out.Localization = localizeConsistency(ctx, cnf, witness, maxCalls, func(ctx context.Context, i int, cnf logicresolver.CNF) (cadical.Result, error) {
		return runner.Solve(ctx, fmt.Sprintf("%s-core-%d", queryID, i), cnf)
	})
	return out, nil
}

func localizeConsistency(ctx context.Context, cnf logicresolver.CNF, witness cadical.Result, maxCalls int, solve func(context.Context, int, logicresolver.CNF) (cadical.Result, error)) ConsistencyLocalization {
	groups := map[string]bool{}
	for _, origins := range cnf.Origins {
		for _, origin := range origins {
			groups[origin] = true
		}
	}
	order := make([]string, 0, len(groups))
	for group := range groups {
		order = append(order, group)
	}
	slices.Sort(order)
	out := ConsistencyLocalization{Witness: witness, Reason: "complete"}
	finish := func() ConsistencyLocalization {
		for _, group := range order {
			if groups[group] {
				out.Conditions = append(out.Conditions, group)
			}
		}
		return out
	}
	for i, group := range order {
		if err := ctx.Err(); err != nil {
			out.Reason = "cancelled"
			return finish()
		}
		if i >= maxCalls {
			out.Reason = "budget_exhausted"
			return finish()
		}
		trial := logicresolver.CNF{Variables: cnf.Variables}
		for j, clause := range cnf.Clauses {
			keep := false
			for _, origin := range cnf.Origins[j] {
				if origin != group && groups[origin] {
					keep = true
				}
			}
			if keep {
				trial.Clauses = append(trial.Clauses, clause)
				trial.Origins = append(trial.Origins, cnf.Origins[j])
			}
		}
		result, err := solve(ctx, i, trial)
		if err == nil {
			err = result.Require(logicresolver.Requirements{SATModelChecked: true, UNSATProofChecked: true})
		}
		step := ConsistencyConflictStep{Removed: group, Result: result}
		if err != nil {
			step.Error = err.Error()
		}
		out.Steps = append(out.Steps, step)
		if err != nil {
			out.Reason = "resolver_failed"
			return finish()
		}
		if result.Status == logicresolver.UNSAT {
			delete(groups, group)
			out.Witness = result
		}
	}
	out.Minimal = true
	return finish()
}
