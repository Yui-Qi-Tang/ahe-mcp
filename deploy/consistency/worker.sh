#!/bin/sh
# Operator-owned paths; this file does not install or provision anything.
set -eu
if [ "$#" -ne 2 ]; then
    echo 'usage: worker.sh /absolute/private/worker.env /absolute/ahe-consistency-worker' >&2
    exit 2
fi
set -a
. "$1"
set +a
exec "$2" run
