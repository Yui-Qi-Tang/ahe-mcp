# AHE MCP

Source-backed evidence for agents: preserve original material, review candidate
claims, and query evidence with its provenance and lifecycle state.

Current version: `dev` preview. Included Detective Desktop: `0.1.0-preview.18`.
MCP and Detective share one root Go module, using Go `1.27.0`.

## Quick start

```sh
git clone https://github.com/Yui-Qi-Tang/ahe-mcp.git
cd ahe-mcp
make build      # MCP and operator binaries
make detective # Detective CLI and source tools
```

For the macOS 13+ Apple silicon Desktop, with Xcode Command Line Tools and
Node.js 22.22.2 or newer:

```sh
make desktop
```

The app, `Start.command`, and synthetic source tool are built in
`apps/detective/build/bin/`. Open `Start.command` for a persistent preview
workspace, or use `make desktop-trial` for a new isolated workspace each time.

Follow [MCP installation](INSTALL.md) for PostgreSQL, migrations, runtime roles
and protected launchers. Follow [Desktop installation](apps/detective/INSTALL.md)
for configuration and upgrades. Building does not configure a DB or download a
model. Desktop is a source-build preview, not a notarized installer.

## Programs

| Program | Purpose |
| --- | --- |
| `ahe-query-mcp` | Read-only evidence queries over stdio |
| `ahe-ingest-mcp` | Separate intake or exact source-claim review profiles |
| `ahe-migrate` | Apply and verify PostgreSQL migrations |
| `ahe-runtime-admin` | Provision or verify bounded runtime roles |
| `ahe-mcp-launch` | Start an MCP profile with protected external credentials |
| `detective` | Source collection, extraction, queries and review workflows |
| `AHE Detective.app` | Local source, candidate and human-review UI |
| `detective-source-demo` | Synthetic, read-only source MCP for local testing |
| `detective-news-source` | Explicitly selected public-source adapter |
| `ahe-detective` | Legacy collection host; not the new CLI or Desktop |

`make adapters` builds the optional Atlassian and CodeGraph preview adapters.
`make build-all` builds all CLI programs and adapters, but not the native app.
See [Detective](apps/detective/README.md) for the application workflow and
[legacy host setup](INSTALL.md#legacy-bundled-detective-installation) for older
collection configurations.

## Authority Model

- Detective owns source acquisition and connector orchestration.
- PostgreSQL is authoritative; source snapshots, pending proposals and admitted
  canonical evidence are distinct states.
- Extraction produces candidates, not approval. Only an explicit review may
  admit a claim; `reject` and `audit_only` record noncanonical decisions.
- Query, intake and review run as separate MCP processes with separate
  credentials. Ordinary downstream agents receive only Query access.
- Sharing a Go module does not give Desktop direct DB access or writer authority.

This is a single-user controlled preview. Query visibility is schema-wide, not
a per-row tenant policy; use a dedicated database and trusted operator.
Unattended production writing is not qualified.

## External Model and Connector Intake

The intake profile accepts exact connector-observed content, not a summary
substituted for the original source:

1. `submit_external_source`: preserve content, provider identity/revision,
   coverage and limitations.
2. `get_extractor_input`: obtain the persisted source view and exact spans.
3. `submit_extractor_output`: submit grounded candidates with an explicit
   extractor name/version, or an empty list to abstain.

The separate `source-claim-reviewer` profile exposes `get_source_claim_review`,
`admit_reviewed_source_claim` and `record_reviewed_source_claim_disposition`.
Show the complete claim, quotations and source metadata before requesting the
human's decision and reason. Never approve a truncated or changed review subject.
Retry an uncertain write only with the saved exact subject, decision and reason;
do not edit receipt IDs or reinterpret a changed request as a retry.

Intake cannot admit evidence. The review profile does not expose general
relation writers or repository activation. See the
[profile and launcher contract](INSTALL.md#bounded-detective-to-pending-mcp-installation).

## MCP Client

Register the approved Query launcher as a stdio server. Replace the example
path with your installation's executable launcher; keep credentials outside
the repository and client tool arguments.

```json
{
  "mcpServers": {
    "ahe-query": {
      "command": "/path/to/ahe-query-launcher",
      "args": []
    }
  }
}
```

Use `tools/list` to discover the running server's schemas. The Query surface is:

| Tool | Purpose |
| --- | --- |
| `get_evidence_record` | Read one exact evidence record |
| `list_evidence_records` | List records with explicit source/lifecycle filters |
| `search_evidence_records` | Bounded lexical lookup |
| `get_grounded_evidence_brief` | Evidence package with lexical recovery and query provenance |
| `list_evidence_neighbors` | One-hop evidence or repository-symbol neighborhood |
| `get_relation_provenance` | Read an edge and its provenance |
| `get_mcp_read_source_states` | Immutable source-state metadata |
| `open_canonical_read_view` | Open a bounded canonical graph snapshot |
| `find_canonical_path` | Find a structural path within an opened view |
| `get_canonical_topology_diagnostics` | Cycle witnesses and contradiction components |
| `get_canonical_contradiction_proposal` | Read an exact contradiction review card |
| `get_canonical_supersession_head` | Read the Supersession writer revision coordinate |
| `get_canonical_supersession_currentness` | Derive snapshot-bound lineage currentness |

Query never invokes a model or writes evidence. Results are evidence packages,
not answers: rank is not truth confidence, and no match is not global absence.
Graph views are bounded by their requested scope and budgets; reopen them after
server restart or handle eviction. A contradiction component does not imply
that every pair contradicts. Lineage currentness does not prove source freshness.

## Tests

From the repository root:

```sh
make verify               # Frontend tests, Go tests, build and vet
make desktop-test         # Above plus full-module race tests
make desktop-startup-test # macOS native build and non-UI startup checks
```

Ordinary tests use deterministic fixtures. Live model/DB tests require a separately
selected opt-in environment; see [database/process tests](INSTALL.md#optional-database-and-process-tests).
Startup checks are not interactive UI acceptance.
Linux full-module verification needs the native dependencies and build tag in
[INSTALL.md](INSTALL.md#shared-module-verification-on-linux). MCP-only builds
do not require Desktop's native libraries.

## Documentation

- [MCP installation and upgrades](INSTALL.md)
- [Detective CLI and Desktop](apps/detective/README.md)
- [Desktop installation and workspaces](apps/detective/INSTALL.md)
- [System design: theory, algorithms, data structures and references](docs/SYSTEM_DESIGN.md)
- [Optional macOS service setup](deploy/macos/README.md)
- [Changelog](CHANGELOG.md)
- [Maintainer lab-to-product release gates](LAB_TO_PRODUCT_RELEASE_GATES.md)
