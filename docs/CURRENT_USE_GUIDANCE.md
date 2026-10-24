# Current-use reasons and governed next steps

This reference explains `get_evidence_use_state` and
`check_current_evidence_plan` under `current-action/v1` and
`grounded-current-action/v1`, plus the limitations of
`get_canonical_supersession_currentness`. It describes existing contracts;
it adds no writer, policy exception or permission to change evidence.

## Read the result before choosing a flow

1. Keep the exact policy, evaluated time, node IDs, reason codes and
   `related_id` values. A reason can identify an ancestor, source or conflict
   edge rather than the requested root. Inspect all reasons and the related
   records; resolving one does not make the whole basis eligible.
2. `blocked` and `excluded` are assessment outcomes at that database cut,
   not promises about reversibility. For example, a withdrawal can be restored
   through review, while a superseded predecessor stays superseded. The table
   describes each cause; the final node outcome combines all causes and parents.
3. Query can diagnose and read back. A named writer requires its separately
   provisioned profile, an authorized task, complete native review and an exact
   explicit human decision with a reason. Discover the live schemas. If the
   capability, source material or decision is missing, report it and stop the
   dependent write/current-use path. Continue supported historical analysis.
4. Newly assigned revisions do not by themselves clear a current-use gate. Do
   not fabricate source identities, semantic slots, parents or positive
   assessments to pass a gate. Do not change policy to escape a failed
   requirement, enable legacy writers, edit canonical records or use direct SQL.
5. After an authorized change, reread the affected bases under the same policy.
   After planning/SAT, call `check_current_evidence_plan` with every action and
   its complete declared basis IDs. A pass concerns those bases at that time;
   it does not prove entailment, external freshness or execution permission.

## Temporal records and currentness are separate

`Temporal.Status` can represent `unknown`, `current`, `stale` and `superseded`.
Public admission writes `unknown`; no public writer sets the other three
values. The enum, validators and policy branches do not advertise such a writer.
Reviewed supersession computes a separate currentness projection from retained
lineage history without rewriting `Temporal.Status`. A derived claim's current
use instead depends on its complete AND ancestry and the remaining policy gates.

The consistency selector's `recorded_current`, `classified_current` and
`ambiguous_frontier` reasons belong to diagnostic selection, not to the
current-action assessment. In particular, inclusion in a consistency/SAT input
does not confer current-use eligibility.

## Fresh-v2 prerequisites and limits

For an external source claim, the governed replacement flow is
`get_supersession_review` → exact human approval →
`admit_reviewed_supersession`, through `supersession-reviewer`.
It requires all of the following:

- A fresh **pending** external source-claim occurrence grounded in an exact
  source snapshot with an externally assigned revision, and 1–64 older admitted
  external source-claim targets. A new revision alone does not justify replacement.
  An already ordinarily admitted replacement cannot be retrofitted by this flow.
- Matching old/new `source_system`, `source_namespace`, `object_type` and
  `object_id`, faithfully identifying the actual provider object. Do not split
  one real document into invented objects to avoid closure requirements.
- A stable, human-reviewed `slot_kind`/`slot_id` and the complete target set for
  that semantic replacement scope. Unrelated facts can belong to different
  lineages within the same source object; they must not be forced into one slot.
- The complete native display, unchanged review subject, explicit approval and
  human reason. Preserve the exact request for uncertain-outcome retries.
  A changed subject or CAS head needs fresh review and a new decision;
  `exact_replay_only` permits only the original retry.

This admits a replacement and enrolls its reviewed targets; it does not mark an
existing claim current in place. Independent initial registration is a separate
reviewed capability described below.
For closure, every admitted external source claim in the source-object manifest
must have a reviewed lineage assignment. Each assignment must be supported by
the actual replacement scope. An unclassified sibling can therefore block
closure even when the selected replacement itself succeeded. Withdrawal alone
does not remove a claim from this manifest or assign its lineage.

Use the returned lineage key to read currentness. Closure plus a unique frontier
can establish the replacement's snapshot-bound currentness; other source,
withdrawal, contradiction and ancestry gates still apply. Old targets become
superseded, not current. A withdrawn replacement does not revive its predecessor.

