package global

import (
	"subtrace.dev/cmd/run/journal"
	"subtrace.dev/config"
	"subtrace.dev/devtools"
)

type Global struct {
	Config   *config.Config
	Devtools *devtools.Server
	Journal  *journal.Journal
}
