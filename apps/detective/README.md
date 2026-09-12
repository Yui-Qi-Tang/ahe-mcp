# AHE Detective

Current Desktop version: `0.1.0-preview.18`. Detective is the source-collection,
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
make desktop
```

- `make build`: MCP and operator programs in `bin/`.
- `make detective`: `bin/detective`, `bin/detective-source-demo` and
  `bin/detective-news-source`.
- `make desktop`: macOS arm64 app, `Start.command` and the synthetic source demo
  in `apps/detective/build/bin/`.
- `make desktop-trial`: build and open a **new** isolated offline workspace on
  each invocation. Previous trials are retained, not reopened or erased.

For repeated use, open the built `Start.command`; it uses a persistent preview
workspace outside the repository. No model, MCP server, source account, or DB
connection starts automatically. This source build is not a prebuilt installer
or a Developer ID-signed/notarized public macOS release.

The older `bin/ahe-detective` is a legacy collection host. It is not this CLI and
does not start Desktop. Its JSON configuration and launchd examples do not
configure this application.

## What the application does

Detective keeps source text visible alongside extracted candidate statements and
exact citations. It can collect from an explicitly configured source MCP, use
an explicitly selected local model, search AHE through Query MCP, and prepare
bounded pending/review handoffs. Saved source receipts and Brief documents can
be reopened without fetching the source or running the model again.

Offline rehearsal uses fixed synthetic data. Its chat is not a semantic reviewer
and typing `admit` does not grant approval or change DB state. Actual tool calls,
model use, queries and writes require switching to actual mode and using their
explicit controls. Each application restart returns to offline mode even when
connection settings have been saved.

Desktop can connect directly to the official Atlassian Rovo MCP with interactive
OAuth. See [the login workflow](INSTALL.md#connect-directly-to-atlassian-rovo-mcp-with-oauth).
Credentials remain in memory; each source tool call still needs confirmation.

Source acquisition belongs to Detective. A shared module does not move provider
connectors into Core, make model output authoritative, or remove the MCP process
boundary. Models may help extract or explain evidence; humans must still judge
whether a claim is supported by the cited source.

## Authority and review

The configured launchers have different responsibilities:

| Connection | What it may do |
| --- | --- |
| Source MCP | Read the explicitly approved source tool and preserve its returned content |
| AHE Query launcher | Read evidence and pending/review state; never write canonical evidence |
| AHE intake launcher | Save source/extraction/pending-proposal state; not admission |
| AHE review launcher | Submit an explicitly confirmed exact review through the separate reviewer profile |

Launchers and credentials stay outside the repository. The app receives absolute
launcher paths, not DB passwords or shell environment assignments. Follow the
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
