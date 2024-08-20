#!/usr/bin/env bash

set -eo pipefail

cmd:subtrace() {
  SUBTRACE_RELEASE_VERSION=$(printf "b%03d" "$(git log --oneline | wc -l)")
  SUBTRACE_COMMIT_HASH="$(git log -1 --format='%H' 2>/dev/null || echo unknown)"
  SUBTRACE_COMMIT_TIME=$(TZ=UTC git log -1 --date='format-local:%Y-%m-%dT%H:%M:%SZ' --format='%cd' 2>/dev/null || echo unknown)
  SUBTRACE_BUILD_TIME="${SOURCE_DATE_EPOCH}"
  if [[ "${SUBTRACE_BUILD_TIME}" == "" ]]; then
    SUBTRACE_BUILD_TIME=$(TZ=UTC date '+%Y-%m-%dT%H:%M:%SZ')
  fi

  LDFLAGS=
  LDFLAGS="${LDFLAGS} -X subtrace.dev/cmd/version.Version=${SUBTRACE_RELEASE_VERSION}"
  LDFLAGS="${LDFLAGS} -X subtrace.dev/cmd/version.CommitHash=${SUBTRACE_COMMIT_HASH}"
  LDFLAGS="${LDFLAGS} -X subtrace.dev/cmd/version.CommitTime=${SUBTRACE_COMMIT_TIME}"
  LDFLAGS="${LDFLAGS} -X subtrace.dev/cmd/version.BuildTime=${SUBTRACE_BUILD_TIME}"

  CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o subtrace
}

cmd:proto() {
  protoc \
    event/event.proto \
    tunnel/tunnel.proto \
    --go_out=. --go_opt=paths=source_relative
}

cmd:clickhouse() {
  case $1 in
    start)
      format_schemas_dir=${SUBTRACE_CLICKHOUSE_FORMAT_SCHEMAS:=/var/lib/clickhouse/format_schemas/}
      mkdir -p "${format_schemas_dir}"
      docker run -d --rm --name subtrace_clickhouse \
        -u $(id -u):$(id -g) -e CLICKHOUSE_UID=0 -e CLICKHOUSE_GID=0 \
        -p 127.0.0.1:8123:8123 \
        -p 127.0.0.1:9000:9000 \
        -e CLICKHOUSE_DB=subtrace \
        -v subtrace_clickhouse:/var/lib/clickhouse/ \
        -v ${format_schemas_dir}:/var/lib/clickhouse/format_schemas/:rw \
        clickhouse/clickhouse-server:23
      ;;
    wait)
      sleep 0.5
      ;;
    clear)
      docker exec -it subtrace_clickhouse clickhouse-client --host 127.0.0.1 --port 9000 --query "drop database subtrace" || true
      docker exec -it subtrace_clickhouse clickhouse-client --host 127.0.0.1 --port 9000 --query "create database subtrace"
      ;;
    stop)
      docker stop subtrace_clickhouse
      ;;
    query)
      docker exec -i subtrace_clickhouse clickhouse-client --database subtrace --format PrettyCompactMonoBlock
      ;;
    "")
      docker exec -it subtrace_clickhouse clickhouse-client --database subtrace
      ;;
  esac
}

main() {
  if [[ "$1" != "" ]]; then
    cmd=$(echo "$1" | tr ':' ' ')
    set -x
    cmd:$cmd
  else
    set -x
    cmd:subtrace
  fi
}

main $*
