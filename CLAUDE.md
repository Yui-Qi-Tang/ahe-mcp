# AHE Project Instructions

## Read First

- This is the shared operational contract for all cooperating agents. AHE is
  an evidence layer, not the downstream agent. The primary agent may explore
  within the user's scope; claims enter canonical evidence only through explicit
  review.
- Use the task routing below. Do not read all of `README.md` by default.
- For installation or operating-system support, read `INSTALL.md`.
- Consult only the matching `README.md` section when changing its contract:
  `Authority Model`, `Programs`, `External Model and Connector Intake`, `MCP
  Client`, or `Tests`.
- For evidence lookup, support tracing or state interpretation, use the
  [shared query skill](.claude/skills/ahe-evidence-query/SKILL.md).
- For external provider intake, use the
  [shared intake skill](.claude/skills/ahe-evidence-intake/SKILL.md). Its separate
  relation and endpoint sections apply only to those authorized workflows.
  Codex agents enter through the matching `.agents/skills/` wrappers so that
  actual producer identity and delegation boundaries are preserved.
- For experiment design, execution or scoring, read the
  [experiment protocol](.claude/skills/ahe-evidence-query/references/experiments.md).
  Consult `README.md`'s evaluation section only when its reported results matter.
- Discover the currently exposed MCP input schemas before calling tools. Do not
  reconstruct schemas from memory or invent provider-specific AHE tool names.

## Agent Division of Work

Unless the user explicitly changes this division, subagents serve only as
text-only adversaries. This applies when creating, reusing or continuing a
subagent; a normal task assignment does not change the division.

- The user and primary agent own problem definition, assumptions, research
  direction, source lookup, design, implementation, experiments, file/data
  operations, verification and final conclusions. Do not delegate those tasks,
  including by relabeling execution or routine review as adversarial work.
- Use a subagent only to challenge an explicit proposition or seek a counterexample
  or logical gap. Supply the fixed assumptions and scope, necessary evidence
  excerpts, verified and unverified parts, and criteria for a valid counterexample.
- The subagent reasons only from material supplied by the primary agent and
  returns textual challenges. It must not research, browse, call tools, write
  code, execute commands/tests, read or write files/data, or create further subagents. Missing
  material must be reported to the primary agent, not collected independently.
- Each challenge identifies the claim, counterexample or gap, why it could hold,
  and what still needs checking. Stay within the fixed scope; label any challenge
  to the assumptions separately instead of changing the problem silently.
- Feedback is provisional. The primary agent must check or reproduce it before
  adopting it, acknowledge valid counterevidence and retain final responsibility.
  Agent agreement or failure to find a counterexample is not independent proof.

An evidence-admission decision does not authorize subagent execution or change
this division. Human approval of exact evidence and authorization for repository
work remain separate under the contracts below.

## Authority and Lifecycle

- PostgreSQL is authoritative. Process-local state is scheduling or cache state,
  never evidence authority.
- Preserve these distinct lifecycle states:

  ```text
  collected source
  != pending proposal
  != admitted canonical evidence
  != active repository generation
  != downstream answer
  ```

- Extraction creates candidate material. Only explicit admission creates
  canonical evidence.
- Keep source collection and provider connectors outside AHE Core. The built-in
  local-model path is optional, not the default external intake path.
- Do not bypass domain APIs with direct writes to canonical or lifecycle tables.

## MCP Boundaries

- `ahe-query-mcp` is read-only. Its results are evidence packages, not final
  answers. Retrieval rank is not truth confidence, and an in-scope miss is not
  proof of global absence.
- `ahe-ingest-mcp` is write-capable and belongs only in a trusted intake,
  review, or lifecycle workflow.
- No query, adapter, extractor, planner, or timer may silently cross the
  admission boundary.
- Do not write arbitrary canonical edges. Contradiction uses its dedicated
  proposal lifecycle. Supersession uses a fresh external pending proposal plus
  its dedicated atomic admission and read-only currentness tools.
- Standard ingestion profiles are separately authorized `core-records`, `intake`,
  `source-claim-reviewer`, `repository-intake`, `endpoint-reviewer`,
  `historical-derivation-reviewer`,
  `supersession-reviewer`, `initial-lineage-reviewer`, `relation-reviewer`,
  `withdrawal-reviewer`, `source-assessment-reviewer` and `contradiction-reviewer`.
  Bounded exact-reviewed derived/code endpoints use the
  endpoint profile. Exact fresh-v2 replacement uses the separate supersession
  reviewer; generic derived admission and repository activation
  writers remain internal contracts, not enabled
  standard MCP tools. Their read-only tools do not authorize these writes.
  If the required tool is absent, stop and report the unavailable capability;
  never enable a legacy profile or substitute direct SQL to follow this guide.

