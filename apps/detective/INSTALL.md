# Install Detective CLI and Desktop

> **Desktop 凍結／目前不工作（不可用） — 2026-09-14.** The Desktop installation,
> launch, connection, upgrade and acceptance steps in this document are frozen
> references, not current instructions. Do not run them as the next task.
> CLI end-to-end engineering intake remains incomplete. Source, tests, binaries and
> saved work are retained; this freeze does not kill an app or remove data.

This guide covers `0.1.0-preview.18` from the AHE MCP source tree. Detective and
MCP share the root `go.mod` and `go.sum`; no second checkout, Go module, or
`go.work` is needed. Record the exact reviewed Git commit used to build it.

The native Desktop target is a **macOS 13+ Apple silicon source-build preview**.
No prebuilt ZIP, stable installer, Developer ID signature or notarization is
provided by this procedure. Local ad-hoc signing is not publisher authentication.

## Requirements

- Go 1.27.0 or a newer patch release in the Go 1.27 line.
- Node.js 22.22.2 or newer, with npm.
- Git, Make and the macOS Xcode Command Line Tools, including the SDK and C compiler.
- Network access for locked Go/npm build dependencies on a clean machine.

The MCP-only `make build` path does not need Node.js or the native Desktop SDK.
The combined `make verify` path needs Node.js to produce the embedded frontend.
No PostgreSQL database or local model is required for offline inspection and
deterministic tests. A model and external sources are optional, separately
configured services; this build does not install or download them.

## Clean-source build

Clone AHE MCP, select the reviewed commit, and run every command below from its
repository root:

```sh
git clone https://github.com/Yui-Qi-Tang/ahe-mcp.git
cd ahe-mcp
git rev-parse HEAD
go version
node --version
npm --version
xcode-select -p
make build
make detective
make verify
```

Historically, `make desktop` uses the pinned Wails build tool and the root Go dependency graph;
it builds the frontend rather than relying on an old checked-in bundle.
`make detective` creates `bin/detective`, `bin/detective-source-demo` and
`bin/detective-news-source`. These are different from the legacy
`bin/ahe-detective` host built by `make build`.

Desktop output is:

```text
apps/detective/build/bin/
  AHE Detective.app/
  Start.command
  detective-source-demo
```

Keep these items together when copying the build to a reviewed local installation
directory. Do not copy source-control directories, test output, credentials or
private workspaces as part of an installation. Record the Git commit alongside
the installed app and MCP binaries so a paired upgrade can be reproduced.

Check the app's version without opening a workspace:

```sh
apps/detective/build/bin/Start.command --version
```

The expected integrated runtime version is `0.1.0-preview.18`; Finder's bundle
version may show the base `0.1.0`, so use this command to identify the preview.
If macOS refuses a copied app, do not disable Gatekeeper or remove quarantine protections as an installation
step. Verify the source/build provenance and use the organization's approved
software installation or signing process. A successful local source build does
not prove that the app can be distributed to other machines without additional
signing and acceptance work.

## First launch: choose the workspace intentionally

For a new isolated test on the build machine:

```sh
make desktop-trial
```

This builds and directly launches a new offline app in a private
`/private/tmp/detective-desktop-trial.*` directory. It prints the actual directory
path, leaves previous windows alone, and retains the directory after exit. Every
invocation creates a different workspace; it does **not** resume a previous
trial. A successful process exit is not UI acceptance.

For repeatable everyday preview testing, open `Start.command` in the built or
copied installation directory. It reuses:

```text
~/Library/Application Support/Detective Preview
```

The launcher asks the app to validate/create the workspace. It does not weaken
permissions or rewrite an incompatible existing directory. A workspace already
owned by another running Detective is refused; close that workspace's existing
window or deliberately choose another directory.

Directly opening `AHE Detective.app` without `Start.command` uses a different
default directory, `~/Library/Application Support/Detective Desktop`. To reopen
a particular earlier trial, use the app executable with its exact retained
canonical private path:

```sh
"apps/detective/build/bin/AHE Detective.app/Contents/MacOS/AHE Detective" \
  -data-dir /absolute/private/existing-detective-workspace
```

The directory must belong to the current user and have the app's required
private permissions (`0700` directories and `0600` saved data files). Do not
recursively change permissions on an unrelated directory to force it to open.
Use the app's displayed workspace identifier to distinguish multiple windows.

## Connect a source and use chat

1. In **資料源與連線**, choose actual mode and configure the source MCP you intend
   to use. For stdio, supply one approved executable launcher path and an explicit
   tool allowlist. Saving settings does not call the source.
