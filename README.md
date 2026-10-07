# AHE MCP

<p align="center">
  <img src="assets/ahe-icon.png" alt="AHE icon" width="160" height="160">
</p>

**Traceable evidence and verified operation results for AI agents.**

[Results](#evaluation) · [Quick start](#quick-start) · [First workflow](docs/FIRST_WORKFLOW.md) · [MCP setup](INSTALL.md)

Development version: **unreleased** · schema **55** · role policy **v7** ·
consistency watch **v1**. Go **1.27.0** module.

## Evaluation

### Bug fixes — 2026-10-07

**Gemma 4 E4B IT-QAT (E4B)** and **Gemma 4 31B IT-QAT (31B)** each tried
these three issues, without and with AHE results.

| Issue | E4B without AHE | E4B with AHE | 31B without AHE | 31B with AHE |
| --- | --- | --- | --- | --- |
| Django 10914 | Not fixed | Declined | **Fixed** | **Fixed** |
| Astropy 12907 | Unfinished | Unfinished | Unfinished | **Fixed** |
| scikit-learn 25570 | Unfinished | Unfinished | Unfinished | Unfinished |

**Bugs fixed, without → with AHE: E4B 0/3 → 0/3; 31B 1/3 → 2/3.**
“Fixed” means the patch passed official tests; “Not fixed” means it was tested
and failed to fix the bug.

| Issue | Repair complexity* | What needs fixing |
| --- | --- | --- |
| Django 10914 | **Low** | Set the upload-permission default to the value explicitly requested by the issue. |
| Astropy 12907 | **Medium** | Preserve input/output dependencies when combining nested model matrices. |
| scikit-learn 25570 | **Medium** | Keep output column names aligned when a transformer selects no columns. |

*Complexity is our rough, post-run judgment of the reasoning needed, not a
SWE-bench label or a rating based on model success. All three reference fixes
replace one line of logic in one source file. [Basis and source limits](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#issue-complexity).

Across 12 answers, **7 ran out of tokens before finishing and 1 declined to propose a
fix**. Four patches reached testing: **3 passed, 1 failed**. Unfinished answers
and declined fixes still count among the three issues; they were not tested.

On Django, **E4B with AHE results declined to propose a fix**, saying the supplied
code did not show how file permissions were applied. Without AHE, it only changed
a comment and failed the bug test. The reviewer judged the refusal too cautious:
the supplied evidence supported changing the default. Neither answer fixed it.

Both sides read the same source material and code-navigation hints; the AHE side
also received existing AHE check/query results. Models did not query AHE live.
These are three familiar issues, one attempt each, under a 4,096-token limit.
[Per-issue results and method](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#gemma-swe-comparison-2026-10-07).

A separate **Sol 6.1 high** test fixed **Django and Astropy in both groups
(2/3 each)**; scikit-learn was not fixed in either. It used a different workflow. [Sol results](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#sol-without-ahe-comparison).

### Evidence answers — 2026-10-06

| What was checked | Model | Without AHE results | With AHE results |
| --- | --- | ---: | ---: |
| Answers making claims the evidence does not support — fewer is better | Gemma 4 E4B IT-QAT | **5/14** | **4/14** |
| Same check | Gemma 4 31B IT-QAT | **0/14** | **0/14** |
| Correctly judged whether evidence was still valid — more is better | Gemma 4 E4B IT-QAT | **9/18** | **15/18** |
| Same check | Gemma 4 31B IT-QAT | **13/18** | **18/18** |

Correct judgments can still have citation mistakes: with AHE, **3/18 E4B** and
**18/18 31B** answers failed the citation rules. These two checks use different
question sets. [Claim checks](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#fresh-six-case-model-results) · [Evidence-status checks](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#fresh-lifecycle-results).

### Product tests — 2026-10-06–07

| Test group | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: |
| Selected Lab cases, including SWE source handling | **214** | **0** | **0** |
| General tests | **1,834** | **0** | **0** |
| PostgreSQL integration and logic checks | **3,582** | **0** | **13** |

Normal runs and runs with Go's race detector gave the same counts. Groups overlap.
The 13 skipped tests require separate commands: **4 were rerun and passed**;
**9 retain earlier passing runs**. [Full test record](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#test-timeout-fixes-and-revalidation).

Earlier results and unfinished work remain in the [reports](docs/EVALUATION.md)
and [work remaining](docs/PRODUCT_REPLAY_20261006_SUPPLEMENT.md#outstanding-work).

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
