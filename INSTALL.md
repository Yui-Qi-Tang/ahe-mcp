# Installing AHE

> **Desktop 凍結／目前不工作（不可用） — 2026-09-14.** Do not install, launch,
> upgrade or accept Desktop as a working product using this guide. Desktop
> procedures below are retained references only. MCP setup is unchanged;
> Detective CLI end-to-end engineering intake remains incomplete.

This guide installs the current controlled-pilot source tree, including the MCP
programs and Detective CLI/Desktop in one Go module. AHE does not yet publish
stable release binaries, packages, or a semantic-versioned release, so
deployments should build and record a reviewed Git commit. The frozen Desktop
code is `0.1.0-preview.18`; its historical installation reference is in
[apps/detective/INSTALL.md](apps/detective/INSTALL.md).

For architecture, authority, and algorithms, see [system design](docs/SYSTEM_DESIGN.md).

## Installation scope

Build the reviewed source commit using the steps below. The current MCP schema
remains 46; Query/intake/source-reviewer tool inventories remain 13/5/3, with
separate protected launchers and database roles. This checkpoint does not require
new migrations or grant new writer authority.

The current practical query client explicitly requests both
`query_mode=practical_multisurface_lexical_v1` and
`response_schema=grounded-evidence-brief-v7` from `get_grounded_evidence_brief`.
Query MCP defaults are unchanged. Discover `tools/list` from the installed
server; an older binary lacking this contract must not be treated as equivalent
or silently replaced with another search mode. The research v6 route remains
separate. See [search contracts and limits](docs/SYSTEM_DESIGN.md#search).

Detective source is now included under `apps/detective/` and uses this
repository's root `go.mod` and `go.sum`. It has no nested Go module and does not
require the former separate checkout. `make detective` builds its CLI and
synthetic source tools; `make desktop` builds its macOS arm64 app and local
launcher. This is a source installation, not a prebuilt or notarized installer.
This repository's `bin/ahe-detective` is still the **legacy collector**, not the
new `bin/detective` CLI or Desktop. Do not use the legacy launchd instructions
below to install Desktop. Follow [Desktop installation and verification](apps/detective/INSTALL.md).

The current MCP implementation uses explicit
launcher and database authority. Query is read-only; the ingestion executable
separates five source/extractor intake tools from a three-tool
`source-claim-reviewer` profile. Its legacy reviewer/operator profiles remain
disabled. Existing typed writers and collector code remain legacy
implementation assets, not newly enabled MCP entrypoints. See
[runtime authority](docs/SYSTEM_DESIGN.md#authority). This guide describes
installation requirements, not completed deployment acceptance.

## Platform support

The core AHE path is not macOS-only.

| Capability | Linux | macOS 13+ | Windows |
| --- | --- | --- | --- |
| Build the six core/operator programs | Supported | Supported | Not qualified |
| `ahe-migrate`, `ahe-query-mcp`, `ahe-ingest-mcp` | Supported | Supported | Not qualified |
| Legacy `ahe-detective` with local Git or text sources | Supported | Supported | Not qualified |
| Offline `gopls` extraction | Supported | Supported, with an additional OS sandbox | Not qualified |
| Legacy Detective MCP-read sources | Not currently supported | Supported preview | Not qualified |
| Atlassian and CodeGraph preview adapters | Not currently supported | Supported preview | Not qualified |
| Included supervised-service packaging | Not included | `launchd` packaging included | Not included |
| New Detective Desktop (`apps/detective`) | Unavailable / frozen | Unavailable / frozen | Unavailable / frozen |

The legacy macOS-only collectors and adapters require AHE's exact `sandbox-exec` loopback policy.
The frozen Desktop code retains a separate [official Atlassian OAuth connection](apps/detective/INSTALL.md#connect-directly-to-atlassian-rovo-mcp-with-oauth),
not a currently supported connection route.
They do not limit the external-agent intake path or the Query MCP. The CI
configuration runs the shared-module race checks on Linux with the native build
dependencies and test-package limit described below.

## Requirements

- Go 1.27.0 or a newer Go 1.27 patch release, matching the root `go.mod`;
- Make and standard POSIX command-line tools;
- a reachable PostgreSQL database;
- Git to clone the source tree;
- Git on `PATH` and a local workspace only when using the Detective Git source.

For the combined verification suite, also install Node.js 22.22.2 or newer and
npm. Building only the MCP/operator binaries with `make build` does not require
Node.js. Building Desktop additionally requires macOS 13+ on Apple silicon and
Xcode Command Line Tools. PostgreSQL is not needed for offline Desktop source
inspection or deterministic tests. Neither model weights nor external MCP
servers are downloaded or configured automatically.

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

All core programs that access PostgreSQL read the DSN from `DATABASE_DSN`. The
name is intentionally `DATABASE_DSN`, not `DATABASE_DSN`.

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
- `ahe-mcp-launch`;
- `ahe-detective`;
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

From the same repository root, build the Detective CLI for development:

```sh
make detective
```

This produces `bin/detective`, `bin/detective-source-demo` and
`bin/detective-news-source`. The Desktop build target remains in the tree but
is outside the current installation/acceptance path. All share the root Go module;
do not run `go mod init` or restore the former Detective `go.mod` beneath it.
The [Desktop installation document](apps/detective/INSTALL.md) retains historical
workspace and signing information, not instructions to resume its use.

Before deployment, verify the checkout, including the frontend required by the
shared module:

```sh
make verify
```

`make verify` installs the locked frontend dependencies with npm, builds its
embedded assets, and runs frontend tests plus the Go tests/build/vet. Go and npm
dependency downloads require network access on a fresh machine; a local model,
source account, or live DB is not part of this ordinary gate. Optional live
integration-test environment variables must be configured only for explicitly
selected disposable environments.

### Shared-module verification on Linux

The root module now contains the Wails Desktop package, so full-module Go
verification on Linux needs a C toolchain, `pkg-config`, GTK 3 and WebKitGTK 4.1
development libraries as well as Node.js. The repository's Ubuntu CI installs
`libgtk-3-dev`, `libwebkit2gtk-4.1-dev` and `pkg-config`, then runs:

```sh
GOFLAGS=-tags=webkit2_41 make verify
GOFLAGS=-tags=webkit2_41 go test -mod=readonly -p 2 -race -count=1 ./...
```

Use that tag when the installed WebKit development library is 4.1. This is a
shared-module build/test path, not a qualified Linux Desktop distribution.
MCP-only `make build` remains independent of the frontend/native Desktop build.
The verification commands limit simultaneous Go test packages to two. Test
assertions, individual deadlines and the race detector remain unchanged.

### Optional database and process tests

Configure `DATABASE_DSN` only for an explicitly selected disposable test database,
then run from the repository root after `make frontend`:

```sh
go test -mod=readonly -count=1 -tags=integration -p 1 -parallel 1 ./...
go test -mod=readonly -count=1 -tags=acceptance -p 1 -parallel 1 ./cmd/ahe-detective
```

On Linux, combine the relevant test tag with `webkit2_41`. Real `gopls` tests
also require an explicit `AHE_GOPLS_PATH`; otherwise they skip. DB-role and
Query/intake subprocess acceptance separately require
`AHE_DBROLE_ACCEPTANCE_DATABASE_DSN`, targeting a fresh disposable database with
closed database/public-schema PUBLIC privileges. Do not share that database
with simultaneous migration tests. Use `GOFLAGS=-race` in addition to
`go test -race` when subprocess builds must also be instrumented; preserve any
required Linux build tag. Skipped opt-in tests are not deployment acceptance.

## Configure PostgreSQL

An authorized operator must first prepare the new dedicated database and one
private schema, such as `ahe`. `public`, system schemas, missing schemas and
multi-schema fallback search paths are rejected by the migration command.
If its DSN specifies `search_path`, it must select only that schema, optionally
followed by `pg_catalog`. The command does not create the database or schema.

Keep separate migration, query and intake DSNs in operator-managed files
outside the repository and outside MCP/Detective JSON. The example paths below
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
safe when the selected schema, migration ledger and checksums match. This round
advances the native schema to 46 while keeping SQL migrations 1–45 unchanged
relative to the preceding source-review checkpoint.
Migration 43 retains the partial unique source-extraction request index.
Migration 44 requires no proposal/canonical/admission authority and exactly the
native empty Supersession head before adding ordinary admission manifests;
45 requires no intermediate ordinary history before adding review bindings.
Migration 46 adds separate append-only bindings for exact reviewed
`reject`/`audit_only` dispositions without canonical nodes or edges; it does not
reinterpret existing admission/review records.
Failed preflight stops application atomically without deleting or rewriting data.
It does not import Core's diverged migration ledger or move historical data.
The new runtime also verifies schema-local index, trigger and guarded function
definitions, not only recorded migration checksums. This round targets new disposable schemas,
not an authorized upgrade of an existing deployment.

The MCP executables verify that exact schema and refuse to serve when its
migrations or authority are missing or drifted. AHE does not ship
application-level downgrade migrations; restoring a separately qualified
database backup is not an automatic application rollback. The legacy
`ahe-detective --migrate` path is not the new explicit-schema provisioning path.

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
ACLs without separate approval. Policy v3 includes the ordinary source-review
tables and the separate reviewed-disposition binding table needed by the
reviewer. None of these profiles receives direct helper
EXECUTE or Supersession/contradiction/repository writer grants. Reviewer table
ACLs still represent trusted raw DML; they are not universal exact-review routing.

Runtime pools apply the selected role and verify policy on every new physical
connection, then recheck the session binding and complete ACL policy before reuse. Query visibility is
the complete database/schema cut, not per-row tenancy. Its canonical read-view
handles belong to the fixed principal and database-role binding and cannot be
transferred to a different process/visibility cut.

## Bounded Detective-to-pending MCP installation

Source acquisition belongs solely to Detective, including its authorized
connectors and delegated extractors. The current MCP boundary consists of:

- `ahe-ingest-mcp` with `AHE_RUNTIME_PROFILE=intake`, exposing only
  `submit_manual_evidence`, `submit_text_source`, `submit_external_source`,
  `get_extractor_input` and `submit_extractor_output`;
- `ahe-query-mcp` for read-only proposal review and evidence queries;
- a separately controlled `source-claim-reviewer` process for exact source review
  and admit/reject/audit_only, never shared with Detective's intake credentials;
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
not a reviewer write. Intake/reviewer/Query inventories are 5/3/13.

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
by this guide. See the [exact review contract](docs/SYSTEM_DESIGN.md#review) and
the [included Detective workflow](apps/detective/README.md#authority-and-review).
Qualify provisioning, authentication and review operations in the selected
environment before use; a source build is not that acceptance.

This MCP boundary does not require the legacy bundled `ahe-detective` binary,
Ollama, host-v4, the preview adapters or macOS. That packaging fact does not
create a second source owner. The included Detective application retains bounded
pending checkpoint/resume and exact-review integration. It calls the separately
configured MCP launchers over stdio rather than sharing the server's DB
credentials or writing directly to PostgreSQL. Sharing source and dependencies
does not qualify a deployment. See the [checkpoint and recovery boundary](docs/SYSTEM_DESIGN.md#detective).

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

A separately authorized Detective intake workflow may receive this different
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

## Legacy bundled Detective installation

The retained host configuration below describes the older bundled collector,
not the included `apps/detective` CLI/Desktop or a new MCP entrypoint. Its
startup and database path have not been converted to the new query/intake role
contract. Use only in a separately qualified legacy environment; do not reuse
the migration-owner or current bounded MCP credentials for this path.

For an already approved legacy local Git/text collection environment:

```sh
cp configs/detective.example.json /absolute/private/ahe/detective.json
chmod 0600 /absolute/private/ahe/detective.json
```

Edit the copied file with absolute deployment identities and workspace paths,
using the [example configuration](configs/detective.example.json).
Workspace registration is immutable: changing its root or source set requires
new `workspace_id` and `registration_request_id` values. Git sources require
`relative_path="."`, `source_interval` of at least one second, `max_steps` of
at least two, and enabled repository maintenance. A `local-prd-text/v1`-only
host may disable repository maintenance. Then start the foreground process:

```sh
DATABASE_DSN="$(cat /absolute/private/ahe/legacy-detective-database-dns)" \
  ./bin/ahe-detective \
  --config /absolute/private/ahe/detective.json
```

The default host-v1 configuration is model-free. The v2-v4 planner, MCP-read,
and local-model contracts are opt-in previews. On macOS, optional per-user
`launchd` packaging is documented in
[deploy/macos/README.md](deploy/macos/README.md). No Linux `systemd` unit or
Windows service definition is currently included.

### Legacy MCP engineering source with whole-unit model selection

This macOS-only preview applies to `bin/ahe-detective`, not the included
`apps/detective` CLI/Desktop. The original `configs/detective.example.json`
collects a Git repository; it enables neither an Ollama planner nor model
proposal extraction. A planner chooses work; it does not extract proposals.
MCP read sources require the planner to remain disabled.

This walkthrough describes the retained whole-unit baseline, not the selected
task-driven engineering workflow. Its wiring is still present; the replacement
is not yet an available end-to-end workflow.

Opt-in `proposal_extraction` uses `whole-span-exact-quote-selection-v1`: the
model selects complete supplied units, not summaries or shortened quotations.
The controller independently checks complete text, the matching reference, and
duplicates. Conditions and table rows cannot be dropped within a selected unit.
Unselected units remain possible; this is not an all-facts completeness test.
Provider names do not impose a blanket model ban. Explicit
`proposal_conversion: true` remains a model-free alternative that copies every
adapter-selected value. Collection-only configurations are not changed.

The worked configuration uses `ahe-mcp-atlassian-adapter` because its
`read_atlassian_document` tool returns `ahe-mcp-read-document-v1`. CodeGraph's
`query_codegraph_function` returns `ahe-codegraph-candidate-v1` instead and
cannot be used as this document source, even with the correct tool name/hash.
Git-source model extraction and CodeGraph-to-document conversion are separate
capabilities; these examples do not add them.

**Prerequisites.** Build with `make build adapters`. Have an explicitly selected
non-production AHE PostgreSQL database provisioned with `ahe-migrate` and the
adapter's pinned `sooperset/mcp-atlassian@v0.23.0` provider installed in a private
environment.

For official remote MCP with OAuth in Desktop, use the
[direct connection workflow](apps/detective/INSTALL.md#connect-directly-to-atlassian-rovo-mcp-with-oauth).
The following restrictions apply to the legacy local-provider walkthrough.

**Network boundary: local fixtures, not a direct Cloud connection.** This
preview restricts both the adapter and provider to loopback networking. There
is no supported config flag, environment variable or alternate outbound profile
that enables direct Atlassian Cloud access. The exact sandbox profile is
checked independently by the [Atlassian adapter](internal/atlassianmcp/adapter.go),
[MCP read runtime](internal/detective/mcp_read_runtime.go), and
[host configuration](internal/detectivehost/config.go), using
[`IsMacOSLoopbackOnlyCommand`](internal/mcpstdio/sandbox.go). Editing the profile
in the example will be rejected; the host also wraps its adapter command.

An authorized local Jira/Confluence test service can run within this boundary.
Reaching a real Cloud tenant requires **operator-built infrastructure outside
AHE**, not a bundled or qualified AHE gateway. For transparent HTTPS forwarding,
that typically means a hostname/DNS override that resolves the tenant to
loopback inside the provider's environment, plus a separately operated TCP/TLS
relay that forwards to the real tenant while preserving TLS SNI, the request
hostname and certificate verification. Changing the URL to `localhost` alone
does not preserve those properties. A purpose-built local HTTP API facade is
another operator-owned design, with its own authentication, exact-response and
revision-preservation requirements. Neither design is supplied or validated by
this walkthrough. Avoid global DNS changes and do not disable TLS verification.

The relay/facade itself must have authorized outbound access and enforce the
intended tenant/destination and credential boundaries. A loopback connection to
a relay does not make the remote destination loopback-only; the operator owns
that additional trust boundary. Without such infrastructure, use a local fixture
and treat real Cloud extraction as unavailable in this preview. A direct-Cloud
opt-in profile would need a separate network-policy design and qualification;
this release does not introduce one.

1. Copy these three examples to a private directory and replace all absolute
   placeholder paths:
   - [adapter configuration](configs/atlassian-adapter.example.json) →
     `atlassian-adapter.json`;
   - [discovery command](configs/detective.mcp-command.example.json) →
     `mcp-command.json`;
   - [host-v4 model selection configuration](configs/detective.mcp-extraction.example.json)
     → `detective-extraction.json`.
   Create the configured workspace directory and keep private config files mode
   `0600`. Give a new pilot its own host/workspace/registration identifiers.
2. Supply `run-atlassian-provider`, a private executable launcher that loads
   credentials from your external secret store, sets `JIRA_URL` or
   `CONFLUENCE_URL` for the authorized fixture/facade, and `exec`s the absolute
   installed `mcp-atlassian` executable. A transparent TLS relay instead retains
   the original tenant hostname in that URL and uses the provider-scoped
   loopback resolution described above. Preserve the fixed read-only environment
   in the adapter example. Do not put credentials in the examples or shell
   command arguments. Process environment is not inherited. The nested
   `provider_command` is already sandbox-wrapped; the host and discovery command
   configurations specify the **unwrapped** adapter executable because the CLI
   wraps those automatically.
3. Discover the actual adapter contract before starting collection:

   ```sh
   ./bin/ahe-detective --discover-tools /absolute/private/ahe/mcp-command.json
   ```

   This command needs no `DATABASE_DSN` or host configuration. It starts the
   selected adapter, initializes MCP, follows `tools/list` pages, prints JSON,
   and exits without `tools/call`, collection or model invocation. It does
   execute the selected adapter's startup code. Child stderr is suppressed to
   avoid exposing provider credentials. A timeout, duplicate tool name, missing
   schema or repeated pagination cursor is an error.

   Copy `provider_tool_name` and `provider_tool_input_schema_hash` from the
   selected result into `mcp_read_sources[0]`. The example pin is checked against
   the repository adapter schema in tests; discovery is the check for your
   actual binary. The hash is `sha256:` plus SHA-256 of Go `json.Marshal` on the
   decoded `inputSchema` map, exactly as `mcpstdio.ToolInputSchemaHash` computes
   it. Do not hash pretty-printed JSON or use the upstream `jira_get_issue`
   schema hash: the host calls the AHE adapter's tool. Annotations are provider
   hints, not proof of safety or result-contract compatibility. Discovery does
   not overwrite pins; a later schema mismatch still fails closed.
4. In the host configuration, set `arguments.object_id` and `source_id` for an
   authorized Jira issue (the example `DEMO-1` is a placeholder). For Confluence,
   use `product: "confluence"` and its page ID. The example explicitly enables
   model extraction and disables conversion; choose a model already installed
   at the selected loopback endpoint. No model is downloaded automatically.
   By default a unit is a complete adapter-selected value. To process existing
   heading-based sections individually, explicitly set `section_mode` to
   `"heading_sections_v1"` and `max_sections`; the product of `max_sections`
   and `max_proposals` must not exceed 32. A unit may contain several facts.
   Each unit is limited to 64 KiB; oversized units and shortened model output
   fail without truncation or repair. Ensure `num_predict` permits the chosen
   unit size; the example's 1024-token budget is not sufficient for every unit.
   The provider must report `done: true` and `done_reason: "stop"`; a token-limit
   stop or missing completion metadata fails even if the text is valid JSON.
   The adapter selects Jira description or Confluence content, not unselected
   fields, comments, history, linked documents or descendants. Collection
   coverage remains separate from selection and factual completeness.
5. With `DATABASE_DSN` supplied externally for the selected test database:

   ```sh
   ./bin/ahe-detective --config /absolute/private/ahe/detective-extraction.json
   ```

   This is a periodic collector; `max_steps` is not a one-shot exit flag. Stop
   with Ctrl-C after observing a completed extraction. Inspect the structured
   `mcp_read_proposal_extraction_completed` event and its selection counts,
   attempt/status/reason, and proposal IDs. A repeated snapshot may be replayed
   without a model call. Inspect failure events too; valid syntax or an exact
   quotation alone is not a successful complete-unit selection.

The flow persists exact source and grounded candidate proposals for human
review. It does not admit canonical nodes/edges. Use the read-only query MCP's
live tools for supported readback and the trusted proposal-review workflow for
pending cards. Compare grounded statements and excerpts before any explicit
admission. A successful discovery only verifies advertised metadata; a complete
runtime qualification additionally needs the actual provider and selected test
database. Selection preserves selected source text; it does not establish
semantic extraction completeness or the truth of source assertions.

## Upgrade

For an MCP **binary-only** replacement against an already qualified schema 46
deployment, record the old and new commits and protect a backup before the
maintenance window. Stop the affected MCP processes, build and verify the new
source, update the reviewed binary paths in the existing protected launcher
configuration, verify the same runtime identity and schema policy, then reconnect
the client and rediscover `tools/list`. This source integration does not add a
migration or authorize new roles, ACL changes, data conversion, or DB clearing.
Do not print or copy credentials into the repository while updating paths.

For Detective, preserve its existing private workspace and original receipts;
replace only the app/launcher binaries from the reviewed build. Follow
[Desktop upgrade](apps/detective/INSTALL.md#upgrade-and-existing-workspaces).
The historical source repository is not an installed workspace and must not be
copied into the public source tree to transfer saved settings.

### Separate database-adoption procedure

The current adoption is **not an in-place upgrade of an existing database**.
The following operational checklist is conditional on separate environment and
role provisioning; it does not authorize data conversion or ACL changes.

1. Back up the PostgreSQL database.
2. Select and record the reviewed AHE commit.
3. Build and verify the new checkout.
4. Stop the affected AHE processes.
5. Prepare the separately selected target database/private schema, operator
   identity and explicit database-wide privilege prerequisites. Do not pre-create
   the runtime role pairs intended for the fresh provision command.
6. Run the matching `ahe-migrate` with the explicit schema and separate
   migration credentials.
7. Create the new runtime pairs and install the target-native role policy after
   migration, through the separately authorized `ahe-runtime-admin provision`.
8. Separately configure LOGIN authentication and protected credentials, then run
   `ahe-runtime-admin verify` using each bounded LOGIN credential.
9. Start only separately qualified query/intake/source-review launchers. Old DSN-only MCP launchers
   and old MCP review/operator profiles no longer start successfully. Do not
   reactivate the legacy collector as a substitute for the disabled writers.

Do not replace or edit an already applied migration. The runtime verifies
embedded migration names and checksums.

## Common startup failures

- `DATABASE_DSN is required`: the MCP client or shell did not provide the DSN.
- missing/invalid runtime principal, profile, role or schema: configure the
  protected launcher; do not inject replacement identity through tool arguments.
- `legacy writer runtime profiles are not enabled`: this executable currently
  accepts only `intake` or `source-claim-reviewer`; do not weaken the gate to recover old writer exposure.
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
