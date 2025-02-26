#!/bin/sh
# Copyright (c) Subtrace, Inc.
# SPDX-License-Identifier: BSD-3-Clause
#
# This script detects your CPU architecture to download and install the latest
# version of the corresponding Subtrace binary.

set -eu

echo_unsupported() {
  echo "If you'd like us to support your system better, please email"
  echo "support@subtrace.dev with the following details we gathered:"
  echo ""

  if type uname 1>/dev/null 2>/dev/null; then
    echo "UNAME=$(uname -a)"
  else
    echo "UNAME=not_found"
  fi

  if [ -f /etc/os-release ]; then
    cat /etc/os-release
  else
    echo "OS_RELEASE=not_found"
  fi

  echo ""
  echo "The installer is exiting."
}

# From [1]:
#
# All the code is wrapped in a main function that gets called at the
# bottom of the file, so that a truncated partial download doesn't end
# up executing half a script.
#
# [1] https://tailscale.com/install.sh
main() {
  # Ideally, we'd like to do this with your distro's packaging system (for
  # example, using .deb for Debian). Until we figure out the requirements for
  # each distro, we use a fallback.

  os=
  if type uname 1>/dev/null 2>/dev/null; then
    kernel=$(uname -s)
    case "${kernel}" in
      Linux) os=linux;;
      *)
        echo "${os} isn't supported by Subtrace yet."
        echo ""
        echo_unsupported
        exit 1
    esac
  fi

  if [ -z "${os}" ]; then
    echo "The installer was unable to determine your operating system."
    echo ""
    echo_unsupported
    exit 1
  fi

  arch=
  if type uname 1>/dev/null 2>/dev/null; then
    machw="$(uname -m)"
    case "${machw}" in
      x86_64)  arch=amd64;;
      arm64)   arch=arm64;;
      aarch64) arch=arm64;;
      *)
        echo "${machw} isn't supported by Subtrace yet."
        echo ""
        echo_unsupported
        exit 1
    esac
  fi

  if [ -z "${arch}" ]; then
    echo "The installer was unable to determine your CPU architecture."
    echo ""
    echo_unsupported
    exit 1
  fi

  if type curl 1>/dev/null 2>/dev/null; then
    dir=$(dirname "$(command -v curl)")
    file="${dir}/subtrace"
    cmd="curl -fsSL -o ${file}"
  elif type wget 1>/dev/null 2>/dev/null; then
    dir=$(dirname "$(command -v wget)")
    file="${dir}/subtrace"
    cmd="wget -q -O ${file}"
  else
    echo "The installer needs either curl or wget to download the binary."
    echo "Please install either curl or wget to proceed."
    exit 1
  fi

  prefix=
  if [ "$(id -u)" != 0 ]; then
    if type sudo >/dev/null; then
      prefix=sudo
    elif type doas >/dev/null; then
      prefix=doas
    else
      echo "The installer needs to run commands as root. We couldn't find"
      echo "'sudo' or 'doas', either."
      echo ""
      echo "Please re-run the installer as root."
      exit 1
    fi
  fi

  url="https://subtrace.dev/download/latest/${os}/${arch}/subtrace"

  set -x
  ${prefix} ${cmd} "${url}"
  ${prefix} chmod +x "${file}"
  set +x
  echo

  echo "Installation complete! Run Subtrace with a token using:"
  echo ""
  echo "  SUBTRACE_TOKEN=... subtrace run"
  echo ""
}

main
