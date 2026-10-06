# Controlled AHE Experiments

Use this protocol for an explicitly requested experiment or evaluation report.
It does not start model calls during ordinary repository work or evidence lookup.

## Identify the Execution Environment

This repository and every worktree belong to AHE MCP, including worktrees stored
under `lab/worktrees/`. Their `lab/` directories hold private test artifacts.
Record checkout, revision, uncommitted changes and exact test selection. Each
reported pass must come from the stated checkout and execution; archives alone
do not establish that a feature was implemented or tested.

## Test Approval Stubs

An authorized experiment may use synthetic approval decisions for synthetic
fixtures or redistributable public cases in an explicitly selected, isolated,
disposable non-production database. Record the fixture, source scope and
database/schema ownership before writing. Company/private source data requires
its own explicit authorization and remains outside public fixtures.

Label simulated decisions and exported results **TEST APPROVAL STUB** or
**APPROVAL STUB**. The stub simulates the approval input; native intake, review,
admission and Query operations must still be executed if the experiment claims
to exercise them. A manually assembled result is a mock, not a native receipt.

The experiment assignment authorizes the declared test setup; it does not
constitute human approval of those claims or justify stubs in an operational
evidence store. Never reuse stub decisions or synthetic authority as real human
approval. Stop before writes if isolation or database ownership is uncertain.
Clean up only the experiment's own processes and disposable resources.

## Freeze the Comparison Before Generation

- State the question being measured: evidence use, unsupported assertions,
  state decisions, repair success, workflow reliability or cost. Evaluate AHE
  as an evidence layer under a fixed downstream model/agent setup.
- Fix case selection, source revisions, input packets, model tag/digest,
  generation limits, prompts, arm definitions, attempt order and retry policy.
  Freeze the scoring rules and keep evaluator-only answers/tests out of model
  inputs. Record setup corrections before generation.
- To measure the added value of AHE beyond source organization, include a
  structured/static evidence control. Keep source availability equal when
  testing presentation or receipts; if retrieval itself is the intervention,
  declare that difference and report source coverage separately.
- Record actual AHE calls, relation identifiers, source readback and receipts.
  Distinguish source-only persistence, relation traversal and admission checks.
  State whether a model chose live tool calls or read frozen controller output.
  Supplying verified outcomes measures assisted interpretation, not independent
  prediction of those outcomes or the isolated effect of one graph edge.
- Preserve every attempt, timeout, schema failure and abstention. Do not
  silently retry, repair model outputs, select the best response or weaken the
  rubric after observing results. Authorized changes start a named revision;
  keep the prior run and reasons for the change.

## Score and Report Separately

Count answers containing unsupported/source-contradicted assertions separately
from incorrect decision fields, citation-contract errors and tested repair
success. Specify denominators and whether multiple assertions count once per
answer. Include explanations and false denials of visible evidence in the
grounding review. A proposed but ineffective repair is not automatically an
invented fact about the source.

Distinguish warranted abstention, unnecessary abstention and an abstention that
still invents an explanation. Keep unresolved reviewer judgments in their own
category within the original denominator; do not treat them as clean answers.
Disclose manual or model-based reviewers, blinding and judgment uncertainty.
Preserve rubric corrections and earlier labels without rewriting model output.

Report model/version, cases, source scope, controls, actual operations and the
observed gains or failures. Repeated conditions are not independent tasks.
Passing tests does not validate every explanation; no observed error does not
establish general reliability. Claims about improved downstream outcomes must
remain bounded to the evaluated setup.

Keep raw captures and historical reviews in Product’s private `lab/` directory. Publish synthetic
fixtures, redistributable public samples, general methodology or quantitative
summaries only within the user's requested repository/publication scope, with
source/license references and limitations. State when the repo lacks inputs,
runners or judgments needed to reproduce a reported result. Never include
credentials, private machine paths or company data in public artifacts.

## Historical Query Replay on the Current Runtime

A frozen source/archive and frozen query expectations do not require running
the current binary against an obsolete schema or role policy. Restore the
archive only into a separately owned disposable PostgreSQL cluster. Before
read-only replay, explicitly run `ahe-migrate`, then `ahe-runtime-admin upgrade`
for the original group/LOGIN/profile and `verify` using the original bounded
LOGIN. Keep Query read-only; it must not migrate or refresh policy itself.

For an upgrade witness, keep role OIDs, attributes and memberships unchanged.
Compare every original table projected onto its original columns; check the
original migration ledger entries remain unchanged while new entries append.
Retain the original source hashes, query plans, expected answers and deadlines.
The replay checks current schema readiness and the complete Query table
inventory, followed by source readback and before/after hashes of all current
tables. A table count alone does not establish the correct schema.

Report actual completed queries against the original denominator. An old
archive rejected before explicit upgrade is not a failed evidence query, and a
successful upgrade alone is not a successful replay. Keep the old failed run
and identify the new run as an operator-upgraded clone.
