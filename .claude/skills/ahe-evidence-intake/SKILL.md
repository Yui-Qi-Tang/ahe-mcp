---
name: ahe-evidence-intake
description: Collect exact Jira, Confluence, or other external provider objects, produce scope-preserving span-grounded evidence, and apply explicit decisions through exact source review. Use for external evidence intake, not news briefs, read-only queries, or repository code extraction.
---

# AHE External Evidence Intake

Use this workflow to turn connector-observed provider objects into reviewable
AHE proposals without moving connector logic or admission judgment into AHE
Core.

## Engineering Evidence Is Not a Brief

For engineering intake, do not replace the requested evidence with a model's
short summary, selected highlights, or a sequence of summary chunks. Preserve
the in-scope requirements, conditions, exceptions, decisions, and status details
as readable grounded claims. Keeping the original body elsewhere does not
compensate for information omitted from the evidence being proposed.

Keep the two workflows separate. Brief provides a short reading orientation
for explicitly selected news/public events; it is not engineering extraction
or a completeness assessment. Do not convert provider content to `manual_text`
to bypass a missing revision, connector or capability. Repository code and git
extraction remain separate workflows.

The native small-model engineering path selects complete, verbatim units from
its current source input instead of writing shorter replacement sentences.
The controller verifies the selected text and its exact span reference even
when the model's output matches the JSON schema. A unit may contain multiple
conditions or table rows; selecting it is neither semantic validation nor proof
that all requested information was selected. Explicit section processing covers
the supplied adapter-selected text, not uncollected provider fields or every
fact in the full document. Oversized or invalid output must fail visibly.

Atlassian/Codegraph model extraction is not categorically disabled.
`proposal_extraction`, deterministic `proposal_conversion`, and collection-only
remain distinct operator choices; never silently enable proposal writing.
This native runner contract is separate from Claude's grounded proposal
workflow below. Do not label Claude-generated claims as native model selections.

