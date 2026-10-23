# Changelog

## Unreleased — current baseline

The current development build requires schema **60** and role policy **v11**.
This summary covers implemented changes since the dated entries below; it is
not a release announcement.

- Added migrations 50–52 for external proposition identity, traceable binding
  corrections/withdrawals/restorations, and immutable external checks and
  representations. These records do not admit evidence or prove semantic
  equivalence.
- Added migrations 53–54 for consistency watches, diagnostic history, proof
  artifacts and record-integrity guards. They brought Query to 26 read-only tools;
  the separate `core-records` profile exposes six record/configuration tools.
- Added the explicitly started `ahe-consistency-worker`: external Boolean
  declarations, currentness policy, checked SAT/UNSAT, conflict localization
  and recomputation with durable polling events. It does not extract meaning
  or repair canonical evidence. See [consistency operation](docs/CONSISTENCY.md).
- Added the [experimental v0 logic resolver](logicresolver/README.md), with
  CaDiCaL/DRAT and a bounded linear Z3 adapter. The Product consistency path
  uses SAT; Z3 UNSAT results have no independently checked proof.
- Added migration 55 for explicitly typed candidate hypotheses and optional
  contradiction-source bindings while preserving prior admission history.
- Added migration 56 / policy v8 for reviewed supersession and endpoint
  disposition, then migration 57 for up to 32 reviewed AND parents while
  retaining historical receipts.
- Added migration 58 / policy v9 for withdrawal, source assessments and
  current-use guards. Its three Query tools bring the current inventory to 29.
- Added migration 59 / policy v10 for exact-reviewed contradiction decisions
  and read-only intake conflict suggestions; intake now exposes six tools.
- Added migration 60 / policy v11 for independently reviewed initial lineage
  registration of an unchanged singleton external claim and joint closure
  proofs. Existing claims, admissions, v2 receipts and derived parents remain
  unchanged. See [registration and limits](docs/INITIAL_LINEAGE_REGISTRATION.md).
- Aligned native/reviewed v2, references and derived writer lock order. Pending
  fresh reviewed v2 acquires governance; its historical replay keeps complete
  verification without that fresh fence. Joint authority and snapshot data are
  reused only within their owning transaction, with current-use checks retained.
- Added runtime-role upgrades that retain the existing group/LOGIN pair.
  Existing installations require the documented migration and role checks;
  see [upgrade instructions](INSTALL.md#upgrade).
- Updated practical multisurface retrieval to plan v2. Its public query-mode
  name remains `practical_multisurface_lexical_v1`, paired with response schema
  `grounded-evidence-brief-v7`; default queries remain unchanged.

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
