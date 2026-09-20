---
name: ahe-evidence-query
description: Use existing AHE evidence to answer questions, trace support, or inspect review and lifecycle state through the read-only Query MCP. Do not use for intake, admission, repository extraction, or news Brief generation.
---

# AHE Evidence Query for Codex Agents

Read [the shared query workflow](../../../.claude/skills/ahe-evidence-query/SKILL.md)
before using AHE evidence. It defines evidence lookup, relation traversal,
bounded absence, state interpretation and supported answers for every agent.

Use the caller's authorized Query access and the live tool schemas. This skill
does not grant connector, writer or repository-intake access. When delegated,
return the evidence and its limitations within the assigned source scope.
If the shared workflow is unavailable, report that mismatch rather than
substituting the intake skill or claiming unperformed AHE operations.
