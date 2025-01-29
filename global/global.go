package global

import (
	"subtrace.dev/config"
	"subtrace.dev/devtools"
)

type Global struct {
	Config   *config.Config
	Devtools *devtools.Server
}
