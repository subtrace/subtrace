package filter

import (
	"fmt"
	"reflect"

	"github.com/google/cel-go/cel"
	"github.com/google/martian/v3/har"
)

type Action string

const (
	ActionInvalid Action = ""
	ActionInclude        = "include"
	ActionExclude        = "exclude"
)

type Filter struct {
	Action  Action
	program cel.Program
}

func NewFilter(expr string, action Action) (*Filter, error) {
	switch action {
	case "include":
	case "exclude":
	default:
		return nil, fmt.Errorf("invalid action %q", action)
	}

	env, err := cel.NewEnv(
		cel.Variable("tags", cel.DynType),
		cel.Variable("duration", cel.DynType),
		cel.Variable("request", cel.DynType),
		cel.Variable("response", cel.DynType),
	)
	if err != nil {
		return nil, fmt.Errorf("create env: %w", err)
	}

	ast, iss := env.Compile(expr)
	if err = iss.Err(); err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}

	if got, want := ast.OutputType(), cel.BoolType; !reflect.DeepEqual(got, want) {
		return nil, fmt.Errorf("invalid output type: got %v, want %v", got, want)
	}

	program, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("create program: %w", err)
	}

	f := &Filter{Action: action, program: program}
	if _, err := f.Eval(map[string]string{}, dummy); err != nil {
		return nil, fmt.Errorf("static test: %w", err)
	}
	return f, nil
}

func (f *Filter) Eval(tags map[string]string, entry *har.Entry) (bool, error) {
	ret, _, err := f.program.Eval(map[string]any{
		"tags":     tags,
		"duration": entry.Time,
		"request": map[string]any{
			"method": entry.Request.Method,
			"url":    entry.Request.URL,
		},
		"response": map[string]any{
			"status": entry.Response.Status,
		},
	})
	if err != nil {
		return false, fmt.Errorf("eval: %w", err)
	}

	if x, ok := ret.Value().(bool); !ok {
		return false, fmt.Errorf("invalid return type: got %T, want bool", ret.Value())
	} else {
		return x, nil
	}
}

var dummy = &har.Entry{
	Time: 1234,
	Request: &har.Request{
		Method: "GET",
		URL:    "/example",
	},
	Response: &har.Response{
		Status: 200,
	},
}
