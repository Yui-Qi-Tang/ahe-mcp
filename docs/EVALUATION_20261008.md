# Results and methods — 2026-10-08

The [README](../README.md#evaluation) shows the main scores. This page separates
software verification, first answers, and diagnostics; the
[repair-feedback report](REPAIR_FEEDBACK_20261008.md) records all 29 stages of
the latest E4B experiment. The [public data](evaluation/results-20261008.json)
contains per-attempt outcomes, test counts and source-record hashes.

The 2026-10-07 Product tests and 2026-10-08 SWE executions used Product commit
`7c21b31952088337d60a99614dce016b7e333371`, schema 55 and role policy v7.
They were completed before this documentation update. No model, database or
software test was rerun merely to prepare these pages.

## Read the scores

| Term | Meaning |
| --- | --- |
| Fixed | A submitted patch passed every selected official bug test and existing test. |
| Not fixed | Tests ran, but at least one required test failed. |
| Declined | The model chose not to supply a repair. There is no patch-test verdict. |
| Unfinished | The output limit was reached without a deliverable patch. |
| Invalid patch | The submitted patch prevented test collection; the affected tests did not run. |
| No valid patch | The answer could not be submitted under the frozen edit/citation rules. |

For scikit-learn, **3/3 bug tests and 168/184 existing tests** means the original
bug tests passed but **16 existing tests broke**. It is not a successful repair.
Unsubmitted answers remain in the planned-attempt denominator. Unexecuted tests
are neither passes nor observed test failures.

## First answers

On 2026-10-08, each model answered three familiar public SWE-bench issues once
per condition. There were 12 new generations, with no retry or manual patch
repair. “Without AHE” received the same source excerpts and external syntax
candidates. “With AHE” additionally received real Product operation/query
results. The comparison is not against an unassisted model with only an issue.

| Issue | Model | Condition | Outcome | Bug tests passed | Existing tests passed |
| --- | --- | --- | --- | ---: | ---: |
| Django 10914 | E4B | Without AHE | Not fixed; comment-only change | 0/1 | 98/98 |
| Django 10914 | E4B | With AHE | Declined; not tested | — | — |
| Django 10914 | 31B | Without AHE | Fixed | 1/1 | 98/98 |
| Django 10914 | 31B | With AHE | Fixed | 1/1 | 98/98 |
| Astropy 12907 | E4B | Without AHE | Indentation error; 15 tests unexecuted | — | — |
| Astropy 12907 | E4B | With AHE | Not fixed | 0/2 | 13/13 |
| Astropy 12907 | 31B | Without AHE | Fixed | 2/2 | 13/13 |
| Astropy 12907 | 31B | With AHE | Fixed | 2/2 | 13/13 |
| scikit-learn 25570 | E4B | Without AHE | 17 existing tests broke | 3/3 | 167/184 |
| scikit-learn 25570 | E4B | With AHE | Output limit; not tested | — | — |
| scikit-learn 25570 | 31B | Without AHE | Fixed | 3/3 | 184/184 |
| scikit-learn 25570 | 31B | With AHE | Output limit; not tested | — | — |

**E4B: 0/3 without and 0/3 with AHE. 31B: 3/3 without and 2/3 with AHE.**
The fixed-budget result does not show an AHE improvement. E4B's Django refusal
was judged overly cautious in the separate, non-blind explanation review;
abstaining is not automatically a correct use of evidence.

| Issue | Approximate repair complexity | Reason |
| --- | --- | --- |
| Django 10914 | Low | Change the upload-permission default explicitly requested in the issue. |
| Astropy 12907 | Medium | Preserve dependency information in nested model matrices. |
| scikit-learn 25570 | Medium | Align transformer outputs and column names when columns are empty. |

These are post-run human ratings, not official SWE-bench difficulty labels.
All three reference fixes change one executable line in one source file;
patch size does not measure diagnosis difficulty.
[Rating basis and source limitations](PRODUCT_REPLAY_20261006_SUPPLEMENT.md#issue-complexity).

### Execution method

- Models: `gemma4:e4b-it-qat` and `gemma4:31b-it-qat`, retaining the
  [pinned model digests](EVALUATION.md#decision-field-scores-per-case-findings-and-fixed-method).
  Ollama 0.40.0, thinking enabled, temperature 0, seed 42, context
  32,768 and output limit 8,192. No input truncation or HTTP failures occurred.
- The controller started fresh PostgreSQL for all three issues, wrote 40 source
  records, admitted 26 derivations with 52 edges using **TEST APPROVAL STUBS**,
  and made 367 recorded stdio MCP calls using 12 runtime login roles. Source
  readback was checked. Models consumed fixed packets; they did not choose or
  execute their own live queries. This does not validate semantic extraction.
- Six fresh original/reference controls behaved as expected, executing 602
  tests. Nine model patches were submitted in fresh containers: eight completed
  716 test executions; the E4B Astropy patch prevented collection of 15 tests.
  Two capped answers and one refusal produced no patch. Controls are not model
  scores. SWE-bench 4.1.0, Docker 29.8.2 and a 900-second evaluation timeout were
  used, with pinned source revisions and container images.
- A read-only package observer initially failed on the older Django Python
  environment. Its failure was retained, the observer was corrected, and all
  six controls ran afresh before model generation. Third-party package versions
  matched within each case; patched Astropy builds changed project-version
  metadata, which was recorded rather than described as byte-identical setups.
- The parent separately inspected the 10 complete visible answers: seven had
  unsupported or source-contradicted claims, two had supported cause statements,
  and one identified a real gap but refused too broadly. One citation was not
  verbatim. This non-blind review is separate from functional test results.

### Output-budget diagnostic

Only scikit-learn 25570 was rerun with output limit **12,288**. The four new
requests changed only that budget field. AHE receipts were retained from the
full run: **no new PostgreSQL instance or MCP call** in this diagnostic.

| Model | Condition | Output tokens | Bug tests | Existing tests | Result |
| --- | --- | ---: | ---: | ---: | --- |
| E4B | Without AHE | 7,273 | 3/3 | 167/184 | 17 regressions |
| E4B | With AHE | 12,288 | — | — | Still capped; no final answer |
| 31B | Without AHE | 5,981 | 3/3 | 184/184 | Fixed |
| 31B | With AHE | 9,657 | 3/3 | 184/184 | Fixed |

Three model patches executed 561 tests; two fresh controls executed another
374. The 31B AHE answer finished beyond the former cap and passed the selected
tests. This resolves that delivery limit for this run; it does not erase the
negative 8,192-token result or demonstrate extra repair benefit. Passing patches
also retained unsupported explanation claims; their general semantic equivalence
was not established.

## E4B diagnostics and feedback

These are separate experiments on the same issue, not extra independent issues
or a single success-rate denominator.

| Experiment | Fresh model answers | Fresh AHE workflow? | Observed result |
| --- | ---: | --- | --- |
| Lossless receipt presentation | 2 | No; retained receipts | Original form capped; compact form finished in 7,512 tokens but included a no-op edit. Neither patch was submitted. |
| Plain-language terminology | 8: 4 repairs, 4 understanding answers | No; retained receipts | State/limit answers improved from 6/7 to 7/7 in each repetition. Both versions omitted the second source in all three source pairs; no valid repair in either condition. |
| Initial short feedback loop | 6 | Yes; 3 snapshots, 474 recorded MCP calls | Both first answers passed bug tests but broke 16 existing tests. Both second corrections scored 0/3 and 172/184. Correction 3 was not generated because the packet exceeded the frozen character limit. |
| Up to 30 corrections | 60, including 6 confirmations | Yes; 35 snapshots, 11,856 recorded MCP calls | AHE first passed at correction 24, without at 28; each successful input passed 3 fixed-seed reruns. |

The compact receipt preserved all fields and could reconstruct the original
bytes; that does not prove equivalent model understanding. The terminology
experiment corrected a scorer-variable collision by rescoring original answers,
without regenerating them. Each of these two diagnostics separately reran two
controls (374 tests), but no model patch reached testing.

The short loop executed 935 model tests and had one refusal (187 unexecuted
tests). It supplied the same explicitly requested 27-line, 1,200-character
source addition to both groups. The later 30-correction experiment changed
context size and feedback/history selection too, and supplied no additional
source. Its success cannot be attributed solely to allowing more iterations.
[Full method, amendments and every round](REPAIR_FEEDBACK_20261008.md).

## Evidence answers

The latest evidence-answer scores remain the **2026-10-06** run; the SWE work
did not rerun or replace them.

| Check | E4B without → with AHE | 31B without → with AHE |
| --- | ---: | ---: |
| Answers with unsupported claims | 5/14 → 4/14 | 0/14 → 0/14 |
| Correct evidence-status decisions | 9/18 → 15/18 | 13/18 → 18/18 |
| Citation-rule failures with AHE | 3/18 | 18/18 |

The first two rows use different question sets. A correct status decision is not
a guarantee of correct citations or faithful reasoning.
[Claim evaluation](PRODUCT_REPLAY_20261006_SUPPLEMENT.md#fresh-six-case-model-results)
and [lifecycle evaluation](PRODUCT_REPLAY_20261006_SUPPLEMENT.md#fresh-lifecycle-results)
preserve the original method and results. The separate Sol 6.1 high repair
comparison remains [2/3 in both conditions](PRODUCT_REPLAY_20261006_SUPPLEMENT.md#sol-without-ahe-comparison),
with Django and Astropy fixed and scikit-learn unresolved.

## Product tests

Completed 2026-10-07 on Go 1.27.1, macOS arm64 and PostgreSQL 18.6. Counts are
leaf Go test cases; groups overlap and must not be added as distinct coverage.

| Group | Normal passes | Race passes | Unexpected failures | Raw skips |
| --- | ---: | ---: | ---: | --- |
| General tests | 1,834 | 1,834 | 0 | 0 |
| PostgreSQL integration | 3,594 | 3,594 | 0 | 13 in each mode; all supplemented below |
| Selected contract/SWE fixtures, identity/withdrawal/Core stress and diagnosis | 214 | 214 | 0 | 0 |
| Chinese Query, scale and query plans | 65 | 65 | 0 | 0 |
| Historical restore/upgrade, Query, SQL and fixed-plan checks | 325 | 325 | 0 | 0 |

All **1,065 top-level Go test entrypoints** on this platform had fresh pass
records in both modes. The 26 original skip records remain in the logs; all 13
entries were separately run in both modes, leaving **zero unrun entries**.
`make verify`, all-tag `go vet` and `make build-all` also completed successfully.
Ten benchmark combinations per mode ran once as execution smoke checks, not
as a performance study.

Integration used `integration,consistencylab,resolverintegration,acceptance`,
with `labreplay,diagnosislab` and explicit opt-in environments for supplements.
Go tests used `-count=1`; race child builds inherited `GOFLAGS=-race`.

Each normal/race concurrent-client run used **17 independent MCP clients,
63 request/reply pairs and 8 contention barriers**, with four distinct waiting
PostgreSQL backends verified at each barrier. Checks covered concurrent extractor
registration, one-time admission, simultaneous readers, identity replay,
single-winner withdrawal and recovery after killing a selected reader or lock
holder. Each run asserted three expected version conflicts and one injected
`57P01`; unexpected errors were zero. The interrupted query still failed; a
later explicit query recovered on the same MCP connection. Whole-server crashes,
network partitions and uncertain write commits were not tested by this result.

Historical databases were restored and upgraded from 78 tables/46 migrations to
92 tables/55 migrations while preserving the same role OIDs, attributes,
memberships, old data and prior migration hashes. Schema-only upgrades correctly
rejected stale role policy until the formal role upgrade ran. Role recreation
was not used to substitute for upgrade testing.

The 214 contract fixture passes include expected rejection/boundary checks. They do
not establish correct extraction, autonomous model reasoning or successful SWE
repairs. SWE-300 was not run.

## Evidence and reproduction limits

The public [data summary](evaluation/results-20261008.json) is an explicit
projection of archived results: numerical outcomes, statuses and hashes, without
machine paths, credentials, prompts, raw model output or private runner commands.
Its source identifiers describe private records; they are not public download
links. Historical scores remain in [the report index](EVALUATION.md).

Raw requests, model outputs, MCP traces, review records, patches, container logs,
controls and manifests are retained privately. The feedback archive contains
2,220 hashed files and was verified unchanged while preparing these docs.
Hashes identify the records used; they do not let a public reader independently
reproduce unavailable inputs. A complete public replay bundle is not yet provided.

Public Go checks and their database prerequisites are documented in
[INSTALL](../INSTALL.md#optional-database-and-process-tests). The published SWE
source fixtures check intake/query behavior; running them alone does not recreate
these model-answer or official patch scores. Public SWE inputs retain their
upstream licenses; no third-party source or reference patch is republished here.
