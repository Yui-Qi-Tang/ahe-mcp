# Changelog

## Unreleased — 2026-09-16

- Added separate MCP profiles for local Git/Go intake and exact-reviewed
  code/derived-specification endpoints, with receipts and Query readback.
- Added migration 49 and role policy v5. Endpoint and relation approval remain
  separate.

## Unreleased — 2026-09-15

- Added a separate exact-review MCP profile for bounded `implements` and
  `references` admission, with independent receipts and Query readback.
- Added native schema migrations 47–48 and isolated relation-reviewer role
  policy. Existing deployments require separate migration qualification.

## dev — 2026-09-12

- Added read-only evidence briefs with lexical query recovery, source citations
  and lifecycle information.
- Added separate exact source-claim review tools for `admit`, `reject` and
  `audit_only`, with explicit decisions and reasons.
- Removed machine-specific paths from public files and Git history; added a CI
  path check. Existing clones should be re-cloned; do not merge old history back.
