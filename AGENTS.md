# AHE Agent Instructions

## Shared Contract and Task Routing

- Read [CLAUDE.md](CLAUDE.md) completely before changing or operating AHE. It
  owns the shared authority, capability, experiment and repository-work rules.
- AHE supplies evidence to models and agents. The primary agent may explore
  within the user's scope; claims enter canonical evidence only through explicit
  review. Downstream explanations still need their own evidence checks.
- Read only the documentation relevant to the task, using the routing in
  `CLAUDE.md`; do not load all of `README.md` by default.

| Task | Codex entrypoint |
| --- | --- |
| Answer with existing AHE evidence, trace support or inspect state | [.agents/skills/ahe-evidence-query/SKILL.md](.agents/skills/ahe-evidence-query/SKILL.md) |
| Collect external provider objects and prepare/apply exact source review | [.agents/skills/ahe-evidence-intake/SKILL.md](.agents/skills/ahe-evidence-intake/SKILL.md) |
| Review independent relations or evidence endpoints | `CLAUDE.md`, then the relevant section of the shared intake workflow |
| Review first lineage membership for an unchanged singleton claim | `CLAUDE.md`, then [initial registration](docs/INITIAL_LINEAGE_REGISTRATION.md) and the shared intake workflow |
| Design, run or report a controlled experiment | [Experiment protocol](.claude/skills/ahe-evidence-query/references/experiments.md) |

Discover the live MCP tools and input schemas before calling them. Standard
`relation-reviewer` and `endpoint-reviewer` capabilities are available only
through their separately provisioned profiles. Contradiction, Supersession and
initial lineage registration use their own separately provisioned exact-review
profiles. Generic derived admission and repository activation remain internal
capabilities; their query/readback tools do not authorize writes. Do not enable
a legacy profile or substitute direct SQL when a required operation is absent.

## Agent and Sub-Agent Authority

- Follow the shared [agent division of work](CLAUDE.md#agent-division-of-work).
  Unless the user explicitly changes it, subagents are text-only adversaries:
  they may challenge supplied claims, but may not use tools, research, execute,
  read/write files or data, or delegate further. The primary agent supplies the
  evidence and fixed scope, verifies every adopted challenge and owns execution
  and the final conclusion. Do not delegate operational work under another name.
- A task assignment defines scope; it is not a human decision to admit, reject
  or retain real evidence as `audit_only`. Approval of code changes, Git actions
  or research is separate from evidence admission approval.
- The primary agent collects source data, prepares proposals and reads back
  review material. Without the exact human decision it stops at the review boundary.
- An evidence decision must carry the exact proposal/attempt or relation
  coordinates, unchanged native review subject/display, outcome, human reason
  and the authorized reviewer binding required by the live workflow. A valid
  existing decision need not be requested again; changed subjects need new
  review. Do not invent missing fields or inject launcher-owned identity.
- Preserve exact inputs for uncertain-outcome retries. Do not infer a decision
  from silence, "continue", "finish", or approval of the overall intake task.
- Keep company data inside its authorized environment. Test approval stubs are
  permitted only under the shared experiment protocol, never as substitutes for
  a human decision in an operational evidence store.

## Environment

This repository and all its worktrees belong to AHE MCP. Their private `lab/`
folders contain verification artifacts. Record the exact checkout, revision and
test selection; follow the reporting rules in `CLAUDE.md` before comparing runs.

## Handoff

For intake/review work, return provider identity and revision; source and
extraction IDs; exact native review material with source excerpts, coverage,
limitations and available version differences; readback states; any missing
decision or capability; and whether canonical mutation occurred. Preserve
extraction omissions separately from source coverage.

For read-only evidence work, return the supported answer, source/record
references, relevant relation or receipt findings, and unresolved limits. An
answer or hypothesis is not a proposal submitted to AHE. Hidden sub-agent logs
must not be the only inspectable record. Keep credentials, DSNs and private
session text out of either handoff.

Follow `CLAUDE.md` for checks, artifact placement, Git
authorization and releases. Routine repository work does not authorize evidence
admission, and a test pass does not authorize a release.
