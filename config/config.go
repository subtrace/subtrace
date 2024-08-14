// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package config

import (
	"os"
)

func ControlURL() string {
	switch val := os.Getenv("SUBTRACE_DEPLOY_ENV"); val {
	case "local":
		return "https://local.subtrace.dev:3000"
	case "staging":
		return "https://staging.subtrace.dev"
	case "prod":
		return "https://subtrace.dev"
	default:
		return "https://subtrace.dev"
	}
}
