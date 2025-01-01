package validate

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"

	"log/slog"

	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"subtrace.dev/rpc"
	"subtrace.dev/tunnel"
)

type Command struct {
	flags struct {
		token string
	}
	ffcli.Command
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "validate"
	c.ShortUsage = "subtrace validate [flags]"
	c.ShortHelp = "validate the Subtrace authentication token"
	c.LongHelp = `
The validate command checks if a Subtrace authentication token is valid.
It accepts a token either via the -token flag or the SUBTRACE_TOKEN environment variable.

Examples:
  # Validate token from environment variable
  export SUBTRACE_TOKEN="your-token"
  subtrace validate

  # Validate token provided via flag
  subtrace validate -token="your-token"

`

	c.FlagSet = flag.NewFlagSet("validate", flag.ContinueOnError)
	c.FlagSet.StringVar(&c.flags.token, "token", "", "token to validate (defaults to SUBTRACE_TOKEN env var)")

	c.Options = []ff.Option{ff.WithEnvVarPrefix("SUBTRACE")}
	c.Exec = c.exec
	return &c.Command
}

func (c *Command) exec(ctx context.Context, args []string) error {
	// Get token from flag or environment
	token := c.flags.token
	if token == "" {
		token = os.Getenv("SUBTRACE_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("no token provided via -token flag or SUBTRACE_TOKEN environment variable")
	}

	// Prepare request with explicit token
	opts := []rpc.Option{
		rpc.WithoutToken(), // Remove default token behavior
		func(r *http.Request) {
			r.Header.Set("authorization", fmt.Sprintf("Bearer %s", token))
		},
	}

	req := &tunnel.ValidateToken_Request{}
	var resp tunnel.ValidateToken_Response

	code, err := rpc.Call(ctx, &resp, "/api/ValidateToken", req, opts...)
	if err != nil {
		return fmt.Errorf("validate token request failed: %w", err)
	} else if code != http.StatusOK || resp.Error != "" {
		err := fmt.Errorf("ValidateToken: %s", http.StatusText(code))
		if resp.Error != "" {
			err = fmt.Errorf("%w: %s", err, resp.Error)
		}
		return err
	}

	if resp.IsValid {
		slog.Info("token validation successful", "status", "valid", "token_prefix", token[:8]+"...")
	} else {
		slog.Warn("token validation failed", "status", "invalid", "token_prefix", token[:8]+"...")
	}

	return nil
}
