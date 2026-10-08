# E4B repair with test feedback — 2026-10-08

On **scikit-learn 25570**, E4B with AHE results first passed all selected tests
at **correction 24**; the condition without AHE results first passed at
**correction 28**. AHE reached the goal four corrections earlier in this shared
search. This is an observed difference, not an isolated causal effect or a
general repair-round guarantee.

| Condition | First success | Main answers, including initial answer | Successful input reruns |
| --- | --- | ---: | --- |
| E4B with AHE results | Correction 24 | 25 | 3/3 passed |
| E4B without AHE results | Correction 28 | 29 | 3/3 passed |

Success means **3 bug tests and 184 existing tests all executed and passed** on
one issue. It does not mean three issues were solved. Correction 0 is the initial
answer. Both conditions stopped on their first success; corrections 29 and 30
were unnecessary under the stopping rule.

## What the model received

Both conditions received the issue, the same 15 pinned source excerpts, 11
external syntax candidates with 22 source links, and shared failed-patch/test
feedback from both conditions. The control was therefore not an independent
unassisted search. The AHE condition additionally received actual database
readback results in a lossless compact format.

The controller performed intake, review, admission and queries through real
Product MCP with separate intake/reviewer/endpoint/query roles. It checked exact
source readback. Each stage used a fresh PostgreSQL snapshot; this was not a
long-running database lifecycle test. Admission used **TEST APPROVAL STUBS**.
External syntax candidates were not treated as proof of semantic extraction,
and this experiment did not activate SAT/conflict reasoning.

The repair loop and feedback selection belonged to the experimental controller.
AHE supplied evidence storage, review, provenance and readback; this result did
not add an autonomous repair policy to Product.

Models read the controller's fixed packet; they did not choose live MCP tools.
The visible packet contained the latest failed observation from each condition
and an index of all past failures. Older full observations were preserved in
AHE but not included in model context. AHE preservation therefore did not give
the model automatic access to all stored history.

## Repair and feedback procedure

1. Freeze the original base, source packet, model and evaluation rules. Generate
   one answer per active condition, starting with correction 0. Freeze both
   requests before either call; alternate which condition runs first by round.
2. Validate the proposed edits/citations without repairing the answer. Apply each
   submitted patch to the clean original base and evaluate it in a fresh
   container. Retest repeated patches rather than borrowing earlier scores.
3. Save failed candidates, test identifiers and actual failure output. Supply
   both conditions the same feedback in the next stage. The visible failure
   excerpt selects the first four groups in lexical order and the first variant
   of each, retaining up to 8,000 characters per group (first/last 4,000 when
   needed), with omission and hash information. Full logs remain archived.
4. Begin with ten corrections, extend in pairs if needed, up to thirty. Stop
   each condition when every selected test passes. A concrete request for missing
   source can add exact pinned source to both conditions, up to 32,000 added
   characters; no unsolicited solution hint or reference patch is supplied.
5. After success, make three fresh generations from that condition's exact
   successful input, with the same fixed seed, fresh context, PostgreSQL and
   containers. Do not give the model its winning answer or update the packet
   between confirmations.

Feedback contains failed-test content, so the same tests help guide and grade
repairs. This is adaptive repair, not held-out evaluation. Human patch fixes,
answer resampling and reference solutions were not included in model input.

The model was `gemma4:e4b-it-qat`, digest
`ee665637121887cf3befff38abbb1be4ee117c7db867d97a67e29049ecd7e15f`.
Ollama 0.40.0 used thinking, temperature 0, seed 42, output limit 12,288 and
context 65,536. The scikit-learn base was
`cd25abee0ad0ac95225d4a9be8948eff69f49690`; Product was `7c21b31`.
Compared with the earlier short loop, context size, feedback detail and history
selection changed as well as the allowed number of corrections. The packet limit
was 200,000 characters, with runtime token/truncation checks as a separate guard.

## Every correction

Each numeric cell gives **bug tests passed; existing tests passed**. “No valid
patch” covers nonmatching or ambiguous edit anchors and missing required edit or
source citations. Those answers were not converted into 187 observed failures.
Per-answer error counts and test outcomes are in the
[public data](evaluation/results-20261008.json), under `feedback30.attempts`.

