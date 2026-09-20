# Your first evidence workflow

This walkthrough uses one synthetic, manually authored policy:

> Refunds for overseas orders must be completed within 7 days.

It demonstrates how a candidate becomes reviewed evidence and how a consumer
traces its source. It does not demonstrate that a refund system implements the
policy. No model is required to copy this one sentence into a proposal.

## Before starting

Complete [installation](../INSTALL.md) in a dedicated non-production environment.
Use separate configured `intake`, `source-claim-reviewer` and Query launchers.
An ordinary answering agent receives only Query; the operator performs the
intake and review steps through their separately authorized clients.

In each client, discover `tools/list`. Expect 5 intake tools, 3 source-review
tools and 13 Query tools. Use the installed schemas if they differ from these
examples; stop on a missing required capability. An empty database is normal.
The examples below are `tools/call` **arguments**, not shell commands or full
JSON-RPC requests. Replace angle-bracket placeholders with returned values.

For a database-free contract demonstration instead, run from the repo root:

```sh
make evidence-boundary
```

That test uses domain functions and a SQL test double, not live MCP admission.

## 1. Preserve the synthetic source

Call `submit_text_source` through intake:

```json
{
  "request_id": "first-refund-source-1",
  "source_id": "mock:first-refund-policy",
  "source_version": "1",
  "raw_text": "Refunds for overseas orders must be completed within 7 days."
}
```

Save `source_snapshot_id` and `extraction_view_id`. These example IDs identify
this delivery; reuse them only for an exact retry. This is genuinely manual
synthetic input. Real connector objects use `submit_external_source` with their
observed provider identity, revision and coverage instead.

Call `get_extractor_input` with:

```json
{"extraction_view_id": "<returned extraction_view_id>"}
```

Check `rendered_text` and the returned `spans`. Preserve the actual `span_id`
covering the sentence. Do not invent IDs or rely on a quotation alone.

## 2. Create a pending candidate

Call `submit_extractor_output` through intake:

```json
{
  "request_id": "first-refund-output-1",
  "source_snapshot_id": "<returned source_snapshot_id>",
  "extraction_view_id": "<returned extraction_view_id>",
  "extractor_definition": {
    "name": "manual-synthetic-walkthrough",
    "version": "1",
    "config": {}
  },
  "extractor_output": {
    "proposals": [{
      "proposal_local_id": "claim-1",
      "statement_text": "Refunds for overseas orders must be completed within 7 days.",
      "evidence_refs": ["<returned span_id>"]
    }]
  }
}
```

Expect `status: pending` and `proposal_count: 1`. Save the returned
`extraction_attempt_id` and `proposal_occurrence_id`. Successful extraction is
not admission. For this single manual sentence, the producer label describes
manual copying, not an external agent or model run.

## 3. Review the exact claim

Through the separate reviewer, call `get_source_claim_review`:

```json
{
  "extraction_attempt_id": "<returned extraction_attempt_id>",
  "proposal_occurrence_id": "<returned proposal_occurrence_id>"
}
```

Show the human the complete returned native display and manifest, including the
exact quotation and source context. Save the returned `subject` object intact.
The human chooses approval, rejection, audit-only or leaving the claim pending,
and supplies the reason. This walkthrough does not supply that decision.

Only after explicit approval, call `admit_reviewed_source_claim` with the saved
`extraction_attempt_id`, the **whole returned subject object** as
`expected_subject`, `decision: approved`, and the actual human reason as
`decision_reason`. The launcher supplies reviewer identity; do not add it to
tool arguments. For reject/audit-only, use
`record_reviewed_source_claim_disposition` with its live schema. Leaving a
proposal pending makes no reviewer write.

On approval, expect `admission_outcome: admitted` and a nonempty `canonical_ref`.
Retain the receipt. These are expected contract fields, not an actual execution
transcript. No test approval stub is used in this manual walkthrough.

## 4. Read and trace through Query

Call `get_evidence_record` through Query:

```json
{"proposal_occurrence_id": "<returned proposal_occurrence_id>"}
```

Verify the admitted state, canonical reference, exact statement and source quote.
Then use `get_evidence_record` with `canonical_id` set to the returned canonical
reference to inspect the canonical record.

To inspect why this claim is source-backed, call `list_evidence_neighbors`:

```json
{
  "canonical_id": "<returned canonical_ref>",
  "direction": "incoming",
  "relation": "supports_claim",
  "limit": 10
}
```

Read the returned source endpoint and relation. Call `get_relation_provenance`
with its returned `canonical_edge_id` to inspect that edge's recorded basis.
`supports_claim` runs from raw evidence to the claim. It is not an
`implements` edge and does not establish runtime behavior.

This sequence is grounded in the repository's
[source-review integration fixture](../internal/mcpintegration/source_claim_review_integration_test.go).
The fixture uses synthetic approval and checks actual stdio/DB behavior; it is
separate from the human review steps above. Run it only after the
[disposable database prerequisites](../INSTALL.md#optional-database-and-process-tests):

```sh
go test -mod=readonly -tags=integration -count=1 -v \
  -run '^TestIntegrationSourceClaimReviewSubprocessExactAdmission$' \
  ./internal/mcpintegration
```

It requires `AHE_DBROLE_ACCEPTANCE_DATABASE_DSN`. A skip is not a pass.

## 5. Answer a question with bounded evidence

Question: **“What is the refund deadline? Does the implementation enforce it?”**

If the record ID is unknown, use `search_evidence_records` or
`get_grounded_evidence_brief` with a query such as `overseas refunds`, following
the live input schema. Read the exact matching record and its source context;
inspect relevant relations before making an implementation claim. Check scope
and truncation instead of interpreting an empty neighborhood as global absence.

An appropriate answer for this one-source walkthrough is:

> The supplied policy requires overseas-order refunds within 7 days
> [cite the returned record and source span]. It does not establish a deadline
> for other orders. The evidence inspected here does not establish whether
> the implementation enforces this requirement.

Replace the citation instruction with actual returned references. An admitted
policy does not justify “all orders within 7 days” or “the code passes.” A real
implementation claim requires separate code evidence and relevant review;
execution claims additionally require appropriate test evidence.

## If the workflow stops

| Observation | Next step |
| --- | --- |
| Required tool absent | Check the selected profile; request the missing authorized capability rather than enabling legacy writers. |
| `review_contract_conflict` before admission | Compare the saved subject/display with the exact attempt. If the review basis changed, obtain a fresh review and decision. |
| Write response lost | Retry the saved exact writer request with the same identity, subject, decision and reason. Do not assume failure or create a replacement decision. |
| `admission_replay_conflict` | Stop changing retry inputs; inspect the original request and outcome. A conflict does not mean nothing was admitted. |
| No query match | Check source/lifecycle scope and availability. Read a known returned record ID before diagnosing a broken connection. |

For startup/role errors, see [installation troubleshooting](../INSTALL.md#common-startup-failures).
For broader evidence questions, use the [query workflow](../.claude/skills/ahe-evidence-query/SKILL.md).