2. Discover tools, inspect the selected tool's schema and complete arguments,
   then confirm the source call. The returned text is saved as the current source.
3. Configure an already-running local OpenAI-compatible endpoint and the exact
   installed model name. The default endpoint/model are only initial settings,
   not proof that either service exists on the machine.
4. Return to **工作台** to read the source and chat. Chat does not automatically
   call source tools, select evidence paragraphs or submit pending proposals.
   The separate task-selection page and workbench selection card are removed;
   the developer CLI and underlying selector remain available separately.

The normal UI has no local-file, saved-work or synthetic-demo opening controls,
and no persistent model-not-started warning on the workbench. The model must
still be explicitly configured and running before it can answer.
**新工作** clears the active work without loading an example, deleting saved
files or changing the applied connection settings. Offline mode does not run
the model or show fake model replies.

Merely saving connection settings does not call tools or the model. Tool
suggestions also require human confirmation before execution. On every restart,
the app returns to offline mode even if its connection settings were saved.

## Local Codebase preset (macOS, unreleased)

**Frozen Desktop reference, not an available CLI setup.** The current CLI source
configuration does not expose this preset. Never substitute an unguarded MCP
command to make the frozen setup appear functional.

Install Codebase separately; Desktop does not download it. The fixed preset
expects the current user's `.local/bin/codebase-memory-mcp` executable. In
**資料源與連線**, choose **加入本機 Codebase（選擇資料夾）**, select one repository,
and apply settings in local mode. Advanced coordinates are read-only/collapsed.
Use the separate **建立／更新此資料夾索引（不入庫）** action before querying it.