## Core Record Writes

The separately provisioned `core-records` profile preserves external proposition
identity decisions and immutable external checks/representations. It also registers explicit consistency watches for the separately started worker.
The MCP recorder does not admit claims, prove semantic equivalence or execute
a solver during a tool call. See [consistency operation](docs/CONSISTENCY.md).
Read exact subjects/history through Query; obtain an explicit decision for identity
bindings or corrections. Preserve request inputs for exact retries. Launcher-owned
`decision_by` and `recorded_by` cannot be supplied by tools. Claimed checker/producer
identity is separate, and database audit text alone is not authentication. Read
[Core setup](INSTALL.md#core-records-profile) before operating this profile.

## External Evidence Invariants

- The user owns collection scope and desired claim granularity. Keep the current
  bounded intake contract; do not expand JSON segmentation or capacity controls
  to work around issue #25. Use already authorized exact excerpts with honest
  omissions, or report that the requested scope cannot be completed. Follow the
  shared [granularity requirements and current limits](.claude/skills/ahe-evidence-intake/SKILL.md#user-controlled-granularity-and-current-limits).
- Engineering evidence intake must not be replaced by Brief, model highlights,
  or summary chunks. Retained source bytes and individually grounded claims do
  not compensate for omitted in-scope information. Preserve requirements,
  conditions, exceptions and status details; disclose extraction omissions
  separately from source coverage. No observed fabrication is not completeness.
- Submit connector-observed text or JSON exactly. Never replace source content
  with a model summary or paraphrase.
- Preserve the actual source-object identity. The external collector chooses
  `revision`, an opaque source version or snapshot identifier; it need not be
  issued by the provider. A content digest is allowed. Do not present a
  collector-issued identifier as provider-issued or alter source identity to
  bypass a conflict. AHE validates retained bindings, not the issuing method.
- Reuse a request ID only for an exact retry. A new delivery uses a new request
  ID; changed content requires a new external revision. The same object/revision
  cannot be rebound to different content or source-level metadata.
- Ground every proposal in persisted span IDs. Abstain when no supported
  proposal is warranted.
- Always provide a stable extractor definition name and version. Supply
  `producer_session_ref` only when a stable, non-secret reference exists; omit
  it rather than inventing one.
- Before admission, show the human the proposed sentence, exact excerpts,
  source title and location, coverage and limitations, external revision, and
  the version difference when a comparable prior revision exists.
- Obtain `get_source_claim_review` for each exact attempt/occurrence and show
  its complete native display. After an explicit decision and reason, use
  `admit_reviewed_source_claim` (`approved`) or
  `record_reviewed_source_claim_disposition` (`reject`/`audit_only`) with the
  unchanged returned subject as `expected_subject`. The launcher supplies the
  reviewer identity. Preserve the exact inputs for uncertain-outcome replay;
  do not fall back to `admit_pending_proposal` or legacy disposition tools.
  AHE records the binding but does not prove the human read the display.

## Independent Relation Review

A separately provisioned `relation-reviewer` exposes `get_implements_review`,
`admit_reviewed_implements`, `get_references_review` and
`admit_reviewed_references`. Follow the shared skill's exact review flow:
node approval does not approve a relation. This profile only connects existing
admitted endpoints; it does not enable derived/repository node creation.
`implements` currently requires a derived specification with complete AND
ancestry and repository-backed Go code. `references` requires supported
markers already present in exact source bytes, not arbitrary URLs or inferred
links. Do not manufacture markers. Relation reject/audit_only is a non-write
human outcome, not a tool in this profile. See [setup and scope](INSTALL.md#independent-relation-reviewer).

## Reviewed Supersession and Endpoint Disposition

A separately provisioned `supersession-reviewer` exposes `get_supersession_review`
and `admit_reviewed_supersession`. Keep the replacement external source claim
pending. Select its exact occurrence, complete 1–64 older source-claim targets
and six-field basis. Display the complete native review with retained old/new
quotes, revisions, version comparison limitations and CAS coordinate. After
explicit human approval and reason, copy `display.request` as `review` and
`subject` as `expected_subject`, with `decision=approved` and a stable request ID.
The launcher supplies reviewer identity. Claim, edges, event, head and receipt
commit together. Head or subject conflicts need a new review and decision;
uncertain outcomes require the unchanged original request. `exact_replay_only`
is historical material and permits only the original retry. Read currentness
with the returned lineage key; it does not prove provider freshness or truth.
Repository code, derived endpoints and already ordinarily admitted replacements
are outside fresh-v2. Never relabel an endpoint or treat a newly assigned
revision as proof of semantic replacement.

Pending fresh reviewed v2 locks `proposal → governance → supersession head`.
Its historical replay still locks the original proposal and performs complete
request, subject, reviewer/reason, receipt and native-authority verification,
but does not acquire the fresh governance fence. This avoids a reverse wait with
references holding governance before its origin-proposal FK lock. Do not add a
fresh-use gate to that replay or generalize this exception to other governance APIs.

A separately provisioned `initial-lineage-reviewer` exposes
`get_initial_lineage_registration_review` and
`apply_reviewed_initial_lineage_registration`. Only an exact independently
approved singleton/unclassified original external claim can gain first
membership; retain the complete review and unchanged subject. The launcher binds
reviewer. Registration preserves the original admission and v2 head; historical
retry is not present clearance. Read currentness and reassess all bases. See
[the native contract](docs/INITIAL_LINEAGE_REGISTRATION.md). Never reuse source
approval, manufacture freshness or silently retire the repeated occurrence.

Endpoint review supports approved admission or explicit `reject`/`audit_only`
through `record_reviewed_endpoint_disposition`, with the same unchanged review,
subject, request ID and human reason. A disposition is terminal for that
occurrence and creates no canonical evidence. Show lifecycle alongside the
historical display; Query reads `endpoint_disposition` using the occurrence ID.
Admission and disposition share request-ID authority. Changed decisions are not
retries, and source disposition tools cannot substitute for endpoint review.

## Historical Derivation Review

Use only the separate `historical-derivation-reviewer` and the [native historical
contract](docs/HISTORICAL_DERIVATION.md). `get_historical_derivation_review` returns
complete sources, required AND ancestry, derivation context and recorded governance
states. New conclusions need not occur in the original source text; quotations
belong to premises. After an exact explicit human decision and reason, use
`apply_historical_derivation_decision` with the unchanged review and subject.
Approved history is permanently ineligible for current use, including required
descendants; reject/audit_only creates no canonical node. Caller policy, class and
reviewer fields are forbidden. Current approval cannot substitute for historical
approval, or vice versa. Query history uses the original proposal occurrence.
Preserve exact retry inputs. Existing conflict-suggestion and human review rules
apply. Qualified manual parents require native first-capture metadata; never retrofit
missing authority or treat historical admission as a current-gate workaround.

## Reviewed Withdrawal and Source Assessments

The separately provisioned `withdrawal-reviewer` and
`source-assessment-reviewer` profiles append governance events after exact native
review. Their authority is distinct: withdrawal stops new current use of a claim;
source assessment annotates an exact snapshot and its original declarations.
Neither edits admission, source bytes, canonical relations or AND parents.
Correction references do not imply supersession or truth. Display the complete
native subject and preserve explicit per-item decisions, human reasons, launcher
identity and exact retry inputs. Historical local records are review material,
not new approval. Do not migrate operational records under code-change approval.

Query exposes versioned use state and governance history. When planning actions
that rely on current evidence, check bases with explicit `current-action/v1` or
`grounded-current-action/v1` before planning, then `check_current_evidence_plan`
with the same purpose on every emitted action's complete basis IDs after
planning/SAT. Its `accepted` result concerns the declared bases under that
policy, not the correctness of the action or completeness of its premises.
A cached view, verification report, SAT result or old receipt is insufficient.
Public derived admission retains the gate inside its committing transaction;
an earlier client check cannot replace it. Admission, current-use eligibility
and action approval remain distinct. Human review must judge whether the parents
support the derived claim and its conditions. The consumer separately validates
the concrete action, required external freshness and execution conditions;
later execution needs its own coordination. Historical inspection does not
enable a historical flag or an internal writer to bypass the public gate.
See [governance contracts](internal/evidenceingestion/GOVERNANCE.md)
for batch preparation, readback, source resolution and bounded completeness.
Batch approval binds the unchanged manifest, native per-item subjects and new
human reasons. Original local records remain review material, including purely
human exclusions that never ran a checker.

## Reviewed Contradiction and Intake Suggestions

A separately provisioned `contradiction-reviewer` exposes
`get_contradiction_review` and `apply_reviewed_contradiction`. Select both exact
admitted endpoints, rationale, explicit scope/conditions, producer identity and
`relation=contradicts`. Show the complete native display and its limitations.
Node approval does not authorize this relation. The getter is read-only; only
an explicit independent `approved`, `reject` or `audit_only` decision with a
human reason persists the pair-v1 proposal and receipt. Copy unchanged
`display.request` as `review` and `subject` as `expected_subject`; use a stable
request ID. Launcher supplies reviewer identity. Preserve all inputs for retry.

A single symmetric pair is stored, with no false-endpoint choice or implied
pairwise edges across a component. `exact_replay_only` is historical material,
not new approval or current-use permission. Rejected/audit-only pairs cannot be
reopened. Correcting an erroneous relation requires a future explicit lifecycle;
do not manufacture duplicate nodes. Legacy contradiction writers remain internal
and disabled in standard profiles. Query exposes actual edges and review receipts.

Before presenting any statement proposal, derived endpoint or relation for a
decision, run `suggest_intake_conflicts` on each pending statement occurrence
and on the origin occurrence of each statement endpoint or statement parent.
The scan is bounded (at most 1000 claims, no pagination). Show the candidates,
an explicit empty result or the failure, with coverage and truncation limits;
never report a failure as zero candidates. Whether to proceed after a failed or
truncated check is the human's decision. The intake tool
`suggest_intake_conflicts` reads bounded candidates for a
persisted statement proposal using external object identity, recorded or
explicitly hinted slots, direct relations and lexical overlap. Inspect matching
basis, snapshot and all coverage/truncation limits. A slot hint creates no binding;
lexical similarity is not a contradiction verdict, and no suggestion is not
proof of no conflict. Suggestions never admit relations, withdraw or supersede.
See [contracts](internal/evidenceingestion/GOVERNANCE.md) and
[setup](INSTALL.md#independent-contradiction-reviewer).

Never place credentials, tokens, DSNs, private conversation text, or other
secrets in source metadata, extractor configuration, session references, or
committed files.

## Controlled Experiments

Follow the [experiment protocol](.claude/skills/ahe-evidence-query/references/experiments.md)
for test approval stubs, frozen comparisons, retained failures and separate
scoring of unsupported assertions, abstention and downstream repair outcomes.
An explicitly authorized experiment may simulate approval only for its declared
synthetic/public cases in an isolated, disposable non-production database.
Label the simulation; it is not human approval or permission to use stubs in
an operational evidence store. AHE operations claimed by an experiment must
actually run. Research authorization does not approve real evidence admission.

## Repository Work

- Identify the checkout before editing or testing. This repository and all
  its worktrees belong to AHE MCP, including those under `lab/worktrees/`.
  Their `lab/` folders contain private verification artifacts.
- Reports must identify checkout, branch/HEAD, dirty state, source hashes, test
  selection and PASS/FAIL/SKIP plus excluded scopes. Preserve earlier failures;
  archives, feature adoption, verification, merge, push and deployment are
  separate states. Never transfer a passing result between environments.

- Use Go 1.27.3 semantics. Run `make verify` for ordinary code changes; add
  `go test -race ./...` when concurrency behavior or CI is in scope.
  For documentation or skill-only edits, validate links, skill structure and
  agreement with the actual contracts; do not run models or broad code tests
  solely because instructions changed.
- Run integration tests only against an explicitly selected non-production
  PostgreSQL database. Never print or commit the value of `DATABASE_DSN`.
- Preserve unrelated changes and the current worktree boundary. Do not commit,
  push, merge or change branches unless explicitly requested. Confirm the
  actual target branch and stage only task-owned files. An existing explicit
  Git request authorizes that scoped action; do not ask for it again.
- Keep `docs/` for current theory, algorithms, data structures and references.
  Keep raw experiment captures, historical reviews, maintainer status and
  release checklists in private Product verification directories or ignored maintainer files. Never force-add
  private files or publish machine paths, credentials or company source data.
- Public synthetic fixtures, redistributable public samples, general methods
  and quantitative summaries may be included within the user's requested
  repository/publication scope. State source/license references, limitations
  and any missing material needed for reproduction. Public summaries do not
  make private raw captures publishable.

## Releases

An ordinary authorized commit or branch push is repository work. It does not
require a release checklist and does not authorize a release. A formal release,
release tag or distribution requires explicit release authorization and the
maintainer's release checklist, supplied in the current task or at a designated
private path. Do not invent the checklist or treat local test success as release
approval. If the checklist is unavailable, stop that release step and report
what is missing; complete other authorized preparation first.
