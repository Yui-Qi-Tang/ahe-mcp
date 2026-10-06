# Product replay supplement — 2026-10-06

**Native Product tests pass after two test-timeout fixes. Autonomous Sol SWE
repairs passed 2/3; scikit-learn still fails its three target tests.** The 2026-10-06–07
continuation is bounded to those fixes and three autonomous Sol SWE workflows;
SWE-300 and older presentation grids are deferred. Earlier failures and model
results remain below with their original scope.

Product base: `844a3a6530120ea5514a2e59bc1be51fa2f82cba`, schema 55, role policy v7,
Go 1.27.1 and PostgreSQL 18.6. Only two tracked Go test files changed in this
continuation; Product runtime logic, schemas and query limits are unchanged.

This repository's `lab/` directories contain private verification artifacts.

## Test timeout fixes and revalidation

The user authorized fixing the observed race-mode timeouts without changing
Product main logic. The changes are now in the tracked test helpers:

- MCP child processes inherit the caller's bounded workflow context. The old
  independent 60-second process lifetime could expire while the enclosing test
  still had time to run. Existing production-test callers remain bounded at
  180, 240 or 600 seconds; child cancellation and cleanup remain active.
- Han fixture preparation uses a transaction-local 30-second `ANALYZE` limit
  and a 120-second total phase limit. The runtime query limit remains two seconds.

No assertions, cases or race checks were removed. This fixes test-lifetime and
fixture-preparation limits; it is not a Product concurrency-algorithm change.

| Fresh validation after the fixes | Pass / fail / skip |
| --- | ---: |
| Selected original contract / SWE packet cases, normal | 214 / 0 / 0 |
| Same selection, race | 214 / 0 / 0 |
| Repository ordinary, normal | 1,834 / 0 / 0 |
| Repository ordinary, race | 1,834 / 0 / 0 |
| PostgreSQL integration / consistency / real solvers, normal | 3,582 / 0 / 13 |
| Same repository matrix, race | 3,582 / 0 / 13 |
| Han query, separately selected | 63 / 0 / 0 |
| Han scale, separately selected | 1 / 0 / 0 |
| Han plan, separately selected | 1 / 0 / 0 |

These are overlapping leaf counts. Parent tests and packages also had no failures.
`make verify`, integration-tagged `go vet` and `make build-all` passed. Child Go
builds retained race instrumentation. Each batch preserved its source snapshot
and cleaned its owned PostgreSQL cluster.

The broad matrices still report 13 intentional opt-in skips. This continuation
explicitly reran four of those entry points: SWE packets, Han query, Han scale
and Han plan. The other nine retain their successful runs in the preceding
supplement; they were not repeated after these two test-only changes. Thus the
combined record covers all 13, but the newest broad command alone does not.
The original failures under the former limits are retained below.

## Autonomous Sol SWE results

On **2026-10-07 (Asia/Taipei)**, three fresh **Sol 6.1 high** agents independently
queried live Product AHE and edited isolated case copies. **2/3 unique issues
resolved in official SWE-bench evaluation.** There was one workflow and one sealed
patch per issue, with no model replacement or post-evaluation repair retry.

| Issue | AHE queries | Edits | Official result | Fail-to-pass / pass-to-pass tests passed |
| --- | ---: | ---: | --- | ---: |
| astropy__astropy-12907 | 7 | 1 | Resolved | 2 / 13 |
| django__django-10914 | 6 | 1 | Resolved | 1 / 98 |
| scikit-learn__scikit-learn-25570 | 10 | 1 | Not resolved | 0 / 184 |

Each agent received only the issue's canonical ID, its fixed gateway and the
protocol. It obtained issue/code evidence through the actual 26-tool read-only
Query MCP, using `get_evidence_record`, `search_evidence_records` and
`get_grounded_evidence_brief`. The parent did not choose subsequent evidence or
give repair answers. The three agents made **23 AHE queries and three edits**.
The mechanical editor required a previously returned exact source anchor and
allowed only that case's files. No local tests were exposed to the agents;
official evaluation followed final-patch sealing.

