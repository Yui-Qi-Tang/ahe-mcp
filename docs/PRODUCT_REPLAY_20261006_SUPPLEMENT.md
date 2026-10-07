# Product replay supplement — 2026-10-06

**Latest Gemma SWE comparison: E4B C/D 0/3 → 0/3;
31B C/D 1/3 → 2/3.** Twelve new model attempts were made on
2026-10-07 using retained AHE receipts; 7 were truncated. The denominator includes
unsubmitted attempts. Native Product tests and the separate Sol 2/3 versus 2/3
comparison retain their earlier execution scope. SWE-300 remains deferred.

Product base: `844a3a6530120ea5514a2e59bc1be51fa2f82cba`, schema 55, role policy v7,
Go 1.27.1 and PostgreSQL 18.6. Only two tracked Go test files changed in this
continuation; Product runtime logic, schemas and query limits are unchanged.

This repository's `lab/` directories contain private verification artifacts.

## Gemma SWE comparison: 2026-10-07

The user selected **Gemma 4 E4B IT-QAT** and **Gemma 4 31B IT-QAT** from the README.
This run generated **12 new fixed-packet answers**: two models × the original
three SWE issues × C/D. It does not reuse old answers as new executions.

| Model | Without AHE results | With retained AHE results |
| --- | ---: | ---: |
| Gemma 4 E4B IT-QAT | **0/3** (1 tested) | **0/3** (0 tested) |
| Gemma 4 31B IT-QAT | **1/3** (1 tested) | **2/3** (2 tested) |

C supplies the same original sources and external syntax-navigation candidates;
D appends the retained, actually executed Product AHE derivation/query readback.
The numerator is the number officially confirmed resolved; each denominator is
**three planned attempts**. An unsubmitted answer has no official test verdict.

| Model | Arm | Resolved / attempts | Patches submitted | Truncated | Abstained | Tested, not resolved | Evaluation incomplete |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| e4b | C | 0/3 | 1 | 2 | 0 | 1 | 0 |
| e4b | D | 0/3 | 0 | 2 | 1 | 0 | 0 |
| 31b | C | 1/3 | 1 | 2 | 0 | 0 | 0 |
| 31b | D | 2/3 | 2 | 1 | 0 | 0 | 0 |

| Model | Arm | Issue | Outcome | Bug-fix tests passed | Regression tests passed |
| --- | --- | --- | --- | ---: | ---: |
| e4b | C | astropy__astropy-12907 | Truncated; not submitted | — | — |
| e4b | D | astropy__astropy-12907 | Truncated; not submitted | — | — |
| e4b | C | django__django-10914 | Tested: not resolved | 0/1 | 98/98 |
| e4b | D | django__django-10914 | Abstained; not submitted | — | — |
| e4b | C | scikit-learn__scikit-learn-25570 | Truncated; not submitted | — | — |
| e4b | D | scikit-learn__scikit-learn-25570 | Truncated; not submitted | — | — |
| 31b | C | astropy__astropy-12907 | Truncated; not submitted | — | — |
| 31b | D | astropy__astropy-12907 | Tested: resolved | 2/2 | 13/13 |
| 31b | C | django__django-10914 | Tested: resolved | 1/1 | 98/98 |
| 31b | D | django__django-10914 | Tested: resolved | 1/1 | 98/98 |
| 31b | C | scikit-learn__scikit-learn-25570 | Truncated; not submitted | — | — |
| 31b | D | scikit-learn__scikit-learn-25570 | Truncated; not submitted | — | — |

31B's additional success is Astropy: C reached the generation limit, while D
finished a patch that passed both bug-fix tests. This is an improvement under
the fixed generation budget; it does not establish that C would fail with more
time. E4B's Django C patch only changed a comment, leaving the default `None`;
the bug-fix test expected `0o644`. All four scikit-learn attempts were truncated,
so this run produced no official repair verdict for that issue.

### Issue complexity

These are **rough manual ratings added after the run**, based on the reasoning
needed to diagnose and repair each issue. The frozen SWE dataset used here has
no `difficulty` field. These ratings are not official SWE-bench labels, measured
completion times, or ratings inferred from model success and truncation.

