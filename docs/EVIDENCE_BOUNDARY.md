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

## AND case: a path does not cover every prerequisite

The source requirement is fixed: **Webhook readiness requires BOTH
signature-verification evidence AND anti-replay evidence.** This experiment
separates ordinary reachability, completeness of the declared AND manifest,
representation of the original requirement, and actual runtime correctness.

```sh
# Four deterministic conditions; no database or model required:
make evidence-and-case
# Optional 16-request pilot; requires a running local Ollama and Python 3.9+:
python3 -B scripts/evidence_and_case.py --model gemma4:12b --output /path/to/private/and-case-run
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

### Frozen AND pilot protocol

The [runner](../scripts/evidence_and_case.py) fixes all four conditions, four
packets, scoring rules and request hashes before the first model call. A gets
the original requirement and full artifact records; B adds the observed generic
path; C adds the same requirement/nodes/edges/manifest again as JSON; D adds the
actual AHE preparation result. Every arm receives the same explicit explanation
of declared-set integrity versus source completeness. This is an instructed
record-reading task, not a test of discovering those concepts unaided. C changes
repetition and presentation, not the underlying facts. D's validation result is
direct evidence for the declared-integrity answer; it is not evidence that the
model independently derived or verified it.

Five fields are scored separately: path existence, declared-AND integrity, full
requirement support, refusal to infer runtime correctness, and the exact missing
branch list. The full-requirement rubric requires both original prerequisites
in the manifest, nodes and edges. Only complete satisfies that rubric. Complete
and underdeclared satisfy declared-set integrity. All conditions have a path;
none establishes runtime correctness. Explanations are retained separately and
cannot silently repair an incorrect decision field.

The run uses the same installed `gemma4:12b` digest, Ollama/Go versions and host
architecture as the refund pilot above, on 2026-09-18. Temperature 0, generation
seed 42, context 16384, generation limit 1024, HTTP timeout 180 seconds, shuffled
request order with seed 919, fresh context, sixteen attempts and no retries.
The protocol records the base commit, relevant source hashes, fixture, oracle,
all requests and their hashes before generation. Raw responses, failures, token
counts, diagnostic wall times and per-condition results remain in the chosen
private directory. This is one authored development case with four variants,
not sixteen independent tasks; no significance or general accuracy estimate is
claimed. Runtime timings are not a controlled latency comparison.

### Observed AND results and limits

All four deterministic conditions passed on base commit
`294ef61cfe76ddf8b9fe6dd9b4032087e807a907` plus the accompanying test and runner.
A disposable Go overlay bypassed only `artifact.Validate()` in
`PrepareTopology`: precisely `missing_edge` and `missing_parent` then failed,
while complete and underdeclared still passed. Production source was unchanged
and the overlay was removed. This verifies sensitivity to the intended boundary;
it does not broaden the check into database or semantic enforcement.

Fifteen of sixteen model generations completed. Complete A returned HTTP 500;
the runner retained the error and continued the other predeclared requests,
without retry. The original error body was not captured; the separate diagnostic
below subsequently reproduced the request and identified its failure mechanism.
All sixteen saved request hashes matched the frozen protocol. Counts below use
completed generations as denominators; the unavailable A condition is not scored
as a model mistake or silently treated as successful.

| Arm | Completed / attempted | Path correct | Declared AND correct | Full requirement correct | Runtime limit correct | Missing branches correct | All five correct |
| --- | --- | --- | --- | --- | --- | --- | --- |
| A | 3/4 | 0/3 | 2/3 | 3/3 | 3/3 | 0/3 | 0/3 |
| B | 4/4 | 4/4 | 2/4 | 4/4 | 4/4 | 1/4 | 0/4 |
| C | 4/4 | 4/4 | 2/4 | 3/4 | 4/4 | 2/4 | 1/4 |
| D | 4/4 | 3/4 | 2/4 | 3/4 | 4/4 | 3/4 | 1/4 |

C's fully correct condition was missing-edge; D's was missing-parent. All
fifteen completed answers returned `no` for declared-AND integrity, incorrectly
rejecting every completed complete/underdeclared control. In complete C and D,
the explanation treated absence of anti-replay from the **single reported path**
as absence from the artifact, despite both edges being supplied. D did so even
with an accepted AHE preparation result. In underdeclared D, the missing source
requirement was correctly identified but conflated with declared-set validity.
Missing-edge D also denied the signature path while correctly identifying the
separate missing anti-replay edge. All completed answers declined to infer
runtime correctness.

These outputs expose confusion between one path, the complete artifact and the
original requirement. They do not show that adding AHE's result reliably fixes
that confusion: C and D tie on fully correct conditions, with different errors.
The demonstrated benefit is the deterministic artifact boundary; simply showing
its result to a model is not equivalent to enforcing it in an execution path.
The synthetic complete condition and underdeclared control must remain in any
future study so blanket rejection cannot masquerade as correct discrimination.

### Follow-up diagnosis: generation failure and decision-field errors

A separate diagnostic preserved the original pilot and froze each diagnostic
phase before its requests. Thirty additional local requests were made, with
full HTTP bodies and owned-server logs retained privately. These are adaptive
development diagnostics, not new held-out benchmark cases or replacements for
the original scores.

The first phase replayed the original first six requests in order. The first
five answer strings matched byte-for-byte; complete A again returned HTTP 500,
now with `prediction aborted, token repeat limit reached`. Unloading the model
and sending complete A alone reproduced the same error. A streaming diagnostic
then exposed an unfinished `reason` string ending in 101 consecutive `1`
characters before that error. Streaming returned HTTP 200 with an error event;
HTTP status alone therefore does not indicate a successful generation.

The pinned [Ollama v0.34.1 completion implementation](https://github.com/ollama/ollama/blob/v0.34.1/llm/llama_server.go)
aborts after its repeated-content counter exceeds 100. Its
[non-streaming generate handler](https://github.com/ollama/ollama/blob/v0.34.1/server/routes.go)
returns that error as HTTP 500. This identifies the reproduced failure as a
generation repetition abort. It does not establish the model-internal cause of
the degeneration. The original Python helper stored only the exception string,
losing the HTTP error body; future capture must retain both error bodies and
partial streams.

The second phase compared streaming generate with default thinking, generate
with `think:false`, and chat with the same system/user text, schema and sampling
options, for complete A and all four D conditions (15 requests). Complete A
finished under both changed settings but still gave an incorrect declared-AND
field. The chat implementation has separate handling that delays schema
constraints during thinking; generate passes its format directly to completion
for this model. Endpoint choice is consequently a material harness setting.
Simply switching endpoints or disabling thinking did not resolve the errors.

The third phase kept the original generate settings and all D facts, adding
either the exact output schema to the prompt or that schema plus explicit
field-by-field definitions (eight requests). This follows the
[Ollama documentation's recommendation](https://docs.ollama.com/capabilities/structured-outputs)
to include the schema in the prompt as well as in `format`. The original pilot
provided conceptual rules in its system text but only supplied the exact output
schema through `format`.

| Diagnostic on the four D conditions | Finished valid answers | All five fields correct | Other outcomes |
| --- | --- | --- | --- |
| Original generate settings, streamed | 4/4 | 1/4 | Original decisions reproduced |
| Generate with thinking disabled | 4/4 | 0/4 | Several path/declared-set errors remained |
| Chat, default thinking | 2/4 | 0/4 | Two exhausted the 1024-token budget with no final answer |
| Generate, schema also in prompt | 4/4 | 1/4 | Complete's declared-set decision corrected; missing list still wrong |
| Generate, schema plus field definitions | 4/4 | 2/4 | Underdeclared corrected; missing-edge declared-set decision regressed |

Independent recomputation from fixture nodes, edges and parent manifests matched
all four frozen oracles. The observed semantic errors fall into three groups:

- **One path treated as the whole artifact.** Complete C/D dismissed the
  anti-replay edge because it was absent from the signature-only path witness,
  although the full edge list contained it.
- **Different completeness checks conflated.** Underdeclared D treated the
  absent source requirement as a defect in the smaller, internally valid declared
  set. Missing-edge D sometimes used incomplete AND support to deny the intact
  signature path.
- **Output fields contradict the explanation.** Complete A under both changed
  runtime settings explained that the declared set was satisfied but returned
  `declared_and_integrity: no`. With the schema in the prompt, complete D instead
  returned both completeness fields as `yes` while listing both present branches
  as missing.

The ablations support sensitivity to prompt-to-field mapping and generation
configuration; they do not isolate a unique neural cause or establish a reliable
fix. Original answer-quality counts combine reasoning, output mapping and
runtime handling. A subsequent comparison must first calibrate those interfaces
on separate development controls, retain semantic consistency checks, and freeze
the repaired protocol before testing held-out cases. Deterministic artifact
validation remains independently demonstrated by the Go experiment.

### Repaired replay and larger local model

The separate [v2 runner](../scripts/evidence_and_case_v2.py) retains the original
fixture, four context arms, five scored fields and oracle. It adds the schema
and explicit field definitions to the prompt, distinguishes an individual path
witness from the full edge list, specifies `[]` when no required branch is
missing, and clarifies that runtime correctness being unproven is not evidence
that the runtime is incorrect. It records contradictions without repairing or
retrying answers. HTTP errors, partial streams, truncation, invalid output and
incorrect decisions are separate outcomes.

The selected baseline for subsequent local experiments is **`gemma4:31b-it-qat`
with this repaired protocol**. Retain 12B as a historical comparison rather than
silently substituting it for that baseline. The command below reproduces the
completed paired study and therefore explicitly runs both models; it is not a
single-model default command. Freeze new cases and their scoring before running
the selected 31B configuration.

```sh
# Both models must already be installed; use your own running local server.
python3 -B scripts/evidence_and_case_v2.py \
  --model gemma4:12b --model gemma4:31b-it-qat \
  --port 11434 --output /path/to/private/and-repair-run
