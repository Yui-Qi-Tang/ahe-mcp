# Scoped declaration consistency

Native Go APIs in `internal/evidenceingestion` check one exact proposition
namespace and scope. The [product workflow](../../docs/CONSISTENCY.md) exposes
readback through Query and configuration through core-records. The current contract is schema 54 and role policy
v7; diagnostic history remains separate from canonical admission. Pouch and
external consumers are unchanged.

## Participation policy

`ConsistencyScope.Policy` is explicit and versioned:

| Policy | Participating evidence |
| --- | --- |
| Empty or `bound-declarations/cnf-v0` | Every currently bound claim, including historical content; the original behavior. |
| `classified-frontier/cnf-v1` | Excludes proven superseded/stale, expired and not-yet-valid evidence. Keeps every completely classified live frontier, including competing branches. Unknown currentness blocks the conclusion. |

Both read the complete bound scope before selecting. Withdrawn bindings are
excluded; corrections are resolved before filtering. Distinct derivations,
AND parents, binding heads, definitions, payloads, provenance, temporal records,
integrity and edges remain in the result. Missing parents, limits and row-level
filtering fail the read. It does not silently expand the proposition scope.

The frontier policy reads complete admitted supersession lineages, including
source-object claims and replacement events outside the proposition scope, in
the same repeatable-read transaction. These dependencies and the participation
reasons are included in the view fingerprint. An unrelated supersession-chain
change can conservatively trigger recomputation. An unclassified object claim
blocks a frontier verdict; it cannot be hidden by a stored `current` label.
Without a lineage, an explicit stored `current` temporal status is accepted as
recorded. This is admitted-database currentness, not provider freshness or truth.

A derived claim participates only when every required AND parent participates.
An excluded parent excludes that derivation; an unknown parent blocks it unless
another required parent is already excluded. Alternative derivations remain
separate. Validity windows use RFC3339 timestamps and `[from,to)` semantics;
malformed, reversed or zero-length windows block. Branches are never ranked or
silently resolved. UNSAT across branches means the combined declarations conflict,
not that each branch is individually inconsistent.

## Check and locate a conflict

1. `ReadConsistencyScope` returns the complete view, its fingerprint `ID`, and
   participation reasons. Inspect the selected nodes.
2. Supply a `ConsistencyRequest` with that `ExpectedViewID`, an explicit
   `RuleVersion`, optional background rules, and exactly one condition per
   selected canonical node. Each condition is a conjunction of disjunctive
   clauses over exact `(subject, property, context)` Boolean identities.
3. `CheckCanonicalConsistency` rereads the view and rejects a changed fingerprint.
   It checks rules alone, then rules plus evidence, with a shared 30-second
   deadline. SAT requires a checked total assignment; UNSAT requires a checked
   DRAT proof. Inspect both the error and `Outcome`.
4. `DiagnoseCanonicalConsistency` additionally deletes whole node/rule condition
   groups to identify **one irreducible contradictory set**. Each accepted
   deletion has a checked UNSAT proof; retained groups have checked SAT deletion
   tests. This is inclusion-minimal, not the smallest set or all conflicting sets.
   At most 64 extra calls share a 60-second total deadline. Budget, UNKNOWN,
   cancellation and errors leave localization explicitly incomplete while
   preserving an already verified global conflict.

| Outcome | Meaning |
| --- | --- |
| `compatible_declarations` | The selected normalized conditions and rules have a satisfying assignment. |
| `conflicting_declarations` | Rules alone are satisfiable; the combined selected set is not. |
| `inconsistent_rules` | Rules already contradict; localization concerns rules, without blaming evidence. |
| `inconclusive` | Unknown currentness, missing/invalid normalization, stale/empty view, failed read, limit, cancellation or solver/validation failure. |

Localization keeps `node:` and `rule:` identities separate, preserves each
formula's origins, and never reruns participation policy on deletion subsets.
Removing a condition for diagnosis is not a hypothetical retraction or repair.
A localized derived-node group can still rely on parents retained in the full
view but absent from the localized formula. The subset is not an independently
admissible evidence package; inspect those parents through the preserved view.
No step selects truth, withdraws evidence, or invents implications from graph edges.

## Durable results and recomputation

