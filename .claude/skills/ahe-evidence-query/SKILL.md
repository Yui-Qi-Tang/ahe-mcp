---
name: ahe-evidence-query
description: Use existing AHE evidence to answer questions, trace supporting relationships, or inspect review and lifecycle state through the read-only Query MCP. Use for evidence lookup and reasoning, not source intake, admission, repository extraction, or news Brief generation.
---

# Answer with AHE Evidence

Produce an answer whose factual claims can be checked against the retrieved
sources, relationships and recorded state. AHE supplies the evidence; the
connected model or agent remains responsible for its explanation.

## Scope and Entry Points

The primary agent performs queries and verification under the
[shared division of work](../../../CLAUDE.md#agent-division-of-work). Subagents
only return text challenging supplied material; they cannot query tools,
research, read/write files or data, execute, or delegate further. The primary
agent verifies any challenge before using it in the answer.

Use the already authorized Query launcher and discover its live schemas.
No intake, admission, source rewriting, profile changes or direct database
writes are part of this skill. If AHE is unavailable, report the limitation;
any separately authorized source-only analysis must be identified as such.
Do not describe a static catalog or an invented receipt as an executed AHE
workflow.

Choose the smallest useful entry point for the question:

| Need | Query tools to consider |
| --- | --- |
| A known record | `get_evidence_record` |
| An information need or search phrase | `get_grounded_evidence_brief` (defaults to `deterministic_lexical_recovery`) |
| An intentionally exact lexical lookup over proposal statements | `search_evidence_records` (exact-only; no `query_mode`) |
| Records for an exact source, revision or lifecycle scope | `list_evidence_records` |
| Supporting links and their recorded basis | `list_evidence_neighbors`, `get_relation_provenance` |
| A path or topology within a bounded snapshot | `open_canonical_read_view`, then `find_canonical_path` or `get_canonical_topology_diagnostics` |
| Source revision or governed lineage state | `get_mcp_read_source_states`, or `get_canonical_supersession_currentness` with an observed `lineage_key` |
| Current-action eligibility and final basis check | `get_evidence_use_state`, then `check_current_evidence_plan` under the same explicit policy |

`get_grounded_evidence_brief` is a deterministic read-only evidence package,
not a model-generated news summary. Inspect its source context, record state,
search surface and recovery trace when exposed. Do not silently change the
user's source/revision scope or treat omitted context as absent source data.

### Empty exact lookup and path terms

A complete empty exact lookup exposes `query_execution.recovery_options` with
separate `get_grounded_evidence_brief` calls. These are suggestions, not executed
attempts or promised matches. Preserve their query, filters and limit. The first
option uses the existing deterministic recovery default. Wider experimental
routes require explicit paired parameters: `experimental_multisurface_lexical_v1`
with response schema `grounded-evidence-brief-v6`, or
`practical_multisurface_lexical_v1` with `grounded-evidence-brief-v7`.
Inspect the live schema for the exact schema identifiers before calling.

PostgreSQL full-text tokenization can keep `/v1/order-items` as a path token,
while `order-items` becomes multiple required query lexemes. Thus a statement
such as `Clients call POST /v1/order-items to create an order.` can miss an
exact resource-name search even though the path contains that text. Recovery
routes have different bounded surfaces; none guarantees a hit. An observed
full path can also be tried within the same scope. An empty result never proves
global absence.

## Follow the Evidence Needed for the Claim

Read the relevant exact records, available original excerpts and qualifications.
When the answer depends on support, dependencies, implementation relationships
or admission state, follow the relevant relationships and read their provenance
or receipts. A list of lexical hits alone does not resolve those questions.
For a simple fact already supported by an exact excerpt, extra graph traversal
is unnecessary.

- Use record and relation identifiers returned by AHE. Respect the stored
  endpoints, directions, relation types, scope and limits from the live schema;
  do not infer edge orientation solely from a relation's name.
- For an AND-dependent claim, inspect the declared parent set and each required
  parent's source context when exposed. A single path does not establish that
  every parent is present. Declared completeness cannot detect a prerequisite
  omitted from the declaration itself. If the available view cannot establish
  the needed set, keep that part unresolved.
- Distinguish source-backed support, `derived_from`, `implements`, `references`,
  `contradicts` and `supersedes`. A navigation edge is not semantic entailment,
  runtime behavior, transitive implementation or proof that tests passed.
- Compare current lifecycle/receipt fields with the question being asked.
  A stored review display can describe a historical pre-admission state; pending,
  rejected, audit-only and admitted records have different authority. Node
  admission is not independent relation approval. Do not infer exact replay
  from an existing admission alone.
- Preserve conflicting evidence and competing interpretations. A contradiction
  component does not imply that every pair has a contradiction edge. Governed
  lineage currentness does not prove external source freshness; never invent
  lineage keys, source revisions or missing authority fields.

## Interpret Current-Use Reasons

When current use is requested, select `current-action/v1` or
`grounded-current-action/v1` according to the requested purpose. Read
`get_evidence_use_state` before planning; after planning/SAT, recheck every
action's complete declared basis with `check_current_evidence_plan` under the
same policy. Admission history or an old pass is not current-use clearance.

For a blocked/excluded result or a currentness limitation, read the
[reason code reference](../../../docs/CURRENT_USE_GUIDANCE.md). Follow each
`related_id` to the actual parent, source or conflict edge. Outcomes are not
terminality labels: reviewed restore clears only withdrawal, while superseded
predecessors do not revive. Do not assume every reason has a public repair.

`unclassified_currentness` is not an instruction to set `Temporal.Status`.
Fresh-v2 requires a genuine same-object pending replacement and exact human
review; it does not retrofit an admitted replacement. An unchanged singleton
external source claim may qualify for separate exact-reviewed initial lineage
registration; this read-only skill does not approve it. An empty registration
catalog retains `projection`; a nonempty catalog returns `joint_projection`.
Inspect its complete witness and the full use policy. See the
[native contract](../../../docs/INITIAL_LINEAGE_REGISTRATION.md). `incomplete_currentness`
can require justified lineage assignments for other claims in the same source
object, without collapsing their semantic slots. If the prerequisites or
separately authorized reviewer are absent, report the gap and stop dependent
current use. This read-only skill does not authorize the writer flow. Continue
supported historical analysis without presenting it as current clearance.

## Handle Missing or Bounded Evidence

Inspect returned truncation, omitted context, query filters and completion
metadata. Expand or refine within the authorized scope when it can resolve the
question. Do not invent cursor parameters or assume a larger limit proves
coverage. Cached graph views may expire or disappear on restart; reopen a view
and identify any changed snapshot when that matters.

No search match or `found_in_view=false` supports only the reported search/view
scope. If necessary source context is unavailable through Query, report the gap
or use an already authorized source connector with provenance clearly separated.
Do not acquire a writer profile just to complete a read-only answer.

Answer the supported portion when possible. Abstain from unsupported conclusions
without inventing their cause or claiming that visible evidence is missing.
Label useful hypotheses as hypotheses; do not silently write them back as
canonical evidence.

## Return an Inspectable Answer

Lead with the supported finding. Include source/record references and, when
material, the relationships, receipts, revisions and bounded scope used. State
unresolved points or missing capabilities where they affect the conclusion.
Scale detail to the question instead of always returning a full graph dump.

Check the explanation as well as the selected decision: a correct field or a
passing patch can accompany an unsupported factual claim. Citation syntax,
source grounding, completeness and repair success are distinct checks.

For a request to design, run or score an experiment, also read
[the experiment protocol](references/experiments.md). Ordinary evidence lookup
does not start an experiment or require loading that protocol.

## Reviewed lifecycle readback

For a declined endpoint, read `get_evidence_record` by exact proposal occurrence.
Its `endpoint_disposition` preserves the native review subject, request ID,
reviewer, reason and `rejected`/`audit_only` outcome without canonical evidence.
A terminal historical display is not a pending review. For reviewed fresh-v2
Supersession, read `supersession_admission` by its canonical ID and use Query currentness
with its observed lineage key. Superseded source claims remain admitted;
currentness is snapshot-bound and does not prove provider freshness or truth.
The reviewed-v2 writer's historical replay retains its proposal lock and complete
exact/native-authority checks, but not the pending fresh governance fence. Query
access does not authorize invoking that writer; see the
[shared replay boundary](../../../CLAUDE.md#reviewed-supersession-and-endpoint-disposition).

## Historical derivation boundary

Follow the [historical contract](../../../docs/HISTORICAL_DERIVATION.md). A derived
judgment may be new rather than quoted; trace all declared parents and induction
context without calling premise excerpts the conclusion's original text.
`historical_only` and its required descendants cannot obtain current clearance.
Query history uses `kind=historical_derivation` and the original occurrence;
readback never authorizes a write or promotion. The separate historical profile
requires its own exact human decision, not a current approval or failed current gate.
