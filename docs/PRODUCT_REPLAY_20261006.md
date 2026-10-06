# Product replay — 2026-10-06

This is the earlier run. The [subsequent supplement](PRODUCT_REPLAY_20261006_SUPPLEMENT.md)
adds fresh model/SWE results, historical query experiments, and the later
test-timeout fixes and revalidation. Results below retain their original run scope.

Report version: **1**. Product source: **cd1419cbebf4d99f8e0cc2e02b26755f2df6aeae**;
branch: `codex/product-lab-replay-20261006`; unreleased schema **55**, role policy
**v7**, consistency watch contract **v1**. The Product implementation was unchanged
through execution. This report and README are documentation changes after the run.

## Product result

The selected fixtures passed on the current Product checkout. This is a replay
of the selected cases plus repository contract checks, not a claim that every
historical research workflow ran or that downstream models always answer correctly.

| Batch | Leaf passes | Failures | Skips |
| --- | ---: | ---: | ---: |
| Selected contract / SWE packet replay | 214 | 0 | 0 |
| Same replay with race instrumentation | 214 | 0 | 0 |
| Repository ordinary tests | 1,834 | 0 | 0 |
| Repository ordinary tests with race instrumentation | 1,834 | 0 | 0 |
| Repository integration + consistency + real solver + acceptance | 3,582 | 0 | 13 |
| Same integration matrix with race instrumentation | 3,582 | 0 | 13 |

A leaf is a terminal test without child subtests. Parent containers and packages
without tests are excluded. The same cases appear in multiple batches; summing
these rows would overstate independent coverage. Race instrumentation includes
subprocess builds via `GOFLAGS=-race`; outer package/subtest scheduling used
`-p 1 -parallel 1`. This does not disable concurrency deliberately created inside
a test, but it is not arbitrary concurrent package scheduling coverage.

The earlier repository-delta convergence regression passed **20/20** repetitions.
`make verify`, `go vet` with the integration tag set and `make build-all` also
passed. Real CaDiCaL, DRAT verification and Z3 ran using pinned binaries and
hashes, rather than relying only on solver doubles.

### What the 214 checks contain

| Test entry point | Leaf cases | Tested subject |
| --- | ---: | --- |
| `TestLifecycleLabNative` | 18 | Registered lineage lifecycle and currentness |
| `TestFullLabOriginalRelationContract` | 2 | Original relation contracts |
| `TestResearch20261001Disposition` | 1 | Recorded disposition |
| `TestResearch20261002CoreBoundary` | 34 | Admission boundary controls |
| `TestResearch20261002CoreStress` | 48 | Core boundary stress cases |
| `TestResearch20261002ReportBindingStress` | 12 | Report-to-subject binding |
| `TestResearch20261003BindingLifecycle` | 28 | Binding correction, withdrawal and history |
| `TestResearch20261003PropositionIdentity` | 27 | Proposition and justification identity |
| `TestIntegrationSearchLab02BodyPGOneRow` | 1 | PostgreSQL body-query control |
| `TestIntegrationSearchLab02BodyPG` | 22 | PostgreSQL body-query cases |
| `TestIntegrationExtractionDiagnosisReadback` | 18 | Persisted diagnosis readback fixtures |
| `TestIntegrationSWERelationPackets` | 3 | Public-source SWE intake, relations and Query packets |

The last row executes the **packet stage**, not fresh repair generation or the
SWE-bench patch evaluator. The diagnosis fixtures likewise do not certify an
extractor's semantic fidelity. Research harnesses and synthetic approval stubs
remain test mechanisms; passing them does not create new public MCP capabilities.

### Latest consistency and migration coverage

The integration matrix also passed the three-way contradiction case and its
pairwise satisfiable controls, exact-coverage/stale-view checks, localization,
currentness/frontier policies, RLS, consistency watch lifecycle, worker restart
and contention, event commit order, and worker interleaving. The Product
round-trip fixture exercised the compiled worker finding the three-way conflict,
withdrawal making an old result stale and triggering recomputation, and new
unconfigured evidence blocking until configuration was supplied through MCP.
Role-upgrade fixtures retained identities across their upgrade steps; this is
separate from creating a fresh database and checking only the latest roles.

The auxiliary frozen-input checks passed **6/6** coverage artifacts and **34/34**
support-topology cases. A fresh PostgreSQL lifecycle export regenerated **39/39**
currentness packets with identical content to the original comparison inputs.
These are compatibility/preparation checks, not additional model answers or
claims of complete source extraction.

### Skips retained explicitly

The integration matrix recorded these opt-in entry points as skipped:

