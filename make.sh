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
  LDFLAGS="${LDFLAGS} -X subtrace.dev/cmd/version.Release=${SUBTRACE_RELEASE_VERSION}"
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

main() {
  if [[ "$1" != "" ]]; then
    cmd=$(echo "$1" | tr ':' ' ')
    cmd:$cmd
  else
    cmd:subtrace
  fi
}

main $*
