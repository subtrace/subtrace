package tags

import (
	"os"
	"strings"

	"subtrace.dev/event"
	"subtrace.dev/tags/cloudtags"
	"subtrace.dev/tags/gcptags"
	"subtrace.dev/tags/kubetags"
)

// SetLocalTagsAsync fetches information about the local machine and sets tags
// on the given the event template. Since it requires network requests to fetch
// things like IMDS, it should be called in a separate goroutine.
func SetLocalTagsAsync(tmpl *event.Event) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	} else {
		tmpl.Set("hostname", hostname)
	}

	cloud := cloudtags.CloudUnknown
	cloudBarrier := make(chan struct{})
	go func() {
		defer close(cloudBarrier)
		if cloud = cloudtags.GuessCloudEnv(); cloud != cloudtags.CloudUnknown {
			return
		}
		if cloud = cloudtags.GuessCloudDMI(); cloud != cloudtags.CloudUnknown {
			return
		}
		cloud = cloudtags.GuessCloudIMDS()
	}()

	go func() {
		<-cloudBarrier
		switch cloud {
		case cloudtags.CloudGCP:
			c := gcptags.New()
			if project, err := c.Get("/computeMetadata/v1/project/project-id"); err == nil {
				tmpl.Set("gcp_project", project)
			}

		case cloudtags.CloudFly:
			flytags := new(event.Event)
			for _, name := range []string{"FLY_MACHINE_ID", "FLY_REGION", "FLY_PUBLIC_IP"} {
				if val := os.Getenv(name); val != "" {
					flytags.Set(strings.ToLower(name), val)
				}
			}

			tmpl.CopyFrom(flytags)

		case cloudtags.CloudPorter:
			portertags := new(event.Event)
			for _, name := range []string{"PORTER_NODE_NAME", "PORTER_POD_NAME", "PORTER_POD_REVISION", "PORTER_APP_SERVICE_NAME"} {
				if val := os.Getenv(name); val != "" {
					portertags.Set(strings.ToLower(name), val)
				}
			}

			tmpl.CopyFrom(portertags)
		}
	}()

	if partial, ok := kubetags.FetchLocal(); ok {
		tmpl.CopyFrom(partial)
		go func() {
			<-cloudBarrier
			switch cloud {
			case cloudtags.CloudGCP:
				tmpl.CopyFrom(kubetags.FetchGKE())
			}
		}()
	}
}
