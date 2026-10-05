// Package cadical runs pinned CaDiCaL and DRAT-trim executables. SAT assignments
// are checked against every input clause; UNSAT requires a checked DRAT proof.
// Adapted from Pouch internal/sat; see ../NOTICE.
package cadical

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/internal/toolrun"
)

// Config pins both tools and a new artifact directory. MaxProofBytes limits
// proofs accepted after solver exit, not disk consumption during execution.
type Config struct {
	SolverPath, SolverSHA256   string
	CheckerPath, CheckerSHA256 string
	ArtifactDir                string
	Limits                     logicresolver.Limits
	MaxProofBytes              int64
}

// Runner keeps private tool copies. Concurrent Solve calls use distinct query
// directories; duplicate IDs fail atomically. Callers own artifact retention.
type Runner struct {
	root   string
	config Config
}

// Result carries the total Boolean assignment when decoding succeeds.
// Assignment presence alone does not imply a successful model check.
type Result struct {
	logicresolver.Result
	Assignment map[int]bool `json:"assignment,omitempty"`
}

// New verifies executable hashes before creating private copies.
func New(c Config) (*Runner, error) {
	limits, err := c.Limits.WithDefaults()
	if err != nil {
		return nil, err
	}
	c.Limits = limits
	if c.MaxProofBytes < 0 {
		return nil, fmt.Errorf("%w: negative proof limit", logicresolver.ErrInput)
	}
	if c.MaxProofBytes == 0 {
		c.MaxProofBytes = 64 << 20
	}
	root, err := toolrun.Prepare(c.ArtifactDir,
		toolrun.Tool{Name: "cadical", Path: c.SolverPath, SHA256: c.SolverSHA256},
		toolrun.Tool{Name: "drat-trim", Path: c.CheckerPath, SHA256: c.CheckerSHA256})
	if err != nil {
		return nil, err
	}
	if err := toolrun.WriteJSON(filepath.Join(root, "limits.json"), struct {
		Limits        logicresolver.Limits
		MaxProofBytes int64
	}{c.Limits, c.MaxProofBytes}); err != nil {
		return nil, err
	}
	return &Runner{root: root, config: c}, nil
}

// Solve retains exact inputs, metadata, output and check records. All processes
// share one deadline. It never silently fills an incomplete assignment.
func (r *Runner) Solve(ctx context.Context, id string, cnf logicresolver.CNF) (result Result, err error) {
	result.Result = logicresolver.Result{Status: logicresolver.Unknown, Profile: "cnf/v0", SolverSHA256: r.config.SolverSHA256, CheckerSHA256: r.config.CheckerSHA256}
	ctx, cancel := context.WithTimeout(ctx, r.config.Limits.Timeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = r.config.Limits.CheckCNF(cnf); err != nil {
		return result, err
	}
	dir, err := toolrun.Query(r.root, id)
	if err != nil {
		return result, err
	}
	defer toolrun.Finish(dir, id, &result.Result, &err)
	input, err := json.Marshal(cnf)
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
	var formula bytes.Buffer
	if err = cnf.WriteDIMACS(&formula); err != nil {
		return result, err
	}
	if formula.Len() > r.config.Limits.MaxInputBytes {
		return result, logicresolver.ErrLimit
	}
	result.FormulaSHA256, err = toolrun.Save(dir, "input.cnf", formula.Bytes())
	if err != nil {
		return result, err
	}
	process, err := toolrun.Execute(ctx, dir, "solver", filepath.Join(r.root, "tools", "cadical"), r.config.Limits.MaxOutputBytes, "--no-binary", "--seed=0", "input.cnf", "proof.drat")
	if err != nil {
		return result, err
	}
	stdout, err := os.ReadFile(filepath.Join(dir, "solver.stdout"))
	if err != nil {
		return result, err
	}
	switch {
	case process.ExitCode == 10 && exactLine(stdout, "s SATISFIABLE") && !exactLine(stdout, "s UNSATISFIABLE") && !exactLine(stdout, "s UNKNOWN"):
		result.Status = logicresolver.SAT
		result.Model.Present = hasAssignment(stdout)
		result.Assignment, err = assignment(stdout, len(cnf.Variables))
		if err != nil {
			return result, fmt.Errorf("%w: %v", logicresolver.ErrProtocol, err)
		}
		result.Model.Decoded = true
		if err = toolrun.WriteJSON(filepath.Join(dir, "assignment.json"), result.Assignment); err != nil {
			return result, err
		}
		if err = cnf.CheckAssignment(result.Assignment); err != nil {
			return result, fmt.Errorf("%w: %v", logicresolver.ErrValidation, err)
		}
		result.Model.Checked = true
		result.Model.Method = "total-assignment/every-input-clause/v0"
		result.Model.TargetSHA256 = result.InputSHA256
	case process.ExitCode == 20 && exactLine(stdout, "s UNSATISFIABLE") && !exactLine(stdout, "s SATISFIABLE") && !exactLine(stdout, "s UNKNOWN"):
		result.Status = logicresolver.UNSAT
		proof, statErr := os.Lstat(filepath.Join(dir, "proof.drat"))
		if statErr != nil || !proof.Mode().IsRegular() {
			return result, fmt.Errorf("%w: missing regular proof", logicresolver.ErrProtocol)
		}
		result.Proof.Present = true
		if proof.Size() > r.config.MaxProofBytes {
			return result, fmt.Errorf("%w: proof", logicresolver.ErrLimit)
		}
		checked, checkErr := toolrun.Execute(ctx, dir, "checker", filepath.Join(r.root, "tools", "drat-trim"), r.config.Limits.MaxOutputBytes, "input.cnf", "proof.drat")
		if checkErr != nil {
			return result, checkErr
		}
		output, readErr := os.ReadFile(filepath.Join(dir, "checker.stdout"))
		if readErr != nil {
			return result, readErr
		}
		if checked.ExitCode != 0 || !exactLine(output, "s VERIFIED") || exactLine(output, "s NOT VERIFIED") {
			return result, fmt.Errorf("%w: proof checker", logicresolver.ErrValidation)
		}
		result.Proof.Checked = true
		result.Proof.Method = "drat-trim/exit-zero-exact-verified"
		result.Proof.TargetSHA256 = result.FormulaSHA256
	case process.ExitCode == 0 && exactLine(stdout, "s UNKNOWN") && !exactLine(stdout, "s SATISFIABLE") && !exactLine(stdout, "s UNSATISFIABLE"):
		// UNKNOWN is a valid answer; consumer acceptance rejects it.
	default:
		return result, logicresolver.ErrProtocol
	}
	return result, ctx.Err()
}
