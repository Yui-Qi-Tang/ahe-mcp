# macOS launchd packaging

This directory packages the foreground `ahe-detective` binary for a per-user
`launchd` supervisor. It does not install a LaunchAgent and does not run schema
migrations.

This packaging is optional and does not make AHE Core macOS-only. Linux and
macOS can run `ahe-migrate`, `ahe-query-mcp`, `ahe-ingest-mcp`, and the local
Git/text Detective path; see [INSTALL.md](../../INSTALL.md) for the platform
matrix.

## Boundary

- Build the binary from a reviewed commit.
- Apply migrations as a separate trusted deployment step.
- Keep the host JSON and database credential outside the repository.
- Store only the absolute credential-file path in the plist. Never store the
  database URL in the plist.
- The credential file must be a regular file with mode `0400` or `0600`.
- The wrapper starts the host without `--migrate`.
- `launchd` restarts only unsuccessful exits and applies a per-process
  4,096-descriptor hard limit. It does not change the shell or system default.

## Prepare

Build the four core programs, including the trusted migration command:

```sh
make BIN_DIR=/absolute/release/bin build
chmod 0700 /absolute/release/run-ahe-detective.sh
chmod 0600 /absolute/private/database-dns
```

Apply migrations before bootstrapping or restarting the LaunchAgent:

```sh
DATABASE_DSN="$(cat /absolute/private/database-dns)" \
  /absolute/release/bin/ahe-migrate
```

The migration command is the schema-writing deployment boundary. The
LaunchAgent wrapper never runs it. `ahe-detective` and `ahe-query-mcp` verify
the current schema read-only and refuse startup when it does not match the
embedded migration contract. The separately launched `ahe-ingest-mcp` performs
the same verification.

Render `com.ahe.detective.plist.example` by replacing every
`__AHE_DETECTIVE_*__` token with an absolute path. Validate the rendered file:

```sh
plutil -lint /absolute/release/com.ahe.detective.plist
```

The normal per-user lifecycle is:

```sh
launchctl bootstrap "gui/$(id -u)" /absolute/release/com.ahe.detective.plist
launchctl print "gui/$(id -u)/com.ahe.detective"
launchctl kill SIGTERM "gui/$(id -u)/com.ahe.detective"
launchctl bootout "gui/$(id -u)/com.ahe.detective"
```

`bootout` is required before replacing the binary, config, wrapper, or plist.
Inspect the configured stdout/stderr JSON logs and PostgreSQL lifecycle rows
before declaring a restart healthy.
