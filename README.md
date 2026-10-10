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
**expose evidence gaps instead of forcing an answer**. These six experiment
families examine evidence judgments and working repairs separately.
Models are **Gemma 4 E4B IT-QAT** and **Gemma 4 31B IT-QAT**.
Both conditions receive the same source material; the AHE condition also
receives recorded Product operation and query results.

**Using evidence correctly.** Scores are answers with every required decision
field correct / questions. Higher is better; explanations are checked separately.

| Experiment | E4B without AHE | E4B with AHE | 31B without AHE | 31B with AHE |
| --- | ---: | ---: | ---: | ---: |
| 1. Are all necessary premises supported? | 1/4 | **2/4** | 4/4 | 4/4 |
| 2. Can an outdated review still authorize admission? | 4/5 | 3/5 | 5/5 | 5/5 |
| 3. Was the code–requirement link separately approved? | 3/5 | **5/5** | 5/5 | 5/5 |

E4B improved in two families but regressed on outdated reviews. Answers with
unsupported claims fell **5/14 → 4/14** overall: **1/5 → 0/5** for independent
links, but **1/5 → 2/5** for outdated reviews. 31B had **0/14 in both conditions**.
These findings show where evidence helped and where the model still misread it;
they are not a general guarantee. [Per-family scores and method](docs/EVALUATION.md#six-family-readme-comparison).

**Repairing code.** These are first answers from a separate run. “Fixed” means
every selected bug test and existing test passed.

| SWE experiment | E4B without AHE | E4B with AHE | 31B without AHE | 31B with AHE |
| --- | --- | --- | --- | --- |
| 4. Django 10914 | Ineffective comment-only edit | Identified missing code; no patch | **Fixed** | **Fixed** |
| 5. Astropy 12907 | Patch could not run | Tested; bug not fixed | **Fixed** | **Fixed** |
| 6. scikit-learn 25570 | Passed bug tests; broke 17 existing tests | Output limit; no patch | **Fixed** | Output limit; no patch |

**Not forcing an answer:** on Django, E4B with AHE identified a real missing
code path and stopped, making the evidence gap visible. The supplied code still
supported a limited default-setting change, so refusing the entire repair was
too broad. Gap detection is useful; it is not a completed repair.

At the **8,192-token** limit, fully repaired issues were **0/3 → 0/3** for E4B
and **3/3 → 2/3** for 31B (without → with AHE). A separate **12,288-token**
scikit-learn rerun let **31B pass both conditions**; E4B with AHE still reached
the limit. Output truncation is not a decision to abstain.
[Test counts, complexity and method](docs/EVALUATION_20261008.md).

**Using failure history:** in two E4B shared-feedback runs on scikit-learn,
full passes appeared at **correction 24 versus 28**, and **answer 8 versus
no pass in 30** (with versus without AHE). In the latter experiment, neither
own-failures-only group passed within 30. The external controller supplied
feedback; these observations do not isolate AHE's effect or prove a general speedup.
[Correction loop](docs/REPAIR_FEEDBACK_20261008.md) · [Sharing comparison](docs/REPAIR_SHARING_20261010.md).

These are small, familiar case sets. Explanation reviews used one unblinded
reviewer. Separate [currentness and citation checks](docs/EVALUATION_20261008.md#evidence-answers)
also retain both improved decisions and citation failures.

**Product checks:** normal and Go race runs each passed **1,834 general tests**,
**3,594 PostgreSQL integration tests** and **214 selected Lab/SWE source cases**.
No unexpected failures; all 13 opt-in entries separately passed in both modes.
Groups overlap. These software checks are separate from model outcomes.

[Evaluation goals, all reports and limitations](docs/EVALUATION.md).

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
