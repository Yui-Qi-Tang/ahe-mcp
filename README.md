# AHE MCP

**Artifacts give rise to hypotheses; evidence determines which hypotheses may become part of the world.**

**The model may explore freely, but it cannot cross the evidential boundary.**

Source-backed evidence for agents: preserve original material, review candidate
claims, and query evidence with its provenance and lifecycle state.

Current version: `dev` preview. Retained Detective Desktop code: `0.1.0-preview.18`.
MCP and Detective share one root Go module, using Go `1.27.0`.

> **Desktop frozen / unavailable — 2026-09-14.** Desktop is not a
> supported working product. Its code and previous test results are retained,
> not offered for installation or acceptance. Stabilize Detective CLI first;
> this does not disable the AHE MCP servers.

## A citation is not an approval

**AHE binds a review to the exact claim, source context and lifecycle state.**
Finding relevant code or a quotation is the start of that process.

Try the reproducible, synthetic evidence-boundary experiment:

```sh
make evidence-boundary
```

Requires Go 1.27 and the module dependencies; no database, model, API key or
Desktop setup. It calls AHE's existing domain functions, with a SQL test double
for the source-revision collision case.

The source says **“Refunds for overseas orders must be completed within 7 days.”**
Both that statement and **“Refunds for all orders must be completed within
30 days.”** can cite the very same real span. AHE keeps both as **pending
candidates**: a valid citation does not establish semantic truth or admission.
Once a review package is built, changing its bound context invalidates that
review, even when the quoted source bytes stay the same.

| Controlled case | Observed domain result |
| --- | --- |
| Supported statement + exact citation | Pending; no canonical reference |
| Unsupported statement + same exact citation | Also pending; semantic limitation exposed |
| Unknown citation span | Rejected |
| Unchanged exact review subject | Validation passes; this is not admission |
| Changed claim, revision, coverage, or proposal state | Old review rejected in all four cases |
| Changed review display bytes | Rejected |
| Different source bytes under the same provider revision | Rejected; mock stored counts unchanged |

**10/10 fixed contract cases passed** on 2026-09-18 with Go 1.27.1
(`darwin/arm64`). This is a bounded contract check, not a hallucination-reduction
rate, a PostgreSQL/MCP integration result, or an agent accuracy benchmark.
The semantic counterexample is part of the result, not an excluded failure.
See the [test](internal/evidenceingestion/evidence_boundary_test.go) and
[method, limitations and next-stage comparison protocol](docs/EVIDENCE_BOUNDARY.md).

### How this complements code graphs

[codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) and
[CodeGraphContext](https://github.com/CodeGraphContext/CodeGraphContext) document
code indexing, relationship queries and impact analysis. AHE adds a different
contract: preserve sources, keep extraction pending, bind an explicit review,
and retain provenance for admitted evidence. A code graph can supply structural
context to that workflow.

This experiment measures AHE's contract only. It does not measure those projects,
claim they lack comparable safeguards, or establish that AHE improves an agent's
answers. That requires a controlled agent study with the same source material.

### One case: same call graph, different review validity

A synthetic refund change from **7 to 30 days** preserves the parsed call graph
`HandleRefund → Eligible`. The old source-claim review passes when unchanged,
but AHE returns `review_contract_conflict` when it is reused for the new proposal.
This is source-claim validation, not approval of the code change.

A local `gemma4:12b` pilot on 2026-09-18 used one unchanged control and one changed
condition, with four fixed input packets (eight calls, no generation retries):

| Input packet | Old review matches the changed proposal? Model field; correct answer is **no** |
| --- | --- |
| A: source and review records | `yes` — incorrect; its explanation nevertheless recognized the mismatch |
| B: A + parsed call graph | `yes` — incorrect |
| C: B + the same provenance facts also organized as JSON | `no` — correct |
| D: C + actual AHE domain validation result | `no` — correct |

**C and D tied: this case does not establish an AHE-specific answer-quality gain.**
All four arms correctly matched the unchanged review and recognized that neither
pending proposal was canonically admitted. All four also incorrectly listed
affected functions in the no-code-change control. These failures are retained.
The call graph uses Go's standard parser, not a measured third-party product;
this is a fixed-packet pilot, not an interactive agent/MCP benchmark.
See [case method and reproduction](docs/EVIDENCE_BOUNDARY.md#single-case-local-model-pilot)
and the [runnable pilot](scripts/evidence_boundary_case.py).

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
| `ahe-ingest-mcp` | Separate intake, source, endpoint and relation-review profiles |
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

## Evidence Endpoint Review

Separate `repository-intake` and `endpoint-reviewer` profiles create
immutable Git/Go code endpoints and explicitly reviewed derived specifications.
Capture/extraction stays pending; endpoint admission binds exact source context,
complete AND parents and the human reason. Query retains the endpoint receipt.
See [setup and bounds](INSTALL.md#repository-and-derived-endpoint-writers).
These tools do not enable Desktop or approve independent relations.

## Independent Relation Review

A separately authorized `relation-reviewer` exposes `get_implements_review`,
`admit_reviewed_implements`, `get_references_review` and
`admit_reviewed_references`. Read the complete native review, obtain explicit
approval of that exact relation, then submit the unchanged subject and reason.
Node approval alone does not approve an edge. Query readback includes the
independent admission receipt.

This bounded profile connects an admitted derived specification (complete AND
ancestry) to admitted repository-backed Go code, or resolves exact source
reference markers. It does not create derived/code endpoints, admit arbitrary
edges, or interpret URLs as references. See [supported scope and setup](INSTALL.md#independent-relation-reviewer).

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