| Group | Entry points | This run |
| --- | --- | --- |
| Han | `TestIntegrationHanOutcomeReadOnlyLab`, `TestHanOutcomeFrozenPlan`, `TestIntegrationHanPlanIsolatedLab`, `TestIntegrationHanQueryMCPIsolatedLab`, `TestIntegrationHanScaleIsolatedLab` | Five historical query-research opt-ins not selected |
| Multisurface | `TestIntegrationMultisurfaceReadOnlyLab`, `TestMultisurfaceFrozenPlan`, `TestIntegrationMultisurfaceSQLControls` | Three historical query-research opt-ins not selected |
| Practical | `TestIntegrationPracticalAnchorReadOnlyLab`, `TestPracticalAnchorFrozenPlan`, `TestIntegrationPracticalAnchorSQLControls`, `TestIntegrationPracticalSQLControls` | Four historical query-research opt-ins not selected |
| SWE packets | `TestIntegrationSWERelationPackets` | Skipped in the broad matrix; explicitly executed as three cases in the 214-case batch |

The twelve query-research opt-ins need their separately prepared snapshots,
plans and/or isolated environment configuration. Their absence was part of the
frozen selection, not evidence they pass. They were not deleted or silently
counted as replacements. The normal/race skip lists are identical.

## Model results

The run completed **100 scored generations**: 42 Gemma responses and 58 Sol
responses. One additional report counterparty was unscored. A fixed-rule pass
requires every scored decision field to be correct and, where requested, valid
evidence references. Free-text explanations are reviewed separately.

| Common question family | Presentations | Gemma fixed-rule passes | Sol fixed-rule passes | Gemma correct decisions | Gemma valid required citations |
| --- | ---: | ---: | ---: | ---: | ---: |
| Requirement coverage versus runtime proof | 12 | 6 | 12 | 6 | Not required |
| Currentness of necessary premises | 12 | 5 | 12 | 12 | 5 / 12 |
| Replacement, scope and identity | 18 | 10 | 18 | 17 | 11 / 18 |
| Total | 42 | **21 / 42** | **42 / 42** | **35 / 42** | **16 / 30** |

Sol additionally passed **16/16** conflict/support presentations, for **58/58**
overall. Its decisions were correct on 42/42 common presentations and its required
citations were valid on 30/30. Gemma did not run the additional conflict set.
The twelve requirement questions have no citation requirement; the scorer's
vacuously true citation flag for them is not counted as citation success.

### What failed or remained ambiguous

- **Gemma: six unsupported runtime decisions.** In all six presentations with
  complete declared support, it marked runtime correctness as proven. These
  packets describe synthetic in-memory topology, with no execution proving
  runtime behavior. Three explanations also explicitly made this unsupported
  inference. Complete prerequisite coverage is not runtime proof.
- **Gemma: one unsupported scope decision.** With neutral identifiers, the AB
  presentation classified independently owned policies as different scopes,
  although both explicitly applied to production and no applicability difference
  was supplied. Reversing the same records to BA produced abstention. The BA
  answer still failed the citation rule. With the original suggestive identifiers,
  both orders abstained, also without the required references.
- **Gemma: fourteen citation failures.** Seven currentness answers cited a JSON
  container name instead of the required record identifier. Seven relationship
  answers returned an empty evidence list despite the explicit reference rule.
  Their decision fields were correct. These failures are disjoint from the seven
  decision errors in this particular run.
- **Sol: five explanation ambiguities.** Four answers described one current and
  one superseded premise as “both parents are not current,” which can mean either
  “not both” or “neither.” Another described an unknown/current pair with wording
  that could assign unknown to each premise instead of their combination. The
  individual state fields were correct. Adding the composition table did not
  remove the four negation ambiguities.
- **Sol: two potential approval overclaims.** For a proposal whose approval
  decision was not supplied, AB and BA explanations used “unapproved.” This may
  overstate missing information, but the complete explanations can also be read
  as shorthand for no usable approval in the packet. They remain potential
  overclaims, not confirmed errors about real approval status.

The main agent's unblinded explanation review identified four clear Gemma
explanation errors (three runtime claims and the scope inference), plus three
potential claims that absence of replacement authority means no replacement.
It identified no confirmed Sol explanation error under this calibration, while
retaining the five ambiguities and two potential overclaims above. Decision,
citation and explanation labels overlap and must not be added together.
“No clear defect identified” is not proof of explanation fidelity.

### Comparison with earlier answers

| Frozen scorer, recounted consistently | Original Gemma pilot | Previous Product replay | This Product replay |
| --- | ---: | ---: | ---: |
| Gemma common 42 | 19 / 42 | 21 / 42 | 21 / 42 |
| Sol common 42 | — | 42 / 42 | 42 / 42 |
| Sol additional conflict 16 | — | 16 / 16 | 16 / 16 |

All 42 new Gemma parsed final answers, including explanation and references,
matched the previous Product replay exactly. All 42 HTTP response bodies had
new hashes and new server timestamps, with a newly recorded request for each;
these were fresh inference calls, not copied answers. Sol's common decision
fields also matched the previous replay, while explanation wording varied.
This is observed repeatability on fixed inputs, not evidence of general
reliability or improvement from the Core port. The older Gemma pilot's lower
score cannot be causally attributed to Product changes by this comparison.

## Model protocol

