# E4B repairs with shared failures — 2026-10-10

In this completed Product experiment, **shared failures with AHE results** first
passed the selected scikit-learn 25570 tests at **answer 8**. Its next 22 answers
also passed. The other three groups did not fully pass within 30 answers each.
Answer 1 is the initial answer; these counts include it.

| Feedback and evidence | First full pass | Later answers passing / attempted | Best bug tests; existing tests |
| --- | --- | --- | --- |
| Own failures, with AHE | None in 30 | Not applicable | 3/3; 168/184 |
| Own failures, without AHE | None in 30 | Not applicable | 3/3; 168/184 |
| Shared failures, with AHE | Answer 8 | 22/22 | 3/3; 184/184 |
| Shared failures, without AHE | None in 30 | Not applicable | 3/3; 182/184 |

Success requires all **187 selected tests** to execute and pass. These are not
the entire scikit-learn test suite. The 23 successful answers produced one
identical patch, not 23 different repairs. After first success, the controller
froze that successful input and generated again with the same seed through
answer 30. It did not supply the winning answer. These confirmations measure
repeatability of one input, not independent searches or future correctness.

## Comparison and feedback

There was one fresh pair of models per sharing policy, each pairing an AHE
condition with a condition without AHE results. Each answer proposed a complete
patch against the original code. Code did not accumulate between answers.

Both conditions started with the same 15 pinned source records and 11 external
AST syntax candidates. The control therefore had organized source evidence;
it was not a bare model. Models answered without tools. The controller handled
source intake, retrieval, patch submission and tests.

- **Own failures:** show the latest failed answer and the latest failure that
  actually executed tests, deduplicated to at most two detailed records, plus
  an index of earlier failures.
- **Shared failures:** keep the same own records and add up to two peer failure
  records under the same rule, plus the peer failure index. This increases
  information and usually input length; there was no equal-length control.
- **Timing:** freeze all four requests before generating any answer in that
  round. Only earlier failures are visible. Withhold a peer's first successful
  answer and every later result, including its successful patch identifier.
- **Diagnostics:** include prior candidate edits, scores and every failed test
  name. Select detailed output mechanically: the first four test-name groups
  in lexical order and first variant of each. Keep at most 8,000 characters per
  group, using the first and last 4,000 when needed. Older full details remain
  archived but are not available to the model.
- **Source requests:** provide exact pinned Python declarations only when the
  model specifies their path and name, capped at 32,000 added characters per
  group. No operator diagnosis, reference patch or extra test source is added.

The AHE condition used real Product MCP with fresh isolated PostgreSQL snapshots
and separate intake/reviewer/endpoint/query LOGIN roles. Every source readback
was checked byte for byte. Admission used **TEST APPROVAL STUBS**, not a claim
of human semantic review. This workflow did not activate SAT/conflict reasoning.
AHE stored and returned evidence; feedback selection and repair policy belonged
to the experimental controller. Initial same-arm requests, parsed answers and
patches matched across the two sharing policies.

## Execution and costs

| Feedback and evidence | Full passes | Tested but not fully passing | Test collection failed | Not submitted |
| --- | ---: | ---: | ---: | ---: |
| Own failures, with AHE | 0 | 20 | 0 | 10 |
| Own failures, without AHE | 0 | 20 | 2 | 8 |
| Shared failures, with AHE | 23 | 6 | 0 | 1 |
| Shared failures, without AHE | 0 | 20 | 1 | 9 |

All **120 answers** count, with **no resampling**. Of 92 submitted patches,
89 executed all 187 tests and three prevented collection through invalid
indentation. Another 28 answers were not submitted: citation/format failures,
invalid edit anchors, one refusal and one output truncation. Those 31 answers
are **5,797 unexecuted test slots**, not 5,797 observed test failures.
There were **16,643 actual test executions**. Two fresh original/reference
controls separately ran 374 tests and are excluded from model scores.

The formal AHE workflow completed **60 fresh PostgreSQL snapshots and 13,890
recorded MCP calls**. Owned model processes, databases and evaluation containers
were checked as cleaned up. Preflight checks are separate from these totals.

