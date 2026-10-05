# Scoped declaration consistency — experimental v0

`ReadConsistencyScope` and `CheckCanonicalConsistency` are native Go APIs. They
are not exposed as MCP tools and do not change admission or graph schemas.

The profile `bound-declarations/cnf-v0` selects every currently bound canonical
claim in one exact proposition namespace and scope, across definition revisions.
Selection does not depend on a starting node, connectivity, ranking or an LLM.
Withdrawn bindings are excluded; a correction is resolved before scope filtering.
The reader retains distinct derivations, their full AND parents, binding heads,
definitions, payloads, provenance, temporal records, integrity and internal edges.
Missing parents, row-level filtering and limits fail the read.

This is a set of **bound declarations**, not a set of temporally current facts.
Historical and stale content can be included. Unbound claims and other scopes are
outside the conclusion. Supersession/currentness policy is not applied. Graph
relations are preserved for traceability but are not compiled into logic rules.

Usage:

1. Read an exact `ConsistencyScope` using an unfiltered Core reader. Inspect the
   returned members and `ID`.
2. Supply a `ConsistencyRequest` with that `ExpectedViewID`, an explicit
   `RuleVersion`, optional background `Rules`, and exactly one `Evidence`
   condition per canonical member. Each condition is CNF over exact
   `(subject, property, context)` Boolean identities. Context is opaque; the API
   does not infer temporal overlap, entity identity or causality. No integer or
   auxiliary variable IDs are accepted from the normalizer.
3. Call `CheckCanonicalConsistency` with a pinned `cadical.Runner` and a fresh
   query ID. The API rereads the DB and rejects an obsolete expected view. The
   rules are checked alone before the combined set. Both solver calls share a
   30-second deadline. SAT requires a checked total assignment; UNSAT requires a
   checked DRAT proof. Always inspect **both error and Outcome**.

Outcomes:

| Outcome | Meaning |
| --- | --- |
| `compatible_declarations` | These normalized declarations and rules have a satisfying assignment. |
| `conflicting_declarations` | Rules alone are satisfiable; the complete supplied set is not. |
| `inconsistent_rules` | Rules already contradict one another; no evidence blame is assigned. |
| `inconclusive` | Missing/invalid normalization, stale/empty view, incomplete read, limit, cancellation, solver or validation failure. |

The result contains the checked snapshot, input identity, solver/checker records,
and formula origins. It identifies the **whole checked set**, not a minimal
conflicting subset. Store the request and result together with the runner's
artifacts for audit; this API does not persist them or sign arbitrary caller data.
There is no auto-withdrawal, truth selection, repair or background invalidation.
Concurrent writes after the read produce a new state; the old result remains a
historical result and must be checked again before use against the new state.

Correct normalization and explicit rules remain preconditions. Neither an
annotation for each node nor a proof of the encoded formula verifies source
fidelity. Empty condition lists are rejected; an empty clause explicitly means
false. AND parent records describe required provenance, not an automatically
invented implication from parents to conclusions.

Limits: 256 members, 4096 internal edges; 4096 rules, 100,000 clauses, 1,000,000
literals, 100,000 distinct atoms, 16 MiB normalization text before encoding. The
resolver also enforces its own encoded-input/output and accepted-proof limits.
This is not an operating-system resource sandbox.

Ordinary tests: `go test ./internal/evidenceingestion -run '^TestConsistency'`.
Reader integration tests require an explicitly disposable `DATABASE_DSN` and
`-tags integration -run '^TestIntegrationConsistency(Scope|RLS)$'`. Real solver
acceptance additionally uses `-tags integration,consistencylab`, test
`TestIntegrationConsistencySolverLab`, and explicit `AHE_CONSISTENCY_REPORT_DIR`,
`AHE_CONSISTENCY_CADICAL`, `AHE_CONSISTENCY_DRAT`. This opt-in acceptance fails on
missing prerequisites; it never silently skips a prepared case. Tool hashes are
fixed in the acceptance fixture. Pouch and Detective require no changes.
