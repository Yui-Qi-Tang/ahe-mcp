# Evidence-boundary experiment

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

## Single-case local-model pilot

This pilot adds one authored synthetic case, not a held-out benchmark. The
[paired Go test](../internal/evidenceingestion/evidence_boundary_case_test.go)
prepares two valid source-claim packages: a day-7 rule and a day-30 rule. The
unchanged control reuses the original package; the changed condition presents
the old review against the new proposal. Both proposals remain pending, with
no canonical reference. "Reviewed" in the fixture means the previously prepared
review package, not an actual human approval. No database or admission writer
is involved. The policy review does not authorize a code deployment.

The example code is:

```go
func Eligible(ageDays int) bool { return ageDays <= 7 }
func HandleRefund(ageDays int) bool { return Eligible(ageDays) }
```

Changing the constant to 30 changes behavior but preserves the direct call
edge. The test computes both graphs using `go/parser` and `go/ast`; it does not
mock a third-party code-graph product. It also calls AHE's existing
`ValidateExactSourceClaimReviewSubject`: the unchanged pair passes, reusing the
old review against the new proposal conflicts, and the new proposal's own exact
review passes. This last control rules out an invalid replacement fixture.

### Frozen generation protocol

The [Python runner](../scripts/evidence_boundary_case.py) requires Python 3.9+
and a running loopback Ollama server with an already installed model. It does
not download models. Run from the repository root, choosing a new private
output directory outside this repository:

```sh
# Domain case only, without a model:
go test -mod=readonly -count=1 -run '^TestEvidenceBoundaryRefundCase$' -v ./internal/evidenceingestion
# Optional eight-request local-model pilot:
python3 -B scripts/evidence_boundary_case.py --model gemma4:12b --output /path/to/private/refund-case-run
```

Initial model: `gemma4:12b`, digest
`4eb23ef187e2c5462566d6a1d3bbbc2f1346d0b4327cbb66d58fffbcc9b2b05c`;
Ollama `0.34.1`, Go `1.27.1`, `darwin/arm64`, 2026-09-18. Temperature 0,
seed 42, context 16384, generation limit 1024, HTTP timeout 180 seconds per
request, fresh generation context per request. The same prompt and response
schema are used throughout. Eight requests were fixed before generation,
ordered with shuffle seed 918, with no retries. A fixed seed does not guarantee
byte-identical output across different runtimes or hardware.

The runner saves the fixture, source/runner hashes, model digest, all eight
requests, oracle, execution order and request hashes before its first model
call. It retains raw responses, explanations, token counts, errors, per-case
scores and a report. Runtime timings are diagnostic only: model loading and
other local verification work were not controlled for a latency comparison.

| Arm | Supplied context |
| --- | --- |
| A | Code before/proposed, both source claims, revisions, exact quotes, source hashes, pending states and exact review coordinates |
| B | A plus the actual parsed direct call graphs |
| C | B plus the same provenance facts repeated as structured JSON; no new approval information |
| D | C plus the observed AHE domain validation result |

This tests fixed evidence presentation, not live tool selection, retrieval,
PostgreSQL persistence or MCP transport. C adds repetition as well as structure;
it is not a pure formatting intervention. The oracle was authored before model
generation, but was not independently adjudicated. Exact schema fields are
scored separately from explanations; mismatches are disclosed, not repaired.

### Observed results and limits

All eight generations completed. The saved request hashes matched the frozen
protocol. One earlier setup attempt failed while decoding Go's chunked test
logs, before any model call; its output was retained and the log reconstruction
was corrected before freezing the generation protocol.

| Arm | Changed: old review correctly rejected | Unchanged: old review correctly matched | Changed: affected functions correct | Unchanged: empty affected set correct |
| --- | --- | --- | --- | --- |
| A | No | Yes | Yes | No |
| B | No | Yes | Yes | No |
| C | Yes | Yes | Yes | No |
| D | Yes | Yes | Yes | No |

All arms answered that the direct call graph was unchanged and that canonical
admission was not established, in both conditions. In changed A, the model's
explanation explicitly recognized that the old review was inapplicable, yet its
machine-readable `review_matches_current` field was `yes`. Changed B also
returned `yes`, while noticing different review IDs in its explanation. C and D
returned `no`. All unchanged answers listed both functions as affected despite
the identical source and the explicit empty-set rule.

