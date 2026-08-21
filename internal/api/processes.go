package api

import (
	"net/http"

	"github.com/apm2go/apm2go/internal/container"
	"github.com/apm2go/apm2go/internal/ebpf"
)

// WatchedProcesses reports the non-Java processes apm2go is currently handing
// to eBPF instrumentation. internal/ebpf.Registry implements it.
type WatchedProcesses interface {
	Snapshot() []ebpf.Watched
}

// Process is one watched process, with its container resolved to something a
// person recognises.
type Process struct {
	ebpf.Watched
	// Container is what the container runtime knows about it: the name, the
	// image, and — the reason this is resolved here at all — the Compose
	// project or Kubernetes namespace it belongs to, which is what groups a
	// stack of services together in the UI.
	Container *container.Info `json:"container,omitempty"`
}

// ListProcesses returns the processes apm2go is watching through eBPF.
//
// This answers a question no other endpoint can. /services is built from stored
// spans, so a service that is being watched and has simply had no requests is
// absent from it — indistinguishable, from the outside, from a service apm2go
// never found. On a real host that is the common case rather than the corner
// one: an idle nginx in front of an idle application is watched, is reporting
// CPU and memory, and appears nowhere until somebody makes a request.
func (h *Handlers) ListProcesses(w http.ResponseWriter, _ *http.Request) {
	if h.Processes == nil {
		// eBPF instrumentation is off, or this is a server-mode instance with
		// no processes of its own to watch. An empty list is the truthful
		// answer, not an error.
		writeJSON(w, http.StatusOK, []Process{})
		return
	}

	watched := h.Processes.Snapshot()
	out := make([]Process, 0, len(watched))
	for _, process := range watched {
		entry := Process{Watched: process}
		if process.ContainerID != "" && h.Containers != nil {
			entry.Container = h.Containers.Resolve(process.ContainerID)
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}