In **工作台**, select the saved Codebase connection, fetch its tool list and ask
a question. Review each proposed tool/argument card before confirming the read.
The network block applies to the Codebase process tree, not to the separately
configured local-model service. Other MCP servers are not available in chat.
Do not replace the preset with a wrapper or remote server to bypass startup
errors. See [the operation boundaries](README.md#network-blocked-codebase-chat-unreleased).

## Connect directly to Atlassian Rovo MCP with OAuth

Desktop supports the official `https://mcp.atlassian.com/v2/mcp` endpoint using
Streamable HTTP. It does not require `mcp-remote`, a local provider, an adapter
launcher, a gateway, or a DNS override. This is the Desktop source connection;
the legacy `ahe-detective` collector and its pinned local adapter remain separate.

1. In **資料源與連線**, choose **實際模式**, add a source connection and select
   **Atlassian 官方 MCP · OAuth**. The endpoint is fixed. Review the proposed
   allowlist (`getAccessibleAtlassianResources`, `getJiraIssue`,
   `getConfluenceContent`) and apply settings.
2. Select the saved server under **手動來源工具**, then press **登入 Atlassian**.
   Copy the displayed authorization URL and open it yourself. Detective does not
   inspect or automatically open a browser. Complete Atlassian consent before
   the roughly two-minute operation deadline; the application's Cancel action
   stops waiting and closes the loopback callback.
3. After Detective reports login complete, press **取得工具清單**. Select a
   discovered tool and inspect its live input schema. A separately confirmed
   `getAccessibleAtlassianResources` call can obtain the `cloudId`; use the exact
   returned site ID and the selected tool's schema for subsequent calls.
4. Confirm the exact tool and arguments for each read. OAuth login alone does not
   fetch Jira/Confluence content, run a model, or send anything to AHE. The raw
   MCP result and its text projection follow the existing private source-receipt
   flow; provider revisions remain unknown unless separately established.

The client discovers protected-resource and authorization-server metadata,
registers a public client, and uses authorization code + PKCE S256 with a random
state and a `127.0.0.1` callback on an ephemeral port. It requests only account,
Jira/Confluence read/search and offline-refresh scopes. Access and refresh tokens
stay in backend memory and are excluded from settings, UI state, receipts and
logs. Refresh occurs only when an explicit source operation needs it. A failed
refresh or HTTP 401 requires login again; a source call is never replayed.

**清除本次登入**, applying settings, starting offline rehearsal, or closing the
app forgets local credentials and invalidates the associated tool inventory.
This does not revoke the grant at Atlassian; use your Atlassian account controls
for server-side revocation. Restart begins offline and requires a new login.

Your organization must permit this MCP client, its localhost callback and the
connecting IP. If metadata, registration or consent is refused, ask the site
administrator to check those settings. The UI does not request broader scopes
or switch to API-token authentication. General Streamable HTTP connections remain
unauthenticated and loopback-only; arbitrary HTTPS endpoints are not enabled.

Tool availability depends on granted scopes and site policy. The v2 endpoint
advertises primary tools; this client does not automatically run `discover` or
an execution wrapper to find deferred tools. Read/search primary tools can be
added to the operator allowlist and must be discovered and confirmed normally.

The implementation is covered by synthetic OAuth/MCP tests, including refresh,
callback state validation, cancellation and credential boundaries. A successful
real-tenant consent and read must be separately verified by the operator; local
tests do not prove your organization's access policy allows the connection.

References: [Atlassian OAuth configuration](https://developer.atlassian.com/cloud/rovo-mcp/guides/configuring-oauth-2-1/),
[supported tools](https://developer.atlassian.com/cloud/rovo-mcp/guides/supported-tools/),
and [MCP authorization](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization).

## Connect to AHE MCP

First complete the root [MCP installation](../../INSTALL.md), including the
separately selected private PostgreSQL schema and bounded runtime identities.
Use the same reviewed source commit for MCP and Desktop when qualifying a pair.

In **資料源與連線**, find **AHE 證據庫（選用）** after the source settings and
expand its advanced connection settings. These fields are unnecessary for
source collection or local chat. They do not install or configure PostgreSQL.
If AHE has been installed, enter the full paths of executable launchers
configured and supplied by the AHE installer/operator. The current installation
does not create these files automatically; leave the fields blank if those
paths have not been provided:

- **證據查詢程式** (Query): read-only evidence and pending-state lookup; no writes.
- **待審提交程式** (Intake): source/extraction/pending submissions, with its own
  credentials. Pending proposals have not been admitted as evidence.
- **人工審閱程式** (Review): writes an exact admit/reject/audit_only decision only
  after explicit human confirmation, using separate reviewer credentials.

These programs are also called launchers in the MCP installation guide. Each
value is one executable path, not `VAR=value command`, a password, or a DB
URL. Keep launcher configuration, credentials and saved workspaces outside Git.
Never substitute a migration or operator identity for a failing runtime role.
Source MCP connections and these AHE launchers are different settings.

Start with Query only. Enabling intake/review is an additional deployment and
human-authorization decision, not a consequence of installing the Desktop.
Module sharing does not bypass stdio MCP or make the Desktop a direct DB writer.
See [authority and review](README.md#authority-and-review).

## Upgrade and existing workspaces

1. Record the old and new reviewed source commits. Save work and close the app
   instance that owns the workspace being upgraded.
2. Make a protected backup of that workspace and its original immutable
   receipts/Brief files. Preserve permissions and keep backups outside Git.
3. Build and verify the new app and MCP binaries from the same checkout. Replace
   only the reviewed app, `Start.command`, and accompanying executable files;
   do not replace a workspace with the build directory.
4. If the installation directory changed, review the saved source-command and
   MCP launcher paths. Update protected launcher binary paths without copying
   or displaying their credentials.
5. Reopen through the same entrypoint or exact `-data-dir`. Verify the version,
   workspace identifier and saved records. Restart begins offline; explicitly
   enable actual mode only after checking the connection settings.

Old `Start.command` installs use the persistent `Detective Preview` workspace;
direct app launches use `Detective Desktop`. `make desktop-trial` always creates
a fresh directory, so it is not an upgrade/resume command. Do not move the former
Detective Git repository into a workspace or assume its test artifacts are user
data to import. No DB migration, clearing, role change or evidence repair is
performed by the app build or launcher.

Unsaved chat and drafts are session-only and are not upgrade backups. If an
existing file is refused or a receipt cannot be verified, retain the original
and investigate; do not edit IDs, overwrite the receipt, or resubmit a changed
decision to make the screen look complete.

## Verification and limits

```sh
make desktop-test
make desktop-startup-test
```

The first target runs the shared verification suite and race tests. The second
builds the current app and runs fresh non-UI startup checks for version,
occupied-workspace refusal and invalid-directory refusal. Neither replaces a
human UI walkthrough or the separately selected disposable-DB review/recovery
acceptance. Opt-in model, source and DB experiments are not run just by building
or installing the application.

For Linux full-module verification, use the separate
[GTK/WebKit prerequisite and build-tag instructions](../../INSTALL.md#shared-module-verification-on-linux).
They do not turn this macOS source-build guide into a Linux Desktop release.

This preview is for a single local user. Source/model reliability, reviewer
authorization and formal production-writer qualification remain separate from
the fact that the source compiles and the app launches.
