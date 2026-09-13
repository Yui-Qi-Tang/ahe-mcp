---
name: ahe-evidence-intake
description: Use when a Codex agent or sub-agent collects exact external provider evidence, produces scope-preserving grounded proposals, or prepares and applies exact human source review through AHE MCP. Do not use for news briefs, read-only lookup, or repository code extraction.
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
- unavailable relation/repository workflow boundaries;
- completion reporting.

If that file is unavailable or conflicts with the live MCP schema, stop and
report the mismatch. The live MCP schema controls call shape; the shared skill
controls workflow and safety boundaries.

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
8. Call `get_source_claim_review` for each exact attempt/occurrence; retain and
   show the complete native display and subject with separate coverage notes.
9. Stop for a human decision unless a valid delegated decision already exists.
10. Use `admit_reviewed_source_claim` (`approved`) or
    `record_reviewed_source_claim_disposition` (`reject`/`audit_only`) only with
    the unchanged subject and exact human decision/reason. Preserve them for
    uncertain-outcome retries as described in the shared workflow.
11. Read back final records and report the resulting IDs and states.

Standard profiles do not expose legacy admission/disposition, contradiction,
Supersession, or repository activation writers. Report missing capabilities;
do not enable legacy profiles or substitute direct SQL. Ordinary external
source review does not require converting the source to Brief.

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
