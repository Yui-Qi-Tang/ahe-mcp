# AHE MCP

**Artifacts give rise to hypotheses; evidence determines which hypotheses may become part of the world.**

**The model may explore freely, but it cannot cross the evidential boundary.**

Source-backed evidence for agents: preserve original material, review candidate
claims, and query evidence with its provenance and lifecycle state.

Current version: `dev` preview. Retained Detective Desktop code: `0.1.0-preview.18`.
MCP and Detective share one root Go module, using Go `1.27.0`.

> **Desktop 凍結／目前不工作（不可用） — 2026-09-14.** Desktop is not a
> supported working product. Its code and previous test results are retained,
> not offered for installation or acceptance. Stabilize Detective CLI first;
> this does not disable the AHE MCP servers.

## Quick start

```sh
git clone https://github.com/Yui-Qi-Tang/ahe-mcp.git
cd ahe-mcp
make build      # MCP and operator binaries
make detective # Detective CLI and source tools
```

Follow [MCP installation](INSTALL.md) for PostgreSQL, migrations, runtime roles
and protected launchers, and [Detective CLI](apps/detective/README.md) for the
retained command-line capabilities and gaps. Building does not configure a DB
or download a model. Desktop launch instructions are frozen reference material,
not part of quick start.

## Programs

| Program | Purpose |
| --- | --- |
| `ahe-query-mcp` | Read-only evidence queries over stdio |
| `ahe-ingest-mcp` | Separate intake or exact source-claim review profiles |
| `ahe-migrate` | Apply and verify PostgreSQL migrations |
| `ahe-runtime-admin` | Provision or verify bounded runtime roles |
| `ahe-mcp-launch` | Start an MCP profile with protected external credentials |
| `detective` | Source collection, extraction, queries and review workflows |
| `AHE Detective.app` | Frozen / unavailable; retained code, not a supported workflow |
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

## Graph Data Model

AHE's `canonical-evidence-graph/v1` models evidence and claims, rather than
code symbols. PostgreSQL remains authoritative; a `CanonicalArtifact` is a
bounded, read-consistent snapshot, not a second database.

### Node kinds

| Kind | Meaning |
| --- | --- |
| `raw_evidence` | Source material backing a claim |
| `source_claim` | A claim grounded in source material |
| `derived_claim` | A claim derived from an explicit parent set |
| `candidate` | Task-candidate vocabulary in the graph schema |

Node kinds are not admission states. Pending proposals are stored separately;
their existence does not create admitted canonical evidence. A schema type
also does not imply that the standard MCP profiles expose its writer.

### Edge relations

| Relation | Meaning and direction |
| --- | --- |
| `supports_claim` | Raw evidence → source claim |
| `derived_from` | Parent → derived target; the complete parent manifest expresses AND dependency |
| `contradicts` | Symmetric contradiction, stored as a normalized endpoint pair |
| `supersedes` | New replacement → old target |
| `references` | Source-backed reference between evidence nodes |
| `implements` | Source-backed implementation relation between evidence nodes |

### Record structure

| Type | Contents |
| --- | --- |
| `CanonicalNode` | ID, kind, and references to payload, provenance, temporal and integrity records |
| `CanonicalEdge` | ID, `from`, `to`, relation and provenance reference |
| `EvidencePayload` | Source, title, optional quotation and locator, claim, applicability and target anchors |
| `CanonicalArtifact` | Schema and snapshot IDs, nodes, edges and their supporting records, including derivations |

Provenance retains origin grouping and extraction/review traceability; multiple
excerpts from one document are not independent sources. Temporal metadata and
integrity digests are separate from the claim. A graph path proves structural
connectivity, not truth, and the whole evidence graph is not assumed to be a DAG.

See the [Go data types](internal/evidencegraph/canonical.go) and
[graph semantics](docs/SYSTEM_DESIGN.md#graph) for the detailed contracts.

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
go test -race ./...        # Full-module race tests; not Desktop acceptance
```

Ordinary tests use deterministic fixtures. Live model/DB tests require a separately
selected opt-in environment; see [database/process tests](INSTALL.md#optional-database-and-process-tests).
Retained Desktop tests may run as regression checks; passing them does not
unfreeze Desktop or establish interactive usability.
Linux full-module verification needs the native dependencies and build tag in
[INSTALL.md](INSTALL.md#shared-module-verification-on-linux). MCP-only builds
do not require Desktop's native libraries.

## Documentation

- [MCP installation and upgrades](INSTALL.md)
- [Detective CLI and Desktop](apps/detective/README.md)
- [Frozen Desktop installation reference and CLI setup](apps/detective/INSTALL.md)
- [System design: theory, algorithms, data structures and references](docs/SYSTEM_DESIGN.md)
- [Optional macOS service setup](deploy/macos/README.md)
- [Changelog](CHANGELOG.md)

## License

[MIT](LICENSE). Third-party dependencies and materials remain subject to their
respective licenses.
