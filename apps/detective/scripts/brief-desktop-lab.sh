#!/bin/sh
# Synthetic Desktop lab only: no existing DB, profile, source or credential is used.
# The new cluster and its artifacts are retained; this script never deletes a DB.
set -eu
umask 077

fail() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
    printf '%s\n' \
        'Usage: sh scripts/brief-desktop-lab.sh setup /absolute/ahe-mcp [postgres-bin-dir]' \
        '       sh scripts/brief-desktop-lab.sh start|stop|status|snapshot /absolute/lab-dir' \
        'setup creates a new private-socket cluster, empty DB and protected launchers.' \
        'No Desktop, model, source intake or review writer is started.' \
        'stop retains the cluster and artifacts. snapshot exports only this lab DB.' \
        'For another empty trial, run setup again; there is no clear/delete command.'
}

safe_path() {
    case "$1" in /*) ;; *) fail 'An absolute path is required.' ;; esac
    case "$1" in *[!a-zA-Z0-9_./-]*|*/../*|*/./*|*/|/) fail 'Lab paths require plain canonical ASCII paths without spaces or metacharacters.' ;; esac
}

canonical_dir() {
    safe_path "$1"
    [ -d "$1" ] && [ ! -L "$1" ] || fail 'Expected an existing non-symlink directory.'
    task_resolved=$(CDPATH= cd -P "$1" && pwd -P)
    [ "$task_resolved" = "$1" ] || fail 'Directory must already use its canonical path.'
}

private_dir() {
    canonical_dir "$1"
    case "$(uname -s)" in
        Darwin) task_mode=$(stat -f '%u %Lp' "$1") ;;
        Linux) task_mode=$(stat -c '%u %a' "$1") ;;
        *) fail 'This lab script supports macOS or Linux only.' ;;
    esac
    [ "$task_mode" = "$(id -u) 700" ] || fail 'Lab directory must be current-user-owned mode 0700.'
}

# Clear ambient PG*, DATABASE_DNS and shell startup configuration. PostgreSQL
# clients use -w and a lab-owned empty passfile, never the operator passfile.
clean() { env -i PATH="$task_path" LC_ALL=C "$@"; }
pg_client() { clean PGPASSFILE="$lab_dir/credentials/no-passwords.pgpass" "$@"; }

pg_status() { clean "$pg_bin/pg_ctl" -D "$lab_dir/pgdata" status; }

start_cluster() {
    private_dir "$socket_dir"
    clean "$pg_bin/pg_ctl" -D "$lab_dir/pgdata" -l "$lab_dir/postgres.log" \
        -o "-c listen_addresses= -c unix_socket_directories=$socket_dir -c unix_socket_permissions=0700 -p 55439" \
        -w -t 30 start >>"$lab_dir/cluster-control.log" 2>&1
}

stop_cluster() {
    clean "$pg_bin/pg_ctl" -D "$lab_dir/pgdata" -m fast -w -t 30 stop \
        >>"$lab_dir/cluster-control.log" 2>&1
}

sql() {
    pg_client "$pg_bin/psql" -X -w -v ON_ERROR_STOP=1 -qAt \
        -h "$socket_dir" -p 55439 -U ahe_brief_operator -d ahe_brief_lab "$@"
}

load_lab() {
    canonical_dir "$1"
    case "$1" in "$repo_dir"/build/brief-desktop-lab.*) ;; *) fail 'Refusing a directory outside this repository lab prefix.' ;; esac
    lab_dir=$1
    private_dir "$lab_dir"
    [ -f "$lab_dir/lab.marker" ] && [ ! -L "$lab_dir/lab.marker" ] || fail 'Missing lab ownership marker.'
    IFS= read -r task_marker <"$lab_dir/lab.marker"
    [ "$task_marker" = 'ahe-brief-desktop-lab/v1' ] || fail 'Wrong lab ownership marker.'
    [ -f "$lab_dir/pg-bin.path" ] && [ ! -L "$lab_dir/pg-bin.path" ] || fail 'Missing PostgreSQL binary coordinate.'
    [ -f "$lab_dir/socket.path" ] && [ ! -L "$lab_dir/socket.path" ] || fail 'Missing private socket coordinate.'
    IFS= read -r pg_bin <"$lab_dir/pg-bin.path"
    IFS= read -r socket_dir <"$lab_dir/socket.path"
    canonical_dir "$pg_bin"
    case "$socket_dir" in /private/tmp/ahe-brief.*) ;; *) fail 'Wrong private socket prefix.' ;; esac
    private_dir "$socket_dir"
    private_dir "$lab_dir/pgdata"
    [ -f "$lab_dir/pgdata/PG_VERSION" ] || fail 'Missing initialized lab cluster.'
    task_path="$pg_bin:/usr/bin:/bin:/usr/sbin:/sbin"
}