All **41 agent tool calls** matched the gateway trace. All **27 quoted passages**
were present in the cited frozen source and returned AHE evidence. Main-agent
review, completed before these patches' official evaluation, identified no
unsupported assertion in the three explanations. This is a single unblinded
reviewer's bounded finding, not proof of general explanation reliability.
The Django answer explicitly attributed its storage-mode observation to the
issue report and disclosed the absent save/chmod implementation; it did not
present that mechanism as independently observed runtime evidence.

**scikit-learn did not resolve:** all three target tests failed while 184 existing
tests passed. The patch applied cleanly, but changed the error to
`generator raised StopIteration`. It stopped producing the empty passthrough
transformer; `_update_fitted_transformers` still consumed a fitted transformer
for that entry, then exhausted the iterator on the numerical entry. That
consumer's definition was absent from all 15 frozen records for this issue.
The agent disclosed the missing lifecycle implementation, yet its proposed
repair was incomplete. The pre-test quotation/static review remains recorded;
the broader repair-sufficiency claim was contradicted by execution. Therefore
the three explanations are **not** reported as three verified correct diagnoses.
This exposes a bounded evidence/repair gap, not a demonstrated AHE retrieval
failure on complete stored evidence. Adding the missing source was not tested
in this run, and no evaluator feedback or replacement attempt was given.

The source pool was the same 40 pinned records used for the preceding three-issue
Gemma experiment. The protocol is different: autonomous queries and edits,
different model/runtime/budget, and no new matched static-control Sol arm.
Therefore **Sol 2/3 and Gemma 31B D 2/3 are not a controlled model comparison**,
nor evidence that AHE caused a gain. These are three familiar development issues,
not SWE-300 or population-wide performance.

### Execution boundaries and controls

- Each case had its own PostgreSQL instance, Query scope and source-copy editor.
  The gateway enforced allowed operations; the agent runtime itself did not
  enforce a whole-tool/filesystem sandbox. AHE-only access is supported by full
  tool-call audit, not by claiming hard isolation of the agent.
- Limits were 30 minutes, 40 AHE calls and 20 edit attempts per workflow.
  All three finished within those limits. Source admission used test approval
  fixtures, not real human source review.
- Three base-fail and three gold-pass controls from the preceding supplement
  were reused after verifying their report hashes, pinned revisions and unchanged
  evaluator inputs/harness. They were not rerun or counted as model successes.
- Parent/child encrypted assignment hashes matched. Prepared plaintext prompts
  are retained and dispatch is operator-attested; the local logs provide no
  provider plaintext receipt. This limitation is distinct from tool-trace audit.
- One initial third-cluster `initdb` hit macOS shared-memory capacity before any
  model call. After the first two sessions were cleaned, serial setup succeeded.
  The failed setup, subsequent setup and all cleanup receipts remain recorded;
  no model attempt was discarded. No system limits or Product logic changed.
- An initial audit falsely compared encrypted dispatch text with plaintext.
  Its report is retained; the corrected audit compares opaque delivery hashes
  and separately audits concrete calls. This was an audit error, not an observed
  source-prompt substitution.

All owned PostgreSQL and MCP processes were stopped and their temporary data and
sockets removed. Official evaluator containers were removed; cached images and
raw reports remain available for reproduction. Product implementation did not
change during these workflows.

## Fresh six-case model results

The original six-case suite combines three synthetic families (AND prerequisites,
stale review, and independently reviewed `implements` relations) with three SWE
issues. There are 55 unique presentations per model: 46 synthetic and nine SWE.
Every presentation received one new inference attempt, without answer repair or
selection among retries. This is 110 attempts, not 110 independent issues.

- A: original source text and operation history.
- B: the original intermediate AND presentation; four synthetic cases only.
- C: structured/static evidence.
- D: that evidence with current Product results.

### Official SWE repairs

Each entry is resolved attempts / all three issue attempts in that arm.

| Model | A: source/history | C: static | D: with AHE |
| --- | ---: | ---: | ---: |
| Gemma 4 E4B IT-QAT | 1/3 | 0/3 | 0/3 |
| Gemma 4 31B IT-QAT | 1/3 | 1/3 | 2/3 |

