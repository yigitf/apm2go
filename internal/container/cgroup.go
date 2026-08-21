package container

import (
	"context"
	"regexp"
	"strings"
)

// The cgroup path is the one source that is always there. It needs no socket,
// no permission beyond reading /proc, and works for Docker, containerd, CRI-O
// and podman alike — because the path is written by the kernel, not by a
// runtime's API.
//
// What it gives is limited: an id, and on Kubernetes a pod UID. That is not a
// service name, but it is enough to group a pod's containers together and to
// tell an operator which pod to go look at.

var (
	// Kubernetes encodes the pod UID into the cgroup path. Both the systemd
	// driver ("kubepods-burstable-pod<uid>.slice", with dashes) and the
	// cgroupfs driver ("kubepods/burstable/pod<uid>") appear in the wild.
	podUIDSystemd = regexp.MustCompile(`pod([0-9a-f]{8}_[0-9a-f]{4}_[0-9a-f]{4}_[0-9a-f]{4}_[0-9a-f]{12})`)
	podUIDCgroups = regexp.MustCompile(`pod([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
)

// cgroupRuntime derives what it can from the cgroup path alone.
type cgroupRuntime struct{}

// NewCgroup returns the fallback Runtime, which is always available.
func NewCgroup() Runtime { return &cgroupRuntime{} }

func (c *cgroupRuntime) Name() string { return "cgroup" }

// Available is always true: reading /proc is the one capability apm2go already
// requires in order to do anything at all.
func (c *cgroupRuntime) Available() bool { return true }

// Lookup returns the id it was given. The pod UID comes from PodUIDFromCgroup,
// which the scanner calls with the raw cgroup text.
func (c *cgroupRuntime) Lookup(_ context.Context, containerID string) (*Info, error) {
	return &Info{ID: containerID, Source: "cgroup"}, nil
}

// PodUIDFromCgroup extracts a Kubernetes pod UID from a cgroup path, returning
// the empty string when the process is not part of a pod.
//
// The systemd driver writes underscores where the UID has dashes, so the result
// is normalised back to the canonical form an operator would paste into kubectl.
func PodUIDFromCgroup(cgroupText string) string {
	if match := podUIDCgroups.FindStringSubmatch(cgroupText); len(match) > 1 {
		return match[1]
	}
	if match := podUIDSystemd.FindStringSubmatch(cgroupText); len(match) > 1 {
		return strings.ReplaceAll(match[1], "_", "-")
	}
	return ""
}
