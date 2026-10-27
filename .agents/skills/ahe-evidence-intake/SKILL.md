---
name: ahe-evidence-intake
description: Use when the primary Codex agent collects exact external provider evidence, produces scope-preserving grounded proposals, or prepares and applies exact human source review through AHE MCP. Do not use for news briefs, read-only lookup, or repository code extraction.
---

# AHE Evidence Intake for Codex Agents

Follow the same evidence workflow used by Claude Code while preserving the
actual producer identity and the human approval boundary. Follow the shared
[agent division of work](../../../CLAUDE.md#agent-division-of-work): the primary
agent owns operations; subagents only challenge supplied material in text and
may not use tools, research, execute, read/write files or data, or delegate further.

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

Use the shared [current-use guidance](../../../docs/CURRENT_USE_GUIDANCE.md)
when a gate blocks the workflow. A listed review flow is conditional on its
prerequisites and exact human decision, not a promise or permission to repair.

Inherit the shared restriction on summary-first engineering intake. Exact
quotes, retained original bytes, or no observed fabrication do not establish
that the proposed evidence preserves the requested information. Keep known
omissions visible; do not use Brief/manual_text as an external-intake fallback.

Apply the shared [user-controlled granularity and current limits](../../../.claude/skills/ahe-evidence-intake/SKILL.md#user-controlled-granularity-and-current-limits).
The user chooses source scope and claim granularity; native spans remain
line-based. Do not silently narrow scope, rewrite originals, or expand
segmentation/capacity controls to make a task fit. Existing scope authorization
need not be requested again; a materially narrower scope needs the user's choice.

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
- For an adversarial challenge, supply the proposition, fixed assumptions,
  scope, evidence excerpts and counterexample criteria. Collect any missing
  evidence yourself and verify the returned challenge before adopting it.

## Human Decision Boundary

The primary agent prepares and reads back the complete review package, then
presents it to the human. Without an explicit decision for the exact unchanged
subject, stop before admission or disposition. Approval of evidence does not
permit a subagent to execute the workflow or change its text-only role.

A valid human decision contains all of:

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

## Review Handoff

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

## Historical derivation boundary

Follow the [historical contract](../../../docs/HISTORICAL_DERIVATION.md). A derived
judgment may be new rather than quoted; trace all declared parents and induction
context without calling premise excerpts the conclusion's original text.
`historical_only` and its required descendants cannot obtain current clearance.
Query history uses `kind=historical_derivation` and the original occurrence;
readback never authorizes a write or promotion. The separate historical profile
requires its own exact human decision, not a current approval or failed current gate.
