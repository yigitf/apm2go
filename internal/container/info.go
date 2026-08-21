// Package container puts a human-readable identity on a containerized process.
//
// Discovery can already tell that a process is in a container and read a
// 64-character hex id out of its cgroup path. That id is useless to a person:
// nobody recognises their payment service by its container hash. This package
// turns it into the name, image and orchestration labels an operator actually
// uses, so a service can be called "checkout" rather than "3f9a1c...".
//
// Every source here is optional. A host with no container runtime socket, or
// one apm2go is not permitted to read, still gets whatever the cgroup path
// alone can provide — apm2go never requires a runtime API to function.
package container

import (
	"context"
	"strings"
)

// Info is what is known about the container a process runs in.
type Info struct {
	// ID is the runtime's container id.
	ID string `json:"id,omitempty"`
	// Name is the human-facing container name, without any leading slash.
	Name string `json:"name,omitempty"`
	// Image is the image reference the container was created from.
	Image string `json:"image,omitempty"`

	// ComposeProject and ComposeService are set for Docker Compose workloads,
	// where the service name is what the author called the component.
	ComposeProject string `json:"compose_project,omitempty"`
	ComposeService string `json:"compose_service,omitempty"`

	// PodName, PodNamespace and PodUID identify a Kubernetes pod.
	PodName      string `json:"pod_name,omitempty"`
	PodNamespace string `json:"pod_namespace,omitempty"`
	PodUID       string `json:"pod_uid,omitempty"`
	// AppLabel is the workload's app name, from the conventional Kubernetes
	// labels. It is usually the best service name available.
	AppLabel string `json:"app_label,omitempty"`

	// Source names which lookup produced this, so the UI can say where a
	// surprising name came from.
	Source string `json:"source,omitempty"`
}

// ServiceName returns the most meaningful name for the workload, or empty when
// nothing better than an id is known.
//
// The order runs from most deliberate to most incidental: a Kubernetes app
// label is chosen by whoever deployed the workload, a Compose service name by
// whoever wrote the file, and a container name is often generated.
func (i *Info) ServiceName() string {
	if i == nil {
		return ""
	}
	switch {
	case i.AppLabel != "":
		return i.AppLabel
	case i.ComposeService != "":
		return i.ComposeService
	case i.PodName != "":
		return trimPodSuffixes(i.PodName)
	case i.Name != "":
		return i.Name
	default:
		return ""
	}
}

// trimPodSuffixes strips the generated suffixes Kubernetes appends, turning
// "checkout-7d9f8b6c4d-x2k9p" into "checkout". Without this every pod restart
// would look like a brand new service.
func trimPodSuffixes(podName string) string {
	parts := strings.Split(podName, "-")
	// A Deployment adds a ReplicaSet hash and a pod suffix; a StatefulSet adds
	// only an ordinal. Trim from the right while the segment looks generated.
	for len(parts) > 1 && isGeneratedSuffix(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, "-")
}

// isGeneratedSuffix reports whether a name segment looks machine-made: a short
// alphanumeric hash, or a bare ordinal.
func isGeneratedSuffix(segment string) bool {
	if segment == "" || len(segment) > 10 {
		return false
	}

	var digits, letters int
	for _, r := range segment {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z':
			letters++
		default:
			// Anything else is part of a real name.
			return false
		}
	}
	// A pure ordinal, as StatefulSets use.
	if letters == 0 {
		return true
	}
	// A hash segment mixes digits into letters; a real word such as "service"
	// or "api" has none.
	return digits > 0
}

// Runtime looks up container metadata from a specific source.
type Runtime interface {
	// Name identifies the source in logs and in the UI.
	Name() string
	// Available reports whether this source can be used at all, so an
	// unavailable one is skipped without an error per lookup.
	Available() bool
	// Lookup returns what is known about a container id.
	Lookup(ctx context.Context, containerID string) (*Info, error)
}
