# AHE Agent Instructions

## Shared Contract

- Read `CLAUDE.md` completely before changing or operating AHE. Despite its
  filename, it is the shared operational contract for every cooperating agent.
- Do not read all of `README.md` by default. Follow the targeted reading rules
  in `CLAUDE.md`.
- For Jira, Confluence, or other external evidence intake, use the repository
  skill at `.agents/skills/ahe-evidence-intake/SKILL.md`.
- Inspect the live MCP tool list and input schemas before calling tools. Names
  and schemas observed from the server override remembered examples.

## Agent and Sub-Agent Authority

- A task assignment defines work scope; it is not human approval for evidence
  admission, rejection, or `audit_only` disposition.
- A sub-agent may collect authorized source data, persist exact source as an
  immutable snapshot, produce grounded proposals, and prepare review cards.
- A sub-agent may apply an admission or disposition only when its assignment
  includes the human's explicit decision, the exact proposal IDs, the selected
  outcome, and every human-supplied decision field required by the live tool
  schema. Otherwise it must return the review package to the parent agent and
  stop at the review boundary.
- Never infer approval from "continue", "finish", silence, approval of a code
  change, or approval of the overall research task.
- Keep company data inside its authorized environment. Do not copy private
  source content into repository fixtures, commits, public issue text, or an
  unrelated experiment environment.
- Return compact, inspectable results. Do not rely on hidden sub-agent logs as
  the only record of source identity, proposal IDs, review state, or failures.

## Claude-Compatible Intake Behaviour

- Keep connectors and source collection outside AHE Core. The cooperating
  agent supplies exact connector-observed source plus provider-controlled
  identity and revision metadata.
- Never replace source content with a summary, paraphrase, or inferred
  reconstruction before `submit_external_source`.
- Treat extraction output as candidate material. Only governed admission may
  create canonical evidence.
- Use the read-only query MCP for lookup and readback. Use the ingestion MCP
  only inside a trusted intake and review workflow.
- Show humans proposal sentences and grounded source context, not graph
  fragments alone. Every review card must include exact excerpts, source title
  and location, provider revision, coverage, limitations, and a version
  difference when one is available.
- Use the dedicated proposal workflow for canonical `contradicts`. For
  `supersedes`, keep the new external-source proposal pending, read its exact
  older targets and the current Supersession head, then call
  `admit_pending_supersession` only after explicit approval of the complete
  target set and six-field source-object/slot basis.
- Never write arbitrary relation edges or infer replacement from provider
  revision order. Query currentness only with the lineage key returned by AHE;
  do not supply completeness, members, a winner, status, head, or hashes.
- The relation workflows above are internal contracts: standard ingestion
  profiles do not expose contradiction or Supersession writers. Stop when a
  required tool is unavailable; do not enable a legacy profile or use direct SQL.

## Handoff Format

When a sub-agent returns intake work to a parent agent, include:

1. Provider object identity, revision, coverage, and limitations.
2. Source snapshot and extraction view IDs.
3. Extraction attempt and proposal occurrence IDs.
4. A numbered human review card for each pending proposal or relation.
5. Readback status and any capability that was unavailable.
6. The exact next action requested from the human.
7. An explicit statement of whether any canonical mutation occurred.

Do not expose credentials, tokens, DSNs, or private session text in the
handoff.

## Repository Work

- Preserve unrelated user changes and respect the current worktree boundary.
- Do not commit, push, merge, or change branches unless explicitly requested.
- Use Go 1.27.0 semantics. For ordinary code changes run `make verify`; add
  `go test -race ./...` when concurrency behaviour or CI is in scope.
- Run integration tests only against an explicitly selected non-production
  PostgreSQL database. Never print or commit `DATABASE_DNS`.
- Keep `docs/` for current theory, algorithms, data structures and their
  references. Keep experiment logs, raw captures and historical reviews in a
  private lab outside this repository; never publish the lab's machine paths.
- Follow `LAB_TO_PRODUCT_RELEASE_GATES.md` before public publication. A local
  documentation or test pass does not authorize a commit, push or release.