If no genuine eligible replacement exists, or same-object identity, semantic
scope, complete targets, review material or the authorized capability cannot be
established, stop that replacement path and report the unmet prerequisite.
There is no public "mark current", arbitrary membership assignment, cross-object
identity migration or duplicate-retirement operation in fresh-v2. Repository
code and derived endpoints are outside this replacement flow. See
[installation and stable slots](../INSTALL.md#supersession-reviewer).

## Unchanged singleton initial registration

When a repeated source occurrence resolves to the same canonical C, fresh-v2
rejects self-targeting. Schema 60 offers independently approved
`get_initial_lineage_registration_review` →
`apply_reviewed_initial_lineage_registration` through a separately provisioned
`initial-lineage-reviewer`. It requires exactly one admitted external source
claim in the source object, no prior membership, an unused stable lineage and a
persisted observation of C's original snapshot/view. No fake revision is needed.
This grants first membership, keeps the v2 head and derived parent IDs, and leaves
the repeated occurrence pending. It does not prove provider recontact/freshness.

Query returns legacy `projection` when the store-wide registration catalog is
empty; otherwise it returns versioned `joint_projection`. A complete joint
witness with one frontier can classify C, but source, withdrawal, contradiction
and AND-ancestry gates still apply. A subsequent genuine v2 replacement excludes
C without retargeting existing derived claims. See [the exact native contract,
review fields and limits](INITIAL_LINEAGE_REGISTRATION.md).

## Reason code reference

"Diagnose" below means inspect exact native records and authoritative coverage;
it does not authorize record repair. If the original record is structurally
inconsistent, report the coordinates to the maintainer and stop dependent use.
Do not set completeness flags yourself or assume another retry will repair data.

| Reason code | Meaning and disposition | Governed next step, prerequisites and stop condition |
| --- | --- | --- |
| `unclassified_currentness` | No recognized currentness for this record; blocked. | For an external source claim, use only a route whose prerequisites hold: [fresh-v2](#fresh-v2-prerequisites-and-limits) creates a replacement C2; reassess C2, while the old target remains superseded. [Initial registration](#unchanged-singleton-initial-registration) retains the singleton unchanged C; reassess that same C, with no replacement node. Neither route guarantees eligibility without the complete policy checks. Without an eligible reviewed route, stop current use. For other kinds, diagnose the record; do not relabel it to fit these flows. |
| `incomplete_currentness` | Currentness coverage is incomplete; blocked. | Read the observed lineage with `get_canonical_supersession_currentness`. If its limitation identifies unclassified source-object claims, inspect each and use fresh-v2 only for justified replacements in their own reviewed scopes. Stop if any required assignment cannot be established. Never assert closure from a partial view. |
| `unresolved_lineage_currentness` | A lineage exists but lacks a current status or usable witness; blocked. | Inspect closure, frontier and witness. An ambiguous frontier may be resolved by a genuine reviewed fresh replacement of its complete semantic target set, subject to fresh-v2. Missing/inconsistent authority needs diagnosis. Closure alone is insufficient when multiple frontier nodes remain. |
| `currentness_without_lineage` | A non-unknown currentness status has no lineage coordinate; blocked. | Diagnose projection and retained authority. No public field setter repairs this condition; do not invent a lineage key or use temporal status as a workaround. |
| `superseded` | This predecessor is excluded by reviewed lineage history. | Stable for the old record under the current contract. Inspect the frontier and independently assess a supported replacement. No unsupersede operation exists here; withdrawing a replacement does not revive the old record. |
| `recorded_stale` | Stored temporal status excludes this record. | No public writer sets or clears this status. Diagnose the record's origin; use independently supported eligible evidence if available. Do not promise a mark-current repair. |
| `recorded_superseded` | Stored temporal status excludes this record, separately from lineage projection. | Same public-writer limitation as `recorded_stale`. Inspect retained provenance and lineage without treating this field as a public supersession command. |
| `withdrawn` | An explicit stop-use event excludes this claim. | Conditionally reversible: `get_withdrawal_review` → approved `apply_reviewed_withdrawal` with `operation=restore` in the reviewed request, using `withdrawal-reviewer`. Restore needs its own exact review and human reason; it clears only withdrawal. Reassess all other gates. |
| `unknown_withdrawal` | Withdrawal state or history is incomplete/unrecognized; blocked. | Diagnose `get_evidence_governance_history` for the exact target and the authoritative projection. Do not submit restore merely because the status is unknown. Stop until history is established. |
| `invalid_withdrawal_coordinate` | Withdrawal revision/event coordinates are inconsistent; blocked. | Diagnose retained events and their revision chain. No caller-supplied coordinate fix or blind restore is authorized. Stop dependent use. |
| `active_contradiction` | An admitted peer's explicit state makes a reviewed contradiction active; blocked. | Inspect the exact pair, scope and both endpoints. An independently justified reviewed withdrawal or eligible supersession may change endpoint state; do not choose a false endpoint or withdraw one just to pass. Separately, when actual assessment time reaches a peer's valid, already recorded `Temporal.ValidTo`, that peer becomes excluded: this reason may disappear without a writer, while the recorded pair remains. Future expiry is not present clearance; do not alter clocks or time bounds. After either a lifecycle change or natural expiry, reread `get_evidence_use_state` and rerun the full `check_current_evidence_plan` under the same policy with every action's complete basis IDs. Other reasons and all AND ancestors still apply; removing this reason alone does not establish eligibility. Stop dependent current use if the full gate rejects or is unavailable. There is no public pair deletion/correction flow here. |
| `unknown_conflict_peer` | A contradiction peer is missing, unconfirmed or explicitly blocked; blocked. | Follow the related edge, inspect the peer and resolve its actual reasons when supported. A blocked peer cannot make its own conflict disappear. Stop if peer authority remains unknown. |
| `ineligible_parent` | A required AND parent is blocked or excluded; that outcome propagates. | Follow `related_id` to the parent's reasons. Apply only the parent's supported flow, then reassess descendants. Do not drop parents or wrap them in another derivation. Immutable ancestry cannot be rewritten through this gate. |
| `source_check:retained-byte-integrity/v1` | Required or existing source findings fail or remain unresolved. | Inspect the exact snapshot/hash and source findings. `get_source_assessment_review` → approved `apply_reviewed_source_assessment` through `source-assessment-reviewer` can record supported findings/resolutions. A genuine pass is required where the policy demands it; absent proof means stop. Original bytes are not repaired by an assessment. |
| `source_check:provider-revision-correspondence/v1` | Source correspondence is missing, failed, conflicting or inconclusive under the policy. | This historical criterion name does not require provider-issued numbering. Use the same exact source-assessment flow with material bound to the originally declared external revision and snapshot. Today's content, matching revision text or a self-computed digest alone cannot establish source correspondence. If comparison material is unavailable, preserve the uncertainty and stop dependent use. |
| `source_check:declared-source-coverage/v1` | Source coverage findings do not satisfy the policy. | Review the exact snapshot and original coverage declaration through the source-assessment flow. Corrected annotations do not rewrite the declaration or count as positive checks. Resolve all applicable findings explicitly; if coverage cannot be established, stop or use independently captured/admitted suitable evidence. |
| `admission_not_confirmed` | Canonical admission is not confirmed; blocked. | Inspect `get_evidence_record` and its exact occurrence/receipt. A genuinely pending supported occurrence needs its own source or endpoint review and explicit admission decision. An inconsistent existing canonical record needs diagnosis, not re-admission. Admission alone does not resolve currentness. |
| `missing_node` | The requested or required canonical node is absent from the snapshot; blocked. | Verify the exact ID/store with `get_evidence_record`. Correct a caller ID only from observed authority; missing ancestry needs diagnosis. Stop when the required record cannot be established. |
| `unsupported_node_kind` | The basis is not a source claim or derived claim supported by this policy. | Use a supported admitted claim only if it actually supplies the needed evidence. Do not relabel raw evidence/code or fabricate a derived wrapper to bypass this restriction. |
| `missing_and_parents` | A derived claim has no AND parents; blocked. | Diagnose the immutable derivation and admission receipt. No public parent-edit operation repairs it. A separate supported derivation would require complete parents, current-use checks and exact endpoint review; do not treat that as repairing the old node. |
| `incomplete_parent_manifest` | The complete AND manifest is not established; blocked. | Diagnose native derivation/parent authority and loader completeness. A displayed path is not a complete manifest. Stop; do not truncate the required parents or set a completeness flag. |
| `missing_source_basis` | A source claim has no native source binding; blocked. | Diagnose its occurrence, extraction and source authority. No text-match relinking or public source-binding repair is offered. Independent capture/admission creates separate evidence, not a repair of the old claim. |
| `missing_source` | A referenced source is absent from the snapshot; blocked. | Inspect the exact source ID and native binding. Stop when source authority cannot be loaded; do not substitute another snapshot with similar text. |
| `incomplete_source_bindings` | Source-binding coverage is incomplete; blocked. | Diagnose exact extraction/source bindings and authoritative read completeness. Do not infer bindings from object IDs or text or mark a partial load complete. |
| `incomplete_source_assessments` | Complete source-assessment history is unavailable; blocked. | Inspect `get_evidence_governance_history` with `kind=source_assessment` and the exact snapshot. Establish full history before considering a new assessment; a new pass cannot hide missing history. |
| `incomplete_incident_contradictions` | Complete incident pairs, including peers outside the display, are unavailable; blocked. | Diagnose authoritative relation coverage. Widening a displayed graph is not itself a completeness proof. Stop until the full relevant authority is established. |
| `invalid_validity_window` | Recorded time bounds cannot be parsed or form an invalid interval; blocked. | Diagnose retained temporal data. No public temporal-field editor is exposed; do not change the clock or rewrite bounds to pass. |
| `not_yet_valid` | The assessment time precedes the recorded validity start; excluded now. | Reassess when the actual validity window starts, provided the record remains otherwise eligible. No writer is needed to advance real time; a future timestamp is not present-use permission. |
| `expired` | The recorded validity end has been reached; excluded. | Stable for that recorded interval. Use independently supported currently eligible evidence; there is no public expiry-extension setter. |

`source_check:*` can be blocked or excluded depending on the applicable findings.
Under `grounded-current-action/v1`, absent positive checks also block. Under
`current-action/v1`, complete absence of findings for a criterion is permitted,
but existing failed or unresolved findings are still evaluated. A later pass
does not automatically erase earlier findings; reviewed resolutions must name
the relevant earlier events and cover their criteria.

## Supersession limitations and unavailable results

| Returned limitation or state | Interpretation and next step |
| --- | --- |
| `one or more admitted external source_claim records for this source object have no reviewed lineage assignment` | The source-object manifest prevents closure. Inspect the unclassified claims and the fresh-v2 prerequisites above. Multi-claim assignment requires justified reviewed replacement; the singleton initial-registration route does not cover admitted siblings. If any claim cannot be classified through an eligible flow, report the gap and stop dependent current use. |
| `currentness is limited to this exact admitted PostgreSQL snapshot; it does not prove provider freshness or global truth` | An enduring scope boundary, not a defect the writer can remove. Obtain separately authorized provider observations when external freshness matters; never report lineage currentness as a freshness/truth check. |
| `closure_available=false` or an unknown/ambiguous frontier | Do not choose a current winner. Read the limitation, coverage and frontier; follow the applicable currentness row above. An otherwise valid graph may still lack closure or a unique frontier. |

RLS restrictions, exceeded bounds, malformed snapshots and cycles can return a
tool error rather than a reason-code assessment. No usable assessment means no
clearance. Report the actual error and affected scope; do not substitute an empty
complete result or keep retrying unchanged deterministic failures. An authorized
maintainer may diagnose access or loader problems; this guide grants no profile
changes or database repair authority.

Implementation references: [assessment policy](../internal/evidenceuse/policy.go),
[time/source state](../internal/evidenceuse/state.go),
[authoritative adapter](../internal/evidenceingestion/governance_projection.go),
[closure](../internal/evidencesupersession/closure.go) and
[governance review contracts](../internal/evidenceingestion/GOVERNANCE.md).
