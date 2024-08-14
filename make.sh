#!/usr/bin/env bash

set -eo pipefail

cmd:subtrace() {
  CGO_ENABLED=0 go build -o subtrace
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
