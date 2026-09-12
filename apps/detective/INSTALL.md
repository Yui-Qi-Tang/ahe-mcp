# Install Detective CLI and Desktop

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
make desktop
```

`make desktop` uses the pinned Wails build tool and the root Go dependency graph;
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

## First functional check without company data or a DB

1. Start in the default offline mode. Open the synthetic example and inspect its
   source/candidates. Offline chat replies are fixed rehearsal text; typing
   `admit` does not record a real decision.
2. To exercise a real local **synthetic source MCP**, open the source/connection
   settings, select actual mode, add a stdio connection, and supply the absolute
   path to the built `detective-source-demo` executable. Its allowed tool is
   `read_status`; use an empty JSON object, `{}`, for the tool arguments.
3. Save/apply settings, explicitly discover tools, select `read_status`, inspect
   its schema and arguments, then confirm the source call. This demo reads no
   network, company files or DB. A loaded demo source is not canonical evidence.
4. For extraction, separately configure an already running local
   OpenAI-compatible model endpoint and exact installed model name, then request
   extraction explicitly. The default example endpoint is
   `http://127.0.0.1:11434/v1`; the example model name is
   `gemma4:e4b-it-qat`. Neither is proof the service/model exists on this machine.

Merely saving connection settings does not call tools or the model. Tool
suggestions also require human confirmation before execution. On every restart,
the app returns to offline mode even if its connection settings were saved.

## Connect to AHE MCP

First complete the root [MCP installation](../../INSTALL.md), including the
separately selected private PostgreSQL schema and bounded runtime identities.
Use the same reviewed source commit for MCP and Desktop when qualifying a pair.

Enter the approved absolute launcher paths in Desktop settings:

- Query: read-only AHE evidence and pending-state lookup.
- Intake: source/extraction/pending submissions, with its own credentials.
- Review: separately authorized exact admit/reject/audit_only workflow, with its
  own reviewer credentials and explicit human confirmation.

Each value is one executable path, not `VAR=value command`, a password, or a DB
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
