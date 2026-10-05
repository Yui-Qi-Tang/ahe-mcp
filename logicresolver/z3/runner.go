// Package z3 runs a caller-pinned Z3 executable for linear.Problem. It checks
// SAT models with exact arithmetic but provides no independently checked UNSAT
// proof. Consumers requiring that guarantee must reject such results.
package z3

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/internal/toolrun"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/linear"
)

// Config pins Z3 and a new artifact directory with per-Solve limits.
type Config struct {
	SolverPath, SolverSHA256 string
	ArtifactDir              string
	Limits                   logicresolver.Limits
}

// Runner has no application dependencies or mutable query state.
type Runner struct {
	root   string
	config Config
}

// Result preserves both the backend report and any decoded assignment.
type Result struct {
	logicresolver.Result
	Assignment linear.Assignment `json:"assignment"`
}

// New verifies Z3 before creating a private executable copy.
func New(c Config) (*Runner, error) {
	l, err := c.Limits.WithDefaults()
	if err != nil {
		return nil, err
	}
	c.Limits = l
	root, err := toolrun.Prepare(c.ArtifactDir, toolrun.Tool{Name: "z3", Path: c.SolverPath, SHA256: c.SolverSHA256})
	if err != nil {
		return nil, err
	}
	if err := toolrun.WriteJSON(filepath.Join(root, "limits.json"), l); err != nil {
		return nil, err
	}
	return &Runner{root: root, config: c}, nil
}

// Solve checks satisfiability, then requests values only for SAT. Both processes
// share one deadline. The second response must still be SAT, with a total model
// that satisfies the original typed problem; it need not be the first model.
func (r *Runner) Solve(ctx context.Context, id string, p linear.Problem) (result Result, err error) {
	result.Result = logicresolver.Result{Status: logicresolver.Unknown, Profile: "bool-cnf-linear-integer-conjunction/v0", SolverSHA256: r.config.SolverSHA256}
	ctx, cancel := context.WithTimeout(ctx, r.config.Limits.Timeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = p.CheckLimits(r.config.Limits); err != nil {
		return result, err
	}
	dir, err := toolrun.Query(r.root, id)
	if err != nil {
		return result, err
	}
	defer toolrun.Finish(dir, id, &result.Result, &err)
	input, err := json.Marshal(p)
	if err != nil {
		return result, err
	}
	if len(input) > r.config.Limits.MaxInputBytes {
		return result, logicresolver.ErrLimit
	}
	result.InputSHA256, err = toolrun.Save(dir, "input.json", input)
	if err != nil {
		return result, err
	}
	formula, err := p.SMTLIB()
	if err != nil {
		return result, err
	}
	script := formula + "(check-sat)\n"
	if len(script) > r.config.Limits.MaxInputBytes {
		return result, logicresolver.ErrLimit
	}
	result.FormulaSHA256, err = toolrun.Save(dir, "input.smt2", []byte(script))
	if err != nil {
		return result, err
	}
	out, err := r.execute(ctx, dir, "solver", "input.smt2")
	if err != nil {
		return result, err
	}
	switch strings.TrimSpace(string(out)) {
	case "unsat":
		result.Status = logicresolver.UNSAT
		return result, ctx.Err()
	case "unknown":
		return result, ctx.Err()
	case "sat":
		result.Status = logicresolver.SAT
	default:
		return result, logicresolver.ErrProtocol
	}
	names := make([]string, 0, len(p.Booleans.Variables)+len(p.Integers))
	for _, v := range p.Booleans.Variables {
		names = append(names, fmt.Sprintf("b%d", v.ID))
	}
	for _, v := range p.Integers {
		names = append(names, fmt.Sprintf("i%d", v.ID))
	}
	if len(names) > 0 {
		script += "(get-value (" + strings.Join(names, " ") + "))\n"
	}
	if len(script) > r.config.Limits.MaxInputBytes {
		return result, logicresolver.ErrLimit
	}
	if _, err = toolrun.Save(dir, "model.smt2", []byte(script)); err != nil {
		return result, err
	}
	out, err = r.execute(ctx, dir, "model", "model.smt2")
	if err != nil {
		return result, err
	}
	result.Model.Present = strings.Contains(string(out), "(") || (len(names) == 0 && strings.TrimSpace(string(out)) == "sat")
	result.Assignment, err = parseAssignment(out, len(p.Booleans.Variables), len(p.Integers))
	if err != nil {
		return result, fmt.Errorf("%w: %v", logicresolver.ErrProtocol, err)
	}
	result.Model.Decoded = true
	if err = toolrun.WriteJSON(filepath.Join(dir, "assignment.json"), result.Assignment); err != nil {
		return result, err
	}
	if err = p.CheckAssignment(result.Assignment); err != nil {
		return result, fmt.Errorf("%w: %v", logicresolver.ErrValidation, err)
	}
	result.Model.Checked = true
	result.Model.Method = "total-bool-and-big-integer-evaluation/v0"
	result.Model.TargetSHA256 = result.InputSHA256
	return result, ctx.Err()
}

func (r *Runner) execute(ctx context.Context, dir, stem, input string) ([]byte, error) {
	record, err := toolrun.Execute(ctx, dir, stem, filepath.Join(r.root, "tools", "z3"), r.config.Limits.MaxOutputBytes, "-smt2", input)
	if err != nil {
		return nil, err
	}
	if record.ExitCode != 0 {
		return nil, fmt.Errorf("%w: z3 exit %d", logicresolver.ErrProtocol, record.ExitCode)
	}
	return os.ReadFile(filepath.Join(dir, stem+".stdout"))
}
