# Scoped consistency checks

This product checks whether explicitly supplied Boolean conditions can hold
together in one named evidence scope. External code supplies the conditions and
rules. AHE preserves the original evidence and reports the formal result; it
does not infer those conditions, choose which statement is true, or repair data.

Current contract: schema **54**, role policy **v7**, `consistency-watch/v1`.
The generic resolver API remains v0. This product path uses checked SAT and DRAT;
the generic bounded SMT adapter is not used here.

## Components and authority

| Component | Operation |
| --- | --- |
| Query MCP | Read complete scope, watch configuration, events, result and proof bytes |
| core-records MCP | Append a watch configuration, correction, pause or explicit retry |
| `ahe-consistency-worker` | Poll configured watches, solve and persist diagnostic history |
| `ahe-runtime-admin upgrade` | Update the policy of the same existing group/LOGIN pair |

The worker uses a separately operated **core-records** LOGIN. It cannot admit
claims, collect sources or change canonical graph nodes/edges. That database
profile is a trusted raw writer; SQL credentials can append diagnostic records,
so a database record alone is not authentication or proof that a solver ran.
MCP binds `recorded_by` to the launcher identity. Native/old configurations may
omit this audit field. It is not an authorization credential.

Normal evidence search does not automatically solve every scope. The operator
starts the worker; the recorder explicitly registers watches. Existing external
check/representation records remain opaque and are not automatically converted
into consistency watches.

## Configure the worker

Build with `make build`. Independently install CaDiCaL and DRAT-trim and pin their
expected binary SHA-256 values. No executable paths, hashes, database credential
or process options are accepted through an MCP tool.

Provide these settings through the trusted process manager environment:

| Setting | Value |
| --- | --- |
| `DATABASE_DSN` | Explicit PostgreSQL URL for the bounded LOGIN, including host, database and sslmode |
| `AHE_DATABASE_NAME` / `AHE_DATABASE_LOGIN` | Exact database and LOGIN names |
| `AHE_DATABASE_SCHEMA` / `AHE_DATABASE_ROLE` | Migrated private schema and installed core-records NOLOGIN group |
| `AHE_CONSISTENCY_CADICAL` / `AHE_CONSISTENCY_DRAT` | Absolute executable paths |
| `AHE_CONSISTENCY_CADICAL_SHA256` / `AHE_CONSISTENCY_DRAT_SHA256` | Expected binary SHA-256 hashes |
| `AHE_CONSISTENCY_ARTIFACT_DIR` | Existing absolute directory, mode 0700, private to the service account |
| `AHE_CONSISTENCY_INTERVAL` | Optional polling interval; default 5s, accepted 10ms–1h |

Ambient `PG*` variables, service files and implicit credential files are refused.
Startup verifies the exact LOGIN/group profile and native schema. Worker errors
are redacted at the process boundary, so database errors cannot print credentials.

Run `ahe-consistency-worker identity` to verify the binaries and obtain
`engine_id`. This operation does not open PostgreSQL and removes its temporary
identity session. Execution limits and tool hashes are included in the identity;
storage paths are not. Use the same configuration for subsequent execution.

`ahe-consistency-worker once` performs one scan and exits. Its successful exit
means the scan finished; it can include blocked or failed diagnoses. Inspect the
stored result, not only the process exit code or a completed event.

`ahe-consistency-worker run` polls until SIGINT/SIGTERM or an operational error.
A database/read/archive failure or engine mismatch stops it with a nonzero exit.
Use an external supervisor to restart with a delay; a process restart resumes
invalidated targets without a completed result. A bad engine watch can stop this
single-engine worker: pause or correct it through MCP before restarting. Do not
interpret a restart loop as successful recomputation.

A Linux systemd template and a macOS launchd template are in
[`deploy/consistency`](../deploy/consistency/README.md). They are installation
examples, not automatically installed services. Both use the same foreground
command, delayed error restart and signal-based shutdown. The operator supplies
private configuration and credentials. Service-manager installation itself is
separate from repository validation.

Each execution start retains a private session under the artifact directory.
Successful results and exact artifact bytes are stored in PostgreSQL. Session
files are retained for failures and troubleshooting; the operator may remove
old sessions after checking durable archival. They are not an unbounded disk
quota mechanism; provision and monitor storage separately.

## Operate through MCP

Discover tools from the actual Query and core-records processes. Query has five
consistency reads; core-records has one configuration writer.

1. Call `get_consistency_scope` with explicit `namespace`, `scope_ref`, `policy`,
   `max_nodes` (1–256) and `max_edges` (1–4096). It fails on missing necessary
   parents, hidden rows or excess bounds. It does not expand into other scopes.
2. Inspect the returned nodes and participation reasons. Supply external CNF:
   each condition has an exact canonical node `id` and `clauses`; clauses are AND,
   literals inside each clause are OR. A literal has an exact
   `{subject, property, context}` atom and Boolean `negated`. Empty clauses mean
   false. IDs, contexts and rules are declarations, not inferred semantics.
3. Call `register_consistency_watch` with the complete configuration below.
   `expected_revision=0` creates a new watch; a subsequent update must use the
   `current_revision` returned by `get_consistency_watch`.
