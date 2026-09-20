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
per-case assertion findings and reviewer limitations. Its tables remain the
single current result reference; historical pilot tables are omitted from the
main documentation to avoid mixing protocols. Original records remain in the
private lab and prior Git revisions.

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
