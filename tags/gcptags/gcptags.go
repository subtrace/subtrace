// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package gcptags

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
}

func New() *Client {
	return &Client{}
}

func (c *Client) Get(path string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// ref: https://cloud.google.com/compute/docs/metadata/querying-metadata
	req, err := http.NewRequest("GET", "http://169.254.169.254"+path, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("metadata-flavor", "Google")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("got status %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(b), nil
}