| Model / issue | A | C | D |
| --- | --- | --- | --- |
| E4B / Astropy 12907 | Truncated | Truncated | Truncated |
| E4B / Django 10914 | Resolved | Submitted, not resolved | Abstained |
| E4B / scikit-learn 25570 | Truncated | Truncated | Truncated |
| 31B / Astropy 12907 | Truncated | Truncated | Resolved |
| 31B / Django 10914 | Resolved | Resolved | Resolved |
| 31B / scikit-learn 25570 | Truncated | Truncated | Truncated |

Eleven of 18 attempts reached the generation limit. Their missing patches are
unsolved attempts, not silently excluded observations. Six patches were submitted
to the official harness; five resolved. E4B's Django A answer had a citation
failure despite its passing patch. All three 31B Django explanations contained
an unsupported assertion despite passing patches. Patch success, citations and
explanation fidelity therefore remain separate outcomes.

Before model evaluation, the three unmodified base revisions failed their
expected fail-to-pass tests while retaining pass-to-pass tests; all three gold
patches resolved. These six controls validate this bounded harness setup and are
not model successes. The official harness revision was `f7bbbb2` with the retained
environment's pip-24 build pin recorded separately. Gold patches and evaluator
tests were kept out of model input.

Fresh Product gates ran actual stdio MCP intake, independent relation review,
persistence and readback for all three SWE source packets. That gate alone is
not a SWE repair score. The subsequent model generation and official evaluation
above are the steps missing from the earlier Product replay.

### Synthetic decisions and explanations

Each cell below is **answers with at least one wrong decision field / answers
with an unsupported assertion**, over the stated number of presentations.

| Model | A, n=14 | B, n=4 | C, n=14 | D, n=14 |
| --- | ---: | ---: | ---: | ---: |
| E4B | 4 / 3 | 2 / 2 | 6 / 5 | 4 / 4 |
| 31B | 0 / 0 | 0 / 0 | 0 / 0 | 0 / 0 |

These axes overlap and must not be added. Main-agent manual review covered all
110 visible responses, including the partial visible text of one truncated
attempt. It was unblinded and had no independent rater. No unsupported assertion
identified is not proof of complete explanation quality; missing explanations
and unresolved judgments remain separately recorded.

### Runtime and comparison limits

Both Gemma model digests, original schemas and sampling options were preserved:
temperature 0, seed 42, context 32768, generation limit 4096, thinking enabled.
The owned local Ollama server had cloud access disabled and was stopped after
generation. Ollama changed from historical **0.34.1** to **0.34.4**. Of 110 requests,
70 were identical across all request fields; 40 prompts included freshly produced
Product receipts. All original A/C SWE requests were identical.

The comparison changes the overall evidence presentation, not a single isolated
graph mechanism. Familiar development cases, a small dependent sample and the
runtime change leave causal attribution and general reliability unestablished. In particular,
neither the new truncations nor score changes have been attributed to a uniquely
identified cause. The historical 31B D score of 3/3 is not the fresh result of 2/3.

## Fresh lifecycle results

The original 39 packets across two models produced **78/78 completed answers**,
with no technical failures or retries. C0 withholds lifecycle information;
C1 supplies full static records; D additionally supplies Product currentness
results. All 78 original request bodies matched byte for byte. The same model
digests and sampling options were retained, with Ollama 0.34.1 to 0.34.4 again
preventing a same-runtime replication claim.

| Model / arm | All three lifecycle fields correct | Wrong explanation | Unresolved explanation | Citation-contract error |
| --- | ---: | ---: | ---: | ---: |
| E4B / C1 static | 9/18 | 9/18 | 2/18 | 2/18 |
| E4B / D Product results | 15/18 | 2/18 | 5/18 | 3/18 |
| 31B / C1 static | 13/18 | 5/18 | 0/18 | 2/18 |
| 31B / D Product results | 18/18 | 0/18 | 0/18 | 18/18 |

All 78 answers preserved both structural decisions. The six shared C0 answers
correctly retained unknown lifecycle states; they are not multiplied across
the 36 C1/D pairs. E4B had nine pairs with improved fields and nine ties; 31B had
five improvements and 13 ties. Neither model had field regressions.

