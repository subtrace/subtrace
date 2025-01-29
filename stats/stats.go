//go:build !linux

package stats

import (
	"context"
)

func Loop(ctx context.Context) {
}

func Load() map[string]string {
	return map[string]string{}
}