task_script_dir=$(CDPATH= cd -P "$(dirname "$0")" && pwd -P)
repo_dir=$(CDPATH= cd -P "$task_script_dir/.." && pwd -P)
mcp_repo_dir=$(CDPATH= cd -P "$repo_dir/../.." && pwd -P)
task_operation=${1:-help}
case "$task_operation" in
    help|-h|--help) usage; exit 0 ;;
    start|stop|status|snapshot)
        [ "$#" -eq 2 ] || { usage >&2; exit 2; }
        load_lab "$2"
        case "$task_operation" in
            start)
                [ -f "$lab_dir/setup.complete" ] || fail 'Setup did not complete; preserve diagnostics and create a fresh lab.'
                if pg_status >/dev/null 2>&1; then fail 'This lab is already running.'; fi
                start_cluster || fail 'Lab startup failed; inspect its retained private logs.'
                printf 'Lab started: %s\n' "$lab_dir"
                ;;
            stop)
                if pg_status >/dev/null 2>&1; then
                    stop_cluster || fail 'Lab stop did not complete; inspect its retained private logs.'
                else
                    fail 'Lab is not confirmed running; no process was stopped.'
                fi
                printf 'Lab stopped; all data and artifacts retained: %s\n' "$lab_dir"
                ;;
            status) pg_status ;;
            snapshot)
                [ -f "$lab_dir/setup.complete" ] || fail 'Cannot snapshot an incomplete lab setup.'
                task_dump_dir=$(mktemp -d "$lab_dir/snapshot.XXXXXX")
                pg_client "$pg_bin/pg_dump" -w -h "$socket_dir" -p 55439 \
                    -U ahe_brief_operator -d ahe_brief_lab -Fc -f "$task_dump_dir/ahe_brief_lab.dump" \
                    >"$task_dump_dir/dump.log" 2>&1 || fail 'Snapshot failed; partial artifact retained.'
                clean "$pg_bin/pg_restore" --file=/dev/null "$task_dump_dir/ahe_brief_lab.dump" \
                    >"$task_dump_dir/decode.log" 2>&1 || fail 'Snapshot archive decoding failed; artifacts retained.'
                shasum -a 256 "$task_dump_dir/ahe_brief_lab.dump" >"$task_dump_dir/SHA256SUMS"
                printf 'Lab DB archive saved and decoded (not an actual restore test): %s\n' "$task_dump_dir"
                ;;
        esac
        exit 0
        ;;
    setup) [ "$#" -ge 2 ] && [ "$#" -le 3 ] || { usage >&2; exit 2; } ;;
    *) usage >&2; exit 2 ;;
esac

mcp_dir=$2
canonical_dir "$mcp_dir"
[ "$mcp_dir" = "$mcp_repo_dir" ] || fail 'Select the AHE MCP root that contains this Detective checkout.'
[ -f "$mcp_dir/internal/mcplaunch/config.go" ] && [ -f "$mcp_dir/cmd/ahe-runtime-admin/main.go" ] || fail 'Select the current native AHE MCP checkout.'
if [ "$#" -eq 3 ]; then
    pg_bin=$3
else
    task_pg_ctl=$(command -v pg_ctl) || fail 'PostgreSQL tools are not installed; supply their existing directory.'
    pg_bin=$(CDPATH= cd -P "$(dirname "$task_pg_ctl")" && pwd -P)
fi
canonical_dir "$pg_bin"
for task_program in initdb pg_ctl postgres psql createdb pg_dump pg_restore; do
    [ -x "$pg_bin/$task_program" ] || fail "Missing installed PostgreSQL program: $task_program"