| Issue | Rating | Basis |
| --- | --- | --- |
| Django 10914 | Low | The issue explicitly requests `0o644`; the repair changes the single visible upload-permission default from `None`. |
| Astropy 12907 | Medium | Diagnosis requires understanding nested model composition and preserving the right-hand dependency matrix in `_cstack`. |
| scikit-learn 25570 | Medium | Diagnosis requires matching nonempty transformer outputs with their column names in pandas output; the reference fix filters empty outputs in `_hstack`. |

All three reference patches affect one source file and replace one executable
line; the scikit-learn patch also adds two comment lines. Small patches can still
require nontrivial diagnosis. This is a small set of localized repairs, not
coverage of large changes across multiple files or a calibrated difficulty scale.

Evidence availability is a separate limit. The scikit-learn packet omitted
`_update_fitted_transformers`, relevant to the earlier Sol patch's `StopIteration`.
That omission does not make the original issue intrinsically harder or establish
that the reference repair requires changing that consumer. Gemma's four attempts
on this issue ended at the generation limit and supplied no tested patch; they
are not four demonstrated incorrect repairs.

### Fixed conditions and retained evidence

- Exactly **40 original source records**, base revisions and the external syntax
  candidates were retained. The twelve serialized requests match their previous
  C/D requests byte for byte, including system prompt and answer schema. D only
  appends the original AHE readback section; no new consumer code, tests, gold
  patches or earlier answers entered model inputs.
- Model digests and **Ollama 0.34.4** match the earlier run. Settings remained
  temperature 0, seed 42, context 32,768, generation limit 4,096, `think=true`,
  streaming. Original filtered request order was retained: E4B then 31B.
  One call per cell; no answer repair, longer-budget retry or best-of selection.
- The generation limit is not a guarantee of 4,096 visible-answer tokens.
  **7/12** returned terminal `done_reason=length`; visible output was
  separately retained and reviewed. A truncated explanation can contain a
  detectable unsupported assertion even though no patch is submitted.
- AHE receipts originate from **2026-10-06**. Stored source, provenance and earlier
  check records were verified unchanged by hashes; **zero new AHE/PG operations**
  were run in this extension. This measures interpretation of fixed evidence,
  not model-chosen live retrieval or an isolated graph-mechanism effect.
- The same six base-fail/gold-pass reports and evaluator dataset were hash-checked
  and reused, not rerun. The existing official harness evaluated only valid
  nonempty patches once, with 900 seconds per instance and two workers per group.
  Final test identifiers and denominators were checked against the base controls.
  Empty/invalid/truncated answers were not converted into official test failures.

### Explanations and comparison limits

Every visible answer, including partial visible output, was reviewed by the main
agent using the original source-grounding rubric before official patch testing.
This is one unblinded reviewer, aware of prior results; no model scored the cases.
A text-only counterparty challenged the denominator/reporting claims only.

E4B's Astropy C partial answer calls the original `_compute_n_outputs` incomplete,
although the supplied excerpt simply ends mid-function. The Django C explanation
also promotes the issue author's tentative security rationale to an established
fact. Such unsupported explanations remain separate from exact quotations and
repair tests. E4B's Django D abstention is unnecessary for the bounded default
change, while its permission wording remains an unresolved judgment rather than
a clean answer. In 31B's Django answers, C adds an unsupplied process-umask
mechanism and D promotes the security hypothesis to fact. Both default-change
patches passed, while those explanations remain unsupported. Per-answer judgments are retained
in the compact results.

**12/12 visible outputs exactly matched the preceding run**, including empty
outputs where applicable. These are fresh calls under the same deterministic
settings, not additional independent issues. The observations apply to this
bounded workflow on three familiar development cases; they do not establish
population-wide reliability. Sol's autonomous results use different model,
budget and interaction rules and remain separate.

