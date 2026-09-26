# Evaluation and reproduction

The [README evaluation](../README.md#evaluation) is the current quantitative
summary: two pinned local models, three synthetic task families and three
selected public SWE-bench cases. Earlier pilots used different inputs or
protocols. Do not pool their counts with the current six-case run.

## Read the numbers

| Measure | Unit | Meaning |
| --- | --- | --- |
| Unsupported assertions | Answer containing at least one confirmed unsupported assertion | Lower is better; explanation and decision are both inspected. |
| Decision errors | Answer containing a wrong required decision field | Separate from explanation quality. |
| SWE repairs | Public case passing the selected official repair tests | Higher is better; a passing patch can have an unsupported explanation. |
| Completed calls | Parseable, schema-valid responses | Delivery success, not correctness. |
| Unresolved judgments | Answers whose assertion assessment remains uncertain | Included in the denominator, never counted as clean. |

The current run has 110 calls: 84 synthetic A/C/D, 8 AND traversal-control B,
and 18 SWE A/C/D. Each model has 14 synthetic questions and 3 public tasks per
main arm. These are selected development cases with one attempt per cell,
not independent replications or an estimate of population-wide reliability.

C provides structured/static evidence; D additionally provides observed AHE
operation results and queried provenance. For SWE, a controller actually
traversed AHE, and the models consumed its frozen output. This does not measure
models autonomously choosing live tools, and does not isolate AHE from another
system supplying equivalent verified results. Public review/admission uses
**TEST APPROVAL STUBS**, not human approval.

The README includes the fixed settings, model digests, source commits,
per-case assertion findings and reviewer limitations. Its headline counts are
preserved; the AND breakdown below describes the same original answers.
Historical pilot tables are omitted from the main documentation to avoid mixing
protocols. Original records remain in the private lab and prior Git revisions.

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
  operation outputs used by the current run.
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
source fidelity. A future live-agent comparison is a different experiment.
