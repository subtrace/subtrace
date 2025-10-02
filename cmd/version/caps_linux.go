package version

import (
	"fmt"

	"golang.org/x/sys/unix"
)

var capNames = []string{
	"chown",
	"dac_override",
	"dac_read_search",
	"fowner",
	"fsetid",
	"kill",
	"setgid",
	"setuid",
	"setpcap",
	"linux_immutable",
	"net_bind_service",
	"net_broadcast",
	"net_admin",
	"net_raw",
	"ipc_lock",
	"ipc_owner",
	"sys_module",
	"sys_rawio",
	"sys_chroot",
	"sys_ptrace",
	"sys_pacct",
	"sys_admin",
	"sys_boot",
	"sys_nice",
	"sys_resource",
	"sys_time",
	"sys_tty_config",
	"mknod",
	"lease",
	"audit_write",
	"audit_control",
	"setfcap",
	"mac_override",
	"mac_admin",
	"syslog",
	"wake_alarm",
	"block_suspend",
	"audit_read",
	"perfmon",
	"bpf",
	"checkpoint_restore",
}

func HasCapability(capability int) bool {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false
	}

	if capability < 32 {
		return (data[0].Effective & (1 << uint(capability))) != 0
	} else if capability < 64 {
		return (data[1].Effective & (1 << uint(capability-32))) != 0
	}
	return false
}

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
