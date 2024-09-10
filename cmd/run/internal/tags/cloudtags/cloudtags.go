// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package cloudtags

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

const (
	CloudUnknown = iota
	CloudAWS
	CloudGCP
	CloudAzure
)

func readDMI(name string) string {
	if b, err := os.ReadFile(fmt.Sprintf("/sys/class/dmi/id/%s", name)); err == nil {
		return string(b)
	}
	if b, err := os.ReadFile(fmt.Sprintf("/sys/devices/virtual/dmi/id/%s", name)); err == nil {
		return string(b)
	}
	return ""
}

// GuessCloudDMI uses /sys/class/dmi/id/* heuristics to guess the cloud
// provider Subtrace is running on.
func GuessCloudDMI() int {
	switch val := readDMI("product_name"); {
	case val == "Google", val == "Google Compute Engine":
		return CloudGCP
	case val == "Microsoft Corporation":
		return CloudAzure
	}

	switch val := readDMI("bios_version"); {
	case val == "Google":
		return CloudGCP
	case strings.HasSuffix(val, "amazon"):
		return CloudAWS
	}

	switch val := readDMI("sys_vendor"); {
	case val == "Google":
		return CloudGCP
	case val == "Microsoft Corporation":
		return CloudAzure
	}

	return CloudUnknown
}

// GuessCloudIMDS uses the Instance Metadata Service (IMDS) endpoint to guess
// the cloud provider.
func GuessCloudIMDS() int {
	resp, err := http.Get("http://169.254.169.254")
	if err != nil {
		return CloudUnknown
	}
	defer resp.Body.Close()

	// Azure doesn't seem to include any identifying info in the response.
	if resp.Header.Get("server") == "EC2ws" {
		return CloudAWS
	}
	if resp.Header.Get("metadata-flavor") == "Google" {
		return CloudGCP
	}
	return CloudUnknown
}