This run observed better decision scores alongside worse citation compliance;
it **did not meet the original joint success criterion**. This does not establish
that either change caused the other or that the combination is unavoidable.
E4B D still answered `no` instead of `unknown` for
the joint-currentness condition in three cases. Two explanations explicitly
made that false inference; the third only said that `yes` could not be proved,
so its wrong decision did not automatically count as a false factual assertion
in the explanation. That reason still does not justify the answer `no`.
All 18 31B D answers cited field names such as `ahe_currentness_results` instead
of exact evidence identifiers required by the frozen citation contract. Their
correct decisions and explanations do not erase those citation failures.

The main agent manually reviewed every answer with model and C1/D labels hidden,
then sealed and hashed all 78 labels before reading the identity map. C0 data
availability and receipt language can reveal conditions: this is single-rater
partial masking, not independent annotation. Ambiguous explanations remain
unresolved; this reviewer uncertainty is separate from model abstention or a
warranted `unknown` answer. Original aggregation code recounted all 78 raw captures offline.
The owned server was stopped. An initial obsolete runtime assertion stopped
before any model call; its failure and the corrected runtime declaration are
retained separately.

Historically, lifecycle-all-correct scores were E4B C1/D **0/18 and 0/18**, and
31B **15/18 and 15/18**. Both runs remain recorded; their differences cannot be
attributed solely to Product. The compact result file includes all 36 paired
field changes, six shared controls, per-answer labels and response hashes.

## Earlier supplementary tests and original case coverage

| Batch | Leaf pass / fail / skip |
| --- | ---: |
| Original selected contract / SWE packets, normal | 214 / 0 / 0 |
| Same selection, race | 213 / 1 / 0 |
| Additional Product topology, race | 40 / 0 / 0 |
| Repository ordinary | 1,834 / 0 / 0 |
| Repository ordinary, race | 1,834 / 0 / 0 |
| Repository integration / consistency / real solvers | 3,582 / 0 / 13 |
| Same repository matrix, race | 3,576 / 6 / 13 |

These batches overlap. Parent-test and package failures were checked in addition
to leaf counts. Race instrumentation includes child Go builds. Real CaDiCaL,
DRAT verification and Z3 executed. The repository-delta regression passed 20
repetitions; `make verify` and `make build-all` passed.

