# E4B with AHE: peer failures versus repeated own failures — 2026-10-10

This completed comparison produced **no fully passing repair**. Peer failure
details preserved more existing behavior at one checkpoint, made no difference
at another, and led to an answer without citations at the third. None of the
five tested patches repaired any test that the model's own previous patch had
failed. The result does not explain the [earlier shared run's answer-8 success](REPAIR_SHARING_20261010.md).

All six fresh answers used **Gemma 4 E4B IT-QAT with AHE** on
`scikit-learn__scikit-learn-25570`. The donor peer failures came from the
historical group without AHE. Checkpoint numbers refer to inputs before those
historical answers; these new answers did not feed into one another.

| Historical checkpoint | Added detailed feedback | Bug tests passed / 3 | Existing tests passed / 184 |
| --- | --- | ---: | ---: |
| Answer 2 | Different peer failure | 0 | 184 |
| Answer 2 | Repeated own failure | 0 | 171 |
| Answer 5 | Different peer failure | 3 | 182 |
| Answer 5 | Repeated own failure | 3 | 182 |
| Answer 7 | Different peer failure | Not run: missing citations | Not run |
| Answer 7 | Repeated own failure | 0 | 179 |

Success requires all **187 selected tests**, not the entire scikit-learn suite.
At answer 5, the prior own patch already passed the three bug tests. Both new
patches were byte-identical to that known failing patch and still failed two
existing `drop` cases; their 3/3 scores are not new repair gains.

At answer 2, the peer condition also repeated its own known failing patch.
The repeated-own condition introduced 13 regressions among tests both donors
previously passed, by treating column-name `Index` objects as data for
concatenation. Both new patches passed all 16 tests unique to the peer's prior
failure set. Fewer regressions therefore do not establish that peer-specific
counterexamples were incorporated into a new repair.

At answer 7, the repeated-own patch called a missing `self._transform_one`
attribute. The peer answer proposed edits but supplied no citations; its
functional correctness is unknown. The pair cannot support a complete
functional comparison.

## Fixed comparison

- Select every pre-success checkpoint from answers 2–8 with exactly one own
  and one peer detailed failure record, both previously tested on all 187
  tests, with different patches and failure sets. This yields answers 2, 5
  and 7. Answers 3/6 have different detail counts; answers 4/8 have identical
  donor patches and failure sets.
- Within each pair, change only the second detailed failure record: either
  retain the peer record or copy the own record exactly, including its identity.
  No second independent event is invented. Original code, source supplements,
  first own record, history index, AHE readback receipt and model/scoring
  settings remain fixed. Each answer proposes a complete patch against the
  original code.
- The common history index still contains peer summaries. This measures the
  added value of **peer details**, not a completely unshared control. Both
  conditions use the same source pool in AHE; the controller selects the
  displayed details. The common system instruction permits repeated records
  and clarifies that readback receipts may list records not displayed in full.
  Neither condition is an exact replay of the old historical request.
- Preserve original detail text without padding or added repair hints. Input
  tokens for peer/repeated-own conditions are 20,305/18,117 at answer 2,
  23,090/22,372 at answer 5 and 23,675/23,198 at answer 7. Content, identity,
  length and later token positions cannot be isolated from each other here.
- Generate all six answers before testing; allow no retries or operator
  repairs. Even identical patches receive separate fresh test containers.

The controller performed intake, retrieval and feedback selection; the model
answered without tools. Each condition used real Product stdio MCP and a fresh
isolated PostgreSQL database with four distinct LOGIN roles. Source readback
was checked byte for byte. Public fixtures used **TEST APPROVAL STUBS**, not
human semantic review or a validated automatic extractor. These results do not
measure autonomous model query strategy or SAT/conflict reasoning.

## Execution and accounting

All six model answers ended normally without input/output truncation or
infrastructure errors. Five patches each executed all 187 selected tests:
**935 test executions, no skips**. The missing-citation answer accounts for
**187 unexecuted test slots**, not observed test failures. Fresh original-code
and reference-patch controls scored 0/3 plus 184/184 and 3/3 plus 184/184,
respectively: another 374 tests, or **1,309 actual test executions** overall.

Citation quality is separate: answer-2 peer had a nonempty quotation attributed
to the wrong source record. The unchanged scorer tested its constructible
patch while retaining that citation error. Answer-7 peer had no citations and
was not submitted. The other four answers passed the citation checks.

The six formal runs made **1,164 MCP calls** across six fresh databases and
24 distinct LOGIN roles. The recorded audits verified cleanup of those
databases, six owned model services and seven evaluation containers, including
the two controls. Preflight activity is excluded from these formal totals.

## Scope, versions and evidence

These are three related checkpoints from one issue and one trajectory, with
one fresh answer per condition and an operator who knew the historical
results. They neither establish a general convergence advantage nor show
that shared information is useless. Failure records reached the model, but
the model could still resubmit exactly the same failed patch. The mechanism
behind the historical success remains unresolved.

- Product revision: `068390090d4951f671346d68069786034f9bfcc0`.
- scikit-learn base: `cd25abee0ad0ac95225d4a9be8948eff69f49690`.
- Model: `gemma4:e4b-it-qat`, digest
  `ee665637121887cf3befff38abbb1be4ee117c7db867d97a67e29049ecd7e15f`.
  Ollama 0.40.0; thinking enabled; temperature 0; seed 42; context 65,536;
  output limit 12,288.

The [data summary](evaluation/repair-peer-failures-20261010.json) preserves the
six result rows, separate citation outcomes, token counts and provenance
hashes. Raw requests, answers, patches, MCP receipts, test logs and runners
remain in a sealed private archive of 400 files. Its seal SHA-256 is
`8e4d8fc471b11000f1b939c4b7238975ebf6e1863a8047b77e47e7e01f3c9731`.
Hashes identify unavailable evidence; this repository does not provide a
complete reproduction bundle. This documentation commit adds no Product
runtime changes and does not rerun models, race tests or the whole Product suite.

The public case comes from [SWE-bench](https://github.com/SWE-bench/SWE-bench)
and [scikit-learn](https://github.com/scikit-learn/scikit-learn/issues/25570).
Upstream retains its [scikit-learn license](https://github.com/scikit-learn/scikit-learn/blob/cd25abee0ad0ac95225d4a9be8948eff69f49690/COPYING)
and [SWE-bench license](https://github.com/SWE-bench/SWE-bench/blob/main/LICENSE).
No third-party source, test patch or reference solution is republished here.
