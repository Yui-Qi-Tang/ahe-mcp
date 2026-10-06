# Installing AHE

This guide installs the MCP and operator programs from source. AHE does not yet
publish stable release binaries or a semantic-versioned release. Build and
record a reviewed Git commit for each deployment.

For architecture, authority, and algorithms, see [system design](docs/SYSTEM_DESIGN.md).

## Installation scope

Build the reviewed source commit using the steps below. The current MCP schema
is 54. Query/intake/source-reviewer inventories are 26/5/3. Separate
`relation-reviewer`, `endpoint-reviewer` and `repository-intake` profiles expose
4/2/2 tools; `core-records` exposes six record/configuration tools. The full
migration chain through 54 and role policy v7 are required. Do not perform a
binary-only replacement against an older schema or reuse source-review
credentials for endpoint/relation writes.

The current practical query client explicitly requests both
`query_mode=practical_multisurface_lexical_v1` and
`response_schema=grounded-evidence-brief-v7` from `get_grounded_evidence_brief`.
Query MCP defaults are unchanged. Discover `tools/list` from the installed
server; an older binary lacking this contract must not be treated as equivalent
or silently replaced with another search mode. The research v6 route remains
separate. See [search contracts and limits](docs/SYSTEM_DESIGN.md#search).

The current MCP implementation uses explicit
launcher and database authority. Query is read-only; the ingestion executable
separates five source/extractor intake tools from a three-tool
`source-claim-reviewer`, a four-tool `relation-reviewer`, and two-tool
`endpoint-reviewer` and `repository-intake` profiles.
Its legacy reviewer/operator profiles remain
disabled. Existing internal typed writers remain retained
implementation assets, not newly enabled MCP entrypoints. See
[runtime authority](docs/SYSTEM_DESIGN.md#authority). This guide describes
installation requirements, not completed deployment acceptance.

## Platform support

The core AHE path is not macOS-only.

| Capability | Linux | macOS 13+ | Windows |
| --- | --- | --- | --- |
| Build the six core/operator programs | Supported | Supported | Not qualified |
| `ahe-migrate`, `ahe-query-mcp`, `ahe-ingest-mcp` | Supported | Supported | Not qualified |
| Offline `gopls` extraction | Supported | Supported, with an additional OS sandbox | Not qualified |
| Atlassian and CodeGraph preview adapters | Not currently supported | Supported preview | Not qualified |

The optional preview adapters require macOS sandbox support. They do not limit
the external-agent intake path or Query MCP. CI verifies the MCP module on Linux.

## Requirements

- Go 1.27.0 or a newer Go 1.27 patch release, matching the root `go.mod`;
- Make and standard POSIX command-line tools;
- a reachable PostgreSQL 16+ database; qualify the selected major version with
  the migration and integration gates below;
- Git to clone the source tree;

The CI public-file path check also requires Node.js; `make verify` and MCP
builds require only the Go toolchain. Models and external MCP servers are not
downloaded or configured automatically.

AHE does not provision PostgreSQL databases or authentication credentials. The
new `ahe-runtime-admin provision` command creates a fresh bounded role pair in
an already prepared database; it never creates a password. The role used by
`ahe-migrate` must be able to create and alter the AHE tables, indexes, and
constraints in one pre-existing private schema. It must not be reused as a
serving-runtime identity. The project has not yet declared a PostgreSQL
major-version support matrix, so qualify the selected supported release with the migration and
integration-test gates. This adoption round selects a new dedicated
non-production database and an explicit private schema, retaining existing
databases and their history unchanged. A new schema inside a shared database
does not by itself establish isolation from that database's existing grants.

All core programs that access PostgreSQL read the DSN from `DATABASE_DSN`.

The MCP executables additionally require launcher-owned configuration:

| Variable | Query MCP | Intake MCP | Migration command |
| --- | --- | --- | --- |
| `DATABASE_DSN` | Bounded query LOGIN credentials | Different bounded intake LOGIN credentials | Separate trusted migration-owner credentials |
| `AHE_RUNTIME_PRINCIPAL_ID` | Required fixed consumer identity | Required fixed intake identity | Not used |
| `AHE_DATABASE_ROLE` | Required installed `query` NOLOGIN group role | Required installed `intake` NOLOGIN group role | Not used |
| `AHE_DATABASE_SCHEMA` | Required exact authoritative schema | Required exact authoritative schema | Required pre-existing private schema |
| `AHE_RUNTIME_PROFILE` | Not used; Query profile is fixed by the binary | Required exact value `intake` | Not used |

Missing or invalid values fail startup; there is no trusted default profile or
fallback schema. These values belong to the protected launcher, not tool
arguments or MCP client identity metadata. A launcher principal does not prove
the identity of a human reviewer.

## Build from source

Clone the repository and record the exact commit selected for deployment:

```sh
git clone https://github.com/Yui-Qi-Tang/ahe-mcp.git
cd ahe-mcp
git rev-parse HEAD
go version
make build
```

`make build` writes these core programs to `bin/`:

- `ahe-migrate`;
- `ahe-runtime-admin`;
- `ahe-consistency-worker`;
- `ahe-mcp-launch`;
- `ahe-query-mcp`;
- `ahe-ingest-mcp`.

Build the macOS-only preview adapters only when they are required:

```sh
make adapters
```

To place binaries in a reviewed deployment directory:

```sh
make BIN_DIR=/absolute/release/ahe/bin build
```

Before deployment, run the Go tests, build and vet:

```sh
make verify
go test -mod=readonly -p 2 -race -count=1 ./...
```

These commands work without frontend assets or native UI libraries on Linux
and macOS. Dependency downloads require network access on a fresh machine.
Optional live-test variables must target explicitly selected disposable environments.

### Optional database and process tests

Configure `DATABASE_DSN` only for an explicitly selected disposable test database,
then run from the repository root:

```sh
go test -mod=readonly -count=1 -tags=integration -p 1 -parallel 1 ./...
```

Real `gopls` tests also require an explicit `AHE_GOPLS_PATH`; otherwise they skip. DB-role and
Query/intake subprocess acceptance separately require
`AHE_DBROLE_ACCEPTANCE_DATABASE_DSN`, targeting a fresh disposable database with
closed database/public-schema PUBLIC privileges. Do not share that database
with simultaneous migration tests. Use `GOFLAGS=-race` in addition to
`go test -race` when subprocess builds must also be instrumented;  Skipped opt-in tests are not deployment acceptance.

## Configure PostgreSQL

An authorized operator must first prepare the new dedicated database and one
private schema, such as `ahe`. `public`, system schemas, missing schemas and
multi-schema fallback search paths are rejected by the migration command.
If its DSN specifies `search_path`, it must select only that schema, optionally
followed by `pg_catalog`. The command does not create the database or schema.

Keep separate migration, query and intake DSNs in operator-managed files
outside the repository and outside MCP JSON. The example paths below
must already contain the corresponding credentials, with directory mode `0700`
and file mode `0600`; do not overwrite an existing credential file. Do not print
their contents or place credentials directly in a launcher or tool payload.

After the new schema is explicitly provisioned, apply this target repository's
embedded forward-only migrations with the separate migration identity:

```sh
DATABASE_DSN="$(cat /absolute/private/ahe/migration-database-dns)" \
  AHE_DATABASE_SCHEMA=ahe \
  ./bin/ahe-migrate
```

Success returns credential-free JSON containing `schema_version`, `schema`,
`changed`, `applied_migrations`, and `latest_migration`. Repeating the command is
safe when the selected schema, migration ledger and checksums match. The current
native schema is 54; migrations 1–53 retain their original checksums.
Migration 43 retains the partial unique source-extraction request index.
Migration 44 requires no proposal/canonical/admission authority and exactly the
native empty Supersession head before adding ordinary admission manifests;
45 requires no intermediate ordinary history before adding review bindings.
Migration 46 adds separate append-only bindings for exact reviewed
`reject`/`audit_only` dispositions without canonical nodes or edges; it does not
reinterpret existing admission/review records.
Migrations 47–48 add independent implementation/reference receipts and guarded
edge admission. They refuse existing unreceipted edges of those relation types;
they do not retroactively approve, delete or rewrite those edges.
Migration 49 adds exact endpoint review bindings without rewriting existing
evidence or relabeling historical approvals. Qualify backup restoration before
upgrading retained evidence; for an isolated trial, restore into a new database
without importing old ownership or runtime ACLs and verify the retained rows.
A restore with `pg_restore --no-acl` also drops the native denial of function
execution to `PUBLIC`. Before migration, the authorized operator must restore
that denial in the selected AHE schema (for example,
`REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA ahe FROM PUBLIC;`).
Do not grant missing runtime privileges or weaken the migration verifier to
make a restored database pass. Then migrate and provision fresh runtime
identities. Keep the original database and launchers unchanged until a separate
client cutover is approved.
Failed preflight stops application atomically without deleting or rewriting data.
It does not import Core's diverged migration ledger or move historical data.
The new runtime also verifies schema-local index, trigger and guarded function
definitions, not only recorded migration checksums. An installation example is
not authorization to upgrade an existing deployment or change its client configuration.

The MCP executables verify that exact schema and refuse to serve when its
migrations or authority are missing or drifted. AHE does not ship
application-level downgrade migrations; restoring a separately qualified
database backup is not an automatic application rollback.
### Runtime role provisioning gate

Migration success does not install runtime roles. Query, intake and reviewer each need
a different bounded `LOGIN NOINHERIT` session identity and its own installed
`NOLOGIN` group role. Each LOGIN must have exactly one non-admin, non-inheriting
`SET ROLE` membership in its matching group, not direct schema/table grants.
Neither identity may own the database/schema/objects or have superuser,
`BYPASSRLS`, role-creation, database-creation or other excess authority.

The target-native `internal/dbrole.InstallPolicy` API still accepts only a
pre-existing group role. The new `ahe-runtime-admin provision` command adds the
bounded operator entrypoint: verify native migrations, create a **new** group
and LOGIN, install the selected policy and exact membership atomically. Either
existing role name causes rejection; it never takes over, repairs or rotates an
existing role. Database/schema preparation and PostgreSQL authentication remain
external operator responsibilities. Do not substitute an operator DSN to make a
serving runtime start. See the [runtime authority boundary](docs/SYSTEM_DESIGN.md#authority).

For existing installations, use `ahe-runtime-admin upgrade` with the original
pair/profile after migration; see [Upgrade](#upgrade). This is distinct from
fresh provisioning and never creates a missing role.

This first role-creation entrypoint requires a separately authorized PostgreSQL
16+ superuser connection whose session identity is unchanged. Non-superuser
role creation can introduce creator-admin memberships; this slice refuses that
path instead of weakening the closed runtime policy. No serving process receives
the provisioning identity or its credentials.

Large-object authority is checked through PostgreSQL 16-compatible catalog ACLs,
including PUBLIC, inherited grants and owner defaults. The check is not skipped
on PostgreSQL 16/17 and does not require the PostgreSQL 18-only
`has_largeobject_privilege()` function. The minimum remains PostgreSQL 16.
Migration constraint verification also binds each constraint to its expected
table, since `LIKE ... INCLUDING CONSTRAINTS` can copy a CHECK name to another
table in the same schema.

After separately preparing the dedicated database, private schema, migrations
and database-wide prerequisites, an authorized operator may run this example
with its separate protected operator credential file:

```sh
DATABASE_DSN="$(cat /absolute/private/ahe/operator-database-dns)" \
  AHE_DATABASE_NAME=ahe_next_eval \
  AHE_DATABASE_SCHEMA=ahe \
  AHE_DATABASE_ROLE=ahe_query_runtime \
  AHE_DATABASE_LOGIN=ahe_query_login \
  AHE_RUNTIME_PROFILE=query \
  ./bin/ahe-runtime-admin provision
```

Use distinct new names and the corresponding profile for intake and source
review. No password, pg_hba rule or credential file is generated; the operator
must separately qualify authentication for those LOGIN identities. Then run
the same command with `verify` and the corresponding **bounded LOGIN**
credential, not the operator credential. `verify` checks actual runtime login,
role policy and native schema without writing evidence or ACLs. An interrupted
or lost provisioning response is uncertain: use `verify`, never overwrite a
role pair or assume it failed. Existing pairs make another `provision` fail.

This v1 operator command accepts an explicit single-target PostgreSQL URL whose
database matches `AHE_DATABASE_NAME`. Ambient `PG*` variables, service/passfile
indirection, client certificate files and implicit connection targets are not
accepted. Choose `sslmode=disable`, `require` or `verify-full` explicitly;
`verify-full` uses system roots. Do not lower TLS protection to work around a
deployment's unsupported authentication or private-CA requirement.
Unix sockets require explicit `sslmode=disable`, because PostgreSQL does not
apply TLS to that transport; local socket authentication must be qualified separately.

Database-wide `PUBLIC CREATE` and `TEMPORARY` authority must already be closed
by an explicitly approved provisioning step. The API checks that boundary but
does not change it; it does replace the selected group's ACL and remove PUBLIC
authority inside the selected schema. It also rejects excess authority through
other schemas. Never run that installation against existing/shared database
ACLs without separate approval. Policy v5 includes independent relation/endpoint receipts and the endpoint lock-only helper.
The relation reviewer may read the bounded schema and INSERT only canonical
edges and the two relation receipt tables; it cannot create nodes or change
source proposals. Query, intake, source-claim-reviewer and relation-reviewer
receive no direct helper EXECUTE or Supersession/contradiction/repository writer
grants. The separate endpoint profile's lock-only exception is documented below. Reviewer table
ACLs still represent trusted raw DML; they are not universal exact-review routing.

Runtime pools apply the selected role and verify policy on every new physical
connection, then recheck the session binding and complete ACL policy before reuse. Query visibility is
the complete database/schema cut, not per-row tenancy. Its canonical read-view
handles belong to the fixed principal and database-role binding and cannot be
transferred to a different process/visibility cut.

## Repository and derived endpoint writers

These are separately provisioned profiles, not extra source-reviewer tools.
Apply migration 49 and provision/verify matching roles with the operator
workflow above. A schema upgrade also changes the required runtime policy.
For fresh installations, provision distinct fresh
pairs for the new installation and verify each one, rather than rerunning
`provision` on old names or manually adding a missing grant.

- `repository-intake`: `capture_repository_snapshot` and
  `extract_repository_go`. Add `repository_root` (an absolute approved local
  repository directory) and `repository_id` (an operator-assigned identity)
  to this profile's protected launcher JSON. Capture requires trusted Git at
  `/usr/bin/git`. Both fields are forbidden for
  other profiles. The tool accepts a full immutable commit SHA, never a branch,
  directory or command. Git transport protocols, inherited Git configuration
  and replacement objects are disabled. No checkout, build, gopls, dependency
  download or model runs. Capture retains the native limits: 10,000 tracked
  regular Go files, 16 MiB/file, 256 MiB total. Parser extraction is limited to
  256 files and 8 MiB. The generation stays inactive; use returned proposal IDs
  or Query `lifecycle_scope=all`.
- `endpoint-reviewer`: `get_endpoint_review` and
  `admit_reviewed_endpoint`. `kind=repository_code` requires a verified
  repository-backed Go proposal and forbids `derivation`.
  `kind=derived_spec` requires an original-source-backed statement proposal
  plus `derivation`: 1–8 unique sorted admitted `parent_node_ids`, `method`,
  `producer` and `trace_ref`. Submit the statement through existing source
  and extractor intake; never invent an original document for a derivation.
  Complete AND ancestry is limited to 64 nodes and depth 8.

Show the current `lifecycle` together with the entire immutable `display`
(at most 1 MiB), including source context, code
path/commit/blob, parents and limitations. Oversized complete-file/source context
is refused, not silently truncated. Nested source-basis effects describe
provenance context only; approval is for the requested endpoint. Obtain the
human decision and reason, then submit the exact `review`, returned `subject`
as `expected_subject`, `decision=approved`, `decision_reason` and a stable
`request_id`. Preserve these unchanged for uncertain-outcome retries.

Check `lifecycle.mode` first: `pending_admission` is a new review;
`exact_replay_only` means the endpoint is already admitted and includes its
canonical ID and original receipt. In that case the nested display is the
historical pre-admission snapshot, not a new pending proposal. Only the original
unchanged admission request may be retried; do not request a new decision.
An admitted proposal without a matching endpoint receipt is refused.

Admission atomically saves the endpoint, native support/AND edges, ordinary
manifest and independent endpoint receipt. Query `get_evidence_record` with
its canonical ID returns `endpoint_admission`. The endpoint role cannot
collect sources, activate repositories or write independent relations.
Canonical UPDATE remains forbidden; its only executable helper is a
schema-pinned, bounded `FOR KEY SHARE` reader. Exact review is not proof of
human attention or semantic entailment.

Only after both endpoints are admitted should the separate relation reviewer
prepare `implements`. Generic, contradiction and Supersession writers remain
disabled. There is no new endpoint `reject`/`audit_only` writer; never relabel
code as a source claim to use source-disposition tools.

## Core records profile

`core-records` is a separate `ahe-ingest-mcp` profile for externally authorized
identity decisions and external reports. Provision a fresh role pair with
`AHE_RUNTIME_PROFILE=core-records ./bin/ahe-runtime-admin provision` using the
same protected operator setup as the other profiles. Its protected launcher
configuration uses `ahe-ingest-mcp`, `profile: core-records` and a fixed principal.
Do not share its credentials with Query, intake, automatic extractors or reviewers.

Its six tools are `bind_canonical_proposition`, `change_proposition_binding`,
`record_external_check`, `record_external_representation` and
`link_external_check_representation` and `register_consistency_watch`. The last
registers externally supplied normalized conditions for the separately started
[consistency worker](docs/CONSISTENCY.md); it does not execute a solver in the
MCP request or admit evidence. Read subjects, current bindings, explicit
history and stored checker material through Query before preparing writes. Show
an identity decision's original claim, exact identity/definition and reason;
record only an explicitly authorized decision. A correction requires the exact
previous revision/reference/from_id; stale changes fail rather than being ranked.
Exact retries preserve their original request IDs and content.

The launcher sets `decision_by` / `recorded_by`; those fields are forbidden in
MCP input. Claimed checker/producer names remain separate external assertions.
These are trusted Go writer guarantees: database credentials grant raw DML and
SQL alone does not authenticate the claimed recorder. Database constraints do
enforce immutable history, exact subjects, transition ordering, dependency shape
and complete checker-input links. The binding table's UPDATE privilege exists
only for row locking; its trigger rejects actual UPDATE. This profile cannot
collect sources, admit claims or write canonical graph nodes/edges.

External declarations do not prove equivalence, faithful extraction, executed
checks, freshness, sufficiency or approval. Missing/supplied state and linked
checks do not trigger automatic invalidation/recomputation. Dependency lookup
bounds output but has no large-dataset latency qualification. Ordinary downstream
agents should retain Query access only.

## Independent relation reviewer

This optional profile is separate from source intake and source-claim review.
Provision a dedicated LOGIN/NOLOGIN pair with
`AHE_RUNTIME_PROFILE=relation-reviewer ./bin/ahe-runtime-admin provision`,
supplying the same explicit database/schema prerequisites and role environment
variables described above.
Keep its authentication external and verify that exact runtime policy before
starting `ahe-ingest-mcp` with `AHE_RUNTIME_PROFILE=relation-reviewer`.
For protected `ahe-mcp-launch` configurations, select binary
`ahe-ingest-mcp` and profile `relation-reviewer`; do not share its launcher
or credentials with the extraction agent.

The four tools are `get_implements_review`, `admit_reviewed_implements`,
`get_references_review` and `admit_reviewed_references`. Discover their live
schemas. Display the complete returned review, obtain explicit human approval
of that exact relation, then submit its unchanged `review` and `subject`
(as `expected_subject`), `decision=approved`, an explicit reason and a stable
request ID. The launcher supplies reviewer identity; tool arguments cannot.
Preserve those exact admission inputs for an uncertain-outcome retry.

Supported scope:

- `implements`: an already-admitted derived specification, its complete
  recursive AND ancestry, and an already-admitted repository-backed Go code
  endpoint. The profile creates neither derived nodes nor repository endpoints;
  it does not expose a direct source-specification writer.
- `references`: qualified external-source evidence with original-byte
  `[[ahe-ref:#anchor]]` or snapshot-pinned reference markers and exact
  `[[ahe-anchor:...]]` targets. Arbitrary URLs, Jira links and guessed references
  are not accepted. Do not add markers to captured originals to bypass this.
- Each write atomically saves one edge and its independent receipt. Node
  approval is not edge approval. A stale review, changed retry or ambiguous
  target fails closed. This profile does not persist relation `reject` or
  `audit_only` decisions; retain those non-write outcomes with the human.
- Use Query `get_relation_provenance` readback: the returned relation includes
  `implements_admission` or `references_admission`. A graph edge or locally
  constructed receipt is not proof that the native writer committed.

These checks bind exact content and the trusted reviewer principal. They do
not authenticate the human conversation or prove semantic correctness.

## Bounded external intake MCP installation

Source acquisition belongs to the separately authorized external client and its
connectors and extractors. The current MCP boundary consists of:

- `ahe-ingest-mcp` with `AHE_RUNTIME_PROFILE=intake`, exposing only
  `submit_manual_evidence`, `submit_text_source`, `submit_external_source`,
  `get_extractor_input` and `submit_extractor_output`;
- `ahe-query-mcp` for read-only proposal review and evidence queries;
- a separately controlled `source-claim-reviewer` process for exact source review
  and admit/reject/audit_only, never shared with the intake client's credentials;
- the separately migrated and role-qualified private schema.

Intake writes source/extraction/pending-proposal state; it is not read-only and
cannot admit, reject, activate, run collectors or mutate canonical evidence.
The internal 43-tool registry is not the current executable's exposed
capability set. Setting `legacy-reviewer` or `legacy-operator` causes startup
rejection, even though those profiles and typed workflow implementations exist
internally. The reviewer profile exposes only `get_source_claim_review`,
`admit_reviewed_source_claim` and `record_reviewed_source_claim_disposition`;
its launcher principal must match the typed writer
binding. Tool arguments cannot supply reviewer identity. Its canonical writes
require the exact complete display/subject, explicit approval and reason.
Reviewed `reject`/`audit_only` also require the exact review and explicit human
decision, but create no canonical nodes or edges. `pending` is a local pause,
not a reviewer write. Intake/reviewer/Query inventories are 5/3/26.

Preserve exact connector-observed text/JSON and provider identity/revision.
`exact_excerpt` and `truncated_document` coverage require limitations;
`full_document` forbids them. Reusing a provider revision with different source
content or source-level metadata conflicts. Extraction request IDs are reusable
only for exact retries; use a new ID for different source/extractor inputs.
An empty proposal list records abstention, not a failure or admission.

`extractor_definition` requires a name and version, each at most 200 UTF-8 bytes.
Its config permits 64 entries, 200-byte keys, 4096-byte values and at most 60 KiB
of compact JSON. Optional `producer_session_ref` must be an opaque identifier,
not credentials or conversation text; omit it if unavailable.

The review getter accepts pending proposals only. If a terminal write's outcome
is uncertain, retry the saved exact writer request with its original subject,
extraction attempt, principal, outcome and reason; do not construct a new review
or change the request to recover a receipt.

For a separately authorized reviewer, use the same protected launcher shape as
intake, but with its own reviewer credential file, installed
`source-claim-reviewer` group, fixed reviewer principal, and
`AHE_RUNTIME_PROFILE=source-claim-reviewer`. Never reuse intake or migration-owner
credentials. The environment table above describes intake; reviewer uses the
same variable names with these distinct bindings. No real launcher is provisioned
by this guide. See the [exact review contract](docs/SYSTEM_DESIGN.md#review).
Qualify provisioning, authentication and review operations in the selected
environment before use; a source build is not that acceptance.

Both MCP programs use JSON-RPC over stdio. Configure the MCP client to launch
them; do not run them as shared TCP services, and do not allow logs on stdout.

After the provisioning and authentication gates are satisfied, use
`ahe-mcp-launch` with a protected, closed v1 configuration. For example,
`/absolute/private/ahe/query/config.json` contains no DSN, only its file path:

```json
{
  "schema_version": "ahe-mcp-launcher/v1",
  "binary_path": "/absolute/release/ahe/bin/ahe-query-mcp",
  "database_dns_file": "/absolute/private/ahe/query/database-dns",
  "database": "ahe_next_eval",
  "session_user": "ahe_query_login",
  "schema": "ahe",
  "role": "ahe_query_runtime",
  "profile": "query",
  "principal_id": "query-consumer"
}
```

Both configuration and DSN must be bounded regular files owned by the running
operator with mode `0600`; their directory must be protected. Symlinks, special
files and writable untrusted paths are rejected. Protect the binaries and
launcher too. The launcher fixes the environment, checks explicit database/login
coordinates and executes the selected MCP binary with unchanged stdio. It
does not authenticate a human, prove binary authenticity, configure a DB or
bypass the runtime's independent schema/role verifier.

An operator-created `/absolute/private/ahe/query-launcher` can contain:

```sh
#!/bin/sh
set -eu
exec /absolute/release/ahe/bin/ahe-mcp-launch --config /absolute/private/ahe/query/config.json
```

The separate `/absolute/private/ahe/intake-launcher` uses its own protected
configuration: `profile=intake`, `binary_path` ending in `ahe-ingest-mcp`, its
intake LOGIN/group/principal and separate credential file:

```sh
#!/bin/sh
set -eu
exec /absolute/release/ahe/bin/ahe-mcp-launch --config /absolute/private/ahe/intake/config.json
```

These are templates, not provisioned launchers or proof that the named roles
exist. Replace paths and fixed identities with the reviewed configuration;
protect launcher files with mode `0700` and do not add logging or shell tracing.
Each MCP `command` is one absolute executable path, not shell assignments.

An ordinary read-only consumer should receive only the query launcher:

```json
{
  "mcpServers": {
    "ahe-query": {
      "command": "/absolute/private/ahe/query-launcher"
    }
  }
}
```

A separately authorized external intake workflow may receive this different
pending-only process:

```json
{
  "mcpServers": {
    "ahe-ingest": {
      "command": "/absolute/private/ahe/intake-launcher"
    }
  }
}
```

Keep the client configuration protected. The ingestion server must not be
registered in an ordinary downstream consumer profile. Clients should call
`tools/list` and use the returned input schemas instead of copying a frozen
tool schema from documentation.

After connecting the client, follow the [first evidence workflow](docs/FIRST_WORKFLOW.md)
to check the tool inventories, pending/admitted states and exact source readback.
An empty new database is expected; no search result alone is not a connection test.

## Upgrade

This build requires schema **54** and database role policy **v7**. Updating a
schema-49 installation is **not** a binary-only replacement: migrations 50–53 add
Core record and consistency-history tables. From schema 49, all profiles need
SELECT on eleven new tables; from schema 52, five new diagnostic tables. Refresh
the authorized core-records writer grants as well.

For a separately authorized upgrade, protect a backup, stop serving processes,
record old/new commits, apply the product's `ahe-migrate`, and refresh the same
existing group roles using `ahe-runtime-admin upgrade` with their original
profile, group, LOGIN, database and schema environment settings. Use the separate
operator credential for this step. The command rejects missing roles, wrong
memberships and unsafe identities, and applies/checks policy in one transaction.
It never recreates users, changes passwords or grants a new membership.

Then run `ahe-runtime-admin verify` with each original bounded LOGIN credential
before restarting and rediscovering `tools/list`. A successful upgrade reports
`updated_existing_pair=true` and `runtime_login_verified=false`: it does not
claim that the operator authenticated as that LOGIN. An uncertain result must be
resolved with verify; do not run provision or replace identities. Old ACLs fail
startup until refreshed. For a group shared by multiple logins, upgrade one exact
pair, then verify every serving login. Coordinate all role administration while
services are stopped; concurrent privileged role deletion/recreation is outside
this operator contract. Repository validation does not deploy this upgrade.

Schema 53 requires migration 54 before this binary starts. Stop all writers and
workers, then migrate and verify the original LOGINs. This guard-only upgrade
keeps policy v7 and existing ACLs. It refuses incompatible historical JSON integer
representations or configured-event hashes with SQLSTATE 23514; preserve those
records for review rather than deleting or rewriting history to force an upgrade.

Provision `core-records` separately only if its writes are authorized. Existing
profiles gain reads, not the new write capability. For an already qualified
schema-54/policy-v7 installation, a binary-only replacement retains existing
protected launcher paths, identities and credentials; verify them before reconnecting.
Never copy credentials into repository artifacts.

### Separate database-adoption procedure

Use this procedure when adopting retained evidence into a separately provisioned
target database. For an existing native ahe-mcp installation, use the in-place
schema and role upgrade above. This separate-target checklist is conditional on
environment and role provisioning; it does not authorize data conversion or ACL
changes.

1. Back up the PostgreSQL database.
2. Select and record the reviewed AHE commit.
3. Build and verify the new checkout.
4. Stop the affected AHE processes.
5. Prepare the separately selected target database/private schema, operator
   identity and explicit database-wide privilege prerequisites. Do not pre-create
   the runtime role pairs intended for the fresh provision command. If restoring
   retained evidence, follow the restoration safeguards above, including native
   PUBLIC function-execution denial and historical-row verification.
6. Run the matching `ahe-migrate` with the explicit schema and separate
   migration credentials.
7. Create the new runtime pairs and install the target-native role policy after
   migration, through the separately authorized `ahe-runtime-admin provision`.
8. Separately configure LOGIN authentication and protected credentials, then run
   `ahe-runtime-admin verify` using each bounded LOGIN credential.
9. Start only the separately qualified launchers required for the selected
   workflow: query, intake, source-review, relation-review, repository-intake or
   endpoint-review. Old DSN-only launchers and legacy review/operator profiles
   no longer start successfully. Do not
   reactivate the legacy collector as a substitute for the disabled writers.

Do not replace or edit an already applied migration. The runtime verifies
embedded migration names and checksums.

## Common startup failures

- `DATABASE_DSN is required`: the MCP client or shell did not provide the DSN.
- missing/invalid runtime principal, profile, role or schema: configure the
  protected launcher; do not inject replacement identity through tool arguments.
- `legacy writer runtime profiles are not enabled`: this executable currently
  accepts `core-records`, `intake`, `source-claim-reviewer`, `relation-reviewer`, `endpoint-reviewer` or `repository-intake`; do not weaken the gate to recover old writer exposure.
- schema/search-path rejection: pre-provision the exact private schema and
  remove fallback from the migration connection configuration, not from an
  existing database's ACLs.
- `database role policy violation`: inspect the approved role/membership/ACL
  provisioning. Migration-owner credentials and broader grants are not fixes.
- `database schema is not current`: run the matching build's `ahe-migrate`.
- a macOS loopback-sandbox error on Linux: a macOS-only preview path was
  selected; use external connector intake or a supported local source instead.
- JSON-RPC parse failures: ensure no wrapper, logger, or shell startup file
  writes non-protocol output to MCP stdout.