# Offline transport, strict decoding, scoring and failure-classification checks:
python3 -B -m unittest discover -s scripts -p 'test_evidence_and_case_v2.py' -v
```

Both models receive all sixteen cells under the same revised protocol. Requests,
oracle, model digests, source hashes, order and the conditional Luna follow-up
rule are frozen before generation. Model order is 12B then 31B; each uses the
same shuffled case/arm order. Endpoint is `generate`, with streaming capture,
explicit `think:true`, temperature 0, seed 42, context 16384 and output limit
1024. No retries, model downloads, database or admission writes occur. Results
are a development replay on already inspected cases, not a held-out accuracy
estimate. Comparison with v1 includes prompt/interface changes; comparing the
two v2 models holds that interface fixed. Model size and quantization differ,
so any difference cannot be attributed to parameter count alone.

The preselected fallback is a fresh `gpt-5.6-luna` task if the 31B run contains
any execution/output failure, wrong scored field or consistency violation. It
receives all sixteen revised packets under neutral identifiers, without the
oracle, condition names, diagnosis, previous answers or source-file paths.
This is a separate workflow diagnostic: batching packets in a Codex task changes
context, system instructions and decoding relative to fresh Ollama requests.
Its score must not be presented as a controlled model-ranking result.

#### Repaired replay results — 2026-09-18

All 32 requests completed normally, without HTTP/server errors, truncation or
schema failures. The 12B digest was unchanged from v1. The installed 31B QAT
model was `gemma4:31b-it-qat`, digest
`e0812a55773bfeac846b2d605b4d93638b8dfa7119d9587f3d91475afc78185e`
(`30.7B`, `Q4_0`); 12B was `11.9B`, `Q4_K_M`. Both used Ollama `0.34.1`
and the `gemma4` renderer/parser. Each paired request differed only in model
identity. The original fixture and v1 source hashes were unchanged.

| Model / protocol | Completed / attempted | All five correct / attempted | A | B | C | D |
| --- | --- | --- | --- | --- | --- | --- |
| 12B, original v1 | 15/16 | 2/16 | 0/4 | 0/4 | 1/4 | 1/4 |
| 12B, repaired v2 | 16/16 | 10/16 | 1/4 | 2/4 | 3/4 | 4/4 |
| 31B QAT, repaired v2 | 16/16 | 16/16 | 4/4 | 4/4 | 4/4 | 4/4 |

The 12B v2 errors were missing-edge A/B/C, missing-parent A/B and underdeclared A.
Every v2 path and runtime-proof decision was correct. The 12B declared-set field
was correct in 10/16 packets; full requirement and missing branches each in
12/16. The simple consistency checks flagged none of these six wrong answers:
self-consistent answers can still be factually false. Missing-parent B also
ended its explanation with a negative declared-set conclusion while its earlier
JSON decision remained `yes`; free-text contradictions are outside the simple
deterministic consistency rules.

A separate, unblinded review read the reasons for all sixteen 31B answers and
all ten fully correct 12B answers against the fixture. No material false claim
was observed in that bounded subset. This is an explanation sanity check, not
an independent blinded adjudication or a completeness guarantee. Eleven offline
transport/scoring tests and `make verify` passed.

The repaired interface improved this 12B development replay, and the larger
local configuration answered all fixed packets correctly. This does not isolate
which prompt change caused improvement, establish a general model ranking, or
show an AHE-specific advantage: 31B also passed A/B/C without AHE's validation
result. The 12B C-versus-D difference is one already-inspected condition, not a
statistically supported effect. The original failure captures and scores remain
available. No 31B fallback condition fired, so the prepared Luna task was **not
launched** and there is no Luna result to report.

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

### Stale-review local-model protocol

The [runner](../scripts/evidence_stale_review_case.py) uses the selected
`gemma4:31b-it-qat` configuration: `generate`, streaming capture, `think:true`,
temperature 0, seed 42, context 16384 and output limit 1024. It freezes the
fixture, schema, field definitions, oracle, source hashes, exact installed model
identity and all requests before generation. Five conditions and three arms
produce fifteen requests, shuffled with seed 921, once each without retries.

- **A:** all old-review and latest-state facts, the exact submitted metadata,
  and the same explicit transaction rules supplied to every arm.
- **C:** A plus precisely the same facts repeated as JSON.
- **D:** C plus the actual final writer receipt and database observations.

There is no static-call-graph arm: an unchanged call path does not identify the
runtime state or executed branch. A and C receive the same latest state as D;
they are not forced to predict from an obsolete snapshot. D directly receives
the outcome, so its score measures receipt interpretation, not independent
prediction or an AHE-specific reasoning improvement.

The five scored fields are the exact request outcome, whether it adds authority
records, whether a canonical claim exists afterward, whether the quote still
matches its source, and the final proposal outcome. Request outcomes distinguish
new admission, exact replay, state conflict and replay conflict. Explanations
are retained separately and cannot correct a wrong decision field. Transport,
truncation, invalid output and incorrect answers remain separate outcomes.

```sh
python3 -B scripts/evidence_stale_review_case.py \
  --fixture /path/to/private/fixture.json \
  --model gemma4:31b-it-qat --port 11434 \
  --output /path/to/private/stale-review-model-run
