# AHE Detective

> **Desktop 凍結／目前不工作（不可用） — 2026-09-14.** Stop Desktop feature work,
> installation and UI acceptance. Retain its code, tests, built app and saved
> work without treating them as a usable product. CLI is the development focus,
> but its end-to-end engineering intake is not yet qualified. See
> [current status and CLI-first review](../../STATUS.md).

Frozen Desktop code version: `0.1.0-preview.18`. Detective is the source-collection,
extraction and human-review client included in AHE MCP. Its Go commands and
internal packages share the **repository-root `go.mod`** with MCP, using Go
`1.27.0` semantics. There is no separate Detective module or `go.work` setup.
The frontend keeps its own npm dependency lockfile.

The imported implementation baseline is the former Detective repository's
`a828b49` (preview.17). Preview.18 identifies this shared-module source build.
The original Git history and private experiments are retained outside this
repository; the old checkout is not needed for installation. Earlier preview.16
DB/UI acceptance and preview.17 engineering results do not automatically qualify
this newly built app or a different deployment.

## Install and run

Follow [INSTALL.md](INSTALL.md), including its workspace and upgrade guidance.
All commands below run from the **AHE MCP repository root**, not this directory:

```sh
make build
make detective
make verify
```

- `make build`: MCP and operator programs in `bin/`.
- `make detective`: `bin/detective`, `bin/detective-source-demo` and
  `bin/detective-news-source`.
- Desktop build/trial targets and `Start.command` are retained for reference,
  not recommended launch or acceptance steps while frozen. No current process
  is stopped and no existing workspace is deleted by this documentation freeze.

The older `bin/ahe-detective` is a legacy collection host. It is not this CLI and
does not start Desktop. Its JSON configuration and launchd examples do not
configure this application.

## What the application does

The Desktop descriptions below inventory frozen implementation, not supported
runtime capabilities. The standalone CLI sections describe existing slices,
not proof that collection, selection and external admission are integrated.

Detective keeps source text visible alongside extracted candidate statements and
exact citations. It can collect from an explicitly configured source MCP, use
an explicitly selected local model, search AHE through Query MCP, and prepare
bounded pending/review handoffs. The normal Desktop starts from an explicitly
confirmed source MCP call; local-file, saved-work and synthetic-demo opening
controls are not part of that UI. Existing recovery services and CLI remain.

Task-driven engineering intake is the selected development direction, not an
available end-to-end workflow yet. The normal Desktop has no separate
task-selection page or workbench selection card. Chat and source-tool
suggestions do not run the task selector. The new, unreleased Codebase preset
adds a separately confirmed read to chat; other MCP servers remain manual-only.
The ordinary pending/review client remains bound to a single-item
`manual_text` contract. Desktop's three-decision UI currently belongs to Brief,
not arbitrary engineering sources. See the [current status and code inventory](../../STATUS.md)
for what is retained, being rewired, or planned to leave the main workflow.
The `taskextract` library freezes task/source scope and copies selected paragraph
ranges verbatim. A narrow `desktop.ExtractTask` adapter now uses the existing
controlled loopback model, after input validation. Selector prompt v3 receives
only the objective, source title and supplied units in a versioned input.
Full provenance and missing-source scope remain available to the controller and
reviewer. Bounded model annotations stay separate from verbatim candidate text
and human decisions. Initial live checks produce reviewable candidates, but
task relevance still needs scrutiny; exact citations do not prove completeness.
The local `detective task inspect/run/read` CLI and retained Desktop service
use this selector and save complete task/source context. The component, service
and regression tests remain, but are not exposed by the normal Desktop UI.
Generic saved MCP text is not a provider-qualified source envelope; external
intake and human decisions are not wired to task selection yet.

### Desktop source and chat

**Frozen historical procedure — do not execute for current acceptance.**

