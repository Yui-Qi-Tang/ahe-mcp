# Query response detail and review conflict context

These contracts control material returned to a consumer. They do not change
admission authority, current-use policies, retained sources or execution rights.

## Assessment-only Query responses

`get_evidence_use_state`, `get_evidence_record` and `open_canonical_read_view`
accept optional `use_state_mode`:

| Value | Response |
|---|---|
| Omitted or `full` | Existing response, including the complete use-state snapshot when present. |
| `assessment_only` | Unchanged complete assessment, plus `evidence-use-summary/v1` context; the whole-store snapshot is omitted. |

For example:

```json
{"canonical_ids":["canon-node:example"],"policy":"current-action/v1","use_state_mode":"assessment_only"}
```

For `get_evidence_use_state`, the result is the summary itself. Record and open-view
responses contain `use_state_summary` instead of `use_state`; their existing record,
source/provenance and bounded graph artifact remain available. A pending occurrence
without a use projection has neither field. Null, empty and unknown explicit modes
are rejected. The mode is not a policy selector.

The summary retains every assessed root/AND-ancestor decision, reason, evaluation
time and the original `state_digest`. Its `snapshot_context` identifies the omitted
complete snapshot's version, store, cut and counts, and explicitly says:

- `snapshot_omitted: true`;
- `digest_scope: complete_snapshot`;
- `digest_recomputable_from_response: false`;
- `global_absence_inference_allowed: false`.

Use `full` when a machine consumer needs the complete snapshot to recompute the
digest. Do not feed a summary to a Snapshot evaluator or treat it as a pruned but
complete graph. The open-view cache/handle still retains the full native state;
changing transport mode does not redefine the handle's authority or graph scope.
Separate calls may observe different cuts.

Native evaluation still loads its complete bounded store and applies the same
policies. The 10,000-node loading limit remains; this option does not solve that
capacity limit or promise a fixed response size for arbitrarily large relevant
ancestry. Exact source/record content can itself be large. Before current use,
recheck all declared bases with `check_current_evidence_plan` under the same policy.

## Automatically delivered review candidates

`get_source_claim_review` (transport contract `source-claim-review-mcp/v2`),
`get_endpoint_review` and `get_historical_derivation_review` automatically return
outer `conflict_candidates` context. Source review scans its selected statement;
derived review scans that statement and statement origins in its complete bounded
ancestry. Repository-code review returns `status: not_applicable`.

The context is computed in the same read-only repeatable-read transaction as the
review. It identifies the store, governance revision and PostgreSQL read snapshot.
Every distinct target has its proposal/canonical coordinates, matching candidates
and coverage. Existing matching/order is retained: direct relations, slot/object
identity and lexical overlap. Each target scans at most 1,000 canonical claims and
selects at most 20 candidates; other pending proposals are not searched. Complete
ancestry limits remain in force, with at most 65 advisory targets permitted.

The complete encoded advisory is at most 128 KiB. To fit, whole lowest-ranked
candidates are removed from the most populated target first, with deterministic
tie handling. Candidate text is never shortened. Each target retains
`matched_claims` (inside its bounded scan), actual `returned_claims`, scan/return
truncation and `byte_budget_omitted`, and the effective `return_limit`; `max_targets` and
`max_bytes` state the total limits. `byte_budget_truncated` summarizes byte
omissions. Zero returned candidates with omissions is not an empty search.

Source review additionally budgets the advisory against its complete 1 MiB
response envelope. If even the full target metadata cannot fit alongside the exact
review, the getter fails; it does not trim the approval display or hide targets.
SQL, permissions, RLS and material errors likewise fail the getter, rather than
returning a successful empty candidate list.

### Advisory and exact approval are separate

The explicit binding is `advisory_not_bound_to_review_subject_or_receipt`, and the
observation is `current_read_snapshot`. Show the entire exact approval display
and the advisory with its limits. Candidates are not contradiction verdicts;
an empty bounded observation does not establish absence of conflict.

The existing display, subject, decision receipt and exact retry contract are
unchanged. Candidate changes alone do not invalidate approval. The decision
receipt does **not** prove which candidates the reviewer saw; retain the complete
getter response separately when that observation needs an audit record. Even a
saved response does not prove human reading or recognition.

On an endpoint/historical `exact_replay_only` read, the approval display remains
historical, while the outer advisory describes the new read cut. Do not attribute
today's candidates to the original approval. Writers do not scan candidates while
reconstructing or replaying the exact decision.

No tools, write privileges, migrations or role-policy grants are added. Relation,
supersession and contradiction getters do not yet bundle this context; use the
existing separately authorized intake suggestion flow for their statement
endpoints. Never enable a legacy profile to obtain missing capabilities.
