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

### Current local experiment baseline

**Use `gemma4:31b-it-qat` with the repaired v2 protocol for follow-up local
experiments.** The 12B runs below are historical pilots and comparison controls.
The selected configuration uses explicit field definitions and output schema,
Ollama `generate` with streaming capture, `think:true`, temperature 0, seed 42,
context 16384 and output limit 1024. Errors and partial output are retained;
answers are neither retried nor rewritten.

The [repaired AND replay](docs/EVIDENCE_BOUNDARY.md#repaired-replay-and-larger-local-model)
records the exact model digest, reproduction command and scoring rules. New
cases must freeze their inputs and scoring before generation; the observed
16/16 result below applies only to the existing development packets.

### Do verified receipts reduce wrong answers?

**In two fixed admission-state cases, yes; in the AND-support case, E4B still
misinterpreted the evidence.** A new 92-call replay on 2026-09-19 tested both
`gemma4:e4b-it-qat` and `gemma4:31b-it-qat` across experiments 1–3.
The goal is fewer incorrect answers when using AHE-MCP evidence.

The table counts **incorrect answer objects / attempted**; any wrong decision
field makes the answer incorrect. Lower is better. A supplies facts and rules;
C repeats the facts as JSON (and includes a traversal witness in experiment 1);
D adds AHE's verified validation result or operation receipt.

**Quantitative summary across all 14 fixed variants:** E4B's incorrect answers
fell from **8 to 4** with verified AHE results: **57.1% → 28.6% error rate**,
a **28.6-percentage-point decrease** and **50.0% relative error reduction**.
Against the structured-facts control, errors fell from **6 to 4**:
**42.9% → 28.6%**, or **33.3% relative error reduction**. These are observed
differences on this fixed suite, including the AND case that did not improve.

| Model | A errors / 14 | C errors / 14 | D errors / 14 | A → D decrease | C → D decrease |
| --- | --- | --- | --- | --- | --- |
| E4B IT-QAT | 8/14 (57.1%) | 6/14 (42.9%) | 4/14 (28.6%) | 4 answers; 28.6 pp | 2 answers; 14.3 pp |
| 31B IT-QAT | 1/14 (7.1%) | 1/14 (7.1%) | 0/14 (0%) | 1 answer; 7.1 pp | 1 answer; 7.1 pp |

Each variant has equal weight in this descriptive summary: four AND variants,
five stale-review variants and five implements variants. The table covers
**84 primary A/C/D calls**; the eight secondary B calls bring the run to 92.
Paired by the same variant, D corrected four of E4B's A errors and two of its C
errors; it corrected one error from each 31B control. No previously correct
answer became incorrect under D, using the all-decision-fields criterion.
31B's zero observed errors under D means **0/14 in this run**, not zero future
error risk. The explanation errors described below are scored separately.

`Error rate = incorrect answer objects / attempted answers` (all attempts
completed here). `Absolute decrease = control error rate − D error rate`;
`relative reduction = (control errors − D errors) / control errors`, with
equal denominators. **pp** denotes percentage points. Relative reduction is
undefined when the control has zero errors; it is not a claim of statistical
significance or an estimate for unseen tasks. Values are rounded to one decimal.

The per-experiment breakdown retains the cases with no improvement:

| Model | Experiment | A: facts | C: structured facts | D: verified result |
| --- | --- | --- | --- | --- |
| E4B IT-QAT | 1. AND support | 3/4 (75%) | 3/4 (75%) | 3/4 (75%) |
| E4B IT-QAT | 2. Stale review | 3/5 (60%) | 2/5 (40%) | 1/5 (20%) |
| E4B IT-QAT | 3. Independent `implements` approval | 2/5 (40%) | 1/5 (20%) | 0/5 (0%) |
| 31B IT-QAT | 1. AND support | 0/4 (0%) | 0/4 (0%) | 0/4 (0%) |
| 31B IT-QAT | 2. Stale review | 1/5 (20%) | 1/5 (20%) | 0/5 (0%) |
| 31B IT-QAT | 3. Independent `implements` approval | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) |

All 92 requests completed without HTTP, truncation or schema failures; none
answered `unknown`, and none was retried. The secondary traversal-only B arm
in experiment 1 had E4B 3/4 errors and 31B 0/4. Scores were recomputed from raw
responses after verifying the frozen inputs and source hashes.

These results support **receipt-assisted error reduction in these state
interpretation tasks**. They also preserve the limits: E4B still confuses path
existence with AND completeness, and 31B's correct stale-review receipt answer
invented a change in the decision reason. Correct fields do not guarantee a
faithful explanation. The fourteen variants are reused authored cases, not a
held-out benchmark or a general error-rate estimate. This measures the value of
supplying verified outcomes, without isolating AHE from another service that
supplies the same outcomes.