| Correction | E4B with AHE | E4B without AHE |
| --- | --- | --- |
| 0 | No valid patch; not tested | 0/3; 184/184 |
| 1 | No valid patch; not tested | No valid patch; not tested |
| 2 | Declined; not tested | No valid patch; not tested |
| 3 | No valid patch; not tested | 3/3; 168/184 |
| 4 | No valid patch; not tested | 3/3; 168/184 |
| 5 | No valid patch; not tested | No valid patch; not tested |
| 6 | 0/3; 168/184 | 0/3; 168/184 |
| 7 | 0/3; 168/184 | No valid patch; not tested |
| 8 | 0/3; 168/184 | 0/3; 168/184 |
| 9 | 0/3; 168/184 | 0/3; 168/184 |
| 10 | 0/3; 168/184 | 3/3; 168/184 |
| 11 | No valid patch; not tested | 0/3; 168/184 |
| 12 | Patch prevented test collection | No valid patch; not tested |
| 13 | 3/3; 168/184 | 3/3; 168/184 |
| 14 | 3/3; 168/184 | 3/3; 168/184 |
| 15 | 3/3; 168/184 | 3/3; 168/184 |
| 16 | No valid patch; not tested | 3/3; 168/184 |
| 17 | 3/3; 168/184 | No valid patch; not tested |
| 18 | No valid patch; not tested | 3/3; 168/184 |
| 19 | No valid patch; not tested | 3/3; 168/184 |
| 20 | 3/3; 168/184 | 3/3; 168/184 |
| 21 | No valid patch; not tested | 3/3; 168/184 |
| 22 | No valid patch; not tested | No valid patch; not tested |
| 23 | 3/3; 182/184 | 3/3; 168/184 |
| 24 | 3/3; 184/184 | 3/3; 168/184 |
| 25 | Stopped after success | 3/3; 168/184 |
| 26 | Stopped after success | 3/3; 168/184 |
| 27 | Stopped after success | No valid patch; not tested |
| 28 | Stopped after success | 3/3; 184/184 |

At correction 23, the AHE patch fixed the bug but failed two existing Pandas
output tests involving dropped transformers: **3/3; 182/184**. The actual failures
were written to the next fresh AHE snapshot and read back through MCP; both
conditions received them. At correction 24, AHE's patch iterated the actual output
list and passed all 187 tests. The control continued breaking 16 existing tests
through correction 26, supplied no valid patch at 27, and passed all 187 at 28.
No human repair was substituted. Passing this suite does not prove correct
behavior in every untested transformer configuration.

## Confirmation and accounting

Each condition's three confirmations passed **3/3 bug tests and 184/184 existing
tests**. All three regenerated the same patch as that condition's first success.
These are fixed-seed reruns of a frozen successful input, not three independent
searches from correction 0 and not evidence that future repairs always pass.

| Main-stage count | With AHE | Without AHE |
| --- | ---: | ---: |
| Answers | 25 | 29 |
| Patches submitted | 13 | 21 |
| Complete evaluations | 12 | 21 |
| Patches preventing collection | 1 | 0 |
| Unsubmitted answers | 12 | 8 |
| Distinct submitted patches | 7 | 6 |

The two conditions share some patches: there were **9 distinct main-stage
patches** in total, not 13. Including six confirmations, there were **60 new
answers**, **39 complete model evaluations / 7,293 test executions**, and
**3,927 unexecuted test slots**: 20 unsubmitted answers plus one collection
failure, each accounting for 187 tests. No generated answer hit an input or
output limit. Two separate fresh original/reference controls executed 374 tests;
the original scored 0/3 and 184/184, the reference 3/3 and 184/184.

The model workflow completed **35 PostgreSQL snapshots and 11,856 recorded
successful MCP calls**. Initial preflight calls (156) and amendment-validation
calls (588) are separate. Failed preparation calls are not included in successful
totals. Fewer corrections do not by themselves establish lower end-to-end time
or token cost; database work, testing and shared feedback also contribute.

## Failures, amendments and limits

- **Correction 12: review display size.** A formatted history index exceeded
  the 128 KiB review limit before generation. The harness switched only that
  index to lossless single-line JSON and repeated the formal review; Product's
  limit was unchanged. The original failure, a diagnostic reproduction and a
  separate diagnostic setup error remain recorded. This was not an extra model
  attempt. The later correction-12 model patch independently caused an
  indentation/collection failure.
- **After correction 23: feedback parser.** A long pytest case name used a
  header form the harness missed. The parser was corrected and replayed against
  28 logs, preserving prior extracted sections. Both original answers and test
  runs were reused; neither model was sampled again to recover this failure.
- **Corrections 22 and 27: source omission.** A control edit referred to real
  `_update_fitted_transformers` code absent from the visible packet. The model
  made no explicit source request under the frozen rule, so no source was added.
  Those unsubmitted edits are not evidence of fabricated source; the packet
  cannot be described as complete.
- **Inference boundary.** This is one issue and one joint feedback trajectory,
  with fixed seed, shared failures, visible test content and recorded harness
  amendments. It does not isolate the value of AHE from another system providing
  equivalent verified feedback, measure autonomous querying, or establish a
  universal iteration limit. The [first-answer failures](EVALUATION_20261008.md#first-answers)
  remain separate results.

Raw requests, responses, patches, logs and amendments remain in the frozen
private archive. All owned PostgreSQL/model processes and evaluation containers
were cleaned up. The published [quantitative summary](evaluation/results-20261008.json)
includes archive hashes and every attempt without those private captures.
See [reproduction limits](EVALUATION_20261008.md#evidence-and-reproduction-limits).