This round generated **42 new Gemma responses** and **58 new Sol responses**;
none are copied from earlier answers. The common 42 presentations comprise
12 requirement/structure/runtime questions, 12 registered-currentness questions
and 18 replacement/scope/identity presentations. Sol additionally answers 16
conflict/support presentations. Paired C/T prompts and A/B record orders are
related presentations, not independent semantic cases.

The requirement T condition adds `requirement_coverage: "not_assessed_by_ahe"`
to the otherwise matched packet. The currentness T condition adds an explicit
three-valued composition table. Relationship AB/BA reverses record order; two
additional presentations replace suggestive source-object names with neutral
identifiers. Conflict A/B compares the complete formal problem alone against
that same problem plus its frozen historical summary. None of these conditions
gives the model a live Query strategy.

Each Gemma request preserves the original bytes, order, schema and options:
`gemma4:31b-it-qat`, Ollama **0.34.4**, temperature **0**, seed **42**,
context **32768**, maximum generated tokens **4096**. Model digest:
`e0812a55773bfeac846b2d605b4d93638b8dfa7119d9587f3d91475afc78185e`.
It used an owned local loopback server with cloud access disabled. All 42 calls
returned HTTP 200, completed the response schema and ended with stop rather than
token truncation; the server was stopped after collection.

Each Sol presentation uses a fresh **gpt-6.1-sol**, effort **high**, with no
history fork, tools, file access or follow-up. Recorded runtime settings matched
all 58 dispatches; each has one completed turn and one final response. The main
agent controls preparation, execution, collection and scoring. No model is asked
to score another model. Original deterministic scorers are unchanged; an earlier
supplement-wrapper metadata omission is corrected consistently when recounting
both old and new answers, while preserving the old wrapper result separately.

All model inputs are fixed evidence packets. Current Product coverage and
currentness outputs were checked against the original packets first. The 16
Sol conflict inputs deliberately retain **historical summaries**, including old
infeasible or omitted-premise behavior, as controls alongside the complete formal
problem. Those summaries are not represented as fresh current Product output.
The 34-case topology check establishes structural compatibility, not equivalence
with external evidence-selection algorithms.

Decision fields, required citation validity and free-text explanation review are
separate measures. The explanation review is by the main agent, unblinded,
without independent annotation. One tool-free counterparty challenged the report's
interpretation; it did not execute or score the experiment. In particular,
wording that might be a bounded shorthand is retained as uncertain rather than
promoted to a confirmed factual error.

The runtime still injected ambient instructions/memory into all 58 Sol contexts;
no-history-fork is not full context isolation. Encrypted parent/child message
identity was checked, but there is no independent plaintext equality attestation,
provider weight digest, matched sampling/compute budget or provider-internal
retry audit. There was one main-agent-requested generation per presentation, no
answer repair and no selection among retries. This is a fixed-reader comparison,
not a test of autonomous retrieval, real extraction, human approval, Pouch,
SWE repair generation or general reasoning reliability.

## Environment and reproduction

Go **1.27.1**, PostgreSQL **18.6**, gopls **0.23.0**, macOS arm64. Databases were
new disposable Unix-socket-only clusters with synthetic or already selected
public-source inputs and explicit **TEST APPROVAL STUBS**. All owned PostgreSQL
clusters were stopped and removed after retaining logs. No operational database,
Pouch or Detective checkout was changed.

Repository command shapes, with the isolated database, role, lexical and solver
prerequisites configured first:

```sh
go test -json -mod=readonly -count=1 -p 1 -parallel 1 -timeout=20m ./...
GOFLAGS=-race go test -race -json -mod=readonly -count=1 -p 1 -parallel 1 -timeout=20m ./...
go test -json -mod=readonly -count=1 -p 1 -parallel 1 -timeout=20m \
  -tags=integration,consistencylab,resolverintegration,acceptance ./...
GOFLAGS=-race go test -race -json -mod=readonly -count=1 -p 1 -parallel 1 -timeout=20m \
  -tags=integration,consistencylab,resolverintegration,acceptance ./...
make verify
go vet -mod=readonly -tags=integration,consistencylab,resolverintegration,acceptance ./...
make build-all
```

See [database prerequisites](../INSTALL.md#optional-database-and-process-tests)
and the [resolver foundation](../logicresolver/README.md) for tool configuration.
The 214-case selection additionally uses `integration,labreplay,diagnosislab`
and the exact test entry points above in `internal/evidenceingestion` and
`internal/mcpintegration`, with the original prepared fixture inputs. The private
runner preserves command receipts, source fingerprints, case-level logs, skips,
model inputs/outputs and cleanup receipts. Those private bundles are not included
in the public repository; these commands alone cannot reproduce the 214 private
fixture denominator or regenerate the model scores.

An initial auxiliary topology test invocation could not access the sandboxed Go
build cache before tests ran. The unchanged test was rerun with a writable cache
and passed. Both logs are retained; this setup failure is not a model attempt or
a suppressed failing Product assertion.