The supported result is narrow: a graph-preserving change invalidates the old
source-claim review, and the domain function detects that mismatch independently
of a model's decision field. The pilot does **not** demonstrate a unique AHE
accuracy advantage: C ties D, no arm is fully correct across both conditions,
and the eight calls are not eight independent tasks. A structured fact packet
already resolves the review-decision error in this run. No actual unauthorized
write, admission or deployment was attempted or blocked by the model harness.

Public documentation reports the bounded result; raw captures remain in the
operator-selected private output directory. Do not publish wider performance
claims from this single development case.

## Next-stage interactive agent comparison protocol — not yet run

The question for an agent study is: **does adding governed evidence reduce
unsupported consequential claims while retaining useful supported answers?**
Code-graph tools already target structural discovery; merely finding another
call chain would not establish an evidence-governance benefit. The upstream
descriptions of [codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp)
and [CodeGraphContext](https://github.com/CodeGraphContext/CodeGraphContext)
were consulted on 2026-09-18 for positioning, not used as measured baselines.
No claim about an absent competitor feature follows from this document.

Before collecting comparative results, freeze and publish a runnable protocol:

1. **Cases and oracle.** Use public or synthetic Go repositories with associated
   policy/requirement revisions. Split development and held-out cases before
   tuning. Include supported impacts, unrelated changes, missing evidence,
   incomplete collection, pending/rejected claims, explicitly superseded rules,
   conflicts, and complete versus incomplete AND-parent sets. Pin every source
   revision and hash. Have independent reviewers determine answerable claims,
   required qualifications and acceptable abstentions before seeing outputs.
   An AHE admission is a treatment input, never the correctness oracle.
2. **Arms.** Run (A) agent + file search + all documents, (B) the same plus one
   pinned code-graph tool, (C) B plus the same review decisions/provenance supplied
   as static structured files, and (D) B plus AHE exposing that identical
   information through governed evidence queries. B measures structural help;
   C controls for added information and human curation; D versus C estimates
   the additional value of governed access. Repeat with a second parser as a
   separate comparison. Allow baseline agents to inspect source and abstain.
3. **Equal conditions.** Freeze model ID, prompt, decoding options, tool schemas,
   accessible documents, tool/output/time budgets and software versions. Use
   fresh sessions and randomized arm order within each case. Disable shared
   memory and external browsing. Record setup, extraction and human review time
   separately from answering time; report total cost as well as query cost.
4. **Outcome rubric.** Primary metric: the paired difference in the fraction
   of tasks containing at least one unsupported consequential assertion.
   Independently score supported-fact coverage on answerable tasks and correct
   abstention on unanswerable tasks so an always-abstain system cannot win.
   Separately score citation entailment, source/revision selection and lifecycle
   handling. Tokens, latency, tool failures and human minutes are secondary.
   Blind adjudicators to arm labels and preserve disagreements and resolution.
5. **Analysis and stopping.** Use a development pilot to estimate variance, then
   fix a power-based held-out sample size, minimum meaningful improvement,
   allowable coverage loss and repetitions before any held-out run. Treat a
   task/repository as the sampling cluster; repeated seeds are not independent
   cases. Report paired effect sizes with cluster-aware 95% confidence intervals,
   raw denominators and coverage/abstention together. Fix infrastructure-failure
   and retry rules in advance, retain every attempt, and report failures by arm.
   A result is inconclusive if uncertainty or coverage loss exceeds the frozen
   criteria. Do not tune prompts or stop early based on favorable held-out scores.
6. **Auditability.** Release permitted fixtures, adapters, frozen prompts,
   scoring rubric, hashes and sanitized per-case outputs with any aggregate
   claim. Separate deterministic fixture admissions from actual human decisions.
   PostgreSQL/MCP integration and independently rerun results are prerequisites
   for an end-to-end claim. Do not convert an unavailable tool into a zero score.

Until that study is run, the supported README claim remains narrow: these
source/review contract checks are reproducible, and the single-case pilot makes
both model errors and the matched-information tie visible. General agent
accuracy and adoption gains remain unmeasured.
