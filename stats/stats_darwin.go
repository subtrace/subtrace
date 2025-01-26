package stats

import (
	"context"
)

type Stats struct{}

func New(ctx context.Context) *Stats {
	return new(Stats)

}

func (s *Stats) GetStatsTags() map[string]string {
	return map[string]string{}
}
