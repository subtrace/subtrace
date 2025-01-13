package global

import (
	"subtrace.dev/config"
	"subtrace.dev/devtools"
	"subtrace.dev/event"
)

type Global struct {
	Config        *config.Config
	Devtools      *devtools.Server
	EventTemplate *event.Event
}
