# Logic resolver foundation

Current API: **experimental v0**, unreleased. This public Go package lives in the
AHE module but has no dependency on AHE storage, MCP or Pouch planning.
It operates on explicitly supplied formulas. It does not retrieve evidence,
infer causality, locate application conflicts, choose winners or authorize writes.

| Package | Input | Successful SAT | Successful UNSAT |
| --- | --- | --- | --- |
| `logicresolver/cadical` | Boolean CNF | Total assignment checked against every original clause | DRAT proof checked by pinned DRAT-trim |
| `logicresolver/z3` | `linear.Problem` | Total Boolean/integer assignment checked against original constraints using exact integers | Backend report only; no independently checked proof |
| `logicresolver/linear` | Typed Boolean CNF plus conjunction of linear integer comparisons | Pure validation and encoding | No solver |

The linear profile supports integer sums of constant multiples compared with a
constant using `=`, `<=`, `>=`. Integers use canonical decimal strings without
machine-size overflow. Boolean and integer IDs are separate, contiguous and
one-based. This is a subset of QF_LIA: no general SMT-LIB input, quantifiers,
nonlinear multiplication, arrays, or Boolean/arithmetic mixed disjunctions.
Unknown operators and malformed sorts/IDs fail explicitly.

## Consumer contract

Construct a concrete runner at the composition point. Consumers may declare the
small interface they actually use; the library exports no universal solver
interface, registry or backend fallback. SAT and linear arithmetic have different
typed inputs and assignments. An adapter can preserve Pouch's existing
`Solve(ctx, id, cnf)` contract without importing its internal types here.

```go
r, err := cadical.New(cadical.Config{
    SolverPath: solverPath, SolverSHA256: solverDigest,
    CheckerPath: checkerPath, CheckerSHA256: checkerDigest,
    ArtifactDir: newDirectory,
})
if err != nil { return err }
result, err := r.Solve(ctx, "power-check", logicresolver.CNF{
    Variables: []logicresolver.Variable{{ID: 1}, {ID: 2}},
    Clauses: [][]int{{-1, 2}, {1}, {-2}},
    Origins: [][]string{{"power-implies-light"}, {"power-on"}, {"light-off"}},
})
if err != nil { return err } // Always inspect error before using the answer.
return result.Require(logicresolver.Requirements{
    SATModelChecked: true, UNSATProofChecked: true,
})
```

`Status` is the backend answer. A SAT response with an invalid assignment retains
its status but returns `ErrValidation`. `Model` records presence, decoding and
checking separately; a proof check has its own record and checker hash. UNKNOWN
is a valid response but never meets `Require`. An SMT UNSAT response cannot meet
`UNSATProofChecked`; no silent downgrade or alternate backend is attempted.
`Require` is a convenience policy check on a returned result, not authentication
of arbitrary caller-constructed data, and does not replace checking `err`.

`InputSHA256` identifies exact `input.json`, including metadata. `FormulaSHA256`
identifies exact `input.cnf` or `input.smt2`. Models are checked against the typed
input; DRAT proofs are checked against the encoded CNF. Hashes identify objects,
not semantic equivalence. Independent encoding tests and truth-table cases check
the mapping within their tested scope; neither establishes source fidelity.

`Variable.Metadata`, per-clause `Origins`, and linear constraint origins preserve
caller mappings. They have no logical force by themselves. Every AND premise or
alternative derivation needed for reasoning must also be encoded in the formula
by the consumer. The resolver does not turn metadata into constraints.

## Execution and artifacts

- Tools must be trusted executables pinned by an explicit lowercase SHA256.
  `New` copies verified bytes to a fresh private directory; it never downloads
  tools. The caller must keep this directory unchanged during use.
- Query IDs reserve new directories atomically. Concurrent calls with distinct
  IDs are isolated; duplicate IDs fail. Inputs must not be mutated concurrently.
- The default 30-second timeout covers all processes of one Solve call together,
  including the SAT proof check or SMT model request. Earlier context deadlines
  win. Synchronous encoding/checking is bounded by dimensions and checked between
  stages; it is not an interruptible hard CPU quota.
- Defaults: 100,000 variables, 1,000,000 clauses/constraints, 4,000,000 literals/
  terms, 16 MiB encoded input, 16 MiB per process output stream. Metadata bytes
  are checked after JSON encoding. Caller allocations are not a memory sandbox.
- CaDiCaL's 64 MiB proof limit is checked **after** solver exit. It is an accepted
  proof limit, not an in-process disk quota. Use a constrained execution host for
  large/untrusted workloads. Callers bound simultaneous calls and total calls.
- Unix cancellation kills the process group. Other platforms use Go process
  cancellation/WaitDelay; descendant termination is not guaranteed there.
- Successful and failed queries retain inputs, outputs, process arguments,
  timings, assignment/proof where available, and an outcome. Returned artifacts
  hash these files. Configuration and pinned tools live at the root. Early
  malformed-input/canceled calls before directory creation have no artifacts;
  callers must record such errors. Callers own retention/removal; no background
  process or `Close` operation remains after Solve returns.

Errors support `errors.Is`: input, unsupported language, size limits, protocol,
validation, consumer guarantee, `context.Canceled` and `context.DeadlineExceeded`.
Filesystem/process setup errors are returned rather than converted to UNSAT.

## Validation and Pouch compatibility

```sh
go test -race -count=1 ./logicresolver/...
go build ./logicresolver/...
go vet ./logicresolver/...
```

Actual tool acceptance is explicitly selected:

```sh
# Set AHE_LOGIC_CADICAL, AHE_LOGIC_CADICAL_SHA256,
# AHE_LOGIC_DRAT, AHE_LOGIC_DRAT_SHA256,
# AHE_LOGIC_Z3, AHE_LOGIC_Z3_SHA256, and AHE_LOGIC_ARTIFACTS.
# The artifact parent must exist; cadical/ and z3/ within it must not exist.
go test -tags resolverintegration -count=1 -run TestRealTools ./logicresolver/...
```

Missing required tools fail this command; they are not skipped. Hermetic shell
fixtures are separate from this acceptance. Windows skips POSIX shell fixtures;
native Windows process behavior has not been validated.

The frozen matrix enumerates all 512 subsets of the nine possible non-tautological
two-variable clauses, including the empty clause. SAT adds eight boundaries and
two historical Pouch formulas; SMT adds eight fixed linear-profile cases. This
is a bounded correctness check, not a performance or general reliability claim.

The Pouch proxy mirrors the fixed `planning.Solver` signature, variable fields,
and `Search` error/verified/status/context/query-budget order. It checks exact
CNF bytes and metadata round trips for the stored synthetic formulas. It does
not run Pouch's encoder, trace reconstruction, path enumeration, CLI or product
integration. Pouch must still supply its own adapter and native integration tests
before adopting this package. Pouch remains unchanged in this phase.

Port provenance and the retained MIT license are in [NOTICE](NOTICE). The
process protocol and CNF checks are adapted from Pouch commit
`d71d15c3a6de776fe4b8fddcdca10d6af6ac47f6`; neutral metadata, detailed validation,
shared deadlines, concurrency isolation, explicit limits and the linear/Z3
profile are changes made here. CaDiCaL, DRAT-trim and Z3 are separately supplied
tools, not vendored dependencies.
