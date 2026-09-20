---
name: ahe-evidence-query
description: Use existing AHE evidence to answer questions, trace supporting relationships, or inspect review and lifecycle state through the read-only Query MCP. Use for evidence lookup and reasoning, not source intake, admission, repository extraction, or news Brief generation.
---

# Answer with AHE Evidence

Produce an answer whose factual claims can be checked against the retrieved
sources, relationships and recorded state. AHE supplies the evidence; the
connected model or agent remains responsible for its explanation.

## Scope and Entry Points

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
| An information need or search phrase | `get_grounded_evidence_brief` or `search_evidence_records` |
| Records for an exact source, revision or lifecycle scope | `list_evidence_records` |
| Supporting links and their recorded basis | `list_evidence_neighbors`, `get_relation_provenance` |
| A path or topology within a bounded snapshot | `open_canonical_read_view`, then `find_canonical_path` or `get_canonical_topology_diagnostics` |
| Source revision or governed lineage state | `get_mcp_read_source_states`, or `get_canonical_supersession_currentness` with an observed `lineage_key` |

`get_grounded_evidence_brief` is a deterministic read-only evidence package,
not Detective's news Brief generation. Inspect its source context, record state,
search surface and recovery trace when exposed. Do not silently change the
user's source/revision scope or treat omitted context as absent source data.

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
