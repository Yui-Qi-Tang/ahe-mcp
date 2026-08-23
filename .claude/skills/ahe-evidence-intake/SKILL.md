---
name: ahe-evidence-intake
description: Collect exact Jira, Confluence, or other external provider objects through an available connector, submit them to AHE, create span-grounded proposals, present human review cards, and apply explicit admission or disposition decisions. Use for external evidence import or ingestion. Do not use for read-only AHE evidence queries or repository code extraction.
---

# AHE External Evidence Intake

Use this workflow to turn connector-observed provider objects into reviewable
AHE proposals without moving connector logic or admission judgment into AHE
Core.

## Preconditions

- Use the source scope from the current user request. When invoked explicitly,
  `$ARGUMENTS` may contain the source references and scope.
- Confirm that the required source connector and the trusted AHE ingestion MCP
  are available. Use the read-only AHE query MCP for proposal enumeration,
  review preparation, and final readback when available.
- Inspect the current MCP tool schemas before the first call. Treat the schemas
  as authoritative; this skill defines workflow and safety constraints, not a
  frozen JSON contract.
- If the source connector or required AHE tool is unavailable, stop and report
  the missing capability. Do not substitute a model-generated source.

## Collect One Provider Object

Process each Jira issue, Confluence page, or other provider object as its own
source intake operation.

1. Read the object through the available connector.
2. Preserve the exact connector-observed text or JSON as `content`. Do not use a
   summary, paraphrase, inferred reconstruction, or rendered text copied from a
   model response.
3. Collect the provider-controlled source system, namespace, object type,
   object ID, revision, title, location, timestamps, content format, and
   observation time. Record the actual connector identity.
4. Use the provider revision returned by the connector. If no reliable revision
   is available, stop and explain the missing authority instead of inventing
   one.
5. Describe coverage honestly:
   - `full_document` has no limitations.
   - `exact_excerpt` and `truncated_document` state what was omitted in
     `limitations`.
   - Never infer global absence from partial collection.

Do not include credentials, access tokens, connector secrets, or private chat
content in the envelope or metadata.

## Fix the Source in AHE

1. Create one non-secret request ID for the intended delivery. Retain it for an
   exact retry of the same payload. Never reuse it with changed payload data.
2. Call `submit_external_source` with schema version
   `external-source-envelope-v1` and the exact collected source.
3. Retain the returned source snapshot ID, extraction view ID, hashes, receipt
   timestamps, revision, and replay state.
4. If AHE reports that the same provider revision already exists with different
   content or provenance, stop. Re-read the connector data and revision; do not
   work around the conflict by changing only the request ID.

A repeated observation of unchanged content may use a new request ID and create
a new receipt over the same immutable snapshot. Changed content must carry a
new provider revision and a new request ID.

## Produce Grounded Proposals

1. Call `get_extractor_input` with the returned extraction view ID.
2. Treat the returned source bytes, hashes, and span catalog as authoritative.
3. Create zero or more candidate proposals:
   - Each proposal states one clear claim.
   - Every factual part is supported by cited span IDs.
   - The proposed sentence may normalize wording, but must not add unsupported
     facts, semantic change claims, deletion claims, or global coverage claims.
   - Conflicting source material remains conflicting; do not silently reconcile
     it.
4. Call `submit_extractor_output` using a new request ID and the returned source
   snapshot and extraction view IDs.
5. Identify the producer as:
   - name: `claude-code-grounded-extractor`
   - version: `ahe-external-intake-v1`
   Treat the version as the proposal-production contract version. Increment it
   when proposal selection or grounding semantics change, not for formatting-only
   edits to this skill.
6. Add bounded, non-secret extractor config only when it improves debugging.
   Supply `producer_session_ref` only if Claude Code exposes a stable opaque
   task or session reference. Omit it rather than inventing one.
7. When no grounded proposal is warranted, submit an empty proposal list and
   report the successful `abstained` result.

When the query MCP is available, read back the pending proposal records before
building review cards. If `proposal_count` is greater than one, list pending
records for the exact source snapshot; do not assume the single occurrence ID
in the submit response identifies every proposal. If query readback is
unavailable, use the exact submitted proposals plus the persisted extractor
input and disclose the missing readback in the review.

## Human Review Stop

Before any admission or terminal disposition, display a numbered review card
for every pending proposal. Each card must include:

- proposal occurrence ID;
- proposed sentence;
- exact source excerpts and their span IDs;
- source title and location;
- provider revision;
- coverage and limitations;
- comparison with a prior revision when one is available, otherwise an explicit
  statement that no comparable prior revision was available;
- any uncertainty that affects interpretation.

Ask which proposal numbers the human approves, rejects, or retains as
`audit_only`, then stop and wait for an explicit decision. Do not interpret
silence, a request to continue analysis, or approval of the overall task as
proposal admission approval.

## Apply the Human Decision

- Call `admit_pending_proposal` only for explicitly approved proposal occurrence
  IDs.
- Call `record_pending_proposal_disposition` with `rejected` or `audit_only`
  only when the human explicitly chooses that outcome.
- Use a configured, non-secret reviewer identity. If a required reviewer
  identity is unavailable, ask rather than inventing one.
- Do not create a derived admission unless the user explicitly requests it and
  the complete admitted parent set is known.
- Use the query MCP to read back admitted or dispositioned records when
  available.

## Relate Conflicting Canonical Nodes

Use this step only after both nodes exist through governed canonical admission.
Do not encode a contradiction as a source-backed statement proposal and do not
write a generic graph edge.

1. Confirm both exact `canon-node:` IDs and read both canonical records.
2. Call `submit_canonical_contradiction_proposal` with the two node IDs, a
   source-bounded explanation of the incompatibility, a new request ID, the
   proposing agent name and workflow version, and an optional stable non-secret
   session reference.
3. Call `get_canonical_contradiction_proposal`. Display a separate review card
   containing:
   - both canonical record payloads and IDs;
   - exact source excerpts, titles, locations, revisions, coverage, and
     limitations for both sides;
   - the proposing agent's rationale, identity, version, and optional session
     reference;
   - the current proposal outcome and any prior decision.
4. Stop and wait. Call `admit_pending_canonical_contradiction` only after the
   human explicitly approves this relation. Use
   `record_pending_canonical_contradiction_disposition` for an explicit
   `rejected` or `audit_only` decision.
5. Read the admitted edge with `get_relation_provenance` when available.

`contradicts` is symmetric. AHE canonicalizes A/B order and retains one governed
proposal per node pair, so do not submit the reverse pair as another relation.
A node pair is single-use across every terminal outcome in v1: after
`rejected`, `audit_only`, or `admitted`, the same two canonical node IDs cannot
be proposed again. Changing the rationale, producer name/version, or session
reference also conflicts instead of creating a new version. Do not create fake
replacement nodes to bypass this limit; report that same-pair reconsideration
is unsupported. A genuinely revised source normally produces new canonical
node IDs and therefore a different pair.
AHE records the reviewer fields but does not prove that the agent showed the
card or that the conversation occurred.

## Completion Report

Report results per provider object and proposal:

- source snapshot ID and extraction view ID;
- provider object identity and revision;
- extraction attempt and proposal occurrence IDs;
- admitted canonical reference and admission decision ID, when admitted;
- rejected, `audit_only`, abstained, replayed, conflicted, or unprocessed state;
- coverage, limitations, and any missing readback capability.
- contradiction proposal and edge IDs plus the reviewer outcome when a
  cross-node contradiction was considered.

Do not claim that AHE cryptographically or independently verified the human
review conversation. The cooperating agent is responsible for showing the
review cards and waiting for the decision.
