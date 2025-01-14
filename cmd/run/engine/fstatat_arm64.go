package engine

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func fstatat(dirfd int, ent *unix.Dirent, stat *unix.Stat_t) syscall.Errno {
	_, _, errno := unix.Syscall6(unix.SYS_FSTATAT, uintptr(dirfd), uintptr(unsafe.Pointer(&ent.Name[0])), uintptr(unsafe.Pointer(stat)), 0, 0, 0)
	return errno
}
