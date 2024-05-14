// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package kernel

import (
	"bytes"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrUnsupportedVersion = fmt.Errorf("unsupported kernel version")

func CheckVersion(want string, log bool) (int, int, error) {
	wantMajor, wantMinor, err := parseMajorMinor(want)
	if err != nil {
		return 0, 0, fmt.Errorf("parse want: parse major/minor: %w", err)
	}

	var buf unix.Utsname
	if err := unix.Uname(&buf); err != nil {
		return 0, 0, fmt.Errorf("uname: %w", err)
	}

	sysname := string(buf.Sysname[:bytes.IndexByte(buf.Sysname[:], 0)])
	nodename := string(buf.Nodename[:bytes.IndexByte(buf.Nodename[:], 0)])
	release := string(buf.Release[:bytes.IndexByte(buf.Release[:], 0)])
	version := string(buf.Release[:bytes.IndexByte(buf.Release[:], 0)])
	machine := string(buf.Machine[:bytes.IndexByte(buf.Machine[:], 0)])
	if log {
		slog.Debug("parsed kernel info", "sysname", sysname, "nodename", nodename, "release", release, "version", version, "machine", machine)
	}

	gotMajor, gotMinor, err := parseMajorMinor(release)
	if err != nil {
		return 0, 0, fmt.Errorf("parse major/minor: %w", err)
	}

	if gotMajor > wantMajor {
		return gotMajor, gotMinor, nil
	}
	if gotMajor == wantMajor && gotMinor >= wantMinor {
		return gotMajor, gotMinor, nil
	}
	return gotMajor, gotMinor, ErrUnsupportedVersion
}

func parseMajorMinor(version string) (int, int, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("invalid version: not enough dots")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse major: %w", err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse minor: %w", err)
	}
	return major, minor, nil
}
