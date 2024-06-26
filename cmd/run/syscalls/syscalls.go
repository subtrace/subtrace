// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package syscalls

import (
	"fmt"
	"runtime"
)

func GetNumber(name string) int {
	nr, ok := names[name]
	if !ok {
		panic(fmt.Sprintf("GOOS=%s, GOARCH=%s: syscall %q not found", runtime.GOOS, runtime.GOARCH, name))
	}
	return nr
}

func GetName(nr int) string {
	for name := range names {
		if nr == names[name] {
			return name
		}
	}
	return fmt.Sprintf("SYS_0x%X", nr)
}
