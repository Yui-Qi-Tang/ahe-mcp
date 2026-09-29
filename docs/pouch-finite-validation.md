# Local finite Pouch validation

Development contract: `pouch-finite-validation/v1` (unreleased).

Core includes an opt-in Go API and `ahe-pouch-validate` command for **typed path and
finite reachability certificates**. The command is read-only and has no database,
encoder, solver or Python dependency. This is not a DRAT verifier or a parser for
all historical Pouch package versions. Existing CNF/assignment/DRAT validation must
still run when that evidence is part of the caller's contract.

## Authority and query

The trusted application supplies exact authority bytes, their pinned SHA-256 and
a request ID outside the sender package. `NewAuthority` copies and validates that
contract. The package cannot replace its model, query, sources or assumptions.
Hash consistency establishes binding to those bytes, not their authorization or
source entailment; the application must obtain authority through its own trusted
process and retain it for audit.

The language has named string-valued coordinates, exact conjunction guards,
constant effects, complete effect/frame partitions, forbidden-state patterns and
unit costs. Bounds: 16 fields, 64 values per field, 64 actions, 256 forbidden
patterns and at most 4,096 Cartesian states. Unsupported types, nulls, omitted or
unknown fields, duplicate/case-aliased keys and oversized inputs fail closed.
`forward_limit` and `return_limit` mean **at most**, not exact-length queries.
Inputs have a 4 MiB bound. No arbitrary expressions, copy effects or source-to-rule
inference are performed. An upstream adapter must explicitly preserve or reject
unsupported semantics instead of silently dropping fields.

A trace contains all coordinates at every step. Validation checks start, domain,
guard, effect/frame, goal, no repeated states and first-hit stopping. An independent
Go breadth-first traversal computes unit-cost shortestness. Legal longer paths
are accepted only if they do not claim to be shortest. Return starts at the full
forward endpoint and must restore the entire caller baseline.

A `no_return` package carries the complete reachable state closure. Core rebuilds
that closure from the declared actions and refuses the conclusion whenever the
baseline is reachable, even outside the supplied return horizon. Missing,
duplicate or foreign closure states fail. This finite certificate is different
from an UNSAT proof of a CNF. Receipt fields remain
`model_conditional_conclusion`, `observed_fact=false`, `native_ahe_receipt=false`.

## Native integration

`evidenceingestion.LoadPouchEndpointReview` runs validation and checks the exact
receipt statement, parent IDs and raw source bytes against native Core records.
Version 1 accepts direct source-claim parents only. The native derivation method
is the contract version, producer is `ahe-pouch-validator`, and trace reference
binds the exact package digest.

`AdmitReviewedPouchEndpoint` repeats validation, requires the exact review subject
and explicit reviewer approval, and delegates to the existing transactional
endpoint admission. The native transaction rechecks the subject, so an intervening
change cannot silently authorize a different statement. A successful model receipt
alone grants no write authority. Source classification labels are preserved data,
not evidence that the original source supports the classification.

These APIs are application entry points; they do not add a default MCP tool or
change generic endpoint semantics. **A generic Core admission receipt does not
prove that Pouch validation ran.** The application must retain the paired validation
receipt, package and authority and use the validated entry point. No universal
semantic gate, signed validation attestation or mandatory SQL enforcement is
claimed. MCP/launcher exposure requires a separate operator configuration design.

The opt-in `TestIntegrationPouchContinuous` acceptance test exercises current
native source admission and separate Query readback, then invokes an external
frozen Pouch matrix/CNF/SAT runner, validates its newly generated typed packets,
and uses this API for reviewed native admission and fresh Query readback. It
requires `POUCH_CONTINUOUS_RUN` (a prepared local run directory) and
`AHE_DBROLE_ACCEPTANCE_DATABASE_DSN` (an explicitly disposable database). The
operator-supplied runner receives no database credentials. Test approval stubs
stay in the test harness; they are not a product reviewer policy.

The caller freezes its authority before generation. The receiver-side adapter
reads the native model declaration, expands finite forbidden memberships to
exact patterns, preserves the full declaration and native ID mapping, and rejects
unsupported guards/effects/costs. This verifies an explicitly declared finite
language, not automatic source interpretation. Search completeness and DRAT
checks remain Pouch/harness responsibilities; Go verifies each path or closure.

A delivery lost after commit is an uncertain acknowledgement, not proof of no
write. Use the same original request and review identity for exact receipt-bound
replay, then fresh Query. Do not automatically issue a new admission request.

## Local command

Build with `go build ./cmd/ahe-pouch-validate`. Supply caller-owned inputs:

```sh
ahe-pouch-validate --authority authority.json \
  --authority-sha256 CALLER_PINNED_DIGEST --request-id CALLER_REQUEST < package.json
```

Exit 0 supplies an accepted receipt; exit 1 supplies a typed rejection; exit 2 is
an execution/input-availability failure. The caller must require exit 0, decode
and compare the exact receipt and package binding, and retain any earlier known
semantic rejection if a later transport error occurs. Do not give an untrusted
solver control of these flags or access to authority files/admission credentials.

Process separation and owner-only files under one UID are not OS identity
isolation. The local tests exercise separate native DB roles; a deployed process
sandbox, separate users, credential custody and operational recovery still need
site-specific validation. This development work publishes and deploys nothing.

## Source reference capacity

The two native derived/ancestor loaders share a 128-source-reference per-node
bound. 129 is refused. The parent count (8), record/source byte bounds and other
graph/depth limits remain. Unrelated 64-reference relation bounds remain unchanged.

## Verification

Unit tests cover exact typed input, binding, replay, shortestness, finite closure
and cancellation. The opt-in `TestIntegrationPouchLocalPipeline` uses a disposable
PostgreSQL database and real MCP intake/review/query with a restricted endpoint
pool; all approvals are test stubs. It covers 128/129 native capacity, named
refusals, injected consumer protocol/process failures and post-commit replay.
The process faults use explicit test doubles; they are not reports of solver or
DRAT failures. Existing endpoint authority/rollback regressions remain separate.
