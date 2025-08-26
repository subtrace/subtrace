#!/bin/sh
set -e

/clickhouse_entrypoint.sh &
clickhouse_pid=$!

export SUBTRACE_CLICKHOUSE_HOST=localhost
export SUBTRACE_CLICKHOUSE_DATABASE=default
subtrace worker -v "$@" &
subtrace_pid=$!

sigterm() {
    kill -TERM "$subtrace_pid" 2>/dev/null
    kill -TERM "$clickhouse_pid" 2>/dev/null
}

sigint() {
    kill -INT "$subtrace_pid" 2>/dev/null
    kill -INT "$clickhouse_pid" 2>/dev/null
}

trap sigterm TERM
trap sigint INT

wait $subtrace_pid $clickhouse_pid
exit_code=$?
kill -TERM "$subtrace_pid" 2>/dev/null || true
kill -TERM "$clickhouse_pid" 2>/dev/null || true
exit $exit_code
