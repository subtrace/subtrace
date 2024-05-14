#!/usr/bin/env bash

set -eo pipefail

cmd:subtrace() {
  CGO_ENABLED=0 go build -ldflags "-X 'subtrace.dev/cli/config.ControlPlaneURI=https://subtrace.dev'" -o subtrace
}

cmd:debug() {
  CGO_ENABLED=0 go build -ldflags "-X 'subtrace.dev/cli/config.ControlPlaneURI=http://127.0.0.1:8080'" -o subtrace
}

cmd:proto() {
  protoc --go_out=. --go_opt=paths=source_relative journal/journal.proto
}

cmd:clickhouse() {
  case $1 in
    start)
      docker run -d --rm --name subtrace_clickhouse \
        -p 127.0.0.1:9000:9000 \
        -v subtrace_clickhouse:/var/lib/clickhouse/ \
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
    "")
      docker exec -it subtrace_clickhouse clickhouse-client --host 127.0.0.1 --port 9000 --database subtrace
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