Do not use the withdrawn exploratory 88%/77% figures as a document-coverage or
Brief-quality metric. They do not establish that either complete workflow lost
that fraction of source information.

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
- The standard executable requires an explicitly selected `intake`,
  `source-claim-reviewer`, `relation-reviewer`, `endpoint-reviewer` or
  `repository-intake` profile; it has no default
  profile. Intake exposes
  `submit_external_source`, `get_extractor_input`, and `submit_extractor_output`.
  A separately authorized reviewer exposes `get_source_claim_review`,
  `admit_reviewed_source_claim`, and `record_reviewed_source_claim_disposition`.
  Independent implementation/reference review uses a separately authorized
  `relation-reviewer`, described below. Legacy admission/disposition,
  generic derived-node, contradiction and Supersession writers are not enabled.
  Exact-reviewed endpoints use the separately authorized profile below. Do not
  enable a legacy profile, change a launcher, or use direct SQL to work around
  a missing capability. See [installation scope](../../../INSTALL.md#bounded-detective-to-pending-mcp-installation).

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
   - Extract from the persisted source itself, not from a model summary.
     Preserve exact wording where practical. Any necessary wording adjustment
     must preserve meaning, conditions, negation, quantities, scope and unknowns;
     it is not permission to compress distinct facts into highlights.
   - Do not add unsupported facts, semantic change claims, deletion claims, or
     global coverage claims. Do not force a fixed number of summary sentences.
   - Conflicting source material remains conflicting; do not silently reconcile
     it.
   - Compare the proposed set with the requested source scope. List represented
     details and known omissions or unprocessed sections in the review notes.
     Explicitly requested subsets are valid; silently narrowing the requested
     scope is not. Limits or unavailable content must be disclosed, not worked
     around by summarizing or inventing completeness.
4. Call `submit_extractor_output` using a new request ID and the returned source
   snapshot and extraction view IDs.
5. Identify the producer as:
   - name: `claude-code-grounded-extractor`
   - version: `ahe-external-intake-v2`

   These producer fields identify Claude Code. Other agents must use their own
   approved producer identity; Codex uses its agent-specific wrapper overrides.
   Do not attribute another agent's extraction to Claude.
   Treat the version as the proposal-production contract version. Increment it
   when proposal selection or grounding semantics change, not for formatting-only
   edits to this skill.
   Version v2 makes scope-preserving extraction explicit. Preserve existing v1
   attempts and receipts; do not relabel or silently regenerate them.
6. Add bounded, non-secret extractor config only when it improves debugging.
   Supply `producer_session_ref` only if Claude Code exposes a stable opaque
   task or session reference. Omit it rather than inventing one.
7. When no grounded proposal is warranted, submit an empty proposal list and
   report the successful `abstained` result.

Source `coverage` describes what was collected, not how much the extractor
retained. A `full_document` source can still yield incomplete proposals.
Grounding, omission, and fabrication are separate checks: matching quotes and
no observed fabrication do not prove completeness. Coverage review is an
assistant check, not an independently measured recall guarantee. Keep its
notes outside the MCP payload unless the live schema explicitly supports them.

When the query MCP is available, read back the pending proposal records before
building review cards. If `proposal_count` is greater than one, list pending
records for the exact source snapshot; do not assume the single occurrence ID
in the submit response identifies every proposal. If query readback is
unavailable, disclose that limit and do not invent missing occurrence IDs.
An exact review response can supply the complete manifest for a known member;
use each member's own exact review before requesting a decision. Submitted text
or a hand-written card cannot substitute for the native review response.

## Human Review Stop

For each pending proposal, call `get_source_claim_review` with its exact
`extraction_attempt_id` and `proposal_occurrence_id`. Retain the returned
`subject`, complete manifest and exact display. The manifest identifies the
batch; it does not approve other members or prove coverage of the source.
Oversized, incomplete, conflicting or unavailable review material stops the
decision path; do not trim it or fall back to a legacy writer.

Display the complete native review material for each numbered proposal. A
short navigation card may accompany it, but must not replace the exact display.
Include:

- proposal occurrence ID;
- proposed sentence;
- exact source excerpts and their span IDs;
- source title and location;
- provider revision;
- coverage and limitations;
- comparison with a prior revision when one is available, otherwise an explicit
  statement that no comparable prior revision was available;
- any uncertainty that affects interpretation;
- the extraction coverage notes, including known omissions, separately from
  the unmodified native display.

Ask which proposal numbers the human approves, rejects, or retains as
`audit_only`, then stop and wait for an explicit decision. Do not interpret
silence, a request to continue analysis, or approval of the overall task as
proposal admission approval.
Approval of a selected claim does not certify the completeness of the entire
document's extraction. If a reason is missing, ask for it; do not invent one.

## Apply the Human Decision

- Before a writer call, retain the exact attempt ID, returned `subject`, human
  decision and reason, and the authorized reviewer launcher binding in the
  permitted private work record. If they cannot be retained for an exact retry,
  stop before writing. Do not put private records or credentials in the repo.
- For explicit approval, call `admit_reviewed_source_claim` with
  `decision=approved`, the attempt ID, the returned `subject` unchanged as
  `expected_subject`, and the human's `decision_reason`.
- For explicit non-admission, use `record_reviewed_source_claim_disposition`
  with the same binding fields and `decision=reject` or `audit_only`.
  `reject` is the request value; the persisted outcome is `rejected`.
- The reviewer identity comes from the authorized launcher. Do not send a
  caller-selected reviewer, forge subject IDs, or add request/session fields
  that the live writer schema does not accept.
- Each call applies only to that displayed proposal. Batch membership,
  a previous approval, or a request to continue is not approval of other claims.
- If the outcome is uncertain, read back when possible and retry only the
  preserved identical subject, decision, reason and reviewer binding. Do not
  obtain a new pending review or resubmit extraction merely to recover a
  decision. Missing saved inputs or conflicting state requires a stop.
- Use Query readback for the final state; report missing readback explicitly.

## Independent Implementation and Reference Review

Use this only when the user authorized relation work and a separately
provisioned `relation-reviewer` is actually available. Do not reconfigure
another profile to obtain write access.

1. Read both admitted endpoints through Query. For `implements`, the current
   writer requires a derived specification and repository-backed Go code, with
   explicit rules for every derived layer and the complete AND ancestry.
   It does not create either endpoint or accept a source specification directly.
   For `references`, both endpoints must have qualified external source
   authority; v1 requires literal `[[ahe-ref:#anchor]]` or snapshot-pinned
   markers and exact `[[ahe-anchor:...]]` targets in the original source.
   Arbitrary URLs, Jira links and inferred mentions are unsupported. Never
   change captured originals or manufacture markers to pass the gate.
2. Discover schemas and call `get_implements_review` or
   `get_references_review`. Show the **complete native display**, including
   source excerpts, code path/revision where applicable, all rules, mapping,
   coverage and limitations. Graph connectivity or a model-written card is
   not a substitute.
3. Ask the user to approve this exact directed relation with a reason.
   Approval of the nodes, a prior relation or an instruction to continue is
   not edge approval. Rejection/audit_only means no relation write; this
   profile does not persist those relation decisions.
4. Only after explicit approval call `admit_reviewed_implements` or
   `admit_reviewed_references` with the returned `review`, unchanged
   `subject` as `expected_subject`, `decision=approved`, reason and a stable
   request ID. The launcher supplies reviewer identity. Do not submit a
   caller-authored receipt or graph as authority. Save the exact inputs for
   uncertain-outcome retries; a changed subject requires a new human review.
5. Query `get_relation_provenance` and inspect `implements_admission` or
   `references_admission`. Report the edge, independent receipt, replay state
   and limitations. Structural navigation is not truth, causal support,
   transitive implementation or currentness.

## Exact Endpoint Review

With the separately provisioned `endpoint-reviewer`, admit a source-backed
statement as `kind=derived_spec` through `get_endpoint_review` → explicit
human approval → `admit_reviewed_endpoint`. Supply the complete 1–8 sorted
admitted AND parents and explicit method/producer/trace reference. Retain source
bytes and qualifications; do not invent a manual source for the derivation.

Show the complete native display and ancestor source context. Nested
source-basis effects are provenance context, not source approval. Copy the
exact request and subject, the human reason and a stable request ID. Reviewer
identity comes from the launcher. Query the resulting node and
`endpoint_admission`. Parser-grounded `kind=repository_code` uses the same
reviewer without derivation.

Repository capture needs its own authorized `repository-intake` launcher,
fixed root/identity and exact commit. It stores tracked Go bytes and parses
pending proposals without running code, fetching data, calling models or
activating a generation. Query returned IDs or `lifecycle_scope=all`.
Neither repository access nor launcher reconfiguration is implied by relation
review permission. Endpoint tools currently support approval only; source
`reject`/`audit_only` tools are not a workaround for code endpoints.

## Relations and Repository Extraction Are Separate

Source intake/review does not itself expose endpoint admission. Generic derived
admission, contradiction, Supersession and repository activation remain disabled. If the task
needs these capabilities, report the unavailable workflow and stop. Do not
substitute an ordinary statement, generic edge, fake replacement node, legacy
profile, or direct SQL. Provider revision order alone does not prove semantic
replacement. Internal relation theory remains in
[system design](../../../docs/SYSTEM_DESIGN.md#graph); it is not permission to
execute a disabled writer. Repository code extraction is outside this skill.

## Completion Report

Report results per provider object and proposal:

- source snapshot ID and extraction view ID;
- provider object identity and revision;
- extraction attempt and proposal occurrence IDs;
- admitted canonical reference and admission decision ID, when admitted;
- rejected, `audit_only`, abstained, replayed, conflicted, or unprocessed state;
- coverage, limitations, and any missing readback capability;
- extraction coverage notes and known omissions, separate from source coverage;
- the exact reviewed subject and resulting decision ID when a decision was made;
- any requested relation or repository workflow that was unavailable.

Do not claim that AHE cryptographically or independently verified the human
review conversation. The cooperating agent is responsible for showing the
review cards and waiting for the decision.
