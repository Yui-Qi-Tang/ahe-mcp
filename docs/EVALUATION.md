# Evaluation and reproduction

The [README evaluation](../README.md#evaluation) separates the
[earlier Product replay](PRODUCT_REPLAY_20261006.md), the
[fresh supplement](PRODUCT_REPLAY_20261006_SUPPLEMENT.md), the
[2026-10-07 Gemma SWE comparison](PRODUCT_REPLAY_20261006_SUPPLEMENT.md#gemma-swe-comparison-2026-10-07), the
[three-issue autonomous Sol run](PRODUCT_REPLAY_20261006_SUPPLEMENT.md#autonomous-sol-swe-results),
[Sol with/without-AHE comparison](PRODUCT_REPLAY_20261006_SUPPLEMENT.md#sol-without-ahe-comparison),
and the historical
six-case benchmark. The rest of this page documents the historical benchmark:
two pinned local models, three synthetic task families and three selected public
SWE-bench cases. Do not pool counts across these different protocols.

## Read the numbers

| Measure | Unit | Meaning |
| --- | --- | --- |
| Unsupported assertions | Answer containing at least one confirmed unsupported assertion | Lower is better; explanation and decision are both inspected. |
| Decision errors | Answer containing a wrong required decision field | Separate from explanation quality. |
| SWE repairs | Public case passing the selected official repair tests | Higher is better; a passing patch can have an unsupported explanation. |
| Completed calls | Parseable, schema-valid responses | Delivery success, not correctness. |
| Unresolved judgments | Answers whose assertion assessment remains uncertain | Included in the denominator, never counted as clean. |

The historical six-case run has 110 calls: 84 synthetic A/C/D, 8 AND traversal-control B,
and 18 SWE A/C/D. Each model has 14 synthetic questions and 3 public tasks per
main arm. These are selected development cases with one attempt per cell,
not independent replications or an estimate of population-wide reliability.

C provides structured/static evidence; D additionally provides observed AHE
operation results and queried provenance. For SWE, a controller actually
traversed AHE, and the models consumed its frozen output. This does not measure
models autonomously choosing live tools, and does not isolate AHE from another
system supplying equivalent verified results. Public review/admission uses
**TEST APPROVAL STUBS**, not human approval.

The historical results below retain the fixed settings, model digests, source
commits, per-case assertion findings and reviewer limitations. The README now shows
the supplementary run's fresh scores; the AND breakdown here describes the
original answers. Original records remain in the private lab and prior Git
revisions. The earlier Product replay did not regenerate this benchmark; the
supplement completed 110 fresh Gemma attempts and evaluated the submitted SWE
patches. Its results and failures are reported separately, without rewriting
these historical scores.

## Historical six-case results

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

For those same four AND answers, separate decision-field scoring shows what the
flat aggregate hides. **Higher is better in this table**; these are correct
fields / four original answers, not additional model calls or replacement
unsupported-assertion counts.

| AND model | Group | Path | Declared-parent integrity | Original-requirement support |
| --- | --- | ---: | ---: | ---: |
| E4B | A | 2/4 | 1/4 | 3/4 |
| E4B | C | 1/4 | 3/4 | 4/4 |
| E4B | D | 1/4 | 3/4 | 4/4 |
| 31B | A | 2/4 | 3/4 | 4/4 |
| 31B | C | 4/4 | 4/4 | 4/4 |
| 31B | D | 4/4 | 4/4 | 4/4 |

E4B's error types changed from A to C even though its affected-answer count did
not. D added no observed improvement over C on these fields; 31B was already
correct in C. The frozen D inputs included full fixture records and validation
results, so this finding does not establish missing graph data. Graph validation
checks the declared set; whether it covers the original requirement is a
separate source comparison. See the [per-variant errors, scoring boundaries and
verification scope](#and-support-score-each-question-separately).

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
attempts that produced no executable patch. These are familiar cases, not held
out; the adapter was developed on them. Each model/case/condition had one attempt.
D receives frozen controller output rather than choosing live tools; review and
admission use test approval stubs. Explanation judgments come from one unblinded
reviewer without independent annotation.

| Public SWE tasks: resolved repairs / 3 attempts | A | C | D |
| --- | ---: | ---: | ---: |
| E4B | 0/3 | 0/3 | 1/3 |
| 31B | 2/3 | 1/3 | 3/3 |

**In these three familiar SWE-bench cases, the AHE-assisted condition produced
more passing repairs than the structured/static control.** Each model and its
source pool were held fixed.

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

### Decision-field scores, per-case findings and fixed method

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
artifacts can pass declared-parent validation while omitting an original
requirement; that coverage comparison is outside the graph validator's contract.

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

The [stdio integration fixture](../internal/mcpintegration/swe_relation_packets_integration_test.go),
`make verify` and focused scorer checks passed. Full final answers, raw captures,
failed setup attempts, per-answer judgments and a hash-verifying recount bundle
are retained in the private lab under this repository's artifact policy. This
page is a quantitative summary, **not a standalone reproducible public
benchmark**.


## AND support: score each question separately

The original AND comparison uses four variants of a repository-authored synthetic
webhook requirement and one answer per model/variant/arm. The public
[fixture and methods](EVIDENCE_BOUNDARY.md#and-case-a-path-does-not-cover-every-prerequisite)
define the structural interventions. The field breakdown is an offline reading
of the same frozen six-case inputs, answers and manual assertion review used by
the README. It adds no generations and does not pool subsequent summary or
holdout experiments with the original run. Full raw answers and review records
remain private, so these tables are not a standalone replay bundle.

Classify the **question**, not the entire variant, by what the operation checks:

| Question / answer field | Evidence needed and scoring boundary |
| --- | --- |
| Path: `path_exists` | A path between the specified endpoints in the stated relation scope. One path does not establish all parents. The original structural fixture has the signature path in all four variants. |
| Declared integrity: `declared_and_integrity` | Every declared parent exists with the required direct edge. `complete` and `underdeclared` pass; `missing_edge` and `missing_parent` fail. |
| Requirement support: `full_requirement_support` | Compare the original requirement with the declaration, nodes and edges. Only `complete` satisfies this fixture's full requirement rubric. The graph validator does not perform this source comparison. |
| Missing branches: `missing_required_branches` | Compare against the original requirement, not merely the declared parent list. The expected list is empty for `complete` and contains `anti_replay` for all other variants. |
| Runtime proof: `runtime_correctness_proven` | Execution evidence would be needed. `no` is correct for all four fixtures: runtime correctness is not established, not disproven. |
| Explanation overclaim | Separately review factual claims in the explanation against visible evidence, including false denials of present data. A correct field does not certify its explanation. |

`underdeclared` therefore has both a graph-verifiable answer (its declared set is
complete) and a source-comparison answer (the declaration omits a requirement).
Likewise, accepting `complete` verifies its declared structure without itself
verifying requirement coverage. An operation not assessing coverage does not
require the downstream model to abstain when the supplied original source and
records support that separate comparison. Unsupported inference and unnecessary
abstention must remain separate outcomes.

### Original errors by variant

Each A/C/D cell lists incorrect fields in **one original answer**. `P` = path,
`I` = declared integrity, `R` = full requirement support, `M` = missing branches.
`none` means these fields were correct. The final column reports the separate
explanation-only label in A / C / D order; it is not the primary whole-answer
assertion label.

| Model | Variant | A wrong fields | C wrong fields | D wrong fields | Explanation overclaim A / C / D |
| --- | --- | --- | --- | --- | --- |
| E4B | `complete` | none | none | none | no / no / no |
| E4B | `missing_edge` | I, R, M | P | P | no / no / no |
| E4B | `missing_parent` | P, I | P | P | yes / no / no |
| E4B | `underdeclared` | P, I | P, I | P, I | yes / no / no |
| 31B | `complete` | none | none | none | no / no / no |
| 31B | `missing_edge` | P | none | none | no / no / no |
| 31B | `missing_parent` | P | none | none | no / no / no |
| 31B | `underdeclared` | I | none | none | no / no / no |

All 24 original A/C/D answers correctly declined to infer runtime proof. Missing
branch lists were correct in 3/4 E4B A answers and 4/4 in each other model/arm.
The original whole-answer unsupported-assertion counts remain E4B **3/4, 3/4,
3/4** and 31B **3/4, 0/4, 0/4**. Explanation-only overclaims are E4B **2/4,
0/4, 0/4** and 31B **0/4, 0/4, 0/4**. These measures must not be substituted
for one another. The explanation labels are from one unblinded reviewer and
have no independent annotation.

The flat E4B aggregate conceals improved declared-set and requirement judgments
alongside worse path judgments from A to C. C and D retain the same erroneous
fields in these four cases. The frozen D packets already contained all fixture
nodes, edges, derivations, temporal records, path results and validator outcomes;
they were not merely a path to one parent. An explicit parent-count/missing-list
summary was absent, but absence of that presentation has not been established as
the cause of the remaining errors.

Correct graph validation is an operation contract. Whether exposing its result
improves a model answer beyond C is an experimental outcome, not a guarantee:
C already contains enough structural information to answer, and 31B reaches
the ceiling in C. The absence of a D-over-C gain here establishes neither a
graph implementation defect nor general model reliability.

### Fixture validation versus product queries

The original AND packets exercise real in-memory `PrepareTopology` and path
algorithms using synthetic artifacts. In rejected fixtures, the generic path
check deliberately bypasses artifact validation; it is not a successful public
Query operation over an invalid read view. A later independent fixture audit
also cannot establish production PostgreSQL/MCP behavior or deployment status.

Product contract checks have separate entry points:

| Check | Public test source | Boundary |
| --- | --- | --- |
| Four AND variants and accepted prepared paths | [AND fixture](../internal/evidenceprojection/and_boundary_case_test.go) | Real Go graph preparation; in-memory synthetic artifacts. |
| Persisted derivation reconstruction, omitted parents/edges and scope recovery | [Canonical read tests](../internal/evidenceingestion/canonical_read_view_test.go) | Real read/assembly code with a SQL test double; no live PostgreSQL. |
| Read-view handle, path and diagnostic scope | [Query handler tests](../internal/evidencequerymcp/canonical_read_view_test.go) | Real handlers with a fake core; no subprocess transport or live database. |
| PostgreSQL-backed read view | [Integration fixture](../internal/evidenceingestion/canonical_read_view_integration_test.go) | Opt-in live database check, separate from model scoring. |

A bounded query can exclude a parent or edge that exists in storage. The reader
refuses to assemble an incomplete declared derivation and identifies the bounded
view in its error; widening the authorized scope can recover the valid artifact
without repairing stored data. A successful truncated view also does not prove
global completeness. See [bounded-query interpretation](EVIDENCE_BOUNDARY.md#bounded-product-queries).

## What a public checkout can reproduce

| Material | Public entry point | Scope |
| --- | --- | --- |
| Citation/review binding | `make evidence-boundary` | Ten deterministic domain cases; SQL test double for source collision. |
| Declared AND support | `make evidence-and-case` | Four structural/manifest conditions; no model or database. |
| Stale review | `make evidence-stale-review-case` | Sequential DB lifecycle checks; requires an isolated disposable DB. |
| Independent implements | `make evidence-implements-case` | Restricted relation admission/query checks; requires disposable DB/role authority. |
| Exact source review over MCP | [First workflow](FIRST_WORKFLOW.md) | Manual walkthrough and separate synthetic stdio integration fixture. |
| SWE relation packets | [Integration fixture](../internal/mcpintegration/swe_relation_packets_integration_test.go) | Executes intake, review and Query against externally supplied packets. |

See [fixture methods and DB prerequisites](EVIDENCE_BOUNDARY.md) before running
the opt-in cases. Do not count a skipped test as success. These checks do not
regenerate the README's model scores.

## What is still missing for the six-case benchmark

The public repo does **not** yet contain a standalone replay bundle for the
110-call run. The SWE test reads `swe-inputs.json` from `AHE_SIX_CASE_ROOT`;
that prepared packet and its complete generation/recount pipeline are not
included as a public fixture. The test does not generate model answers or run
the official SWE patch evaluator.

A complete public replay needs the following matched materials:

- The permitted source inputs, dataset revision, upstream license references,
  and deterministic packet/AST-adapter generation at the pinned commits.
- The frozen per-cell prompts, schemas, request order, fixture hashes and
  operation outputs used by the historical six-case run.
- The model-generation controller and exact dependency/runtime versions.
- Scoring rules plus per-answer judgments, including unresolved cases,
  unsupported assertions, citation errors and retained failed attempts.
- Original generated patches and the official evaluator configuration,
  base/gold controls, test results, and a hash-verifying recount command.

Do not substitute historical Python pilot outputs, reconstruct unavailable
answers, or report a newly assembled packet as an exact replay. Public-source
availability alone does not establish that the frozen experiment inputs are
available. Private/company material and credentials are not required to publish
a future public-only replay bundle and must remain private.

If the original prepared inputs are available privately, the AHE packet stage
alone can be rerun after the
[DB-role prerequisites](../INSTALL.md#optional-database-and-process-tests):

```sh
# Set AHE_DBROLE_ACCEPTANCE_DATABASE_DSN externally for a disposable database.
# Use a private working directory containing the original swe-inputs.json.
AHE_SIX_CASE_ROOT=/path/to/private/six-case-work \
  go test -mod=readonly -tags=integration -count=1 -v \
  -run '^TestIntegrationSWERelationPackets$' ./internal/mcpintegration
```

The fixture creates per-case `*-native-graph.json` outputs and refuses to
overwrite existing files. Use a fresh working directory with a copy of the
original input; preserve frozen captures separately before rerunning. This command validates
only the packet stage; it cannot certify reproduction of the headline scores.

## Scope of the result

The selected cases show that supplying AHE evidence and operation results can
improve downstream outcomes. They also retain failures and unsupported or
unresolved explanations. No competitor benchmark, general reasoning gain,
production reliability rate or independent annotation is established.

For a new experiment, use the [experiment protocol](../.claude/skills/ahe-evidence-query/references/experiments.md):
freeze inputs and scoring before generation, retain all attempts, distinguish
abstention from invented explanations, and score repairs separately from
source fidelity. The live-agent comparison linked above is a separate experiment.
