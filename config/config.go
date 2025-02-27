// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/martian/v3/har"
	"gopkg.in/yaml.v3"
	"subtrace.dev/event"
	"subtrace.dev/filter"
	"subtrace.dev/tags"
)

type Config struct {
	parsed struct {
		AuthCredentials string            `yaml:"authCredentials"`
		Tags            map[string]string `yaml:"tags"`
		Rules           []struct {
			If   string `yaml:"if"`
			Then string `yaml:"then"`
		} `yaml:"rules"`
	}

	filters  []*filter.Filter
	template *event.Event
}

func New() *Config {
	c := &Config{template: event.New()}
	go tags.SetLocalTagsAsync(c.template)
	return c
}

func (c *Config) Load(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	if err := yaml.NewDecoder(bufio.NewReader(f)).Decode(&c.parsed); err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	for key, val := range c.parsed.Tags {
		for _, c := range key {
			switch {
			case c >= 'a' && c <= 'z':
			case c >= 'A' && c <= 'Z':
			case c >= '0' && c <= '9':
			case c == '_':
			default:
				return fmt.Errorf("validate tags: invalid key %q: only letters, digits and underscores allowed", key)
			}
		}

		c.template.Set(key, val)
	}

	for i, rule := range c.parsed.Rules {
		f, err := filter.NewFilter(rule.If, filter.Action(rule.Then))
		if err != nil {
			return fmt.Errorf("validate rules: rule %d: new filter: %w", i, err)
		} else {
			c.filters = append(c.filters, f)
		}
	}

	slog.Debug("parsed config", "rules", len(c.parsed.Rules), "tags", len(c.parsed.Tags))
	return nil
}

func (c *Config) ShouldRedactAuth() bool {
	switch c.parsed.AuthCredentials {
	case "keep":
		return false
	default:
		return true
	}
}

func (c *Config) GetMatchingFilter(tags map[string]string, entry *har.Entry) (*filter.Filter, error) {
	for i := 0; i < len(c.filters); i++ {
		match, err := c.filters[i].Eval(tags, entry)
		if err != nil {
			return nil, fmt.Errorf("filter %d: eval: %w", i, err)
		}
		if match {
			return c.filters[i], nil
		}
	}
	return nil, nil
}

func (c *Config) GetEventTemplate() *event.Event {
	return c.template.Copy()
}
