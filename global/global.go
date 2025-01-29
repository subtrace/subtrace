package global

import (
	"subtrace.dev/config"
	"subtrace.dev/devtools"
	"subtrace.dev/stats"
)

type Global struct {
	Stats    *stats.Stats
	Config   *config.Config
	Devtools *devtools.Server
}