See the [frozen method, failure accounting and reproduction](docs/EVIDENCE_BOUNDARY.md#three-case-error-reduction-replay--2026-09-19)
and [suite runner](scripts/evidence_error_rate_suite.py).

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

### Experiment 1: a path is not complete AND support

**Webhook readiness requires signature verification AND anti-replay evidence.**
A path from signature verification to readiness exists in all four fixtures:

```sh
make evidence-and-case
```

| Controlled artifact | Generic path found | AHE `PrepareTopology` |
| --- | --- | --- |
| Both declared parents, nodes and edges present | Yes | Accepted |
| Anti-replay parent declared, but its edge missing | Yes | Rejected |
| Anti-replay parent declared, but its node missing | Yes | Rejected |
| Anti-replay omitted from the manifest, nodes and edges | Yes | Accepted |

**AHE enforces the declared parent set; it cannot discover a requirement omitted
from that set.** All four expected outcomes passed on 2026-09-18. Temporarily
bypassing artifact validation made both rejection tests fail. This demonstrates
an executable contract beyond reachability, with an explicit semantic limit.
The generic graph baseline is not a benchmark of third-party analyzers, and
these synthetic artifacts prove neither actual admission nor runtime security.

The accompanying `gemma4:12b` pilot attempted 16 fixed requests: 15 completed,
one returned HTTP 500, and none was retried. C (repeated structured facts) and
D (C plus AHE's result) each answered all five scored fields correctly in only
**1/4 conditions**, on different conditions. D still misread the complete and
underdeclared controls despite receiving `accepted` results. This run establishes
no general answer-quality advantage; deterministic enforcement and model
interpretation remain separate. See the [method and full score summary](docs/EVIDENCE_BOUNDARY.md#and-case-a-path-does-not-cover-every-prerequisite)
and [runnable pilot](scripts/evidence_and_case.py).

A [separate diagnosis](docs/EVIDENCE_BOUNDARY.md#follow-up-diagnosis-generation-failure-and-decision-field-errors)
reproduced the HTTP 500 as Ollama's repeated-token abort. It also found
path-versus-artifact confusion, conflated completeness checks and contradictions
between JSON fields and explanations. Explicit field instructions improved some
decisions while other errors remained; the original pilot scores are retained.

The [repaired replay](docs/EVIDENCE_BOUNDARY.md#repaired-replay-results--2026-09-18)
then tested all sixteen packets with explicit field definitions and complete
error capture:

| Configuration | All five fields correct / attempted | Execution failures |
| --- | --- | --- |
| Original 12B pilot | 2/16 | 1 |
| Repaired 12B pilot | 10/16 | 0 |
| Repaired `gemma4:31b-it-qat` pilot | 16/16 | 0 |

Both repaired models received identical packets and settings. These are reused
development cases. The 31B model also passed without AHE's validation result,
so this demonstrates improved behavior of the tested configuration, not a
general accuracy result or an AHE-specific advantage. The conditional Luna
follow-up was not triggered.

### Experiment 2: a review can become stale before submission

**An unchanged citation does not let an old review override a later decision.**
This experiment reads a pending native review, commits a controlled intervening
decision, then submits the original request to AHE's actual PostgreSQL-backed
reviewed writer.

| State before the final request | Actual result | New authority records |
| --- | --- | --- |
| Still pending | New admission | Yes |
| Rejected by another reviewer | `admission_state_conflict` | No |
| Marked audit-only by another reviewer | `admission_state_conflict` | No |
| Admitted by another reviewer | `admission_replay_conflict` | No |
| Identical request already succeeded | Exact replay, same identifiers | No |

**5/5 conditions passed** on 2026-09-18 using a fresh disposable PostgreSQL 18.6
cluster, including a run with Go's race detector. Conflicts and replay left
the tracked authority/lifecycle row counts and full-row hashes unchanged.
Direct source readback confirmed unchanged bytes, hashes and
exact quote spans. The existing canonical claim remains present in both
already-admitted conditions.

Run `make evidence-stale-review-case` with an explicitly selected disposable
non-production `DATABASE_DSN`. See the [fixture, model protocol and limits](docs/EVIDENCE_BOUNDARY.md#stale-review-case-the-state-changes-after-review).
All decisions are synthetic test stubs. This verifies the specified sequential
read/change/submit schedule; it does not cover every concurrent interleaving,
authenticate human approval or benchmark another analyzer.

The fixed `gemma4:31b-it-qat` run completed all 15 requests without execution,
truncation or schema failures:

| Input | All five decision fields correct |
| --- | --- |
| A: old review plus complete latest-state records | 4/5 |
| C: A plus the same facts repeated as JSON | 4/5 |
| D: C plus actual writer receipt | 5/5 |

In A and C, the model mistook another reviewer's existing admission for an
exact replay of its own old request. D interpreted the conflict correctly, but
its explanation also invented a change in decision reason: only the reviewer
had changed. Correct fields therefore do not guarantee a faithful explanation.
The writer rejected the conflicting request and preserved the existing decision.
D receives the result directly, so this is not evidence of a general reasoning
advantage. The triggered Luna task deviated from the frozen no-tools protocol;
it is excluded from a strict model comparison. See the [full results and limits](docs/EVIDENCE_BOUNDARY.md#stale-review-results--2026-09-18).

### Experiment 3: admitted endpoints do not admit an implements relation

An admitted specification and an admitted code fact do not by themselves
establish **“this code implements this specification.”** That directed relation
needs its own exact review, explicit approval and independent admission receipt.

**5/5 PostgreSQL boundary conditions passed** on 2026-09-19 through the actual
restricted relation backend and public query API:

| Client action with both endpoints admitted | Observed result |
| --- | --- |
| No relation operation | No `implements` edge or relation receipt |
| Obtain relation review | Read-only; still no relation |
| Submit without explicit approval | Rejected; tracked authority rows unchanged |
| Approve with a mismatched review subject | Rejected; tracked authority rows unchanged |
| Approve the exact relation review | One directed edge and one independent receipt; no new nodes |

The query also confirmed that neither the reverse edge nor ancestor-to-code
relations appeared. An exact retry preserved the original receipt without new
rows. This is a reproducible authority boundary beyond merely finding two nodes
or a call path; it is not proof that the implementation behaves correctly.

Run `make evidence-implements-case` with an externally configured
`AHE_DBROLE_ACCEPTANCE_DATABASE_DSN` for a separately owned disposable database.
See the [fixture, role-policy prerequisites and model protocol](docs/EVIDENCE_BOUNDARY.md#independent-implements-case-admitted-endpoints-do-not-admit-a-relation).
All approval decisions in this experiment are synthetic stubs.

The repaired `gemma4:31b-it-qat` run completed **15/15**, with all four fields
correct in **A 5/5, C 5/5 and D 5/5**. Review of all fifteen explanations found
no material factual error in this scope; no execution failure occurred, and the
Luna fallback was not triggered. **The arms tie:** this supports the enforced
relation-admission boundary, not a model-accuracy gain or superiority over other
parsers. See the [full results and limits](docs/EVIDENCE_BOUNDARY.md#implements-results--2026-09-19).

A separate repeat with identical model digest, inputs and settings also scored
**15/15** (A/C/D each 5/5); all fifteen generated answers, including explanations,
matched the first run exactly. These are two runs of the same five variants,
not thirty independent tasks or a second database trial.

A matched-input run with **`gemma4:e4b-it-qat`** completed all fifteen requests,
with **12/15** answer objects correct on all four fields:

| Model | A: facts and rules | C: same facts as JSON | D: actual receipt |
| --- | --- | --- | --- |
| `gemma4:31b-it-qat` | 5/5 in each of two runs | 5/5 in each of two runs | 5/5 in each of two runs |
| `gemma4:e4b-it-qat` | 3/5 | 4/5 | 5/5 |

E4B once treated a submission without approval as admitted; twice it reported
successful admission while also saying the relation did not exist. These were
completed incorrect answers, with no HTTP, truncation or schema failure.

**The practical question is whether AHE-MCP helps a model use evidence and
answer with fewer errors.** In these five fixed variants, E4B produced an
incorrect answer in **2/5 cases (40%)** with facts and rules alone, **1/5 (20%)**
when the same facts were also supplied as JSON, and **0/5 (0%)** when supplied
with AHE's verified operation receipts. An answer counts as incorrect if any
of its four decision fields is wrong.

This is initial evidence that the receipt-assisted workflow can reduce errors
when interpreting admission state. The receipt gives the model a verified
outcome to use; making that outcome available is part of the system's intended
value. Five authored variants are not enough to establish a reliable error
rate across other tasks, and this run does not isolate AHE from another system
that supplies the same verified outcomes.

The original Luna diagnostic received a truncated large tool message and did
not answer the test; that attempt remains unscored. A subsequent delivery
diagnosis reproduced the missing middle. Sending one complete record at a time
then returned all fifteen verification tags and fifteen correct answer objects,
without tool calls. This repair is a separate workflow diagnostic, not a fresh
model comparison. See the [cause and repair](docs/EVIDENCE_BOUNDARY.md#luna-delivery-diagnosis-and-repair--2026-09-19).

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