python3 -B -m unittest discover -s scripts -p 'test_evidence_stale_review_case.py' -v
```

Any 31B execution/output failure, wrong scored field or consistency violation
triggers the separately authorized Luna diagnostic with all fifteen frozen
packets under neutral identifiers and no oracle or previous answers. As in the
AND study, a shared Codex task changes the context and decoding workflow; it
cannot establish a controlled model ranking.

This is one authored synthetic case with five variants and an explicitly
ordered schedule. It does not test all concurrent interleavings, MCP transport,
an autonomous agent, third-party analyzers or production authorization. The
writer is deliberately called even where a model should advise against it:
database enforcement and model interpretation are measured separately.

### Stale-review results — 2026-09-18

All five database conditions passed on base commit
`294ef61cfe76ddf8b9fe6dd9b4032087e807a907` plus the accompanying experiment files.
The run used a newly initialized PostgreSQL `18.6` cluster accessible only
through its private Unix socket, with no existing database. The final fixture
also passed under Go `1.27.1`'s race detector. Every conflict and exact replay
left the eight tracked authority-table counts and the complete-row SHA-256 over
those tables plus `proposal_occurrences` unchanged. New admission created one
source claim, one raw-evidence node, one edge, one admission decision and its
manifest/review/node/edge bindings. Exact replay retained the original result
identifiers. Direct readback at all three time points verified raw/rendered
bytes, content hashes, identity-renderer metadata and each exact quote interval.
The temporary cluster was stopped and removed after saving the receipts.

The model run used the same exact installed 31B QAT digest as the AND replay,
`e0812a55773bfeac846b2d605b4d93638b8dfa7119d9587f3d91475afc78185e`,
with Ollama `0.34.1`. All fifteen requests finished with valid output and normal
stop markers. No HTTP/server failure, truncation or deterministic consistency
violation occurred. The request hashes, captured-body hashes, 762 source-file
hashes, fixture and scores were checked after execution. The frozen oracle
independently matched the database observations.

| Arm | Completed / attempted | Request outcome correct | Other four fields, each correct | All five correct |
| --- | --- | --- | --- | --- |
| A: complete current records | 5/5 | 4/5 | 5/5 | 4/5 |
| C: A plus same-information JSON | 5/5 | 4/5 | 5/5 | 4/5 |
| D: C plus actual receipt | 5/5 | 5/5 | 5/5 | 5/5 |

Both scored mistakes occurred in `admitted_by_other_after_review`. A and C
returned `exact_replay` rather than `replay_conflict`. Their explanations
acknowledged that reviewer B had admitted at t1, but compared A's t2 input with
A's original t0 input instead of B's stored admission input. The actual writer
returned `admission_replay_conflict` with no new rows or overwritten decision.
These are ordinary completed but incorrect model answers, not runtime failures.

A separate, unblinded review of all fifteen explanations found an additional
false statement in this condition's D answer: it claimed a different reviewer
**and reason**, although the reason was identical and only reviewer identity
differed. The five-field score remains correct for that response; the reason
error is reported separately, without changing the frozen rubric. The other
twelve explanations had no material factual error observed in this bounded
review. This is not a blinded adjudication or an explanation-completeness proof.

The predefined fallback condition fired, and a fresh `gpt-5.6-luna` Codex task
received all fifteen frozen neutral packets. It then used parent-task lookup
tools despite the no-tools restriction and received an administrative follow-up
requesting direct answers without adding case facts or scoring information.
This is a protocol deviation. The attempt is retained as a workflow diagnostic
and excluded from strict blinded comparison or model-ranking claims. Its final
JSON covered all fifteen identifiers and passed the output schema; only 6/15
answer objects matched all five fields under the frozen mapping. This is the
descriptive score of a protocol-violating, shared-context workflow, not a
qualified comparison with the 31B run. The raw answer and tool receipts are
retained, and no replacement Luna run was substituted.

Twelve offline runner tests, `make verify` and `go test -race ./...` passed.
The initial sandboxed repository check could not open local test listeners and
failed a script-compilation test; unchanged checks passed with the required
local execution access. Those setup failures are retained separately from model
results. The supported result is transaction enforcement for these five
controlled schedules. D's receipt-reading improvement is one condition in an
instructed synthetic case, not a statistically established general benefit.

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

### Implements local-model protocol

The [runner](../scripts/evidence_implements_case.py) keeps the repaired
`gemma4:31b-it-qat` configuration: streaming `/api/generate`, `think:true`,
temperature 0, seed 42, context 16384, output limit 1024, no retries. It freezes
all fifteen requests, source hashes, fixture, field definitions, schema, oracle,
model identity and fallback packets before generation. Arm order uses seed 922.

- **A:** complete bounded endpoint and pair state, exact action/request, and
  explicit independent-relation rules.
- **C:** A plus those identical facts repeated as JSON.
- **D:** C plus the observed backend receipt and database observations.

All arms receive a completed exact-pair lookup, so an absent relation is not
confused with an unqueried relation. Full native displays remain in the private
fixture; the model receives the same bounded projection of decision-relevant
facts in every arm. D adds the outcome directly and measures receipt reading.

Four fields are scored: endpoint admission, operation outcome, relation
existence afterward, and newly written relation authority. For these fresh-pair
cases, the last two fields are correlated. No new canonical nodes and no runtime
correctness proof are fixed checks/limits, not extra points in the model score.
Explanations receive a separate unblinded factual review; correct fields cannot
repair a false explanation, and a plausible explanation cannot repair wrong
fields. Technical failures remain separate from completed incorrect answers.

```sh
python3 -B scripts/evidence_implements_case.py \
  --fixture /path/to/private/fixture.json --model gemma4:31b-it-qat \
  --port 11434 --output /path/to/private/implements-model-run