The owned Ollama process and all owned evaluator containers were cleaned.
Only evaluation documents/private artifacts changed; Product runtime/schema and
Pouch/Detective were unchanged. Native Product tests were not rerun for this
model-only extension. SWE-300 remains deferred.

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
different model/runtime/budget. The later [static-source Sol comparison](#sol-without-ahe-comparison)
adds a control for this autonomous protocol, not for the Gemma fixed-packet protocol.
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

## Sol without AHE comparison

On **2026-10-07**, three fresh **gpt-6.1-sol / high** agents used a static-source
gateway. The with-AHE arm reuses the three sealed workflows above; it was not
regenerated. Each arm has one workflow per issue. All three new patches received
one official SWE-bench evaluation after sealing, with no feedback or repair retry.

| Issue | Without AHE | With AHE | Bug-fix tests passed: without / with | Regression tests passed: without / with |
| --- | --- | --- | --- | --- |
| Astropy 12907 | Resolved | Resolved | 2/2 / 2/2 | 13/13 / 13/13 |
| Django 10914 | Resolved | Resolved | 1/1 / 1/1 | 98/98 / 98/98 |
| scikit-learn 25570 | Not resolved | Not resolved | 0/3 / 0/3 | 184/184 / 184/184 |
| Issues resolved | **2/3** | **2/3** | — | — |

**The paired patches are byte-identical in all three issues.** In particular,
without AHE also changes the scikit-learn empty-selection `elif` to `if` and
fails with `generator raised StopIteration`. Both agents disclosed missing
fitted-transformer lifecycle code; neither produced a complete repair. The
consumer source was not added to this control, preserving the original evidence
boundary. Exact quotations and passing old tests do not establish bug repair.
This run observed no repair-success gain from the AHE workflow. It does not show
that AHE is ineffective generally or establish why either agent chose its patch.

### Matched inputs and remaining differences

- Both arms had the same **40 original source records**, pinned revisions,
  source bytes and editable base files. No old answers, gold patch, official test
  source or additional consumer code entered the new agents' channel.
- Without AHE offers an index, individual source records and the complete static
  packet. It retains the original external syntax-navigation candidates and
  declared omissions, as the fixed-packet C arm did. These candidates existed
  before AHE import; they are not resolved calls or causal proof. No AHE process,
  database, query, admission or lifecycle result was used by this control.
- Both used the same mechanical editor and limits: **30 minutes, 40 read
  operations, 20 edit attempts**. A full static packet costs one read, so equal
  operation caps are **not equal token or information budgets**. Interfaces,
  presentation, actual reads and run order differ; this is a descriptive workflow
  comparison, not an isolated causal estimate of a single AHE mechanism.

| Issue | Without-AHE reads | With-AHE queries | Edits per arm |
| --- | ---: | ---: | ---: |
| Astropy 12907 | 7 | 7 | 1 |
| Django 10914 | 2 | 6 | 1 |
| scikit-learn 25570 | 8 | 10 | 1 |

All **33 new agent tool calls** matched the static gateway trace. All **22 exact
quotes** matched their bound source records and actual responses. The main agent
reviewed the explanations before the new evaluations, but already knew the
with-AHE results; this was a single unblinded review. For scikit-learn, the prior
failure was explicitly recorded as contrary evidence to repair sufficiency,
without being sent to the new solver. No unsupported observed-source assertion
was identified; this is not a claim that all three diagnoses or repairs are correct.

The gateway enforces its own operation restrictions; whole-agent tool adherence
is established by the recorded-call audit, not a hard whole-tool sandbox. Opaque
parent/child assignment hashes match, while plaintext dispatch remains operator
attested. A post-seal message only acknowledged successful submission; it gave
no test result or repair advice.

All six previous base-fail/gold-pass control reports and the official dataset
were hash-verified and reused, **not rerun**. The same evaluator and cached instance
images evaluated the new patches. Static-gateway boundary checks passed **6/6**;
these are harness checks, not additional Product or model successes. An initial
third-agent capacity rejection occurred before that workflow existed; it was
successfully dispatched after another agent finished, with no replacement answer.
Private records retain preparation/path and citation-parser mistakes and their
corrections. All owned static sockets/processes and evaluator containers were
cleaned; cached images and raw reports remain.

Only evaluation documents and private verification artifacts changed in this
control extension. Product runtime/schema did not change, and the native Product
matrix above was not rerun. This adds three new workflows on the **same three
issues**, not three additional issues, the SWE-300 set, or the old unfinished Sol grid.

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