`RegisterConsistencyWatch` stores a versioned scope, policy, rules, external
normalization catalog, solver identity and localization budget. Configuration
changes require the expected revision. `ReadConsistencyWatch` reads an explicit
positive revision or -1 for the latest in its snapshot. MCP binds optional
`recorded_by` to its launcher principal; old/native records may omit it, and it
is audit text, never authentication or admission authority. Exact request retries return a historical
receipt; replay is not proof that a configuration is still active. Pausing is a
new configuration revision. Configuration transactions use a short journal lock
separate from the worker lock, so an update may commit during solving. Pausing
does not cancel the in-flight historical run; the next tick observes the pause.
`cadical.Runner.Identity()` binds binary digests and
resource limits, excluding storage paths.

A native consumer or `ahe-consistency-worker run` explicitly starts
`ConsistencyWorker.Run(ctx)` with an interval
of at least 10 ms, or calls `Tick(ctx)`. This repository change does not start a
deployed service. The worker:

- Detects a changed configuration, source fingerprint or time-based eligibility.
- Commits an `invalidated` event before solving. Initial work also has this event.
- Uses the persisted normalization catalog for currently eligible nodes. A new
  eligible node without an annotation produces `missing_normalization`; a new
  configuration must supply it. No extraction is performed.
- Atomically stores the request, diagnosis, exact artifact bytes and `completed`
  event. `completed` means a result was recorded, including blocked/failed
  results; consumers must inspect the diagnosis and localization status.
- Resumes targets that have an invalidation but no result. A pinned database
  connection serializes workers per watch; losing it prevents that worker from
  publishing. No in-memory-only queue is required.

An unchanged target is not repeated. Recorded solver failures require an explicit
new configuration revision to retry. Database/read/artifact archival errors return
an error without a completed result; `Run` stops for caller supervision. Pending
work is retried when the consumer restarts. Scope-limit/RLS errors cannot become
successful empty reads. Configured engine mismatch also stops the worker.

`ReadConsistencyEvents` is the durable notification interface. Consumers retain
one cursor **per watch**, honor truncation, and can replay pages. Per-watch row
locks order commit visibility so a late commit cannot fall behind an advanced
cursor. Delivery is polling, not an immediate callback, email or webhook. A
change that occurs and disappears between polls need not create an intermediate
run; this is state monitoring, not a full event replay engine.

`ReadConsistencyRun` reads history and checks configuration plus source view in a
fresh transaction. `matches_snapshot` means only that snapshot; `stale` means a
new input/configuration, and read failure remains `unavailable`. A stored record
never claims permanent validity. Concurrent source changes may leave a historical
result, which is marked stale on read and followed by a new run on the next tick.
This check does not fence a later external action against concurrent evidence writes.

`ReadConsistencyArtifact` returns digest-checked bytes from PostgreSQL, independent
of the runner's local files. Each artifact is bounded to 64 MiB and a run to
128 MiB. History rejects UPDATE/DELETE/TRUNCATE; deferred database checks require
the exact distinct artifact manifest at commit. Database checks establish record
integrity, not that a privileged raw SQL writer genuinely ran a solver. The
`core-records` role is a trusted native writer; query roles only read. The small
UPDATE grant on watch rows permits locking; an immutable trigger rejects changes.
Schema rollback refuses to discard registered watches.

## Boundaries and validation

Correct normalization and explicit domain rules remain preconditions. Source
fidelity, entity identity, temporal overlap and causality are not inferred.
Proofs concern the encoded Boolean formula. Generic SMT support is separate;
this native AHE path remains CNF/SAT.

Bounds: 256 members, 4096 internal edges, 4096 rules, 100,000 clauses, 1,000,000
literals, 100,000 atoms, 16 MiB normalization text. The worker checks up to 256
watches by default (configurable through 1024), failing rather than truncating.
Resource limits are not an operating-system sandbox.

Ordinary tests: `go test ./internal/evidenceingestion -run '^TestConsistency'`.
Reader integration uses an explicitly disposable `DATABASE_DSN` and `-tags integration`.
Real solver/lifecycle tests additionally use `-tags integration,consistencylab`
and explicit `AHE_CONSISTENCY_REPORT_DIR`, `AHE_CONSISTENCY_CADICAL`,
`AHE_CONSISTENCY_DRAT`. Missing prepared-suite prerequisites fail, not skip.
Migration and role integration must run serially in an isolated database because
role verification checks authority beyond the selected schema. Preserved-role
fixtures cover schema 49, 52 and 53 upgrades without replacing users or groups.

Schema 54 validates the original JSON number tokens for configuration predecessor,
run revision and artifact byte counts, plus the configured-event hash. This is
not complete validation of arbitrary raw SQL payloads. Migration rejects incompatible
history atomically and preserves existing function identities and grants.