python3 -B -m unittest discover -s scripts -p 'test_evidence_implements_case.py' -v
```

Any technical, output, scored-field, consistency or material explanation failure
triggers one fresh, zero-history `gpt-5.6-luna` collaboration agent with all
fifteen frozen neutral packets, without tools, oracle or earlier answers. Any
protocol deviation is retained. Its shared batch context and different runtime
make it a fallback workflow diagnostic, not a controlled model ranking.

This is one authored synthetic case with five variants and one observation per
cell. It does not measure other parsers, all possible database changes, MCP
subprocess/wire transport, an autonomous agent or authenticated human attention.
An admitted `implements` relation records a bounded reviewed navigation
assertion; it does not prove that the code satisfies the specification or passes
execution tests. All approvals here are synthetic fixture stubs.

### Implements results — 2026-09-19

All five backend/PostgreSQL conditions passed on base commit
`294ef61cfe76ddf8b9fe6dd9b4032087e807a907` plus the accompanying experiment files,
with Go `1.27.1` and PostgreSQL `18.6`. The cluster was newly initialized and
accessible only through its private Unix socket. The actual restricted relation
and query roles passed policy validation. The native recursive AND review,
complete directed lookups, eleven-table snapshots, independent provenance and
exact replay checks all passed. No remaining test schemas or roles were found
before stopping and removing the cluster.

The fixed model was `gemma4:31b-it-qat`, digest
`e0812a55773bfeac846b2d605b4d93638b8dfa7119d9587f3d91475afc78185e`,
served by Ollama `0.34.1`. All fifteen calls finished normally; there were no
HTTP/server failures, truncations, schema errors or deterministic consistency
violations. All four fields were correct in every response:

| Arm | Completed / attempted | Each field correct | All four correct |
| --- | --- | --- | --- |
| A: complete bounded facts and explicit rules | 5/5 | 5/5 | 5/5 |
| C: A plus the same facts repeated as JSON | 5/5 | 5/5 | 5/5 |
| D: C plus actual operation receipt | 5/5 | 5/5 | 5/5 |

A separate unblinded review of all fifteen explanations found no material
factual error in the supplied scope. In particular, the no-operation and
review-only answers did not mistake an absent submitted review subject for an
executed writer conflict. Approved-relation answers did not claim runtime
correctness. This review is not a proof of explanation completeness. No frozen
fallback condition fired in this 31B run, so no Luna attempt was needed for it.

Post-run verification recomputed scores from captured response bytes, matched
the oracle independently to database observations, and verified all request,
response-body, fixture, fallback-packet and 762 source-file hashes. Thirteen
offline runner tests and `make verify` passed. Raw captures, complete native
displays and setup failures were retained outside the repository. The initial
sandbox denied PostgreSQL shared memory; the first two database attempts stopped
at role-policy preflight for default `PUBLIC` privileges (database `TEMPORARY`,
then `public` schema `USAGE`). Only the newly owned database was configured to
satisfy the existing policy. These setup attempts produced no model results.
The dedicated Ollama listener was stopped after capture.

**The arms tie.** The supplied rules and bounded facts already sufficed for the
31B model on these five variants, so this run does not show an accuracy gain
from structured duplication or receipts. Its demonstrated result is the
independent relation-admission boundary: endpoint approval, review preparation
and stored relation authority remain distinct, with an auditable directed
receipt only after a valid exact approval. No third-party parser was measured,
and no unique-feature, general hallucination-reduction or runtime-correctness
claim follows.

#### Identical-configuration repeat — 2026-09-19

A separately declared model-only repetition reused the exact completed database
fixture, all fifteen byte-identical request files, model digest, server version,
settings and order. All 762 source hashes remained unchanged. A, C and D again
scored 5/5 each, with no execution, truncation, schema or consistency failure.
Every generated answer, including its explanation, matched the first run
exactly; the fifteen new raw HTTP captures were retained separately. An
unblinded review of all explanations found no material factual error, so the
Luna fallback remained untriggered. Scores were recomputed from captured bytes,
and request, fixture, source and response-body hashes were verified.

This is 30/30 correct response objects across two runs of the same five variants,
not thirty independent tasks. No second PostgreSQL trial was performed. The
same seed and temperature test repeatability under fixed conditions; they do
not estimate across-seed variability or establish broader accuracy. The three
arms still tie. The dedicated repeat-run Ollama listener was stopped, and the
original experiment artifacts were preserved.

#### E4B matched-input comparison — 2026-09-19

The user-selected model was `gemma4:e4b-it-qat`, the locally installed tag,
with digest
`ee665637121887cf3befff38abbb1be4ee117c7db867d97a67e29049ecd7e15f`.
Ollama `0.34.1` reports `7.5B`, `Q4_0` for this artifact. A frozen launcher
changed only the original harness's `MODEL` selection; the harness itself was
not edited. All fifteen request objects were verified identical to the 31B
baseline except for `model`. The fixture, rules, schema, order, seed, context,
output budget, `think:true`, streaming and no-retry policy remained fixed.
No new database run or canonical write was performed.

| Arm | Completed / attempted | Endpoints correct | Operation correct | Relation after correct | New authority correct | All four correct |
| --- | --- | --- | --- | --- | --- | --- |
| A | 5/5 | 5/5 | 4/5 | 4/5 | 4/5 | 3/5 |
| C | 5/5 | 5/5 | 5/5 | 4/5 | 5/5 | 4/5 |
| D | 5/5 | 5/5 | 5/5 | 5/5 | 5/5 | 5/5 |

All fifteen calls ended normally. There were no HTTP/server, truncation or
schema failures. Three responses had incorrect fields and deterministic
consistency violations:

- `missing_approval`, A (`record-02`): returned `relation_admitted` and new
  authority `yes` despite an empty decision. Its explanation used the matching
  subject and submit action while omitting the required explicit approval. It
  also returned relation-exists `no`, contradicting its claimed admission.
- `approved_relation`, C and A (`record-03`, `record-05`): correctly returned
  admission and new authority, but incorrectly returned relation-exists `no`.
  Both explanations described creation of the relation, contradicting that
  answer field. The explanations do not repair the frozen field scores.

Unblinded review of all fifteen reasons confirmed these inconsistencies. The
D success response's two quoted row hashes matched the actual observations;
row counts and provenance independently confirmed the write. Two other reasons
had minor or ambiguous wording: calling action `none` read-only, and describing
a synthetic decision despite the empty decision field while correctly reporting
the approval-required rejection. They were not promoted to additional definite
material factual errors. This is not a blinded or exhaustive semantic review.

The previously authorized Luna fallback was triggered. A fresh projectless
`gpt-5.6-luna` Codex task received the fifteen frozen neutral packets without
oracle or prior answers. This task execution mode differs from the inherited
harness's collaboration-agent wording and is recorded as a workflow deviation.
No tool calls were observed. Luna returned no answer JSON, reporting that
records 4–12 were missing. The persisted delegation input contains all fifteen
records and matches the complete frozen prompt after XML-entity decoding.
That persisted input does not establish exactly what content remained visible
to the model at generation time. The later delivery diagnosis below resolves
the likely mechanism; it does not replace this historical attempt. **Luna has no score for
this attempt**; this workflow failure is excluded from model-accuracy rankings.

All request and captured-body hashes, the fixture, 762 source hashes and the
separately frozen launcher hash were verified. Scores were recomputed directly
from raw captures and compared with the database-derived oracle. The E4B
listener was stopped afterward. Only documentation and private artifacts
changed; the previously passing harness tests and repository checks were not
rerun without a code change.

To reproduce the model-only variant without editing the frozen 31B runner,
start from the repository root and an already installed E4B QAT model:

```sh
python3 -B - /path/to/private/fixture.json /path/to/private/e4b-run <<'PYMODEL'
import sys
sys.path.insert(0, "scripts")
import evidence_implements_case as study
study.MODEL = "gemma4:e4b-it-qat"
sys.argv = ["evidence_implements_case", "--fixture", sys.argv[1],
            "--output", sys.argv[2], "--model", study.MODEL, "--port", "11434"]
