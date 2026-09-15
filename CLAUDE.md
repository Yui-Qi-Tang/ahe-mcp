# AHE Project Instructions

## Read First

- Detective Desktop is **frozen / unavailable** as of 2026-09-14.
  CLI stabilization comes first. Do not request UI acceptance, start the app,
  extend Desktop or publish it unless the user explicitly unfreezes that work.
  Keep existing code/tests/data. CLI end-to-end engineering intake remains incomplete.

- Treat this file as the operational summary. Do not read all of `README.md` by
  default.
- For Detective extraction contracts, read `docs/SYSTEM_DESIGN.md` and the
  Detective README. Maintainer plans and experimental records are private;
  they are not available runtime capabilities or permission to write.
- For installation or operating-system support, read `INSTALL.md`. The
  `deploy/macos` directory is optional `launchd` packaging, not an AHE Core
  platform requirement.
- Consult only the matching `README.md` section when changing its contract:
  `Authority Model`, `Programs`, `External Model and Connector Intake`, `MCP
  Client`, or `Tests`.
- Use the project skill at
  `.claude/skills/ahe-evidence-intake/SKILL.md` for Jira, Confluence, or other
  external evidence intake work.
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
- The standard ingestion executable enables only `intake` and
  `source-claim-reviewer`. Derived, contradiction and Supersession writers
  below describe internal domain contracts, not enabled standard MCP tools.
  If the required tool is absent, stop and report the unavailable capability;
  never enable a legacy profile or substitute direct SQL to follow this guide.

## External Evidence Invariants

- Engineering evidence intake must not be replaced by Brief, model highlights,
  or summary chunks. Retained source bytes and individually grounded claims do
  not compensate for omitted in-scope information. Preserve requirements,
  conditions, exceptions and status details; disclose extraction omissions
  separately from source coverage. No observed fabrication is not completeness.
- New Brief operations require `detective-brief-source/v2` with `source_kind`
  equal to `news` or `public_event`. The declaration is not automatic content
  classification; do not relabel engineering sources or use `manual_text` to
  bypass external-source identity, provider revision or capability requirements.
- The retained legacy host's small-model engineering path selects complete
  verbatim source units, then independently checks text and span identity.
  This whole-span mode is still wired in the working draft; it is a comparison
  baseline, not the selected task-driven end state or a Desktop capability. It does not
  replace engineering content with Brief summaries. Explicit section processing
  describes the supplied input, not semantic completeness or uncollected fields.
  Model extraction, deterministic conversion and collection-only are separate
  operator choices; do not silently enable proposal writing or categorically
  prohibit a model because the provider is Atlassian/Codegraph.
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
  repository source-generation lifecycle. See `docs/SYSTEM_DESIGN.md#graph`.
- Never place credentials, tokens, DSNs, private conversation text, or other
  secrets in source metadata, extractor configuration, session references, or
  committed files.

## Repository Work

- Use Go 1.27.0 semantics.
- Run `make verify` for ordinary code changes.
- Run `go test -race ./...` when concurrency behavior or CI is in scope.
- Run integration tests only against an explicitly selected non-production
  PostgreSQL database. Never print or commit the value of `DATABASE_DSN`.
- Preserve unrelated user changes. Do not commit, push, merge, or change
  branches unless the user explicitly requests it.
