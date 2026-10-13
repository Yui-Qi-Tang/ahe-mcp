# Evidence governance — Issue #18, Batches 1 and 2

Schema 58 and role policy v9 add native append-only stop-use and source review.
These contracts preserve original evidence, admission, source bytes, relations,
AND parents and historical receipts. Code approval does not authorize a real
review decision or an operational migration.

## Separate semantics

- `withdraw` prevents new current use of one admitted source/derived claim;
  `restore` clears that withdrawal alone. Neither decides truth. Optional
  independently admitted correction IDs are navigation, not replacement edges.
- An explicit reviewed contradiction pair blocks current use while the other
  endpoint is explicitly current and not withdrawn. A blocked peer cannot make
  its own conflict disappear. Components do not imply additional pairs.
- Supersession keeps the existing same-object, six-field lineage and verbatim
  fresh-v2 contract. A withdrawn replacement does not revive its predecessor.
- Source assessments bind one exact snapshot/hash and original declarations.
  Retained-byte integrity, provider-revision correspondence, declared coverage
  and statement support are separate findings. A statement-support finding does
  not establish provenance or automatically withdraw every bound claim.
  Missing original provider revision makes historical comparison inconclusive.
  Comparison snapshots and their retained bytes are shown in the native display.
  Each finding names its assessed field. An optional corrected fidelity/coverage
  value annotates a failed declaration; it never changes the original envelope
  or supplies a positive check. Schema acceptance remains separate from these
  human-reviewed findings. Comparison material identities remain visible to the human reviewer; matching
  revision text alone does not prove correspondence.
  Re-grounding requires independent capture/admission; no text-match relinking.

## Native review and authority

Provision `withdrawal-reviewer` and `source-assessment-reviewer` separately.
Each profile can SELECT the verified store and INSERT only its own event table;
neither can mutate sources, canonical nodes, admission, parents or the other
profile's events. The launcher supplies the reviewer ID. As for existing native
writers, these are trusted Go review routes, not DB authentication of a human.

Use `get_withdrawal_review` / `get_source_assessment_review`, display the entire
unchanged subject, then submit the returned request to the corresponding
`apply_reviewed_*` with an explicit approval, human reason and stable request ID.
The display includes the non-secret store identity, exact target, retained
source material, prior event chain, expected revision/previous event and effect.
Old condensed/mislabeled sources remain withdrawable without a new verbatim
intake precondition. A changed subject or per-target CAS requires new review.

Review preparation reserves space for the complete serialized event, including
JSON escaping and the maximum legal decision fields, within the DB's 1 MiB limit.
An event stores the exact serialized display, subject, decision/reviewer/reason,
request identity and predecessor. Row updates, deletion and truncation are
forbidden. Exact retries return the original receipt even after later governance
changes; changed arguments cannot reuse a request. Source resolutions explicitly
name earlier events on the same snapshot and reassess every criterion in each
referenced event; an unrelated pass cannot erase an omitted failing dimension. Unresolved conflicting findings remain
visible; a later timestamp or pass does not silently erase an earlier failure.

## Query, cache and consumers

`get_evidence_record` (canonical ID or an admitted proposal occurrence), `open_canonical_read_view` and
`get_evidence_use_state` expose `evidence-use-projection/v1` from one unfiltered
repeatable-read cut. Admission, withdrawal, source findings, currentness, time,
actual incident pairs and recursive AND dependencies remain separate. Original
external declarations are labeled as declarations. All claims actually bound to
an assessed source inherit the assessment, including statements that happen to
be correct. No object-ID-wide heuristic is used.

The first adapter loads a complete store with a 10,000-node, 100,000-edge/parent
bound and 10,000-event per-target bound. This is deliberately conservative:
RLS, missing authority, cycles or exceeded limits yield an error/unknown, never
an empty complete result. Display depth does not bound governance ancestry or
incident conflict peers. Large stores need a future equally complete scoped
loader; do not raise a completeness flag on a truncated query.

New read-view handles include the governance digest, so event-only changes
produce new handles while existing cached views remain immutable historical
reads. A cached view, prior receipt, verification report or SAT result is not a
new-use permit.

Select an explicit versioned purpose in `get_evidence_use_state` and
`check_current_evidence_plan`: `current-action/v1` or
`grounded-current-action/v1`. The grounded purpose additionally requires passing
retained-byte integrity, source correspondence and coverage checks; missing, disputed
or inconclusive mandatory checks block use. Neither an omitted policy nor an
unknown/historical policy is a default clearance. After generating a plan/SAT
result, call `check_current_evidence_plan` with the same purpose, every action and
its explicit canonical basis IDs. The server rereads state and evaluates all AND
ancestors; one failed premise cannot be hidden inside a derived wrapper.
`accepted=false` is a governance rejection, not UNSAT. A pass does not establish
semantic entailment or detect omitted premises. Existing consistency/SAT
profiles retain diagnostic semantics and must not stand in for this action gate.