The [earlier 214-case inventory](PRODUCT_REPLAY_20261006.md#what-the-214-checks-contain)
describes the selected cases. The supplemental relation contract now explicitly
checks the relationship's own source proposal as well as both endpoint sources,
at both isolation levels. Some invalid-material controls run in the external
test adapter; they must not be represented as new native semantic rejection.

| Additional original experiment | Execution and observation |
| --- | --- |
| Han outcome, multisurface and practical anchor queries | 144/144, 72/72 and 96/96 actual queries passed; original plan contracts and SQL controls passed |
| Upgrade baselines | Two archive clones retained the same role identities, settings, memberships and old data across upgrade; 78 to 92 tables and 46 to 55 migrations |
| Han query and scale | 63 query leaves and the original scale entry point passed |
| Original consistency SQL controls | 16/16 leaves passed with race: eight schema-drift controls, seven direct-SQL rejection cases and empty rollback/reapply; immutable history and populated rollback checks also executed |
| Original upgrade-to-solver workflow | Passed with race and real CaDiCaL: preserve evidence and role identities through upgrade, change the same writer's profile, persist checked SAT assignments, then compare two query users' reads |
| Coverage and alternative-support topology | Six coverage graphs plus 34 support graphs passed Product topology checks |
| External support / conflict / withdrawal algorithms | Original eight support masks, eight lossy controls, four conflict probes and 24 withdrawal prefixes reran; this is not adoption of evidence selection into Product |
| External search expansion | Original six paired queries reran; query 5 still had no hit, noise increased and no top-three improvement was observed |
| H1–H3 finite enumerators | Original finite checks reran; no claim of a general theorem or native Product policy |
| Supplied-material boundary and stress evaluators | All 17 boundary and 24 stress candidates reran; shared omission remained admitted, and the stress evaluator still had 12 false accepts and three false rejects |
| Original lifecycle native export | All 18 states and 39 packets regenerated; the 78 model attempts are tracked separately below |

The fixed external semantic-checker failures are not erased by native storage
passes. No new natural-language extractor, Pouch execution or Detective change
is claimed.

The additional SQL and upgrade cases use explicit private adapters. SQL controls
include the separate watch registry in setup and immutable-history checks. The upgrade workflow uses
Product's frozen schema-52 role baseline through schema 55; the same source-claim
reviewer identity then changes to `core-records`. It retains the original
evidence/proof/readback assertions using the Product `core-records` permission set. Both owned databases
were cleaned. This role case produced SAT results; it did not exercise DRAT
checking of an UNSAT proof. Product runtime files and existing test files were unchanged during that earlier phase.

### Failures and separate follow-up

The original Han plan run failed while preparing 50,000 rows: `ANALYZE
proposal_occurrences` exceeded its two-second statement timeout. A serial repeat
with the same limits failed at the same stage. The selected SWE race run failed
on scikit-learn; the same-limit serial repeat failed on Astropy and scikit-learn.
The full integration-race run failed on six final Core material cases. Diagnostic
replays observed subprocess response deadlines and an expired process context.

A prospectively recorded, separate follow-up changed only private test overlays:

- MCP launchers inherited the existing suite deadlines (240 seconds for Core
  material, 600 seconds for SWE), replacing the independent 60-second process
  lifetime.
- Only the Han preparation `ANALYZE` phase used a 120-second total budget and a
  transaction-local 30-second SQL budget. Runtime query limits remained two seconds.

All **48 selected MCP leaf checks with race** and the **complete Han plan entry
point** then passed. Frozen inputs, assertions and Product runtime code were
unchanged. This lets the formerly interrupted assertions execute; it does not
prove the original timeout contract passes or uniquely explain every old failure.
At that stage the changes existed only in private overlays. The authorized
continuation above has now integrated them into the two tracked test helpers
and rerun the complete native matrices.

The broad matrices retain their 13 skips. Every skipped entry point was selected
in separate execution: 12 have an unmodified passing run; Han plan passed only
the explicitly changed preparation-budget follow-up. Most historical query
experiments followed their original normal-mode protocol, so this is not a claim
that all 13 separately passed with race. Full logs retain failed repeats too.

Two preparation mistakes were also preserved: a private diagnostic `_test.go`
copy was accidentally discovered by `go vet ./...`; after preserving its bytes as
`.go.txt`, the same integration-tagged vet passed. Initial sandbox-denied PG
initialization attempts ran no Go tests; the unchanged operations subsequently
ran with approved access in fresh directories. All owned PostgreSQL clusters
were stopped and removed.

## Outstanding work

- SWE-300 is explicitly deferred by the user. Older extraction/behavior research
  and the complete historical presentation grids are outside this continuation;
  their coverage is not inferred from the current three issues.
- The old fixed-packet Sol six-case pilot remains partial: eight answers, two
  with known manually transferred input differences. Those records are retained;
  the new autonomous three-issue protocol does not complete or replace that grid.
  There is no pending request for a file-reading exception in the current protocol.
- Distinct older protocols include AND summary v1 (eight treatment attempts),
  summary v2 (24 paired attempts), and coverage marker v3 (16 attempts). Their
  entire grids are not replaced by the six-case suite or earlier 100-answer subset.
  The discovered 850 report documents are not a deduplicated experiment denominator.

No complete-history claim is made. Repository commits, merges and pushes are
tracked separately from experiment outcomes.

## Evidence

[Compact machine-readable results](evaluation/product-replay-20261006-supplement.json)
contain every SWE attempt, official report hashes, per-arm scores and native-log
hashes. Full private inputs, responses, reviews, official reports, PG cleanup
receipts, failed attempts and overlays remain under
`lab/complete-replay-20261006/` in the isolated Product worktree; they are not
published as part of this document.

The [earlier Product report](PRODUCT_REPLAY_20261006.md) retains its original
100-answer Gemma/Sol results and execution date. Those answers were not regenerated
by this continuation. The [historical evaluation](EVALUATION.md) remains historical.
