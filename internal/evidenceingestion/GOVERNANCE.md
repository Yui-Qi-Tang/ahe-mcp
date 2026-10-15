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

For returned reason codes, conditional review flows and stop conditions, use
the [current-use guidance](../../docs/CURRENT_USE_GUIDANCE.md). A reason code
does not authorize a write or promise that a supported repair exists.

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

For use as a current action basis, select an explicit versioned purpose in
`get_evidence_use_state` and `check_current_evidence_plan`: `current-action/v1` or
`grounded-current-action/v1`. The grounded purpose additionally requires passing
retained-byte integrity, source correspondence and coverage checks; missing, disputed
or inconclusive mandatory checks block use. Neither an omitted policy nor an
unknown/historical policy is a default clearance. After generating a plan/SAT
result, call `check_current_evidence_plan` with the same purpose, every action and
its explicit canonical basis IDs. The server rereads state and evaluates all AND
ancestors; one failed premise cannot be hidden inside a derived wrapper.
`accepted=false` is a governance rejection, not UNSAT. `accepted=true` means
the declared basis IDs pass the selected policy at the assessed state and time.
The action manifest contains action IDs and basis IDs, not executable action
content. A pass does not validate the action, establish semantic entailment or
detect omitted premises. Existing consistency/SAT profiles retain diagnostic
semantics and must not stand in for this action gate.

New public `derived_spec` requests require `derivation.use_policy`; the exact
review binds that selected policy and the scoped governance material in its
native display. Omission is retained only for replay of historical terminal
requests that predate this field.
The public `derived_spec` endpoint review includes the scoped use policy/state,
and `AdmitReviewedEndpoint` rechecks both parents and its own bound source inside
the committing transaction. Unclassified source currentness blocks new current
admission. Internal legacy historical materialization remains internal and
confers no current-use authority; public runtimes do not enable that route.
A caller cannot downgrade the public action/derived gate with a historical flag.
This gate limits the public entry point to current-use derived admission; it
does not redefine every admitted canonical record as currently usable. Human
review must still judge the derived claim's support, scope and conditions.
The gate is an additional governance check, not an entailment proof.

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

### Consumer responsibilities

AHE owns the recorded governance state, its policy evaluation and the guard on
its own committing transaction. Consumers use those authoritative assessments;
they must not turn unknown state into currentness or replace the guard with a
prior local check. Consumers own the mapping from evidence to actual actions:

- Select the policy required by the intended use and supply every action's
  complete basis. Omitting a premise or changing the purpose to obtain a pass
  does not make the action supported.
- Check that the evidence supports the action under its actual scope and
  conditions. Review of the canonical derived claim remains a separate
  obligation before admission; action review cannot repair an unsupported claim.
- Recheck required external revisions/content and current governance before
  current use. Coordinate subsequent external execution, including intervening
  changes and expiry; AHE supplies no execution lease or cross-system atomicity.
- Keep historical cache reuse separate from current-use clearance. An unchanged
  verification report can remain valid for its original inputs while current
  eligibility changes. Neither a cache hit, SAT result nor admission receipt
  replaces the current basis check.

For example, `D = AND(A, B)` may be admitted while its parents are eligible.
Withdrawing A later preserves D's admission and receipt, but blocks D's current
use through its ancestry. An admitted-only consumer would still be wrong to
execute using D. Verify each consumer's actual read and execution path; passing
the admission guard or the AHE integration suite does not establish that an
external consumer enforces this boundary. Historical inspection and consistency
diagnosis remain distinct uses; they do not authorize a new current-use write.

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
selection and limitations. The implemented Batch 3 and 4 contracts are described
below.

## Independent contradiction review (Batch 3)

`contradiction-review/v1` binds the exact two admitted records, their available
retained sources (including source-backed AND ancestry), optional grounded
rationale occurrence, rationale, declared scope/conditions, producer, symmetric
`contradicts` effect, store ID and governance revision. No semantic verdict is
inferred. A bounded display that cannot fit its receipt is refused, not truncated.
Getters are read-only: an abandoned preview does not consume a pair. Applying an
explicit independent decision creates the existing pair-v1 proposal and terminal
decision atomically with `reviewed-contradiction/v1` receipt, and with one edge on
approval only. Existing pending pairs require identical legacy payload; native
scope/conditions are additional exact review material, not a new pair identity.

Only `contradiction-reviewer` exposes this path. Its role can INSERT the immutable
receipt command, with no direct canonical/proposal/decision DML. The fixed SQL
trigger restricts effects to that pair. A deferred decision-to-receipt FK makes
receipt collision/ignore or later transaction failure roll back all effects.
Repeatable-read reconstruction takes the shared governance lock before lookup;
exact request replay precedes current-state checks. Changed inputs/reviewer/reason
or terminal re-decisions conflict. A global revision change conservatively stales
an uncommitted review, including unrelated changes; it does not invalidate the
historical receipt. Two concurrent exact retries preserve one original decision.

Query exposes native receipt/display through the existing contradiction proposal
and relation provenance records. Endpoints remain admitted; this relation alone
neither decides which endpoint is false nor changes withdrawal/supersession.
Current-use consumers still apply Batch 2's conflict policy. Component membership
never manufactures pairwise edges. Legacy terminal pairs remain terminal; review
reopening and erroneous-relation withdrawal need a separate lifecycle contract.

## Bounded intake conflict suggestions (Batch 4)

`suggest_intake_conflicts` accepts one successful persisted statement occurrence,
`limit` (default 20, maximum 100), `scan_limit` (default 250, maximum 1000), and an
optional six-field `basis`. The output contract is
`intake-conflict-suggestions/v1`. It scans source/derived claims in canonical-ID
order at one read-only repeatable-read cut. It reports scanned, matched and
returned counts, scan/result truncation, relation and slot bounds, lexical
truncation, available object/anchor/slot basis and store/revision.

Candidates include exact claim/proposal/source coordinates, original statement,
external identity and each matching basis. Object matching requires all four
non-placeholder provider coordinates; unrelated `unknown` values do not coalesce.
Slot matches use persisted lineage membership or a clearly labelled caller hint
whose object matches the proposal's source; a hint does not assign that proposal
to a lineage. Existing-relation matches show only actual direct
contradicts/supersedes edges of an admitted input, including direction and
provenance. Pending inputs have no relation anchor. No transitive clique is built.

Lexical matching uses lowercase words and Han bigrams, at most 256 distinct terms
per statement and at least two shared terms. Ranking prioritizes direct relations,
slot, object, then shared terms; it is not truth confidence. A bounded prefix can
miss relevant later claims, other records, omitted extraction and external
revisions. RLS/inaccessible query failure is an error rather than an empty result.
No suggestions is never proof of no conflict. Suggestions do not create any
proposal, relation, admission, withdrawal, source assessment or supersession;
an independent exact human review remains necessary for every relation decision.
