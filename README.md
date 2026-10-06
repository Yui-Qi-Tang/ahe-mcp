# AHE MCP

<p align="center">
  <img src="assets/ahe-icon.png" alt="AHE icon" width="160" height="160">
</p>

**Traceable evidence and verified operation results for AI agents.**

[Results](#evaluation) · [Quick start](#quick-start) · [First workflow](docs/FIRST_WORKFLOW.md) · [MCP setup](INSTALL.md)

Development version: **unreleased** · schema **55** · role policy **v7** ·
consistency watch **v1**. Go **1.27.0** module.

## Evaluation

### Latest autonomous SWE run: 2026-10-07

**Sol 6.1 high: 2/3 issues resolved.** Each issue used one fresh agent, live AHE
queries and an isolated code copy, with no answer hints or evaluation retries.

| SWE-bench issue | Official repair result | Target tests passed | Existing tests passed |
| --- | --- | ---: | ---: |
| Astropy 12907 | **PASS** | **2/2** | **13/13** |
| Django 10914 | **PASS** | **1/1** | **98/98** |
| scikit-learn 25570 | **FAIL** | **0/3** | **184/184** |

The scikit-learn patch applied but introduced `StopIteration`; it remains a failed
repair. These three familiar issues use a different protocol from the Gemma
fixed-packet results below. [Method, failure analysis and limits](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#autonomous-sol-swe-results).

### Product verification: 2026-10-06–07

Product `844a3a6` with two test-timeout fixes, PostgreSQL 18.6, Go 1.27.1.

| Product checks | Normal: pass / fail / skip | Race: pass / fail / skip |
| --- | ---: | ---: |
| Selected cases, including three SWE source packets | **214 / 0 / 0** | **214 / 0 / 0** |
| Repository ordinary tests | **1,834 / 0 / 0** | **1,834 / 0 / 0** |
| PostgreSQL integration, consistency and solvers | **3,582 / 0 / 13** | **3,582 / 0 / 13** |

Counts overlap. Han query **63/63**, scale and plan also passed separately.
The 13 broad-matrix skips require explicit selection: four were rerun in this
continuation; the other nine retain their preceding passing runs. Product runtime
logic is unchanged. [Current checks and retained earlier failures](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#test-timeout-fixes-and-revalidation).

### Fresh six-case model results: 2026-10-06

110 new Gemma attempts, including **18 SWE repair attempts on three SWE-bench
cases**. Submitted patches received official evaluation; truncations and
abstentions remain in denominators.

| Official SWE repairs — higher is better | Structured/static evidence | With AHE results |
| --- | ---: | ---: |
| Gemma 4 E4B IT-QAT | **0/3** | **0/3** |
| Gemma 4 31B IT-QAT | **1/3** | **2/3** |

| Synthetic answers with unsupported assertions — lower is better | Structured/static evidence | With AHE results |
| --- | ---: | ---: |
| Gemma 4 E4B IT-QAT | **5/14** | **4/14** |
| Gemma 4 31B IT-QAT | **0/14** | **0/14** |

Eleven of the 18 SWE attempts were truncated. A passing repair does not certify
its explanation. These are familiar cases, one attempt per condition, with one
reviewer; Ollama changed from the historical run. The older fixed-packet Sol pilot
remains partial; the autonomous results above are a separate protocol. [Method, per-case results and limits](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#fresh-six-case-model-results).

### Fresh lifecycle answers: 2026-10-06

| All lifecycle decisions correct | Full static records | With AHE results |
| --- | ---: | ---: |
| Gemma 4 E4B IT-QAT | **9/18** | **15/18** |
| Gemma 4 31B IT-QAT | **13/18** | **18/18** |

78 fresh answers, including six shared controls. With AHE, citation-contract
errors remained **3/18** for E4B and **18/18** for 31B; correct decisions do not
mean complete answers passed. [Separate explanation and citation scores](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#fresh-lifecycle-results).

### Earlier fixed-packet answers: 2026-10-06

| Fixed-rule score | Gemma 4 31B QAT | Sol 6.1 high |
| --- | ---: | ---: |
| Common evidence questions | **21/42** | **42/42** |
| Additional conflict/support controls | Not run | **16/16** |

These 100 answers belong to the earlier Product run. Sol still had five explanation
ambiguities and two potential overclaims. [Earlier report](docs/PRODUCT_REPLAY_20261006.md#model-results).
Historical six-case scores remain in [evaluation details](docs/EVALUATION.md).
The remaining replay scope is tracked in the [supplement](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#outstanding-work).

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
