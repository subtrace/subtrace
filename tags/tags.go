package tags

import (
	"os"

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
		if cloud = cloudtags.GuessCloudDMI(); cloud == cloudtags.CloudUnknown {
			cloud = cloudtags.GuessCloudIMDS()
		}
	}()

	go func() {
		<-cloudBarrier
		switch cloud {
		case cloudtags.CloudGCP:
			c := gcptags.New()
			if project, err := c.Get("/computeMetadata/v1/project/project-id"); err == nil {
				tmpl.Set("gcp_project", project)
			}
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