done
task_go=$(command -v go) || fail 'Go is not installed.'
case "$task_go" in /*) ;; *) fail 'Go must resolve to an absolute executable.' ;; esac
canonical_dir "$repo_dir/build"
lab_dir=$(mktemp -d "$repo_dir/build/brief-desktop-lab.XXXXXX")
private_dir "$lab_dir"
socket_dir=$(mktemp -d /private/tmp/ahe-brief.XXXXXX)
private_dir "$socket_dir"
task_path="$pg_bin:/usr/bin:/bin:/usr/sbin:/sbin"
mkdir "$lab_dir/bin" "$lab_dir/config" "$lab_dir/credentials" "$lab_dir/desktop-data"
printf '' >"$lab_dir/credentials/no-passwords.pgpass"
printf '%s\n' 'ahe-brief-desktop-lab/v1' >"$lab_dir/lab.marker"
printf '%s\n' "$pg_bin" >"$lab_dir/pg-bin.path"
printf '%s\n' "$socket_dir" >"$lab_dir/socket.path"
printf 'New isolated lab: %s\n' "$lab_dir"
printf '%s\n' 'No original DB, Desktop profile or model is used. Runtime authentication is private-socket trust, not human identity.'

task_started=no
setup_exit() {
    task_exit=$?
    trap - 0
    if [ "$task_exit" -ne 0 ]; then
        if [ "$task_started" = yes ]; then stop_cluster || true; fi
        printf 'Setup failed. Private diagnostics and cluster retained: %s\n' "$lab_dir" >&2
        printf '%s\n' 'Do not treat a failed receipt as rollback. Do not reuse partial roles; start a fresh lab.' >&2
    fi
    exit "$task_exit"
}
trap setup_exit 0
trap 'exit 130' INT
trap 'exit 143' TERM

# Build only selected local sources, using existing module/toolchain caches.
# Network downloads and automatic toolchain installation are deliberately off.
for task_program in ahe-migrate ahe-runtime-admin ahe-mcp-launch ahe-ingest-mcp ahe-query-mcp; do
    (
        cd "$mcp_dir"
        env -i PATH="$(dirname "$task_go"):$task_path" HOME="$HOME" LC_ALL=C \
            GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
            "$task_go" build -o "$lab_dir/bin/$task_program" "./cmd/$task_program"
    ) >"$lab_dir/build-$task_program.log" 2>&1 || fail "Build failed: $task_program (private log retained; no dependency was installed)."
    chmod 0700 "$lab_dir/bin/$task_program"
done
"$task_go" version >"$lab_dir/go-version.txt"
"$pg_bin/postgres" --version >"$lab_dir/postgres-version.txt"
shasum -a 256 "$lab_dir/bin/"* >"$lab_dir/binary-SHA256SUMS"

clean "$pg_bin/initdb" -D "$lab_dir/pgdata" --username=ahe_brief_operator \
    --auth-local=trust --auth-host=reject --encoding=UTF8 --no-locale \
    >"$lab_dir/initdb.log" 2>&1 || fail 'Private cluster initialization failed.'
task_started=yes
start_cluster || fail 'Private cluster startup failed.'
pg_client "$pg_bin/createdb" -w -h "$socket_dir" -p 55439 -U ahe_brief_operator \
    --maintenance-db=postgres -T template0 ahe_brief_lab \
    >"$lab_dir/createdb.log" 2>&1 || fail 'Isolated database creation failed.'
sql -c 'REVOKE CREATE, TEMPORARY ON DATABASE ahe_brief_lab FROM PUBLIC; REVOKE ALL ON SCHEMA public FROM PUBLIC; CREATE SCHEMA ahe_brief;' \
    >"$lab_dir/schema-setup.log" 2>&1 || fail 'Isolated schema preparation failed.'
task_operator_url="postgresql://ahe_brief_operator@/ahe_brief_lab?host=$socket_dir&port=55439&sslmode=disable&connect_timeout=3"
clean DATABASE_DNS="${task_operator_url}&passfile=/dev/null&sslcert=&sslkey=&sslrootcert=" AHE_DATABASE_SCHEMA=ahe_brief "$lab_dir/bin/ahe-migrate" \
    >"$lab_dir/migration.json" 2>"$lab_dir/migration.log" || fail 'Native migration failed.'
task_current=$(sql -c "SELECT count(*)=46 AND max(migration_name)='000046_evidence_ingestion_reviewed_disposition.up.sql' FROM ahe_brief.schema_migrations;")
[ "$task_current" = t ] || fail 'Unexpected migration coordinate; this lab requires native migration 46.'

for task_profile in query intake source-claim-reviewer; do
    task_role_suffix=$(printf '%s' "$task_profile" | tr '-' '_')
    task_group="brief_${task_role_suffix}_group"
    task_login="brief_${task_role_suffix}_login"
    clean DATABASE_DNS="$task_operator_url" AHE_DATABASE_NAME=ahe_brief_lab AHE_DATABASE_SCHEMA=ahe_brief \
        AHE_DATABASE_ROLE="$task_group" AHE_DATABASE_LOGIN="$task_login" AHE_RUNTIME_PROFILE="$task_profile" \
        "$lab_dir/bin/ahe-runtime-admin" provision >"$lab_dir/provision-$task_profile.json" \
        2>"$lab_dir/provision-$task_profile.log" || fail "Provisioning failed: $task_profile"
    task_runtime_url="postgresql://$task_login@/ahe_brief_lab?host=$socket_dir&port=55439&sslmode=disable&connect_timeout=3"
    clean DATABASE_DNS="$task_runtime_url" AHE_DATABASE_NAME=ahe_brief_lab AHE_DATABASE_SCHEMA=ahe_brief \
        AHE_DATABASE_ROLE="$task_group" AHE_DATABASE_LOGIN="$task_login" AHE_RUNTIME_PROFILE="$task_profile" \
        "$lab_dir/bin/ahe-runtime-admin" verify >"$lab_dir/verify-$task_profile.json" \
        2>"$lab_dir/verify-$task_profile.log" || fail "Bounded LOGIN verification failed: $task_profile"
    printf '%s\n' "$task_runtime_url" >"$lab_dir/credentials/$task_profile.dsn"
    task_binary=ahe-ingest-mcp
    if [ "$task_profile" = query ]; then task_binary=ahe-query-mcp; fi
    # Paths are restricted above, so JSON and shell quoting need no interpolation escape layer.
    printf '{"schema_version":"ahe-mcp-launcher/v1","binary_path":"%s/bin/%s","database_dns_file":"%s/credentials/%s.dsn","database":"ahe_brief_lab","session_user":"%s","schema":"ahe_brief","role":"%s","profile":"%s","principal_id":"mock:detective-desktop-e2e:%s"}\n' \
        "$lab_dir" "$task_binary" "$lab_dir" "$task_profile" "$task_login" "$task_group" "$task_profile" "$task_profile" \
        >"$lab_dir/config/$task_profile.json"
    printf '#!/bin/sh\nexec "%s/bin/ahe-mcp-launch" --config "%s/config/%s.json"\n' \
        "$lab_dir" "$lab_dir" "$task_profile" >"$lab_dir/$task_profile-launcher"
    chmod 0700 "$lab_dir/$task_profile-launcher"
done
unset task_operator_url task_runtime_url
sql -c "SELECT json_build_object('database',current_database(),'schema','ahe_brief','canonical_nodes',(SELECT count(*) FROM ahe_brief.canonical_graph_nodes),'canonical_edges',(SELECT count(*) FROM ahe_brief.canonical_graph_edges),'proposals',(SELECT count(*) FROM ahe_brief.proposal_occurrences),'source_snapshots',(SELECT count(*) FROM ahe_brief.source_snapshots),'authority_effect','empty_disposable_lab_only');" \
    >"$lab_dir/baseline-counts.json"
printf '{"schema_version":"ahe-brief-desktop-lab/v1","database":"ahe_brief_lab","schema":"ahe_brief","socket_directory":"%s","port":55439,"authentication":"private_socket_trust_only_not_human_identity","desktop_data_directory":"%s/desktop-data","query_launcher":"%s/query-launcher","intake_launcher":"%s/intake-launcher","review_launcher":"%s/source-claim-reviewer-launcher","mcp_source_root":"%s","review_boundary":"simulated_operator_fixture_only_not_authenticated_human_approval"}\n' \
    "$socket_dir" "$lab_dir" "$lab_dir" "$lab_dir" "$lab_dir" "$mcp_dir" >"$lab_dir/lab-info.json"
printf '%s\n' 'setup_complete_no_desktop_model_or_evidence_writes' >"$lab_dir/setup.complete"
printf 'Setup complete. Configure Desktop with these executable paths:\n  Query: %s/query-launcher\n  Intake: %s/intake-launcher\n  Review: %s/source-claim-reviewer-launcher\n' "$lab_dir" "$lab_dir" "$lab_dir"
printf 'Use the new Desktop data directory: %s/desktop-data\n' "$lab_dir"
printf 'Stop and retain everything: sh "%s/brief-desktop-lab.sh" stop "%s"\n' "$task_script_dir" "$lab_dir"
printf '%s\n' 'Discover and validate live MCP tool schemas before intake/review. Setup itself does not run either writer.'
