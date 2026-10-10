# AHE MCP

<p align="center">
  <img src="assets/ahe-icon.png" alt="AHE icon" width="160" height="160">
</p>

**Traceable evidence and verified operation results for AI agents.**

[Results](#evaluation) · [Quick start](#quick-start) · [First workflow](docs/FIRST_WORKFLOW.md) · [MCP setup](INSTALL.md)

Development version: **unreleased** · schema **55** · role policy **v7** ·
consistency watch **v1**. Go **1.27.0** module.

## Evaluation

AHE supplies traceable context to help agents **do the right thing** and
**expose evidence gaps instead of forcing an answer**. Historical and later
results remain separate; they are not a version trend or one combined score.

**Synthetic evidence questions.** Models are **Gemma 4 E4B IT-QAT**
and **Gemma 4 31B IT-QAT**. Arrows below mean **without → with AHE results**:
the same source material, with recorded AHE checks and queries added.

| Measure / run | E4B | 31B |
| --- | ---: | ---: |
| Unsupported-claim answers, historical — fewer is better | 6/14 → **3/14** | 1/14 → **0/14** |
| Unsupported-claim answers, later replay — fewer is better | 5/14 → **4/14** | 0/14 → 0/14 |
| Correct evidence-currentness decisions, separate later check | 9/18 → **15/18** | 13/18 → **18/18** |

The later replay's three synthetic families scored as follows. A correct answer
has every required decision field correct; explanations are checked separately.

| Synthetic family | E4B without → with AHE | 31B without → with AHE |
| --- | ---: | ---: |
| Necessary premises | 1/4 → **2/4** | 4/4 → 4/4 |
| Outdated reviews | 4/5 → 3/5 | 5/5 → 5/5 |
| Separately approved code–requirement links | 3/5 → **5/5** | 5/5 → 5/5 |

E4B improved in two families but regressed on outdated reviews. Better decisions
also did not ensure correct citations: the separate currentness check retained
**3/18 E4B and 18/18 31B** citation failures with AHE.
[Run boundaries, historical scores and failures](docs/EVALUATION.md#readme-result-map).

**Three SWE repair issues.** “Fixed” requires every selected bug test
and existing test to pass. Historical successes and later failures are both retained.

| Fully repaired issues: without → with AHE | E4B | 31B |
| --- | ---: | ---: |
| Historical run | 0/3 → **1/3** | 1/3 → **3/3** |
| Later first-answer run, 8,192-token limit | 0/3 → 0/3 | 3/3 → 2/3 |

The later run's individual results:

| SWE issue | E4B without AHE | E4B with AHE | 31B without AHE | 31B with AHE |
| --- | --- | --- | --- | --- |
| Django 10914 | Ineffective comment-only edit | Identified missing code; no patch | **Fixed** | **Fixed** |
| Astropy 12907 | Patch could not run | Tested; bug not fixed | **Fixed** | **Fixed** |
| scikit-learn 25570 | Passed bug tests; broke 17 existing tests | Output limit; no patch | **Fixed** | Output limit; no patch |

On Django, **E4B with AHE exposed a real evidence gap instead of submitting a
patch**. Refusing the whole repair was too broad: a limited default-setting
change was supported. A separate **12,288-token** scikit-learn rerun let
**31B pass both conditions**; E4B with AHE remained capped. Truncation is not
evidence-based abstention. [Test counts, complexity and method](docs/EVALUATION_20261008.md).

**Using failure history:** in two E4B shared-feedback runs on scikit-learn,
full passes appeared at **correction 24 versus 28**, and **answer 8 versus
no pass in 30** (with versus without AHE). In the latter experiment, neither
own-failures-only group passed within 30. The external controller supplied
feedback; these observations do not isolate AHE's effect or prove a general speedup.
[Correction loop](docs/REPAIR_FEEDBACK_20261008.md) · [Sharing comparison](docs/REPAIR_SHARING_20261010.md).

**Evidence storage and lifecycle checks.** These checks test evidence storage and
state changes, separately from model answer quality. Each count passed in both
normal and Go race runs:

| Selected checks | Passed |
| --- | ---: |
| Proposition identity and correction/withdrawal history | **55/55** |
| Admission boundaries, stress and review/report binding | **94/94** |
| Lifecycle, relations and disposition | **21/21** |
| Source queries and stored diagnosis readback | **41/41** |
| Three SWE source packets: intake, relations and Query | **3/3** |
| **Total selected checks** | **214/214** |

The broader AHE MCP runs passed **1,834 general** and **3,594 PostgreSQL integration**
tests in each mode; all 13 opt-in entries also passed separately. Counts overlap.
[Exact coverage](docs/EVALUATION.md#software-contract-checks).

Small, familiar cases and single-reviewer explanation judgments limit these
observations. All scores above are retained executions; this documentation update
ran no new experiments. [Methods, run dates and limitations](docs/EVALUATION.md).

## Quick start

```sh
git clone https://github.com/Yui-Qi-Tang/ahe-mcp.git
cd ahe-mcp
make build
```

Follow [MCP installation](INSTALL.md) to configure PostgreSQL, migrations,
runtime roles and protected launchers. Then try the
[first evidence workflow](docs/FIRST_WORKFLOW.md).
For a database-free contract example, run `make evidence-boundary`.

## Programs

| Program | Purpose |
| --- | --- |
| `ahe-query-mcp` | Read-only evidence queries over stdio |
| `ahe-ingest-mcp` | Separate intake, source, endpoint and relation-review profiles |
| `ahe-migrate` | Apply and verify PostgreSQL migrations |
| `ahe-runtime-admin` | Provision, verify or upgrade bounded runtime roles |
| `ahe-consistency-worker` | Persist checked diagnostics for explicit consistency watches |
| `ahe-mcp-launch` | Start an MCP profile with protected external credentials |

`make build-all` also builds the optional Atlassian and CodeGraph adapters.

## Authority Model

- External clients collect sources; extraction produces pending candidates.
- Only explicit review can admit claims or independent relations.
- PostgreSQL is authoritative. Ordinary agents receive read-only Query access.

This is a single-user controlled preview with schema-wide visibility; use a
separate database and trusted operator. Unattended production writing is not qualified.
See [review and state transitions](docs/SYSTEM_DESIGN.md#review).

## Agent workflows

- Read evidence: [Codex](.agents/skills/ahe-evidence-query/SKILL.md) · [Claude Code](.claude/skills/ahe-evidence-query/SKILL.md).
- Prepare evidence: [Codex](.agents/skills/ahe-evidence-intake/SKILL.md) · [Claude Code](.claude/skills/ahe-evidence-intake/SKILL.md).

These workflows require configured MCP access and do not grant admission authority.

## External Model and Connector Intake

Preserve exact source content, submit grounded candidates, then obtain explicit
review of the unchanged native subject. Intake cannot admit evidence.
See [profiles and setup](INSTALL.md#bounded-external-intake-mcp-installation).

## MCP Client

Register the protected Query launcher as a stdio MCP server and use `tools/list`
to discover its schemas. Query has 26 read-only tools; the separate core-records
profile has six record/configuration tools. Results are evidence packages, not
final answers. [Client configuration](INSTALL.md).

## Tests

```sh
make verify
go test -race ./...
```

Live database/process tests require an explicitly selected opt-in environment.
See [test prerequisites](INSTALL.md#optional-database-and-process-tests).

## Documentation

- [First evidence workflow](docs/FIRST_WORKFLOW.md)
- [Evaluation details](docs/EVALUATION.md)
- [System design and graph contracts](docs/SYSTEM_DESIGN.md)
- [Scoped consistency workflow](docs/CONSISTENCY.md)
- [Logic resolver foundation — experimental v0 API](logicresolver/README.md)
- [Installation and upgrades](INSTALL.md)
- [Changelog](CHANGELOG.md)

## License

[MIT](LICENSE). Third-party dependencies and materials retain their respective licenses.