4. Start the worker or allow the running worker's next poll to process the watch.
5. Call `get_consistency_events` with `watch_id`, `after=0`, and `limit` (1–256).
   Store a separate `next_cursor` per watch and continue while `truncated=true`.
6. For each completed event, call `get_consistency_run` with its exact `run_id`.
   Read `freshness`, `run.diagnosis.outcome`, `reason`, and `localization` separately.

Example configuration shape (replace node IDs with returned canonical IDs and
engine ID with the operator's actual identity):

```json
{
  "contract": "consistency-watch/v1",
  "watch_id": "door-consistency",
  "request_id": "door-config-1",
  "expected_revision": 0,
  "engine_id": "operator-provided-engine-id",
  "max_localization_calls": 64,
  "paused": false,
  "request": {
    "scope": {
      "namespace": "reviewed-example",
      "scope_ref": "door",
      "policy": "classified-frontier/cnf-v1",
      "max_nodes": 256,
      "max_edges": 4096
    },
    "rule_version": "door-rules-1",
    "rules": [],
    "evidence": [
      {
        "id": "canonical-node-id-from-scope",
        "clauses": [[{
          "atom": {"subject": "door", "property": "open", "context": "same-instant"},
          "negated": false
        }]]
      }
    ]
  }
}
```

Supply an entry for every participating node, not just the node illustrated.
A watch keeps a normalization catalog, so annotations for withdrawn/excluded
nodes may remain. New participating nodes without an annotation produce
`missing_normalization`; append a new configuration revision to supply them.
Do not pass `expected_view_id` for a watch: it explicitly monitors changing
snapshots. That field belongs to the native one-shot checking API.
MCP requests are limited to 2 MiB; the native API's larger limit does not widen
the transport contract. Oversize configurations are rejected, not truncated.

To inspect or amend configuration, `get_consistency_watch` requires an explicit
positive `revision`, or `-1` for latest in the read snapshot. To pause or resume,
append the full configuration with the desired `paused` value, a new request ID
and the latest expected revision. To retry a recorded solver failure, likewise
append a new revision even when its formula is unchanged. An exact replay returns
the original historical receipt; it never restores an old active configuration.
Configuration writes use a short journal lock, independent of the worker lock.
A pause can commit during solving: it prevents subsequent ticks, but does not
cancel the in-flight run. That run remains readable as stale history after the
configuration changes. A cancelled write waiting for the journal lock creates
no revision or notification; retry the exact request.

To retrieve proof bytes, call `get_consistency_artifact` with immutable `run_id`,
`artifact_id`, `offset=0`, and `limit` at most 65536. Decode `data_base64`, continue
with `next_offset` until `done`, and verify the assembled bytes against the
returned full-file SHA-256. Keep the same IDs across all chunks. These historical
bytes make no freshness claim; `get_consistency_run` checks that separately.

## Interpret the result

| Field/value | Meaning |
| --- | --- |
| `compatible_declarations` | Selected encoded conditions and rules admit a checked assignment |
| `conflicting_declarations` | Rules are satisfiable but combined evidence is not; checked UNSAT proof |
| `inconsistent_rules` | Rules themselves contradict; do not blame evidence |
| `inconclusive` | Missing/unknown/stale input, empty scope, limits or execution/validation failure |
| `localization.minimal=true` | One irreducible set of fixed formula groups was found |
| `localization.minimal=false` | Localization did not establish minimality; inspect its reason |
| `freshness=matches_snapshot` | Input/configuration match this read's snapshot only |
| `freshness=stale` | History remains readable but no longer matches current input/configuration |
| `freshness=unavailable` | Freshness could not be established |

A three-way contradiction can be detected without pairwise conflict edges.
Graph conflict edges do not automatically create SAT clauses. Localization is
not necessarily the smallest set and is not an evidence withdrawal plan; a
localized derivation can depend on parents retained only in the full input view.
Neither an UNSAT proof nor SAT assignment proves source fidelity or truth.

The default native policy `bound-declarations/cnf-v0` includes historical bound
claims. Explicit `classified-frontier/cnf-v1` excludes proven superseded, expired,
not-yet-valid or invalid derivations, retains competing current branches and
blocks unknown currentness. It does not silently select a winning branch.
Monitoring observes current state on polls, not every intermediate change.
Full limits and transaction semantics are in the
[native contract](../internal/evidenceingestion/CONSISTENCY.md).

## Upgrade an existing installation

Follow [INSTALL: Upgrade](../INSTALL.md#upgrade): backup, stop services, apply
migrations, run `ahe-runtime-admin upgrade` for the original group/LOGIN/profile,
then `verify` with each original serving LOGIN. Update each applicable profile;
new readable tables require policy refresh even for Query. Do not use fresh
users as a substitute for an upgrade. Starting the worker is an explicit
operator action after schema/role verification.

Schema 54 tightens SQL storage checks without changing policy v7 or adding tables.
Version fields and artifact byte counts require canonical nonnegative integer
JSON numbers; strings, decimal/exponent notation and negative zero are rejected.
Configured notifications must name the corresponding configuration hash.
Stop all serving and writing processes before migration. If existing history
violates these checks, migration fails with SQLSTATE 23514 and rolls back;
it does not rewrite immutable records. Preserve the history and review the
upgrade plan. Downgrading guards requires a compatible binary and restores the
old checks; mixed-version operation is not an upgrade guarantee.
