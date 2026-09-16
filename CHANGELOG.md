# Changelog

## Unreleased — 2026-09-16

- Added separate MCP profiles for local Git/Go intake and exact-reviewed
  code/derived-specification endpoints, with receipts and Query readback.
- Added migration 49 and role policy v5. Endpoint and relation approval remain
  separate; Desktop stays frozen.

## Unreleased — 2026-09-15

- Added a separate exact-review MCP profile for bounded `implements` and
  `references` admission, with independent receipts and Query readback.
- Added native schema migrations 47–48 and isolated relation-reviewer role
  policy. Existing deployments require separate migration qualification.

## Unreleased — 2026-09-14

- Froze Detective Desktop as unavailable; retained implementation
  and historical results. Returned development priority to CLI stabilization.
- Fixed premature OAuth callback connection closure with bounded response draining.

Entries below describe earlier checkpoints, not current Desktop availability.

## dev — 2026-09-12

- Included Detective CLI and macOS Desktop `0.1.0-preview.18` in the MCP
  repository, sharing one Go `1.27.0` module.
- Added read-only evidence briefs with lexical query recovery, source citations
  and lifecycle information.
- Added separate exact source-claim review tools for `admit`, `reject` and
  `audit_only`, with explicit decisions and reasons.
- Improved saved-work recovery and exact decision-receipt retries in Desktop.
- Fixed Brief drafts disappearing on page changes or being reused for a
  different source.
- Updated MCP/Desktop source installation, protected launchers and clean-build
  verification.
- Removed machine-specific paths from public files and Git history; added a CI
  path check. Existing clones should be re-cloned; do not merge old history back.