study.main()
PYMODEL
```

The intended system-level outcome is **fewer incorrect answers when a model
uses AHE-MCP evidence**. Here an incorrect answer object has at least one wrong
decision field. E4B's observed error counts were A **2/5 (40%)**, C **1/5 (20%)**
and D **0/5 (0%)**: a 40-percentage-point observed difference between facts alone
and receipt-assisted answering, or 20 points relative to the JSON-facts arm.
These are descriptive differences on these fixed variants, not population
effect estimates.

The useful mechanism demonstrated here is that AHE supplies verified operation
state which the model can interpret instead of predicting that state from rules
and inputs. D receiving that outcome is part of this workflow's intended value.
The E4B result is initial evidence for fewer interpretation errors in this
bounded task. The actual database boundary also stayed intact regardless of
the model's answer.

The 31B baseline passed each arm in two identical-setting runs; E4B was sampled
once per cell. This is one instructed synthetic case with five variants, not
an independently sampled benchmark. It does not establish a stable error rate
across tasks or isolate governed retrieval from any other system supplying the
same verified outcomes. A broader test should measure incorrect answers,
supported-answer coverage and inappropriate abstention together, with fixed
budgets and both unsupported and answerable cases. An additional control given
the same verified outcomes can distinguish the value of obtaining reliable
state from the value of AHE's particular delivery and governance mechanisms.

### Three-case error-reduction replay — 2026-09-19

The question for this replay is **whether verified AHE evidence helps the same
model answer with fewer errors**. It does not attempt to measure an increase in
the model's intrinsic reasoning ability. The treatment includes obtaining a
reliable validator or writer result and supplying it to the model.

The [suite runner](../scripts/evidence_error_rate_suite.py) freezes all 92
requests before generation: 46 for `gemma4:e4b-it-qat`, followed by 46 for
`gemma4:31b-it-qat`. Both exact artifact digests are checked against the locally
installed models. The original repaired AND packets and the original
stale-review and implements packets are reused. Across the two models, only
the request's `model` value changes; within each experiment, the original
shuffled arm/condition order, facts, instructions and schema are unchanged.
There are no retries or replacement answers.

- **A:** complete supplied facts and rules.
- **C:** A plus those same facts repeated as JSON; experiment 1 also includes
  B's generic traversal witness in C and D.
- **D:** C plus the observed native validation result or writer receipt.
- **B:** generic traversal information, retained as a secondary arm only for
  experiment 1. It is not a measured third-party analyzer.

An answer object is incorrect if **any scored decision field is wrong**, using
the existing five-field rubric for experiments 1–2 and four-field rubric for
experiment 3. Report both wrong/completed and (wrong + technical
failures)/attempted; an HTTP, truncated or invalid-schema response must not
disappear from the attempted denominator. Reasons do not rescue incorrect
fields and receive a separate unblinded review. `unknown` responses and
cross-field consistency are reported separately.

The run uses Ollama `0.34.1`, `temperature=0`, `seed=42`, `num_ctx=16384`,
`num_predict=1024`, `think:true` and streaming capture. Each request is stateless.
All three fixture hashes, 767 source-file hashes, exact model digests and
request hashes are frozen; raw HTTP bodies are retained before decoding. These
are new model calls over previously verified fixtures, not new PostgreSQL
trials. The underlying boundary tests and synthetic approvals remain as
described above.

The sample contains three authored scenarios with fourteen variants, observed
once per model/arm cell in this replay. The 92 calls are not 92 independent
tasks. There is no held-out test set, blinded reason adjudication, population
error-rate estimate or causal latency comparison. Another service supplying
the same verified outcomes is not controlled here; the measured intervention
is the complete receipt-assisted workflow.

#### Observed error counts

Every one of the 92 calls completed normally, with zero HTTP/server, truncation
or schema failures and zero `unknown` answers. Thus wrong/completed and
(wrong + technical)/attempted coincide in this run. These are answer-object
error counts, not the number of individually incorrect fields:

| Model | Experiment | A errors | C errors | D errors |
| --- | --- | --- | --- | --- |
| `gemma4:e4b-it-qat` | 1. AND support | 3/4 | 3/4 | 3/4 |
| `gemma4:e4b-it-qat` | 2. Stale review | 3/5 | 2/5 | 1/5 |
| `gemma4:e4b-it-qat` | 3. Independent implements | 2/5 | 1/5 | 0/5 |
| `gemma4:31b-it-qat` | 1. AND support | 0/4 | 0/4 | 0/4 |
| `gemma4:31b-it-qat` | 2. Stale review | 1/5 | 1/5 | 0/5 |
| `gemma4:31b-it-qat` | 3. Independent implements | 0/5 | 0/5 | 0/5 |

Experiment 1's secondary B arm had 3/4 errors for E4B and 0/4 for 31B. The
deterministic cross-field checks flagged E4B experiment-2 A/C/D responses in
1/1/0 cases and experiment-3 A/C/D in 2/1/0 cases. They flagged no AND or 31B
responses; those narrow checks do not catch every contradiction with supplied
facts or prose. All 92 explanations were reviewed separately without blinding.

In experiment 2, D reduces E4B's observed error fraction from A's 60% and C's
40% to 20%, and 31B's from 20% in both controls to 0%. In experiment 3, E4B
changes from 40%/20% to 0%; all 31B arms are already correct. Experiment 1
shows no answer-object improvement for either model. These are descriptive
paired contrasts on these variants, not estimated population effects.

All 46 new 31B answer objects, including reasons, exactly match the corresponding
earlier 31B baselines. All fifteen new E4B implements answer objects likewise
match its earlier run. These repetitions confirm behavior on identical inputs;
they do not add independent tasks. Raw captures were rescored and all 767 source
hashes, fixture hashes, request hashes and captured-body hashes verified. The
two models' request objects match after changing only `model`. Six new metric
tests, 36 existing experiment-runner tests and `make verify` passed. The owned
Ollama listener was stopped after the run.

The explanations expose limitations beyond the scored fields. In experiment 1,
E4B repeatedly denies a surviving signature-to-readiness path because a different
AND branch is incomplete. It also treats an omitted requirement as a declared-set
integrity failure. The native validation result does not correct those errors;
one JSON-facts answer additionally invents a missing node in the missing-edge
variant. In experiment 2, E4B still calls another reviewer's admission an exact
replay even while quoting the conflict receipt. Its correct new-admission
answers also use “exact replay” loosely for resubmitting a pending review.

The 31B experiment-2 receipt answer gets every decision field right but again
claims both reviewer and reason changed. The supplied reason is identical; only
the reviewer changed. This is a definite explanation error, retained separately
from the decision-field score. A verified receipt can reduce state-classification
errors without making every accompanying explanation faithful.

To repeat from previously captured baseline directories and an isolated local
Ollama listener containing both pinned model artifacts:

```sh
python3 -B scripts/evidence_error_rate_suite.py \
  --baseline-and /path/to/private/and-v2-run \
  --baseline-stale /path/to/private/stale-run \
  --baseline-implements /path/to/private/implements-run \
  --output /path/to/private/fresh-suite-run --port 11442