Build with `make desktop` and intentionally choose a workspace using the
[installation guide](INSTALL.md#first-launch-choose-the-workspace-intentionally).

1. In **資料源與連線**, apply the intended source MCP settings.
   Discover the allowed tools, inspect the tool and arguments, then confirm the
   source call. No tool is selected or called automatically.
2. Configure an already-running local model if you want to chat, then use
   **工作台** to read the returned source and converse. Chat does not automatically
   execute source tools, select evidence paragraphs or submit proposals.
3. If an AHE evidence store has separately been installed, configure the optional
   AHE connection to search it. Source collection and local chat do not require
   those AHE program paths.

**新工作** clears the current source, candidates and conversation without
loading synthetic content or changing the applied connections/model mode.
Previously saved files are retained. There is no persistent model-not-started
warning on the workbench; model configuration remains explicit, and removing
that notice does not start a model or conceal a failed operation.

### Network-blocked Codebase chat (unreleased)

**Frozen / unavailable.** This Desktop-only wiring is not a CLI capability;
do not bypass its network restriction with a generic source launcher.

On macOS, **加入本機 Codebase（選擇資料夾）** creates a draft for the installed
`codebase-memory-mcp` binary. Apply settings, explicitly build the repository
index, then select that Codebase source in the workbench and fetch its tool list.
Ask a question, inspect the proposed tool and complete arguments, and press
**確認本次讀取（不入庫）**. A follow-up requires a new confirmation.

Only this fixed preset enters chat. Codebase and its children run under a macOS
network-denying profile, with local Unix IPC permitted and repository writes
denied. Configuration/index use a dedicated cache; ephemeral short IPC paths
are cleaned after each connection. Automatic indexing/watching and the HTTP
graph UI are disabled. Unsupported sandbox/installation or cache conflicts
stop the operation; there is no unguarded fallback or automatic installation.

Each confirmation allows one read, expires after ten minutes and is consumed
even on failure. A phase is bounded to two minutes, an MCP session to one minute,
and raw RPC output to 1 MiB. Results above the 32 KiB answer-input budget are
saved but not silently truncated into an answer. The existing Cancel action
applies throughout the active phase.

Full results and receipts remain separate from answers. `get_code_snippet`
citations are checked against files within the selected repository and retain
relative file/line coordinates plus an observed full-file SHA-256. That hash is
a content version, **not** a Git commit or proof of index completeness. Structural
results remain index observations. Indexing, reading, pending and admission
are separate operations; chat gains no evidence-writer authority.

### Local task selection

The developer CLI accepts a private `detective-task-input/v1` file. Manual
experiment datasets are maintained outside the product repository; they are
not installed or loaded by Desktop. Use an explicitly chosen input and model:

```sh
make detective
./bin/detective task inspect -input /absolute/private/task-input.json -model YOUR_MODEL
```

`inspect` is offline; the explicit model name only binds the prepared input.
After reviewing the objective, supplied source and missing fields, `run` makes
one call to the explicitly selected, already-running local model:

```sh
./bin/detective task run -input /absolute/private/task-input.json \
  -model YOUR_MODEL -base-url http://127.0.0.1:11434/v1 \
  -out /absolute/private/task-run.json
./bin/detective task read -input /absolute/private/task-run.json
```

The text view shows original candidates, model annotations separately, complete
frozen source, byte coordinates, and missing/unprovided/unselected scope.
`read` needs neither the original input file nor a model. It checks internal
consistency, not source authenticity, semantic support or admission. Failed runs
remain failed on read; a successful abstention is distinct. Input JSON is at
most 1 MiB and saved runs at most 2 MiB; files require mode `0600` under `0700`.
Nothing is fetched, submitted to pending, or admitted. Do not place private
source or run files in the repository. Change the objective/scope in a new task
revision and use a fresh output path; never overwrite old results or decisions.

### Brief news/public-event reading

Brief is for explicitly selected news/public-event reading, not engineering
evidence extraction. New source bundles require `detective-brief-source/v2` and
`source_kind: "news"` or `"public_event"`. Existing v1 records remain readable
and their saved proposals reviewable, but cannot be reused for new extraction
or intake submission. Do not relabel engineering content to bypass this boundary.

Brief labels the text actually provided, not an assumed full provider page.
For oversized news/event inputs, `detective brief select` creates a bounded exact
excerpt with parent hash and byte range; `detective brief verify-excerpt` checks
it against the original parent file offline. The summary remains orientation,
not a completeness assessment or admission decision.

For an existing private v2 news/event source JSON, select a reviewed byte range
from its decoded `body` (zero-based, end-exclusive):

```sh
./bin/detective brief select -input /absolute/private/news-parent.json \
  -start-byte 0 -end-byte 512 -reason 'Selected event section; other sections excluded' \
  -out /absolute/private/news-excerpt.json
./bin/detective brief verify-excerpt -parent /absolute/private/news-parent.json \
  -input /absolute/private/news-excerpt.json
```

Replace the example offsets with complete, readable source context on UTF-8
character boundaries. The parent must declare `full_document`; its JSON/body
limits are 1 MiB/512 KiB. The resulting input must still fit 64 KiB JSON, 32 KiB
body and 64 nonempty segments. Use private directories (`0700`) and files
(`0600`); output must be new. These commands do not call a model, MCP or DB.
Use the existing Brief CLI workflow afterward. The normal Desktop does not
provide an excerpt-file opening control.

Offline mode does not run the model or simulate chat replies. Typing `admit`
does not grant approval or change DB state. Actual tool calls, model use,
queries and writes require actual mode and their explicit controls. Each
application restart returns to offline mode even when settings have been saved.

Desktop can connect directly to the official Atlassian Rovo MCP with interactive
OAuth. See [the login workflow](INSTALL.md#connect-directly-to-atlassian-rovo-mcp-with-oauth).
Credentials remain in memory; each source tool call still needs confirmation.

Source acquisition belongs to Detective. A shared module does not move provider
connectors into Core, make model output authoritative, or remove the MCP process
boundary. Models may help extract or explain evidence; humans must still judge
whether a claim is supported by the cited source.

## Authority and review

**資料源與連線** places **AHE 證據庫（選用）** after source configuration.
Its advanced connection settings are collapsed by default. Leave them blank
when only collecting sources or using local chat; they do not configure a DB
automatically. The three AHE program paths have distinct responsibilities:

| Connection | What it may do |
| --- | --- |
| Source MCP | Read the explicitly approved source tool and preserve its returned content |
| 證據查詢程式 (Query) | Read evidence and pending/review state; no writes |
| 待審提交程式 (Intake) | Save source/extraction/pending-proposal state; pending is not admitted evidence |
| 人工審閱程式 (Review) | Write only the explicitly confirmed exact review decision through the separate reviewer profile |

The AHE installer/operator must configure and provide the executable launchers;
the current installation does not create them automatically. Leave unknown
paths blank. Launchers and credentials stay outside the repository. The app
receives absolute executable paths, not DB passwords or shell assignments. Follow the
[MCP installation and role contract](../../INSTALL.md#bounded-detective-to-pending-mcp-installation)
before enabling AHE access. Do not reuse an operator or migration identity to
make a runtime start.

`admit`, `reject` and `audit_only` require the current exact review and an explicit
human decision/reason. Pending material is not canonical evidence; `reject` and
`audit_only` record a disposition without admitting a canonical claim. A model
suggestion, a query result or the presence of an exact citation is not approval.
Missing, conflicting or incomplete receipts must not be repaired by editing
checkpoint IDs or resubmitting changed content under an old request ID.

This is a single-user local preview. Shipping the client does not qualify an
unattended production writer, authenticate a particular human reviewer, or prove
source/model quality for every environment.

## Development and verification

From the repository root:

```sh
make verify
make desktop-test
make desktop-startup-test
```

`make verify` builds embedded frontend assets and checks frontend and shared-module
Go code. `make desktop-test` adds race tests. Both use deterministic fixtures
unless a separately documented live-test environment is explicitly selected.
`make desktop-startup-test` builds a fresh app and checks three non-UI startup
paths; it is not interactive source-to-review or DB acceptance.

Linux full-module verification additionally requires the GTK/WebKit development
libraries and `webkit2_41` build tag in the
[root installation guide](../../INSTALL.md#shared-module-verification-on-linux).
It does not qualify the native Desktop for Linux. Opt-in legacy lab reports and
private captures remain in the former repository and are not ordinary test
prerequisites. When explicitly running the MCP-to-Detective subprocess acceptance,
`AHE_DETECTIVE_SOURCE_ROOT` must name this **AHE MCP repository root**, not
`apps/detective` or the former standalone module; its separate disposable-DB
opt-in is still required.

For Go commands outside the Make targets, build the frontend first with
`make frontend`, then use the root module paths such as
`go test ./apps/detective/internal/desktop`. Do not restore the old import prefix
or add another `go.mod` under `apps/detective`.
