#!/usr/bin/env bash

set -eo pipefail

pinned_version=chromium/6968

cmd:devtools() {
  (cmd:fetch)
  (cmd:bundle)
}

cmd:fetch() {
  if [[ ! -d "depot_tools" ]]; then
    echo "cloning depot_tools..."
    time git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git
  fi

  export PATH="${PWD}/depot_tools:${PATH}"

  if [[ ! -d "devtools-frontend" ]]; then
    echo "fetching devtools-frontend..."
    time fetch devtools-frontend
  fi

  cd devtools-frontend

  echo "syncing to ${pinned_version}..."
  time gclient sync -r "${pinned_version}"

  echo "last commit:"
  git log -1

  echo "building..."
  time gn gen out/Default
  time autoninja -C out/Default
}

cmd:bundle() {
  python3 ./build.py | gzip >bundle/devtools.js.gz
  sha256sum bundle/devtools.js.gz
}

main() {
  if [[ "$1" != "" ]]; then
    cmd=$(echo "$1" | tr ':' ' ')
    cmd:$cmd
  else
    cmd:devtools
  fi
}

main $*
