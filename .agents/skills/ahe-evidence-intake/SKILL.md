---
name: ahe-evidence-intake
description: Use when a Codex agent or sub-agent collects exact Jira, Confluence, or other connector-observed evidence, submits it through AHE MCP, prepares human review cards, or proposes governed contradiction or supersession relations. Do not use for read-only evidence lookup or repository code extraction.
---

# AHE Evidence Intake for Codex Agents

Follow the same evidence workflow used by Claude Code while preserving the
actual producer identity and the parent/sub-agent approval boundary.

## Load the Shared Workflow

Before taking intake actions, read
`../../../.claude/skills/ahe-evidence-intake/SKILL.md` completely. Treat it as
the canonical detailed workflow for:

- exact connector collection and immutable source intake;
- span-grounded proposal production;
- human review cards and admission or disposition;
- governed canonical contradiction and supersession proposals;
- completion reporting.

If that file is unavailable or conflicts with the live MCP schema, stop and
report the mismatch. The live MCP schema controls call shape; the shared skill
controls workflow and safety boundaries.

## Codex-Specific Overrides

- Identify Codex source collection with `collector_id: codex-agent`. Preserve
  the actual connector or tool identity separately in `connector_id`.
- Identify Codex-produced extraction with:
  - name: `codex-grounded-extractor`
  - version: `ahe-external-intake-v1`
- Identify Codex contradiction and supersession proposals with:
  - producer name: `codex-canonical-relation-proposer`
  - producer version: `ahe-canonical-relation-v1`
- Never copy `claude-code`, `stdio-agent`, or another fixture identity into a
  Codex operation. Audit fields describe the agent that actually performed the
  collection, extraction, or relation proposal.
- Increment the extractor version only when proposal selection or grounding
  semantics change. Increment the relation producer version when contradiction
  or supersession proposal and review semantics change. Formatting-only edits
  do not change either version.
- Supply a stable, opaque, non-secret session reference only when the host
  exposes one. Omit it rather than inventing or reconstructing one.
- When delegating, give the sub-agent an explicit provider-object scope and the
  minimum source access required. Do not broaden connector access merely to
  make collection easier.

## Sub-Agent Stop Rule

Unless the assignment carries an explicit human decision for exact proposal
IDs, the sub-agent must stop after producing and reading back the review
package. It returns the package to the parent agent; the parent displays it and
waits for the human.

A valid delegated decision contains all of:

- the exact proposal occurrence or relation proposal IDs;
- the outcome for each ID: approve, reject, or `audit_only`;
- the reviewer identity required by the configured workflow;
- a human-supplied decision reason whenever the live schema requires it.

Canonical relation decisions and terminal source-proposal dispositions require
a decision reason in the current schema. If a tool makes the reason optional
and the human omits it, do not invent one.

General instructions such as "finish the intake", "continue", or "do the next
step" are not a valid decision. If any field is absent or ambiguous, perform no
admission or terminal disposition.

## Claude-Compatible Tool Sequence

Use this order, omitting only steps that the live server proves unnecessary:

1. Discover the connector and AHE MCP tools and inspect their schemas.
2. Read one authorized provider object and preserve exact observed content.
3. Call `submit_external_source` with provider identity, revision, coverage,
   limitations, and a fresh delivery request ID.
4. Call `get_extractor_input` for the returned extraction view.
5. Produce zero or more span-grounded proposals without adding unsupported
   facts or reconciling conflicts.
6. Call `submit_extractor_output` with a fresh request ID and the Codex producer
   definition above.
7. Read back every pending proposal for the exact source snapshot.
8. Build numbered human review cards from persisted records.
9. Stop for a human decision unless a valid delegated decision already exists.
10. Apply only the exact approved admissions or explicit dispositions.
11. Read back final records and report the resulting IDs and states.

For canonical contradictions or replacements, use the corresponding dedicated
proposal, read, review, admission, and disposition tools from the shared skill.
Do not write generic edges. `contradicts` is symmetric; `supersedes` is directed
from the current node to the replaced node. Do not infer supersession from
provider revision order alone.

## Parent Handoff

Return a compact, directly inspectable package containing:

- source identity, revision, coverage, and limitations;
- source snapshot, extraction view, attempt, and proposal IDs;
- numbered review cards with exact excerpts and source locations;
- version differences and uncertainty;
- readback results and missing capabilities;
- the exact human decision still required;
- whether any canonical mutation occurred.

Never place credentials, tokens, DSNs, private conversation text, or raw
company data beyond what the authorized review requires in the handoff.
