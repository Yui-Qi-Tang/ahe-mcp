---
name: ahe-evidence-intake
description: Collect exact Jira, Confluence, or other external provider objects, produce scope-preserving span-grounded evidence, and apply explicit decisions through exact source review. Use for external evidence intake, not news briefs, read-only queries, or repository code extraction.
---

# AHE External Evidence Intake

Use this workflow to turn connector-observed provider objects into reviewable
AHE proposals without moving connector logic or admission judgment into AHE
Core. For existing-evidence lookup, relation traversal or state interpretation,
use [the query skill](../ahe-evidence-query/SKILL.md) instead. The workflow below
uses real human decisions. Test approval stubs are confined to explicitly
authorized isolated experiments under the
[experiment protocol](../ahe-evidence-query/references/experiments.md).

The primary agent performs collection, intake, review preparation and readback
under the [shared division of work](../../../CLAUDE.md#agent-division-of-work).
Subagents may only return text challenging supplied material; they may not use
tools, research, read/write files or data, execute, or delegate further. A human
evidence decision does not change that division or authorize subagent operations.

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

## Preconditions

- Use the source scope from the current user request. When invoked explicitly,
  `$ARGUMENTS` may contain the source references and scope.
- Confirm that the required source connector and the trusted AHE ingestion MCP
  are available. Use the read-only AHE query MCP for proposal enumeration,
  source context and final readback when available. Exact native source review
  comes from `get_source_claim_review` in the separately authorized reviewer
  profile; Query context is not a substitute for that review display.
- Inspect the current MCP tool schemas before the first call. Treat the schemas
  as authoritative; this skill defines workflow and safety constraints, not a
  frozen JSON contract.
- If the source connector or required AHE tool is unavailable, stop and report
  the missing capability. Do not substitute a model-generated source.
- The standard executable requires an explicitly selected profile; it has no
  default. Use the [current profile inventory](../../../INSTALL.md#installation-scope)
  and discover the installed tools. For this intake sequence, use `intake` and
  the separate `source-claim-reviewer`. Intake exposes
  `submit_external_source`, `get_extractor_input`, and `submit_extractor_output`.
  A separately authorized reviewer exposes `get_source_claim_review`,
  `admit_reviewed_source_claim`, and `record_reviewed_source_claim_disposition`.
  Independent implementation/reference review uses a separately authorized
  `relation-reviewer`, described below. Legacy admission/disposition,
  generic derived-node writers are not enabled. Supersession and contradiction
  require their separately provisioned exact-review profiles.
  Exact-reviewed endpoints use the separately authorized profile below. Do not
  enable a legacy profile, change a launcher, or use direct SQL to work around
  a missing capability. See [installation scope](../../../INSTALL.md#bounded-external-intake-mcp-installation).

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

## User-Controlled Granularity and Current Limits

The user chooses collection scope and desired claim granularity. Use the scope
already authorized; do not ask for it again. Each proposal must still state one
clear, supported claim and preserve its conditions, exceptions and uncertainty.
User-selected subsets are valid. If fitting a limit would materially narrow the
requested scope, explain the omission and obtain the user's choice before
submitting that narrower source. Do not silently substitute selected highlights
for a requested complete extraction.

The current external-intake contract is deliberately bounded:

- Source content is at most 512 KiB of UTF-8 bytes and 4,096 physical lines.
- External sources use `external-document-line-v1`, including
  `application/json`. Compact JSON is one span; escaped `\n` is not a physical
  newline. The caller cannot supply alternate span boundaries or JSON pointer
  selectors.
- A source-claim review display is at most 128 KiB for the complete serialized
  review package, including quotes, JSON escaping and metadata. A source or
  excerpt below that size is not automatically reviewable; the native getter
  must produce the complete valid display before a human decision.

Select a reviewable source scope before intake. Use a smaller genuine provider
object when it meets the task, or a user-authorized exact contiguous byte
excerpt with `coverage=exact_excerpt`. State the retained section and omitted
material in `limitations`; keep any known original position in review notes or
limitations without inventing unsupported MCP fields. Partial collection does
not establish global absence or complete extraction. If the task requires the
whole object and it cannot fit, report the unmet scope rather than treating an
excerpt as completion.

Preserve the selected bytes exactly. Do not replace them with decoded JSON/HTML
text, pretty-print, insert newlines, join disjoint excerpts, summarize or
reserialize them to create smaller spans. `application/json` requires the
submitted content itself to be valid JSON. For an exact raw fragment that is
not standalone JSON, use a
truthful supported format such as `text/plain` and disclose its JSON origin;
do not manufacture JSON wrappers. This is still external intake, not
`manual_text` or a model-generated source.

Keep the real provider object identity and revision. Different excerpts, or an
excerpt and the full body, can conflict under the same object/revision. Changing
only the request ID cannot fix that conflict. Do not invent chunk object IDs or
provider revisions, overwrite the stored source, or silently replace an earlier
excerpt. Stop and report the conflict.

For issue #25, the product decision is to keep these limits and let the user
control acquisition scope and claim granularity. Opening automatic JSON
segmentation, caller-defined catalogs or configurable/higher limits adds
identity, exact-review and replay complexity without sufficient benefit for this
workflow. Those extensions are not adopted capabilities. Do not enable or build
them as an intake workaround; preserve existing views, claims, review subjects
and receipts. Never trim a native display or bypass its approval boundary.

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

If a complete, valid human decision already exists for this exact unchanged
native subject/display, apply it without requesting the same decision again.
The primary agent may use an exact human decision supplied with the task when
all required fields and reviewer bindings are present; this does not authorize
subagent execution. Otherwise ask which proposal
numbers the human approves, rejects, or retains as `audit_only`, and wait for
that explicit decision and reason. A changed subject requires fresh review.
Do not interpret silence, a request to continue analysis, or approval of the
overall task as proposal admission approval. Approval of a selected claim does
not certify the completeness of the entire document's extraction. If a reason
is missing, ask for it; do not invent one.

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
3. Obtain approval of this exact directed relation with a reason. Reuse a
   complete, valid existing human decision for the unchanged native subject;
   otherwise show the review and ask. Approval of the nodes, a prior relation
   or an instruction to continue is not edge approval. Rejection/audit_only
   means no relation write; this
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
human approval → `admit_reviewed_endpoint`. Supply the complete 1–32 sorted
admitted AND parents, explicit method/producer/trace reference and
`derivation.use_policy` (`current-action/v1` or `grounded-current-action/v1`).
The review and admission recheck that policy; diagnose blocked bases with the
[reason code reference](../../../docs/CURRENT_USE_GUIDANCE.md), without changing
policy or omitting parents to pass. Retain source
bytes and qualifications; do not invent a manual source for the derivation.
The same per-node limit applies to independent implements review. Complete AND
ancestry remains bounded to 64 nodes and depth 8, with unchanged byte/display
limits; do not omit parents or invent intermediate claims merely to fit a cap.

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
review permission. For a human `reject` or `audit_only`, call `record_reviewed_endpoint_disposition`
with the unchanged `review`, `expected_subject`, human reason and stable
`request_id`. This terminally records that occurrence and creates no canonical
node or edge. Preserve the complete request for uncertain-outcome retry. Show
`lifecycle` alongside the historical display; `exact_replay_only` requires the
original decision, never a new review. Query its occurrence to read
`endpoint_disposition`. Source disposition is not a workaround for endpoints.

## Reviewed Fresh-v2 Supersession

Use only a separately provisioned `supersession-reviewer`. Keep the new external
source claim pending and select its exact occurrence, complete 1–64 older
external source-claim targets and six-field source-object/slot basis. Call
`get_supersession_review`; show its complete native display, exact new/old
quotes and source revisions, coverage/comparison limits, full target set and
CAS head. The stable semantic slot and replacement meaning require human review;
revision order does not establish them. After explicit approval and human reason,
copy the returned `display.request` as `review`, `subject` as `expected_subject`,
and send `admit_reviewed_supersession` with `decision=approved` and stable
`request_id`. Launcher-owned reviewer identity is not a tool argument.

Read the returned receipt and Query currentness with its lineage key. The entire
replacement and receipt commit together. Preserve the original request for
uncertain outcomes; exact replay remains available after later head advancement.
Pending fresh reviewed v2 uses `proposal → governance → supersession head`.
Historical replay keeps its proposal lock and complete exact/native-authority
verification, but does not acquire that fresh governance fence. This exception
does not remove locks from other governance APIs or turn a receipt into current-use
permission; see the [shared contract](../../../CLAUDE.md#reviewed-supersession-and-endpoint-disposition).
A head/subject conflict requires a fresh review and new decision. An
`exact_replay_only` display is historical material. Already ordinarily admitted
new claims, repository code and derived endpoints are outside fresh-v2. Never
substitute legacy tools, generic edges, fake source revisions or SQL.

Choose a documented, stable slot for one independently replaceable fact/aspect
inside the external source object, not for its revision or claim record ID.
Several facts in one function/test may need separate slots. The complete target
set covers the approved replacement scope; it need not include unrelated claims
from that source object. All six basis fields determine the lineage key, while
their semantic suitability still requires human review.

Fresh-v2 requires matching old/new source-object identity in all four provider
fields. Only a genuine same-object new revision can supply a fresh pending
replacement. Do not rewrite connector identity to bridge per-commit IDs, file
moves or a changed ID scheme. An already ordinarily admitted newer claim is not
retrofitted or retired by this flow; disclose any duplicate admitted history.
There is no redundancy marker or cross-object identity-migration operation in
this profile. See [stable slots and existing stores](../../../INSTALL.md#stable-slots-and-existing-stores).

Successful replacement does not guarantee closure or current-use eligibility.
Every admitted external source claim in the source-object manifest needs a
reviewed lineage assignment, possibly in a different semantic slot. Assignment
comes from justified fresh-v2 replacement, or the separately reviewed singleton
initial-registration route below. There is no arbitrary mark-current writer.
Do not replace unrelated claims together or split a real provider object merely
to close a lineage. Withdrawal alone does not remove an unclassified claim from
the manifest. In fresh-v2, old targets become superseded; reassess the replacement,
and do not revive its predecessors when it is withdrawn. Initial registration
instead retains C and requires reassessing that same C under the complete policy.

For unchanged same-C observations, do not self-target or invent a revision.
A separately provisioned `initial-lineage-reviewer` can register first membership
only for an unclassified singleton admitted external source object and an unused
lineage. Call `get_initial_lineage_registration_review` with C, the persisted
original snapshot/view receipt and six-field basis; display the entire native
review for independent human approval. Copy returned `request` as `review` and
`subject` as `expected_subject`; send `apply_reviewed_initial_lineage_registration`
with `decision=approved`, stable `request_id` and human `decision_reason`.
The launcher supplies identity. Source-admission approval is not this decision.
Keep the exact request for retries; changed subjects need fresh review. C,
its admission and derived parents stay unchanged, as does the v2 head. The repeated
pending occurrence remains pending. Read joint currentness and reassess all
bases; receipt IDs and timestamps do not prove provider recontact or freshness.
See [registration limits and native authority](../../../docs/INITIAL_LINEAGE_REGISTRATION.md).

When Query returns `unclassified_currentness`, `incomplete_currentness` or
another blocked/excluded reason, follow the
[current-use prerequisites and stop conditions](../../../docs/CURRENT_USE_GUIDANCE.md).
If a genuine eligible pending replacement, faithful same-object identity,
complete semantic target set, exact human decision or authorized reviewer is
unavailable, stop that write/current-use path and report the missing condition.
After an authorized change, read currentness and reassess all affected bases
under the same policy; a closed lineage can still have an ambiguous frontier
or fail another gate. Do not treat a reason code as automatic write approval.

## Relations and Repository Extraction Are Separate

Source intake/review does not itself expose endpoint admission. Generic derived
admission and repository activation remain disabled. If the task
needs these capabilities, report the unavailable workflow and stop. Do not
substitute an ordinary statement, generic edge, fake replacement node, legacy
profile, or direct SQL. Provider revision order alone does not prove semantic
replacement. Internal relation theory remains in
[system design](../../../docs/SYSTEM_DESIGN.md#graph-relations-and-versions); it is not permission to
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

## Independent Contradiction Review and Intake Suggestions

The separately provisioned `contradiction-reviewer` exposes
`get_contradiction_review` and `apply_reviewed_contradiction`. Discover their live
schemas. Select both admitted endpoints, grounded rationale when available,
explicit scope/conditions, producer identity and symmetric `contradicts` effect.
Show the entire native display with sources and limitations. Node admission is
not relation approval. Only after an explicit human `approved`, `reject` or
`audit_only` decision and reason, copy unchanged `display.request` as `review`
and `subject` as `expected_subject` with a stable request ID. Launcher binds the
reviewer. Preserve exact inputs for retries; changed subjects need new review.
A getter reserves nothing. Terminal pairs cannot reopen; `exact_replay_only`
is historical material, not new approval. Never duplicate nodes to bypass it.

Intake's read-only `suggest_intake_conflicts` can supply bounded candidates from
a persisted statement occurrence. Inspect each matching basis, scan/return limits,
truncation and source coordinates. A caller-declared slot hint is not a lineage
binding. Lexical overlap is not a contradiction verdict; no candidates is not
proof of no conflict. Suggestions never admit, withdraw or supersede evidence.
