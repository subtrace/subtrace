package version

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func GetEffectiveCaps() string {
	effectiveCaps := "unknown"
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err == nil {
		mask := (uint64(data[1].Effective) << 32) | (uint64(data[0].Effective) << 0)
		effectiveCaps = fmt.Sprintf("0x%016x", mask)
		for shift, name := range capNames {
			if mask&(1<<shift) != 0 {
				effectiveCaps += fmt.Sprintf(" +%s", name)
			} else {
				effectiveCaps += fmt.Sprintf(" -%s", name)
			}
		}
	}

	return effectiveCaps
}