New public `derived_spec` requests require `derivation.use_policy`; the exact
review binds that selected policy and scoped state. Omission is retained only
for replay of historical terminal requests that predate this field.
The public `derived_spec` endpoint review includes the scoped use policy/state,
and `AdmitReviewedEndpoint` rechecks both parents and its own bound source inside
the committing transaction. Unclassified source currentness blocks new current
admission. Internal legacy historical materialization remains internal and
confers no current-use authority; public runtimes do not enable that route.
A caller cannot downgrade the public action/derived gate with a historical flag.

Every relevant canonical, admission, supersession, source and extraction binding
write first updates the same store head via statement triggers. Current derived
admission obtains this lock before reloading native review and retains it through
commit. Thus phantom edges, new ordinary claims and source findings serialize
with the check. A stale RR snapshot fails/retries rather than using stale state.
The review subject covers scoped state, not unrelated global revisions.

The guarantee concerns governance data through this transaction and eligibility
at the recorded assessment time. Wall-clock expiry can advance independently;
there is no lease for future execution. External actions after commit require a
separate coordination contract. Historical replay performs no new admission.

## Interim records and batch migration

`InterimGovernanceRecord` (`interim-evidence-governance/v1`) separates exclusion,
source assessment and verification records. Keep it append-only by record ID,
revision and predecessor hash. Include store/claim/hash/occurrence or exact source
coordinates, purpose/scope/reason/outcome, original versus actually checked
materials, historical human reviewer/time, limitations and explicit missing
fields. Checker name/version/time are required when a checker was used. A purely
human exclusion needs no invented checker, check run, criterion or freshness
rule. Source findings and verification records require the actual checked
materials, criterion/version and check time; verification also needs its
freshness rule. Genuinely unavailable history remains an explicit missing field
and blocks migration. Keep the original local record.

`prepare_*_batch` accepts `items: [{record, reason}]`, keeping each original
historical reason separate from the proposed reason for this new decision. It
is read-only: it checks coordinates, hashes, gaps and duplicates, and returns
an `evidence-governance-batch/v1` manifest, its subject and native reviews.
The manifest binds the exact ordered item set (including blocked/verification
records), original records/fingerprints, proposed reasons, native operations,
subjects and stable request IDs. Show it together with the unchanged native
displays. Manifests are limited to 50 items and 256 KiB; split larger review sets.
Local historical approval does not approve this display or impersonate the new
launcher reviewer. Verification-only reports cause no governance write.

`apply_reviewed_*_batch` requires the unchanged `manifest`,
`expected_manifest_subject` and only the explicitly approved `approved_items`.
Copy the returned manifest without rewriting its historical JSON. Its input
schema accepts the serialized history's null optional arrays and exact zero
native request for an omitted operation. This preserves existing record hashes
and batch subjects; it does not authorize a native operation. Executable native
requests and approved decisions retain the strict profile schema. Blocked and
verification-only records stay in the manifest but cannot be applied.
Changing the set, material, native subject or proposed reason requires new
review. Each item has
its own transaction and reports applied/replayed/conflict/failed (or committed
with readback failure). Receipt bodies retain the local record/revision/hash
reference plus the batch subject; retain the original manifest for audit and
Query reads back each history. Retry uncertain results with exactly
the same inputs. The original manifest can replay committed items or apply an
unaffected item whose native subject is still unchanged. A stale item must be
prepared/reviewed anew, independently of other successful items.
No batch atomicity or implicit approval is assumed. Keep local
enforcement until native readback and the actual current-use consumer both pass.

`evidenceuse.ReuseVerification` requires the same claim/hash, criterion/version,
scope and complete material set, plus a live consumer-owned revision/content
recheck on every prospective hit. Stored hashes alone never hit. A semantic
cache hit cannot clear withdrawal/source findings or skip the final action gate.
No new provider-fetch authority is implied by a cache record.

## Acceptance evidence

The synthetic integration suite covers append-only history, CAS/replay, scoped
source resolution, AND propagation, missing visibility, permission boundaries,
stale RR rejection, batch manifest tampering, mixed per-item outcomes and actual
Query/MCP/derived consumers. Real concurrent DB sessions test both
commit orders for withdrawal, ancestor/own-source correction, contradiction,
supersession and new classification/source binding. Tests observe actual lock
blockers and verify either admission rejection or subsequent loss of current use.
Both governance profiles validate minimal prepare inputs and the actual returned
mixed manifest against their published MCP schemas before an unchanged apply
and exact replay. Fixtures include human-only exclusion, source findings,
verification-only reports and blocked records; Query confirms only approved
native events were appended.
Migration/readiness checks verify exact function
bodies, triggers, columns and constraints. Rollback refuses populated governance
history. Private run records under `lab/issue-18` identify the checkout, test
selection and limitations. Batches 3 and 4 are separate follow-ups.
