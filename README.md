# AHE MCP

<p align="center">
  <img src="assets/ahe-icon.png" alt="AHE icon" width="160" height="160">
</p>

**Traceable evidence and verified operation results for AI agents.**

[Results](#evaluation) · [Quick start](#quick-start) · [First workflow](docs/FIRST_WORKFLOW.md) · [MCP setup](INSTALL.md)

Development version: **unreleased** · schema **55** · role policy **v7** ·
consistency watch **v1**. Go **1.27.0** module.

## Evaluation

### Sharing failed repairs — 2026-10-10

**Gemma 4 E4B IT-QAT**, scikit-learn 25570; each group had 30 answers.
“Passed” means all **3 bug tests + 184 existing tests** passed.

| Failure feedback | With AHE results | Without AHE results |
| --- | --- | --- |
| Own failures only | Not fixed in 30 answers | Not fixed in 30 answers |
| Own and peer failures | **First passed at answer 8; next 22 passed** | Not fixed in 30 answers |

After success, the next 22 answers reused the same input and fixed seed; all
produced the same patch. This is one issue and one pair per sharing policy,
not a guarantee of future success or an isolated measure of AHE's effect.
[Method, counts and limitations](docs/REPAIR_SHARING_20261010.md).

### Repairing with test feedback — 2026-10-08

On **scikit-learn 25570**, **Gemma 4 E4B IT-QAT** reached a fully passing
repair **four corrections earlier with AHE results** in this run.

| E4B condition | First fully passing repair | Same successful input, rerun 3 times |
| --- | --- | --- |
| With AHE results | **Correction 24** | **3/3 passed** |
| Without AHE results | **Correction 28** | **3/3 passed** |

Each success passed **all 3 bug tests and all 184 existing tests**. Correction 0
is the initial answer. Both groups shared failed patches and test feedback;
the same tests guided and graded repairs. This shared search does not isolate
AHE's effect or establish a general reduction in rounds. The confirmations used the same
frozen input and fixed seed, not three new searches.
[Method and every round](docs/REPAIR_FEEDBACK_20261008.md).

### First answers to three bugs — 2026-10-08

One answer per issue and condition, with an **8,192-token output limit**.
“Fixed” requires both bug tests and existing tests to pass.

| Issue | E4B without AHE | E4B with AHE | 31B without AHE | 31B with AHE |
| --- | --- | --- | --- | --- |
| Django 10914 | Not fixed | Declined | **Fixed** | **Fixed** |
| Astropy 12907 | Invalid patch | Not fixed | **Fixed** | **Fixed** |
| scikit-learn 25570 | Broke existing tests | Unfinished | **Fixed** | Unfinished |
| **Issues fixed** | **0/3** | **0/3** | **3/3** | **2/3** |

E4B and 31B are **Gemma 4 E4B IT-QAT** and **Gemma 4 31B IT-QAT**.
Both unfinished answers reached the output limit. A separate **12,288-token**
scikit-learn rerun let **31B pass in both groups**; E4B with AHE still ran out of
tokens. These first-answer results remain separate from the feedback experiment.
[Per-issue scores, complexity and method](docs/EVALUATION_20261008.md#first-answers).

### Evidence answers — 2026-10-06

| What was checked | Model | Without AHE results | With AHE results |
| --- | --- | ---: | ---: |
| Answers with unsupported claims — fewer is better | E4B | **5/14** | **4/14** |
| Same check | 31B | **0/14** | **0/14** |
| Correctly judged whether evidence was still valid — more is better | E4B | **9/18** | **15/18** |
| Same check | 31B | **13/18** | **18/18** |

Correct judgments can still have citation mistakes: with AHE, **3/18 E4B** and
**18/18 31B** answers failed the citation rules. The two checks use different
question sets. [Detailed results](docs/EVALUATION_20261008.md#evidence-answers).

### Product tests — 2026-10-07

| Test group | Passed in normal run | Passed with Go race detector |
| --- | ---: | ---: |
| General tests | **1,834** | **1,834** |
| PostgreSQL integration | **3,594** | **3,594** |
| Selected Lab cases, including SWE source handling | **214** | **214** |

**No unexpected failures.** All 13 opt-in entries skipped by the integration
command were separately rerun and passed in both modes; **none remain unrun**.
Groups overlap. These are software checks, not model repair scores.
[Scope, upgrades and concurrent-client checks](docs/EVALUATION_20261008.md#product-tests).

[Current methods and data](docs/EVALUATION_20261008.md) · [Earlier results](docs/EVALUATION.md)

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

- Read evidence: [Codex](.agents/skills/ahe-evidence-query/SKILL.md) · [shared workflow](.claude/skills/ahe-evidence-query/SKILL.md).
- Prepare evidence: [Codex](.agents/skills/ahe-evidence-intake/SKILL.md) · [shared workflow](.claude/skills/ahe-evidence-intake/SKILL.md).

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
