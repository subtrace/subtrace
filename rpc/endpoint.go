package rpc

import (
	"os"
	"strings"
)

func getEndpoint() string {
	endpoint := os.Getenv("SUBTRACE_ENDPOINT")
	if endpoint == "" {
		return "https://subtrace.dev"
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	return endpoint
}

// Format takes a path and returns its URL. For example, given "/api/GetSelf",
// Format will return "https://subtrace.dev/api/GetSelf" as the URL.
//
// The SUBTRACE_ENDPOINT environment variable can be used to override the
// domain name used.
func Format(path string) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") { // TODO: is this too relaxed?
		path = "/" + path
	}
	return getEndpoint() + path
}
