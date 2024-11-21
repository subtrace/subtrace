// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package kubetags

import (
	"os"

	"subtrace.dev/event"
	"subtrace.dev/tags/gcptags"
)

func FetchLocal() (*event.Event, bool) {
	// ref: https://stackoverflow.com/a/54130803/9006220
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return nil, false
	}

	ev := new(event.Event)
	// ref: https://kubernetes.io/docs/tasks/run-application/access-api-from-pod/#directly-accessing-the-rest-api
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		ev.Set("kubernetes_namespace", string(b))
	}
	return ev, true
}

func FetchEKS() *event.Event {
	// TODO: ref: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html
	return nil
}

func FetchGKE() *event.Event {
	c := gcptags.New()
	ev := new(event.Event)
	if location, err := c.Get("/computeMetadata/v1/instance/attributes/cluster-location"); err == nil {
		ev.Set("gke_cluster_location", location)
	}
	if name, err := c.Get("/computeMetadata/v1/instance/attributes/cluster-name"); err == nil {
		ev.Set("gke_cluster_name", name)
	}
	if name, err := c.Get("/computeMetadata/v1/instance/name"); err == nil {
		ev.Set("gke_node_name", name)
	}
	return ev
}

func FetchAKS() *event.Event {
	// TODO: ref: https://learn.microsoft.com/en-us/azure/virtual-machines/instance-metadata-service?tabs=windows
	return nil
}
