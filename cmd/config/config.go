// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package config

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"reflect"

	"github.com/google/cel-go/cel"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Tags  map[string]string `yaml:"tags"`
	Rules []Rule            `yaml:"rules"`
}

func (c *Config) Load(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	if err = yaml.Unmarshal(b, c); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	slog.Debug("parsed config", "rules", len(c.Rules))
	return nil
}

func (c *Config) Validate() error {
	env, err := cel.NewEnv(
		cel.Variable("request", cel.DynType),
		cel.Variable("response", cel.DynType),
	)
	if err != nil {
		return fmt.Errorf("create cel env: %w", err)
	}

	for index, rule := range c.Rules {
		switch rule.Then {
		case "include":
		case "exclude":
		default:
			return fmt.Errorf("config: invalid action in rule: %q. Expected either 'include' or 'exclude'", rule.Then)
		}

		ast, iss := env.Compile(rule.If)
		if err = iss.Err(); err != nil {
			return fmt.Errorf("compile program: %w", err)
		}
		if !reflect.DeepEqual(ast.OutputType(), cel.BoolType) {
			return fmt.Errorf("typecheck program: Got %v, wanted %v result type", ast.OutputType(), cel.BoolType)
		}
		program, err := env.Program(ast)
		if err != nil {
			return fmt.Errorf("create program instance: %w", err)
		}
		c.Rules[index].program = program
	}

	// Test config on a dummmy request and response as a sanity check
	for _, rule := range c.Rules {
		if _, err := rule.Matches(&http.Request{URL: &url.URL{}}, &http.Response{}); err != nil {
			return fmt.Errorf("config test: %w", err)
		}
	}

	return nil
}

func (c *Config) FindMatchingRule(req *http.Request, resp *http.Response) (rule *Rule, found bool) {
	for _, rule := range c.Rules {
		matches, err := rule.Matches(req, resp)
		// Ignore errors here and skip this rule because we want to be robust when tracing requests
		if err != nil {
			continue
		}

		if matches {
			return &rule, true
		}
	}

	return nil, false
}

type Rule struct {
	If   string `yaml:"if"`
	Then string `yaml:"then"`

	program cel.Program
}

func (r *Rule) Matches(req *http.Request, resp *http.Response) (bool, error) {
	celReq := map[string]any{
		"method": req.Method,
		"url":    req.URL.String(),
	}
	celResp := map[string]any{
		"status": resp.StatusCode,
	}

	out, _, err := r.program.Eval(map[string]any{
		"request":  celReq,
		"response": celResp,
	})
	if err != nil {
		return false, fmt.Errorf("evaluting program on rule %q: %w", r.If, err)
	}

	match, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("evaluting program on rule %q: expected bool but got %T", r.If, out.Value())
	}

	return match, nil
}
