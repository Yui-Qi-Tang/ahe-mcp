# AHE MCP

**AHE provides traceable evidence and verified operation results for AI agents.**

[Quick start](#quick-start) · [Evaluation](#evaluation) · [MCP setup](INSTALL.md)

## Results on selected SWE-bench cases

**These SWE-bench cases provide initial evidence that AHE can improve downstream
problem-solving outcomes.**

| Model | Structured/static evidence | With AHE results |
| --- | ---: | ---: |
| Gemma 4 E4B IT-QAT | 0/3 repairs passed | 1/3 repairs passed |
| Gemma 4 31B IT-QAT | 1/3 repairs passed | 3/3 repairs passed |

Three selected, familiar cases; the same models and source pool; one attempt per
model, case and input condition. Passing repairs do not establish that every
explanation is supported. See the [full evaluation](#evaluation) for all input
groups, unsupported-assertion findings and limitations.

## What AHE helps with

- **Trace claims to their sources.** Preserve original material, versions and
  exact excerpts so an agent can inspect the evidence behind a claim.
- **Follow evidence relationships.** Query how claims depend on source material
  and other claims, with their supporting context.
- **Check review and operation state.** Distinguish pending claims from reviewed
  evidence, and inspect recorded outcomes before relying on an earlier review.

## Quick start

Current version: `dev` preview. Retained Detective Desktop code: `0.1.0-preview.18`.
MCP and Detective share one root Go module, using Go `1.27.0`.

> **Desktop frozen / unavailable — 2026-09-14.** Desktop is not a
> supported working product. Its code and previous test results are retained,
> not offered for installation or acceptance. Stabilize Detective CLI first;
> this does not disable the AHE MCP servers.

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

## Evaluation

### Evidence-bounded answers and abstention

The downstream objective is to reduce **unsupported factual claims**. When the
available evidence cannot support an answer, an explicit, well-founded abstention
is a useful outcome. When evidence supports a bounded answer, unnecessary
abstention is a cost. Review the explanation as well as the decision: a model can
decline to make a change while still inventing its cause.

AHE provides provenance, lifecycle state and verified operation receipts; their
presence does not guarantee that every model explanation is faithful.

### Six-cases

**In this fixed suite, supplying AHE results reduced unsupported assertions in
the synthetic tasks and produced more passing repairs in the public tasks.
Public-case explanations still contained unsupported or unresolved claims.**
The question is whether AHE helps models answer within the available evidence.

#### How to read the comparison

Both local models, `gemma4:e4b-it-qat` (E4B) and `gemma4:31b-it-qat` (31B),
answered the same tasks under these three input conditions:

| Group | What the model receives |
| --- | --- |
| A — source/facts | Original source material, facts and task rules |
| C — structured/static | The same material plus structured facts or static syntax associations |
| D — AHE results | The same material plus actual AHE operation results and queried provenance |

**C versus D is the main comparison for the added value beyond organizing
source material.** D includes observed verification results. For SWE, a
controller actually traversed the AHE relations; models read its frozen output
instead of selecting live tool calls themselves. All SWE groups receive the
same full source pool.

The review/admission workflow for the public cases uses test approval stubs
rather than human approval.

Experiments 1–3 contain **14 synthetic questions per model and group**:
four AND-support variants, five stale-review states and five independent
`implements` states. Experiments 4–6 contain **three public SWE tasks per model
and group**: Django 10914, Astropy 12907 and sklearn 25570, each requiring a
diagnosis and patch.

**110/110 calls completed** means that every request finished with a parseable,
schema-valid answer. It does not mean every answer was correct. The total is
84 synthetic A/C/D calls + 8 AND traversal-control B calls + 18 SWE calls.
Each model/question/group combination had one attempt, with no retries,
rewritten answers or fallback model.

#### Primary outcome: answers containing unsupported assertions

**Lower is better.** Count an answer once if it contains at least one factual
assertion that is unsupported or contradicted by the supplied evidence. This
includes explanations, invented old code and false claims that visible evidence
is missing. The count is of **answers, not individual sentences**. Unknown or
abstention alone does not count as an invention.

| Synthetic tasks: affected answers / 14 attempts | A | C | D |
| --- | ---: | ---: | ---: |
| E4B | 7/14 | 6/14 | 3/14 |
| 31B | 4/14 | 1/14 | 0/14 |

For example, E4B's **C 6/14 → D 3/14** means three fewer answers contained
unsupported assertions across the same fourteen questions. Improvement was
uneven: E4B still had **3/4 affected AND answers in A, C and D**. 31B's D result
of **0/14** applies only to these questions and this run.

Public-case judgments include uncertainty, so the three categories are shown
separately. **Each row contains exactly three answers**:

| Public SWE tasks | Group | Confirmed unsupported | Unresolved | No unsupported assertion identified | Total |
| --- | --- | ---: | ---: | ---: | ---: |
| E4B | A | 2 | 0 | 1 | 3 |
| E4B | C | 2 | 0 | 1 | 3 |
| E4B | D | 1 | 1 | 1 | 3 |
| 31B | A | 1 | 2 | 0 | 3 |
| 31B | C | 3 | 0 | 0 | 3 |
| 31B | D | 1 | 1 | 1 | 3 |

For example, E4B D has **one confirmed finding, one unresolved answer and one
answer with no unsupported assertion identified**. Unresolved answers are
already included in the total and are not counted as clean. The last category
does not establish that a patch is correct or every quotation is valid.

One unblinded Codex reviewer assessed these claims. The four unresolved answers
concern a cross-platform Django guarantee and sklearn explanations whose full
fitted-state/output construction is absent from the packets. The reviewer
corrected an initial sklearn branch interpretation that overlooked
fitted-passthrough substitution; the correction and prior judgments are retained.
These counts have no independent annotation.

#### Secondary outcome: repairs passing the official tests

**Higher is better.** The denominator includes all three public tasks, including
attempts that produced no executable patch.

| Public SWE tasks: resolved repairs / 3 attempts | A | C | D |
| --- | ---: | ---: | ---: |
| E4B | 0/3 | 0/3 | 1/3 |
| 31B | 2/3 | 1/3 | 3/3 |

**These SWE-bench cases provide initial evidence that AHE can improve downstream
problem-solving outcomes.** With each model and its source pool held fixed,
the AHE-assisted condition produced more passing repairs than the
structured/static control.

**31B D's 3/3 means all three repairs passed the selected official tests. It
does not mean all three explanations were supported.** Its Astropy patch
passed, but its explanation added an unsupported type/dimension diagnosis;
its sklearn explanation remains unresolved.

Invalid, empty or no-op patches remained unsuccessful attempts and were not
manually repaired. E4B C unnecessarily abstained on Django. Fresh base/gold
controls were valid for all three cases.

The observed gains support using AHE evidence and operation results to help
models stay within evidence boundaries on some tasks. The unchanged AND errors
and the public explanation findings remain part of the result. These are
familiar development cases with one attempt per condition, so they do not
establish improved general reasoning or a general hallucination-reduction rate.
No other code-graph product or service supplying equivalent verification
results was measured.

<details>
<summary>Decision-field scores, per-case findings and fixed method</summary>

**Decision-field errors are a separate secondary measure.** Any wrong decision
field makes the answer incorrect under this contract; explanations are assessed
separately by the primary assertion measure above.

| Synthetic tasks: incorrect decision answers / 14 attempts — lower is better | A | C | D |
| --- | ---: | ---: | ---: |
| E4B | 7/14 | 7/14 | 3/14 |
| 31B | 3/14 | 1/14 | 0/14 |

This explains two apparent mismatches between tables. E4B C has **7/14**
decision errors but **6/14** unsupported-assertion answers: one response selected
`no_write` instead of `review_only` while correctly describing the read-only
effect. Conversely, 31B A has **3/14** decision errors but **4/14**
unsupported-assertion answers: one response selected `replay_conflict` correctly
but described `exact_replay` in its explanation.

Per-case primary findings follow. Fractions count confirmed affected answers
over attempts. **Unresolved** marks the single answer for that public case;
it is neither a confirmed finding nor an answer with no finding.

| Model | Case | A: source/facts | C: structured/static | D: AHE result |
| --- | --- | ---: | ---: | ---: |
| E4B | 1. Declared AND support | 3/4 | 3/4 | 3/4 |
| E4B | 2. Stale review | 2/5 | 2/5 | 0/5 |
| E4B | 3. Independent `implements` | 2/5 | 1/5 | 0/5 |
| E4B | 4. Django 10914 | 0/1 | 0/1 | 0/1 |
| E4B | 5. Astropy 12907 | 1/1 | 1/1 | 1/1 |
| E4B | 6. sklearn 25570 | 1/1 | 1/1 | Unresolved |
| 31B | 1. Declared AND support | 3/4 | 0/4 | 0/4 |
| 31B | 2. Stale review | 1/5 | 1/5 | 0/5 |
| 31B | 3. Independent `implements` | 0/5 | 0/5 | 0/5 |
| 31B | 4. Django 10914 | Unresolved | 1/1 | 0/1 |
| 31B | 5. Astropy 12907 | 1/1 | 1/1 | 1/1 |
| 31B | 6. sklearn 25570 | Unresolved | 1/1 | Unresolved |

Submitted anchored patches were E4B **1/0/1** and 31B **2/2/3** for A/C/D:
nine submissions, of which seven passed. An invented old-code anchor or an exact
no-op edit was retained as a failed patch contract and was never submitted.

For the public cases, a Python AST adapter supplies **syntax-navigation
candidates**, with both caller and declaration excerpts as source parents.
Actual standard stdio MCP intake/review/admission and Query operations persisted
and traversed **26 derived candidates, 52 `derived_from` edges and 89 neighbor
queries**, then read back **40 source records** with identical original bytes.
Reviews are explicit **APPROVAL STUBS** in disposable databases, not real human
approval or proof of a diagnosis. This exercises derivation provenance for
external Python syntax candidates, not native Python semantic ingestion or
Python `implements` admission.

All SWE arms receive the **same full source pool**. C also receives the same
external syntax associations; D adds actual persisted graph readback. The model
receives the frozen output of that executed controller, rather than choosing
live tool calls. Extra source discovery is not credited only to D. Django's
single added syntax association is about `close`; its permission assignment was
already visible in every arm, so the better D answer does not identify that
relation as the causal reason for improvement.

The adapter uses the original issue-only lexical chunks and uniform expansion
bounds: depth three, at most sixteen added declarations and 32000 added
characters. It was developed on these cases before generation, not held out.
Source commits are Django `e7fd69d051eaa67cb17f172a39b57253e9cb831a`, Astropy
`d16bfe05a744909de4b27f5875fe0d4ed41ce607`, and sklearn
`cd25abee0ad0ac95225d4a9be8948eff69f49690`.

Synthetic operations were also rerun: in-memory `PrepareTopology`/path checks,
sequential PostgreSQL stale-review lifecycle checks, and restricted independent
`implements` admission/query checks. D supplies observed operation outcomes;
this is receipt-assisted interpretation, not independent prediction or a
comparison against another service providing equivalent checks. Underdeclared
AND support remains a native semantic limitation.

Generation used temperature 0, seed 42, `num_ctx=32768`, `num_predict=4096`,
streaming and `think:true`. E4B ran first, then 31B, with seed-619 shuffled cell
order inside each model block. Requests, source/fixture/scoring hashes and
oracles were frozen before generation. No HTTP, truncation or schema failures
occurred; all synthetic answers used decided fields rather than `unknown`.
Exact quotation/ID errors remain separate: SWE citation-error answers were
E4B **2/3, 3/3, 2/3** and 31B **2/3, 1/3, 1/3** in A/C/D. Four synthetic B
attempts per model produced E4B 3/4 errors and 31B 0/4.

| Local tag | Frozen digest |
| --- | --- |
| `gemma4:e4b-it-qat` | `ee665637121887cf3befff38abbb1be4ee117c7db867d97a67e29049ecd7e15f` |
| `gemma4:31b-it-qat` | `e0812a55773bfeac846b2d605b4d93638b8dfa7119d9587f3d91475afc78185e` |

The new [stdio integration fixture](internal/mcpintegration/swe_relation_packets_integration_test.go),
`make verify` and focused scorer checks passed. Full final answers, raw captures,
failed setup attempts, per-answer judgments and a hash-verifying recount bundle
are retained in the private lab under this repository's artifact policy. The
README is a quantitative summary, **not a standalone reproducible public
benchmark**.

</details>

## A citation is not an approval

**Artifacts give rise to hypotheses; evidence determines which hypotheses may become part of the world.**

**Models may explore freely; claims enter canonical evidence only through explicit review.**

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
