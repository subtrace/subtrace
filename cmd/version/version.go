package version

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/peterbourgon/ff/v3/ffcli"
	"golang.org/x/sys/unix"
)

var (
	Release    = "b000"
	CommitHash = "unknown"
	CommitTime = "unknown"
	BuildTime  = "unknown"
)

func getExecutableHashInner() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get executable: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open executable: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, bufio.NewReader(io.LimitReader(f, 64<<20))); err != nil {
		return "", fmt.Errorf("copy hash: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var executableHashOnce sync.Once
var executableHash = "unknown"

func getExecutableHash() string {
	executableHashOnce.Do(func() {
		if hash, err := getExecutableHashInner(); err == nil {
			executableHash = hash
		}
	})
	return executableHash
}

func GetCanonicalString() string {
	ret := fmt.Sprintf("%s-%s", Release, CommitHash)
	if Release == "b000" && CommitHash == "unknown" {
		ret += getExecutableHash()
	}
	return ret
}

type Command struct {
	flags struct {
		json bool
	}

	ffcli.Command
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "version"
	c.ShortUsage = "subtrace version [flags]"
	c.ShortHelp = "print subtrace version"

	c.FlagSet = flag.NewFlagSet("", flag.ContinueOnError)
	c.FlagSet.BoolVar(&c.flags.json, "json", false, "output in JSON format")

	c.Exec = c.entrypoint
	return &c.Command
}

func cstr(b []byte) string {
	end := bytes.IndexByte(b, 0)
	if end != -1 {
		return string(b[:end])
	}
	return string(b)
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	fmt.Printf("%s\n", Full(c.flags.json))
	return nil
}

func Full(isJSON bool) string {
	buildGoVersion, buildOS, buildArch := "unknown", "unknown", "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		buildGoVersion = info.GoVersion
		for _, s := range info.Settings {
			switch s.Key {
			case "GOOS":
				buildOS = s.Value
			case "GOARCH":
				buildArch = s.Value
			}
		}
	}

	kernelName, kernelVersion, kernelArch := "Unknown", "unknown", "unknown"
	var buf unix.Utsname
	if err := unix.Uname(&buf); err == nil {
		kernelName = cstr(buf.Sysname[:])
		kernelVersion = cstr(buf.Release[:])
		kernelArch = cstr(buf.Machine[:])
	}

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

	b := new(bytes.Buffer)
	if isJSON {
		enc := json.NewEncoder(b)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"release":        Release,
			"commitHash":     CommitHash,
			"commitTime":     CommitTime,
			"buildTime":      BuildTime,
			"buildGoVersion": buildGoVersion,
			"buildOS":        buildOS,
			"buildArch":      buildArch,
			"executableHash": getExecutableHash(),
			"kernelName":     kernelName,
			"kernelVersion":  kernelVersion,
			"kernelArch":     kernelArch,
			"uid":            os.Getuid(),
			"gid":            os.Getuid(),
			"effectiveCaps":  effectiveCaps,
		})
	} else {
		fmt.Fprintf(b, "%s\n", Release)
		fmt.Fprintf(b, "  commit %s at %s\n", CommitHash, CommitTime)
		fmt.Fprintf(b, "  built with %s %s/%s at %s hash %s\n", buildGoVersion, buildOS, buildArch, BuildTime, getExecutableHash())
		fmt.Fprintf(b, "  kernel %s %s on %s\n", kernelName, kernelVersion, kernelArch)
		fmt.Fprintf(b, "  running on %s/%s with uid %d gid %d\n", runtime.GOOS, runtime.GOARCH, os.Geteuid(), os.Getgid())
		fmt.Fprintf(b, "  effective caps %s", effectiveCaps)
	}
	return b.String()
}

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
