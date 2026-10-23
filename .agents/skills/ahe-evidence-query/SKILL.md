---
name: ahe-evidence-query
description: Use existing AHE evidence to answer questions, trace support, or inspect review and lifecycle state through the read-only Query MCP. Do not use for intake, admission, repository extraction, or news Brief generation.
---

# AHE Evidence Query for Codex Agents

Read [the shared query workflow](../../../.claude/skills/ahe-evidence-query/SKILL.md)
before using AHE evidence. It defines evidence lookup, relation traversal,
bounded absence, state interpretation and supported answers for every agent.

For blocked/excluded current-use results, follow the shared workflow's
[reason code reference](../../../docs/CURRENT_USE_GUIDANCE.md), including
preconditions and stop conditions; Query access does not authorize its writers.

Use the caller's authorized Query access and the live tool schemas. This skill
does not grant connector, writer or repository-intake access. Follow the shared
[agent division of work](../../../CLAUDE.md#agent-division-of-work): the primary
agent performs lookup and verification. Subagents only challenge supplied
material in text; they may not use tools, research, execute, read/write files
or data, or delegate further. The primary agent verifies any adopted challenge
and returns the evidence with its limitations within the user's source scope.
If the shared workflow is unavailable, report that mismatch rather than
substituting the intake skill or claiming unperformed AHE operations.
