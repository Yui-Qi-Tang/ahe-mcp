# AHE Project Instructions

## Read First

- This is the shared operational contract for all cooperating agents. AHE is
  an evidence layer, not the downstream agent. Models may explore freely;
  claims enter canonical evidence only through explicit review.
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
  `source-claim-reviewer`, `repository-intake`, `endpoint-reviewer` and
  `relation-reviewer`. Bounded exact-reviewed derived/code endpoints use the
  endpoint profile; generic derived admission, contradiction, Supersession
  and repository activation writers remain internal contracts, not enabled
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

- Engineering evidence intake must not be replaced by Brief, model highlights,
  or summary chunks. Retained source bytes and individually grounded claims do
  not compensate for omitted in-scope information. Preserve requirements,
  conditions, exceptions and status details; disclose extraction omissions
  separately from source coverage. No observed fabrication is not completeness.
- Submit connector-observed text or JSON exactly. Never replace source content
  with a model summary or paraphrase.
- Use provider identity and revision metadata from the connector. Never invent
  a provider revision to make changed content pass validation.
- Reuse a request ID only for an exact retry. A new delivery uses a new request
  ID; changed content requires a new provider revision.
- Ground every proposal in persisted span IDs. Abstain when no supported
  proposal is warranted.
- Always provide a stable extractor definition name and version. Supply
  `producer_session_ref` only when a stable, non-secret reference exists; omit
  it rather than inventing one.
- Before admission, show the human the proposed sentence, exact excerpts,
  source title and location, coverage and limitations, provider revision, and
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

## Internal Relation Contracts (Not Standard MCP Operations)

The following describes retained domain behavior, not an executable intake
recipe. These writers are disabled in the standard installation; stop and
report the missing capability rather than changing profiles or using SQL.

- When two admitted canonical nodes appear incompatible, call
  `submit_canonical_contradiction_proposal`, then read
  `get_canonical_contradiction_proposal`. Show both grounded canonical records,
  their exact excerpts and source metadata, plus the agent rationale. Call
  `admit_pending_canonical_contradiction` only after explicit approval;
  otherwise use the dedicated contradiction disposition tool.
- `contradicts` is symmetric. Do not submit both A/B and B/A; AHE canonicalizes
  endpoint order and keeps one governed relation per node pair.
- In v1 that node pair is single-use even after `rejected` or `audit_only`.
  Changed rationale, producer metadata, or session reference conflicts rather
  than creating a new proposal version. Do not manufacture replacement nodes
  to bypass this limitation.
- For an explicit version replacement, keep the new external-source proposal
  pending. Read it, every older target, and `get_canonical_supersession_head`.
  Show exact quotes, source titles/locations, provider revision, version
  differences, coverage/limitations, the complete target set, six-field
  source-object/slot basis, and full head revision/event ID coordinate.
- Call `admit_pending_supersession` only after explicit approval. Rejected or
  `audit_only` uses `record_pending_proposal_disposition`. AHE atomically creates
  the fresh claim, exact `new -> old` edges, lineage event, and next head.
- Read back `get_canonical_supersession_currentness` with the returned lineage
  key. Never supply or infer completeness, members, winner, status, head, or
  hashes. Snapshot currentness is not provider freshness or global truth.
- Do not infer replacement or stable slot identity from revision order or
  repository source-generation lifecycle. See `docs/SYSTEM_DESIGN.md#graph-relations-and-versions`.
- Never place credentials, tokens, DSNs, private conversation text, or other
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

- Use Go 1.27.0 semantics. Run `make verify` for ordinary code changes; add
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
