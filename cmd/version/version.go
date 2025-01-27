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
		ret += "-" + getExecutableHash()
	}
	return ret
}

type Command struct {
	ffcli.Command
	flags struct {
		json bool
	}
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

	effectiveCaps := GetEffectiveCaps()

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