python3 -B -m unittest discover -s scripts -p 'test_evidence_error_rate_suite.py'
```

The output directory must be new. Each baseline must retain its `protocol.json`,
`fixture.json` and original request files; source drift is rejected rather than
silently mixed into a replay. The preceding case sections describe how to
generate those baselines. Raw captures and historical failures remain private.

### Luna delivery diagnosis and repair — 2026-09-19

The failed task's complete saved delegation was about 113 KB, approximately
28,000 tokens under the bytes/4 estimate. Both initial delegation and follow-up
messages enter this task as `function_call_output`. The locally cached Luna
metadata specified a 10,000-token tool-output truncation policy. OpenAI's
[tool-response guidance](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-5.3-codex)
describes preserving the beginning and end while omitting the middle of an
oversized output. A complete persisted input therefore does not establish a
complete model-visible input.

Two controlled delivery probes tested this mechanism without asking the model
to solve the case. Fresh random tags were placed in all fifteen records of the
large packet: Luna returned exactly the tags for records 1–3 and 13–15, matching
the previously reported missing middle. A small packet containing fifteen fresh
tags returned all fifteen correctly. Together with the transport and metadata,
this identifies oversized tool-message truncation as the supported cause. The
raw model-facing API request was not captured, so the exact internal cutoff is
not directly observed. This is not evidence of a semantic reasoning failure or
an Ollama limitation.

The repair sent the fifteen original case payloads separately, each in a
7,122–16,560-byte message. Before sending, each message's hash and fresh delivery
tag were frozen. The only addition was an outer record/tag receipt; the original
case system instructions and facts were retained. Each message requested one
answer using only that message, without tools or earlier answers. No retries
were made. All fifteen tags and record IDs matched, all fifteen answer objects
passed the original four-field rubric, and no tool calls were observed. Review
of the explanations found no material factual error within these fixtures.

This was a repair diagnostic in the existing task, which already had history.
It establishes successful small-packet delivery and usable answers; it is not
a fresh-context ranking against either local model. The original no-answer
failure remains unscored and neither old nor new local answers are replaced.

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
source/review and declared-AND checks are reproducible, and the development
pilots retain model errors, infrastructure failures and matched-information
ties. General agent accuracy and adoption gains remain unmeasured.
