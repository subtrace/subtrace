package rpc

import (
	"fmt"
	"os"
	"strings"
)

// Format takes a path and returns its URL. For example, given "/api/GetSelf",
// Format will return "https://subtrace.dev/api/GetSelf" as the URL.
//
// The SUBTRACE_ENDPOINT environment variable can be used to override the
// domain name used.
func Format(path string) string {
	endpoint := os.Getenv("SUBTRACE_ENDPOINT")
	if endpoint == "" {
		endpoint = "subtrace.dev"
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") { // TODO: is this too relaxed?
		path = "/" + path
	}
	return fmt.Sprintf("https://%s%s", endpoint, path)
}