The shared AHE group needed 8 answers to find its first passing patch:
171,379 input tokens, 37,799 output tokens, 542.6 generation seconds and
1,975.1 evaluation seconds. Its 22 fixed-input confirmations add separate costs.
These timings omit database setup and operator checks; they are not end-to-end
runtime. Fewer answers alone do not establish a cost advantage. The
[data summary](evaluation/repair-sharing-20261010.json) separates exploration
from confirmation costs and includes all 120 answer statuses.

## Versions and recorded exceptions

- Product revision: `4c6d84ff6aa8a1b41f1d1a60a7db7817087e8330`.
- scikit-learn base: `cd25abee0ad0ac95225d4a9be8948eff69f49690`.
- Model: `gemma4:e4b-it-qat`, digest
  `ee665637121887cf3befff38abbb1be4ee117c7db867d97a67e29049ecd7e15f`.
  Ollama 0.40.0; thinking enabled; temperature 0; seed 42; context 65,536;
  output limit 12,288. Every answer used a fresh owned model service.
- After answer 12 (48 answers total), feedback parsing stopped on a mismatch
  between pytest parameter names and SWE-bench's whitespace-truncated IDs.
  The recorded amendment changed name matching only. All earlier 47 feedback
  records remained byte-identical; the missing feedback was recovered from
  saved logs. No model answer or test was rerun. Manifest revision 2 preserves
  the original freeze and amendment.
- At answer 21, the own-failures AHE group requested two declarations. The
  existing policy added 2,431 characters from answer 22 onward, only to that
  group. It still did not fully pass. Actual later source visibility therefore
  differed; the full trajectory cannot be reduced to one feedback-card effect.

## What this establishes

The completed [peer-detail comparison](REPAIR_PEER_FAILURES_20261010.md)
compares adding a peer's different failure details with repeating the model's
own failure details at three earlier checkpoints. None of its five tested
patches repaired a remaining own failure. It does not establish the mechanism
behind the answer-8 success recorded here.

The observed result is consistent with shared failure information helping this
AHE-assisted search. It does not isolate which card, repeated detail, history
index, extra context or AHE receipt caused the change. Each policy has only one
coupled pair; answers within a pair are not independent replications. The same
tests supplied feedback and graded patches, so this is adaptive repair, not a
held-out correctness evaluation. Explanations and semantic citation quality
were not independently adjudicated.

The two failures visible before answer 8 were already known to the AHE group;
answer 8 did not receive a new source supplement. The peer had converged to the
same failing patch. A specific causal mechanism needs a separate intervention,
not an interpretation of success alone. An additional transformer-order concern
was identified by source review but has not been tested; it is not an observed
failure and is outside these 187-test scores.

The [earlier shared-feedback run](REPAIR_FEEDBACK_20261008.md) remains a separate
historical result. It used different feedback selection and round numbering;
do not combine its correction 24/28 scores with this answer-8 result.

## Evidence and reproduction limits

The public JSON contains an allowlisted projection of counts, per-answer
statuses and provenance hashes. Raw inputs, model output, patches, MCP records,
test logs and private runners remain in the sealed private archive. The seal
identifies 4,704 files; its SHA-256 is
`722c4f7dccca0ee2763fa0e1648f4c61b797b1c6e1025dab798676eb04de9526`.
Hashes identify unavailable evidence; this repository does not yet provide a
complete bundle to reproduce these model results. Public source-intake tests
alone do not reproduce them. This documentation update did not rerun models,
PostgreSQL integration, race checks or the full Product suite.

The public case comes from [SWE-bench](https://github.com/SWE-bench/SWE-bench)
and [scikit-learn](https://github.com/scikit-learn/scikit-learn/issues/25570).
Upstream source retains its
[scikit-learn license](https://github.com/scikit-learn/scikit-learn/blob/cd25abee0ad0ac95225d4a9be8948eff69f49690/COPYING)
and the harness its [SWE-bench license](https://github.com/SWE-bench/SWE-bench/blob/main/LICENSE).
No third-party source, test patch or reference solution is republished here.
