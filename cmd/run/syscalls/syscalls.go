// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package syscalls

import (
	"fmt"
)

func GetName(nr int) string {
	for name := range names {
		if nr == names[name] {
			return name
		}
	}
	return fmt.Sprintf("SYS_0x%X", nr)
}
