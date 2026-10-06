# Evidence-boundary experiment

> For the historical six-case model results, see the [full evaluation](EVALUATION.md#historical-six-case-results).
> This page explains runnable domain fixtures. Their contract checks are not
> model accuracy scores. Earlier model pilots and delivery diagnostics are
> historical runs, not additional observations in the current comparison;
> their records remain in the private lab and Git history.

## Question and scope

Can a resolved citation be held constant while a change in the claim or its
review context makes an existing exact review invalid?

This executable example measures `reviewable-ingestion/v1` contract behavior.
It does not measure code parsing, semantic entailment, reviewer competence,
agent behavior, or production persistence. The public summary is in the
[README](../README.md#a-citation-is-not-an-approval).

## Reproduce

```sh
make evidence-boundary
# Optional machine-readable output; choose your own private output directory.
go test -mod=readonly -count=1 -json -run '^TestEvidenceBoundaryExperiment$' ./internal/evidenceingestion
```

The Go test fails on any unexpected result. Verify that all ten named subtests
ran with no skips; the parent test is not an eleventh case. `-count=1` disables
the test-result cache. Repeating deterministic cases is not a larger statistical
sample. Initial execution used base commit
`8230050f66a90815c23798f6ce0178b0ba24aa40` plus the accompanying experiment test,
Makefile target and documentation changes; record the checked-out revision and
local diff when rerunning. No production implementation was changed.

## Fixture and controls

The complete synthetic source is this UTF-8 sentence followed by one LF:

```text
Refunds for overseas orders must be completed within 7 days.
```

SHA-256: `23c587a776f043f89e137f7fa66037ebb9451696a3f7b53bad0d8b7f38ea4ef9`.
The [test](../internal/evidenceingestion/evidence_boundary_test.go) supplies a
synthetic provider envelope and deterministic extractor output. No connector or
model was invoked. The successful extraction coordinate is fixture-supplied.

The supported/unsupported pair changes the proposal sentence while retaining
the exact same source identity and resolved citation. Both proposals can obtain
a review package and both remain pending. This is a deliberate counterexample
to the claim that citation validity proves semantic correctness.

Four further paired cases keep the resolved references and raw content hash
unchanged, change one logical dimension (claim, revision, coverage record or
proposal lifecycle), and validate the old review against that changed context.
Each must return `review_contract_conflict`; the unchanged control must still
pass. Coverage is one record containing both its label and limitation list.
These changes are injected into domain inputs; they do not simulate successful
unauthorized database writes. They test mismatch rejection at the validation
boundary, not whether production can ever reach each mismatched state.

The remaining controls exercise an unknown span, exact review acceptance,
one-byte display drift, and source revision collision. The collision uses
production capture code over the existing SQL test double, first checks exact
replay, then changes source bytes with a new delivery ID but the same provider
revision. Snapshot/receipt counts must remain one, with zero proposals and
canonical nodes. No admission writer is invoked by this experiment.

## Interpretation

| Observation | Supported conclusion | Not established |
| --- | --- | --- |
| Same citation, different claims, both pending | Source reference validation and claim truth are separate | Automatic hallucination detection |
| Four context changes invalidate an old review | Review identity binds more than a quotation | Human actually read or understood the review |
| Display drift rejected | Exact display bytes are part of the binding | Correct rendering in a client |
| Same revision cannot replace bytes in the mock | Domain collision behavior matches its invariant | PostgreSQL rollback, role isolation or concurrency |

The ten cases were authored with knowledge of the implementation and existing
tests. They are a transparent contract demonstration, not a held-out evaluation
or a representative error distribution. The count is a conformance count with
no confidence interval. AHE may preserve an incorrectly approved claim; the
experiment establishes neither semantic truth nor better downstream decisions.

Relevant production functions are `materializeReviewableBatch`,
`BuildSourceClaimReviewPackage`, `ValidateExactSourceClaimReviewSubject`,
`BuildSourceClaimReviewDisplayArtifact`, `ValidateExactSourceClaimReviewDisplay`
and `captureExternalSource`. Existing PostgreSQL tests are separate opt-in
integration tests; consult [installation](../INSTALL.md#optional-database-and-process-tests).

## AND case: a path does not cover every prerequisite

The source requirement is fixed: **Webhook readiness requires BOTH
signature-verification evidence AND anti-replay evidence.** This experiment
separates ordinary reachability, completeness of the declared AND manifest,
representation of the original requirement, and actual runtime correctness.

```sh
# Four deterministic conditions; no database or model required:
make evidence-and-case
```

The [Go fixture](../internal/evidenceprojection/and_boundary_case_test.go) builds
digest-backed synthetic artifacts with fixed valid provenance/temporal metadata.
It first calls the generic `graph.FindPath` algorithm on a structural snapshot,
then calls the actual public `evidenceprojection.PrepareTopology` boundary.
The structural baseline deliberately bypasses artifact validation inside this
test; it is **not** a public AHE query over an invalid admitted view. Accepted
artifacts are additionally queried through the public prepared path API with an
explicit `derived_from` filter.

| Condition | Anti-replay node | Its edge to readiness | Declared parents | Signature → readiness path | AHE preparation |
| --- | --- | --- | --- | --- | --- |
| Complete | Present | Present | Both | Found | Accepted |
| Missing edge | Present | Absent | Both | Found | Rejected: missing parent edge |
| Missing parent | Absent | Absent | Both | Found | Rejected: undefined parent node |
| Underdeclared | Absent | Absent | Signature only | Found | Accepted |

All four share the same source requirement and signature path. Missing-edge
removes one edge from complete; missing-parent also removes that parent's node
and associated payload/integrity records; underdeclared additionally removes it
from the manifest. These are explicit artifact interventions, not four unrelated
examples or evidence of successful production corruption. Node labels in the
legend never establish node presence. No database, admission writer, extractor,
runtime execution, third-party parser or MCP transport is used.

The last condition is an intentional semantic counterexample: a manifest may be
internally valid while omitting a requirement stated in the source. AHE checks
the declared set; it does not recover missing requirements from prose. Even the
complete synthetic artifact does not prove a working or secure webhook.

### Bounded product queries

The artifact interventions above describe complete fixture inputs. A product
query has an additional boundary: root nodes, relation filters, traversal depth,
node/edge budgets and the selected snapshot. A parent absent from that result
may still exist in storage. Do not diagnose a stored structural defect solely
from an incomplete query scope.

The canonical reader refuses to materialize a derived node when its declared
parent or matching edge is outside the bounded view. Its error is scoped to that
view; a broader authorized query may succeed without any evidence mutation.
Other valid bounded views can be returned with truncation metadata. Neither
`truncated=false` nor a complete declared set establishes coverage beyond the
requested scope or correspondence with every original requirement.

The [read-layer tests](../internal/evidenceingestion/canonical_read_view_test.go)
use a SQL test double to distinguish persisted derivations from depth, node and
relation restrictions, including selected nodes whose connecting edge was not
traversed. They check that the same stored data remains readable with sufficient
scope. These checks exercise product assembly code, not a live database or a
model's interpretation. See the [AND scoring boundaries](EVALUATION.md#and-support-score-each-question-separately)
for separate structural, source-comparison and explanation measures.

## Stale-review case: the state changes after review

An exact citation and a previously valid review do not guarantee that a later
write is eligible. This case exercises the real PostgreSQL-backed
`AdmitReviewedSourceClaim` and `RecordReviewedSourceClaimDisposition` APIs:

1. Client A reads a native review while the proposal is pending.
2. A controlled intervening operation commits, or the pending state remains
   unchanged in the control.
3. Client A submits its cached exact review commitment.

The [integration fixture](../internal/evidenceingestion/stale_review_case_integration_test.go)
uses a fresh migrated schema for each condition in an explicitly selected
non-production PostgreSQL database. Its reviewer identities and decisions are
synthetic test stubs, not authenticated human approval. Source bytes and quoted
claims remain unchanged. The fixture observes the writer result, proposal state,
canonical reference, authority-table counts and persisted decision/review
records before and after the final request.

| State immediately before the final request | Expected request result | New authority records | Canonical claim present afterward |
| --- | --- | --- | --- |
| Still pending | New admission | Yes | Yes |
| Another reviewer rejected it | State conflict | No | No |
| Another reviewer marked it audit-only | State conflict | No | No |
| Another reviewer admitted it | Replay conflict | No | Yes, from the earlier admission |
| The identical request already succeeded | Exact replay | No | Yes, with the original identifiers |

The admitted-by-other and exact-replay conditions keep the decision reason the
same and vary reviewer identity. They distinguish an invalid attempt to claim
another reviewer's decision from an idempotent retry. A conflict does not imply
that no canonical claim exists. The exact source quote still matches in every
condition; this does not establish semantic truth or permission to admit it.

```sh
# Select a disposable non-production PostgreSQL database through DATABASE_DSN.
# Do not place its value in committed files or terminal output.
make evidence-stale-review-case
# Keep the full JSON test transcript and exported fixture outside the repository:
go test -mod=readonly -tags integration -count=1 -json \
  -run '^TestIntegrationEvidenceBoundaryStaleReviewCase$' \
  ./internal/evidenceingestion > /path/to/private/domain-test.jsonl
```

A passing transcript contains one `STALE_REVIEW_CASE_V1=` JSON export. Extract
it without accepting failed, skipped or incomplete test runs:

```sh
python3 -B - /path/to/private/domain-test.jsonl /path/to/private/fixture.json <<'PY'
import json, pathlib, sys
events = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines()]
assert events and events[-1]["Action"] == "pass"
assert not any(e["Action"] in ("fail", "skip") for e in events)
text = "".join(e.get("Output", "") for e in events)
exports = [line.split("STALE_REVIEW_CASE_V1=", 1)[1] for line in text.splitlines()
           if "STALE_REVIEW_CASE_V1=" in line]
assert len(exports) == 1
with pathlib.Path(sys.argv[2]).open("x") as output:
    json.dump(json.loads(exports[0]), output, ensure_ascii=False, indent=2)
PY
```

Each test schema is removed by the fixture's cleanup. Use a separately owned
disposable database for this experiment and stop/remove it after retaining the
receipts. The runner's five-condition check will reject an incomplete fixture.

## Independent implements case: admitted endpoints do not admit a relation

The third case asks whether two admitted endpoints can be mistaken for an
admitted `implements` assertion. It exercises the actual relation backend and
PostgreSQL writer, then reads the result through the public query server API.
The specification has a validated recursive AND ancestry; the implementation is
an admitted repository-backed Go code fact. The relation direction is explicitly
**specification → implementation**.

The [fixture](../internal/mcpintegration/implements_boundary_case_integration_test.go)
runs five conditions in separate migrated schemas:

| Condition | Simulated client action | Operation outcome | Relation after | New relation authority |
| --- | --- | --- | --- | --- |
| `endpoints_only` | No relation operation | `no_write` | No | No |
| `review_only` | Obtain native relation review | `review_only` | No | No |
| `missing_approval` | Submit valid review with empty decision | `approval_required` | No | No |
| `mismatched_review` | Approve with one changed display-ID coordinate | `review_conflict` | No | No |
| `approved_relation` | Approve the exact native review subject | `relation_admitted` | Yes | Yes |

Both endpoints remain admitted in every condition. Each pair starts without an
`implements` edge or relation receipt. The missing-approval variant changes only
the decision; the mismatch variant changes only one valid-format display-ID
coordinate. A successful write adds exactly one directed edge and one independent
relation admission receipt, with no new canonical node or node admission
decision. The public provenance must retain the exact reviewed display and
receipt. An additional deterministic retry verifies idempotent replay with the
same identifiers and no new rows; it is outside the five model conditions.

The fixture hashes every row of eleven explicitly listed tables: canonical
nodes and edges, admission decisions, implements and references admissions,
derivations and their parents, ordinary admission manifests and node/edge
bindings, and proposal occurrences. No-op, review-only and rejected submissions
must preserve these snapshots. Success must preserve the non-implements portion.
These are bounded snapshots, not hashes of the entire database. Public query
results are checked against complete SQL counts for the exact pair, relation
filter and direction. Reverse and ancestor-to-code queries must remain empty;
an admitted root relation is not inherited by its parents.

Setup computes native review and receipt objects in memory in all conditions,
without persisting the relation. `endpoints_only` means the simulated client has
not obtained that relation review or called the writer. It does not claim that
the fixture setup never constructed an in-memory review.

### Reproduce the implements boundary

Use a newly owned, disposable PostgreSQL database. The test operator needs
schema/role creation authority; actual relation and query calls use separately
restricted roles. The database must meet the role-policy preflight: `PUBLIC`
must not retain database `TEMPORARY` or access to the `public` schema. Configure
those privileges only on the disposable database. Do not alter a shared or
production database to run this experiment.

```sh
# Configure AHE_DBROLE_ACCEPTANCE_DATABASE_DSN externally for the disposable DB.
# Do not print or commit its value.
make evidence-implements-case
# Retain the JSON test transcript outside the repository:
go test -mod=readonly -tags integration -count=1 -json \
  -run '^TestIntegrationEvidenceBoundaryImplementsCase$' \
  ./internal/mcpintegration > /path/to/private/domain-test.jsonl
```

The test fails if its database configuration is absent; a skipped integration
test cannot count as success. Extract the single `IMPLEMENTS_BOUNDARY_CASE_V1=`
JSON object only from a completed transcript with no failure or skip:

```sh
python3 -B - /path/to/private/domain-test.jsonl /path/to/private/fixture.json <<'PYFIXTURE'
import json, pathlib, sys
events = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines()]
assert events and events[-1]["Action"] == "pass"
assert not any(e["Action"] in ("fail", "skip") for e in events)
text = "".join(e.get("Output", "") for e in events)
exports = [line.split("IMPLEMENTS_BOUNDARY_CASE_V1=", 1)[1] for line in text.splitlines()
           if "IMPLEMENTS_BOUNDARY_CASE_V1=" in line]
assert len(exports) == 1
with pathlib.Path(sys.argv[2]).open("x") as output:
    json.dump(json.loads(exports[0]), output, ensure_ascii=False, indent=2)
PYFIXTURE
```

The fixture removes its schemas and roles. Stop/remove the separately owned
cluster after saving the receipts.

## Model evaluation and reproduction

The [current evaluation guide](EVALUATION.md) maps the README metrics to their
inputs and explains what can be reproduced from the public checkout. Running
these fixtures checks AHE contracts; it does not regenerate the six-case model
tables. Historical Python pilot runners remain available in `scripts/`, but
must not be labeled as the current six-case runner.
