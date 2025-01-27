package run

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func hasSysPtrace() (bool, error) {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false, fmt.Errorf("capget: %w", err)
	}

	flag := (uint64(data[1].Effective) << 32) | (uint64(data[0].Effective) << 0)
	mask := uint64(1 << unix.CAP_SYS_PTRACE)
	return flag&mask != 0, nil
}
