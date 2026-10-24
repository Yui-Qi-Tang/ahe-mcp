# Initial lineage registration

Development contract: schema 60, database role policy v11. This Product port
retains AHE Core's `core-initial-lineage-registration/v1` and joint closure
contracts. It adds a separately provisioned MCP capability; Core's migration
number and runtime profiles are not Product deployment instructions.

## When it applies

An unchanged externally assigned revision, statement and source refs can produce a new
pending occurrence that resolves to the same canonical claim C. C cannot replace
itself. An independently reviewed initial registration can assign C's first
lineage membership without assigning another revision or re-admitting C.

The complete admitted source-object manifest must contain exactly one external,
verbatim `source_claim`: C. C must be unclassified and not withdrawn, have no
incoming supersedes edge, and the six-field lineage must be unused. A persisted
intake receipt must identify the original snapshot and extraction view. The
receipt may be a repeated delivery: neither its ID nor database timestamp proves
provider recontact, monotonic observation order, freshness or truth.

This v1 does not register multiple admitted siblings, relocate a member, merge
provider identities, deregister a lineage or retire duplicate occurrences.
The repeated pending occurrence remains pending. Its disposition needs its own
explicit review if desired.

## MCP workflow

Provision `initial-lineage-reviewer` separately, using the standard launcher and
operator workflow in [INSTALL](../INSTALL.md#initial-lineage-reviewer). Its two
tools are:

- `get_initial_lineage_registration_review`: `claim_id`,
  `observation_receipt_id`, and `basis` containing `source_system`,
  `source_namespace`, `object_type`, `object_id`, `slot_kind`, `slot_id`.
- `apply_reviewed_initial_lineage_registration`: `request_id`, the returned
  `request` as `review`, returned `subject` as `expected_subject`,
  `decision="approved"`, and the human's `decision_reason`.

Set `observation_receipt_id` to the `request_id` supplied in the selected
successful `submit_external_source` call. That persisted intake receipt must
match C's original snapshot and extraction view.

Show the complete native review, including original admission, quotes and
source refs, observation, singleton manifest, stable slot, anchor, governance,
derived-use effects and limitations. Source admission approval does not approve
this independent membership decision. The launcher supplies reviewer identity;
it is not a caller argument. Preserve the exact apply request for uncertain
outcomes. A changed subject needs fresh review. A matching historical retry
returns `replayed=true`; it does not grant present eligibility.

## Event, anchor and membership

The append-only payload contains `contract_version`, `binding`,
`review_subject_id`, `request_id`, `reviewer`, `reason`. Its ID is
`initial-lineage-registration:v1:sha256:<digest>`. The exact binding records the
store, C, original admission/occurrence, snapshot/view, grounding and observation
hashes, six-field basis/key, singleton creation manifest and governance digest.
The subject is `initial-lineage-review:v1:sha256:<digest>` of the versioned binding.

`supersession_anchor` contains `chain_key="canonical-supersession/v1"`,
`revision`, `head_event_id`. Revision zero requires JSON null; a positive
revision requires the exact v2 prefix head. Registration does not increment that
head or create a v2 event. The next genuine replacement still uses v2 and targets
the registered C without bootstrapping it.

The table owns a separate unique `request_id` namespace. The same text in an
unrelated writer's namespace is not this decision. Within registration, exact
input replays once; changed input conflicts. Event, request, claim and lineage
uniqueness, composite foreign keys and deferred guards bind each membership to
its real authority. A member has exactly one origin: v2 admission, or independent
registration with null `first_admission_event_id` and `was_bootstrapped=false`.

## Joint currentness and derived evidence

One repeatable-read cut loads the complete v2 prefix, store-wide registration
catalog, typed memberships, supersedes edges and current source-object manifest.
Replay applies R anchored at zero, then each v2 event followed by registrations
anchored at that event. Creation-time singleton conditions establish historical
validity; current closure uses the current manifest, so a later unclassified
sibling blocks closure without invalidating R's exact retry.

Readers verify and replay global authority once within the native transaction,
then validate each lineage's materialization and current manifest against it.
Derived commit shares that transaction's verified snapshot between review and
the final assessment, while evaluating eligibility again at the current time.
Neither the frame nor an eligibility result is reused across transactions or
serialization retries. Pending fresh native v2 and reviewed v2 acquire locks in
the order `proposal → governance → supersession head`. Native v2 replay returns
before that fresh fence. Historical reviewed-v2 replay keeps its original
proposal lock and complete request, subject/reviewer bindings,
receipt and native-authority verification, but does not acquire the fresh
governance fence. References can hold that fence before its origin-proposal FK
lock, so adding it to historical replay would reverse the lock order. This is a
v2 replay boundary, not a rule removing locks from other governance APIs.
The native live writer remains available internally; no legacy MCP profile is enabled.

The joint witness binds store, lineage/basis, algorithm/semantics/coverage,
v2 head/history, registration/membership/edge/object manifests and frontier.
It is a deterministic proof binding to native authority, not a use permit.
An empty store-wide registration catalog preserves legacy `projection` output.
A nonempty catalog returns versioned `joint_projection`, including for old-only
lineages; callers must consume that field. Readiness, RLS and capacity failures
fail closed. Limits are 10,000 registrations and 16 MiB for the canonical catalog
array, 16 KiB per payload/binding, and 1 MiB per saved grounding.

C's canonical payload, temporal status, original admission and source refs are
unchanged. Existing derived claims retain their admissions and exact AND parents.
Their current-use assessment may change when C gains or loses eligibility;
all other parents, own-source findings, withdrawal and contradictions still
apply. A later C2 replacement does not retarget derived parents from C to C2.
Current derived admission and final plan acceptance recheck joint authority
under the governance fence. Re-read Query currentness and the complete use
policy after any change; `grounded-current-action/v1` still needs source assessments.

## Upgrade and acceptance

Migration 60 preserves migrations 1–59 and old v2 event IDs, payloads, receipts,
head revisions and membership origins. Existing roles need a policy-v11 upgrade
and verification using the same LOGINs. All profiles gain one table read;
supersession and relation reviewers also gain the shared governance lock. References
take it before endpoint FK locks; native and reviewed derived writes take it
before the derivation lock. Historical retries retain their original authority;
the v2 replay fence exception is described above. Only the new registration
profile gains INSERT on the registration table. It has no direct
node, membership, lineage, admission or v2 writer grants. Rollback refuses to
discard a nonempty registration catalog.

New pending supersession displays include typed target membership material.
Obtain a fresh review after upgrading; an approval of the old pending display
does not approve a changed subject. Committed old displays and receipts remain
available for their exact original retry.

Acceptance covers unchanged self-target rejection; head-zero registration and
exact retry; genuine v2 replacement without bootstrap; nonzero anchors; real
schema-59 receipts replayed after upgrade; immutable historical derived AND
chains; stale-review/concurrent-writer rejection; bounded role isolation;
joint Query/consistency output; tampered authority and capacity failures. Test
coverage includes complete derived review, commit, exact retry and use-state
readback at 2,000 registrations and the full 16 MiB catalog, plus both orders of
competing supersession/derived admission for the same pending occurrence. Test
approval stubs are restricted to synthetic disposable databases. Passing these
checks is not authorization to migrate an operational store or approve evidence.
