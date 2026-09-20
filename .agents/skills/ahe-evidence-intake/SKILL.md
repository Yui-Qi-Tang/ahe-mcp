---
name: ahe-evidence-intake
description: Use when a Codex agent or sub-agent collects exact external provider evidence, produces scope-preserving grounded proposals, or prepares and applies exact human source review through AHE MCP. Do not use for news briefs, read-only lookup, or repository code extraction.
---

# AHE Evidence Intake for Codex Agents

Follow the same evidence workflow used by Claude Code while preserving the
actual producer identity and the parent/sub-agent approval boundary.

## Load the Shared Workflow

Before taking intake actions, read
[the shared intake workflow](../../../.claude/skills/ahe-evidence-intake/SKILL.md)
completely. It owns the common tool sequence and detailed rules for:

- exact connector collection and immutable source intake;
- span-grounded proposal production;
- human review cards and admission or disposition;
- separately authorized relation/endpoint workflows and unavailable writers;
- completion reporting.

If that file is unavailable or conflicts with the live MCP schema, stop and
report the mismatch. The live MCP schema controls call shape; the shared skill
controls workflow and safety boundaries. For existing-evidence lookup or support
tracing, use [the query skill](../ahe-evidence-query/SKILL.md) instead.

Inherit the shared restriction on summary-first engineering intake. Exact
quotes, retained original bytes, or no observed fabrication do not establish
that the proposed evidence preserves the requested information. Keep known
omissions visible; do not use Brief/manual_text as an external-intake fallback.

## Codex-Specific Overrides

- Identify Codex source collection with `collector_id: codex-agent`. Preserve
  the actual connector or tool identity separately in `connector_id`.
- Identify Codex-produced extraction with:
  - name: `codex-grounded-extractor`
  - version: `ahe-external-intake-v2`
- Never copy `claude-code`, `stdio-agent`, or another fixture identity into a
  Codex operation. Audit fields describe the agent that actually performed the
  collection or extraction.
- Increment the extractor version only when proposal selection or grounding
  semantics change. Formatting-only edits do not change the version.
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

- the exact proposal occurrence IDs;
- for standard source review, the exact extraction attempt ID and unchanged
  native review subject/display that the human reviewed;
- the outcome for each ID: approve, reject, or `audit_only`;
- the reviewer identity required by the configured workflow;
- a human-supplied decision reason.

All three standard source-review decisions require a human-supplied reason.
The authorized launcher supplies reviewer identity; do not inject it into the
writer request. Preserve older v1 records rather than relabeling them.

General instructions such as "finish the intake", "continue", or "do the next
step" are not a valid decision. If any field is absent or ambiguous, perform no
admission or terminal disposition.

## Use the Shared Sequence

Execute the shared intake sequence with the Codex identities above. It owns
exact source persistence, proposal enumeration, native review, the writer
binding and readback. Do not maintain a second tool sequence in this wrapper.
A complete, valid human decision already supplied for the unchanged native
subject need not be requested again. A changed subject requires fresh review;
missing or ambiguous decision fields still stop admission or disposition.

Test approval stubs belong only to an explicitly authorized isolated experiment
under the [experiment protocol](../../../.claude/skills/ahe-evidence-query/references/experiments.md).
They are not human decisions for operational intake.

## Parent Handoff

Return a compact, directly inspectable package containing:

- source identity, revision, coverage, and limitations;
- source snapshot, extraction view, attempt, and proposal IDs;
- numbered review cards with exact excerpts and source locations;
- version differences and uncertainty;
- extraction coverage notes and known omissions, distinct from source coverage;
- readback results and missing capabilities;
- the exact human decision still required;
- any requested relation or repository workflow that was unavailable;
- whether any canonical mutation occurred.

Never place credentials, tokens, DSNs, private conversation text, or raw
company data beyond what the authorized review requires in the handoff.
