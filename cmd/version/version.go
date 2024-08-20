package version

import (
	"context"
	"fmt"
	"runtime"

	"github.com/peterbourgon/ff/v3/ffcli"
)

var (
	Version    = "unknown"
	CommitHash = "unknown"
	CommitTime = "unknown"
	BuildTime  = "unknown"
)

type Command struct {
	ffcli.Command
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "version"
	c.ShortUsage = "subtrace version"
	c.ShortHelp = "print subtrace version"

	c.Exec = c.entrypoint
	return &c.Command
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	fmt.Printf("subtrace version %s (%s) %s %s/%s\n", Version, CommitHash, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return nil
}
