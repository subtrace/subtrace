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

func (config *Config) FindMatchingRule(req *http.Request, resp *http.Response) (rule *Rule, found bool) {
	for _, rule := range config.Rules {
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

func New(filepath string) (*Config, error) {
	if filepath == "" {
		return nil, fmt.Errorf("no filepath specified")
	}
	b, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	c := &Config{}
	if err = yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	slog.Debug(fmt.Sprintf("parsed config file, found %d rules", len(c.Rules)))

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate config file: %w", err)
	}

	return c, nil
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

type Rule struct {
	If   string `yaml:"if"`
	Then string `yaml:"then"`

	program cel.Program
}

type Config struct {
	Rules []Rule `yaml:"rules"`
}
