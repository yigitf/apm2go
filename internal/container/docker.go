package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Docker's socket is spoken to directly over HTTP rather than through its
// client library. The library is a large dependency for what amounts to one
// GET, and apm2go only ever reads: it never creates, starts or stops anything,
// so the socket is mounted read-only and the surface stays that narrow.

// DefaultDockerSocket is where Docker and podman-in-docker-compat listen.
const DefaultDockerSocket = "/var/run/docker.sock"

// Requests carry no API version, so the daemon answers with its own.
//
// Pinning a version looked like the compatible choice and is the opposite:
// Docker refuses clients whose requested version is below its minimum, and
// that floor rises. A build pinned to v1.24 was rejected outright by Docker
// Desktop 4.52 ("minimum supported API version is 1.44") — while a versionless
// request is served by every daemon, old and new. The fields read below have
// been stable across all of them.

// dockerTimeout bounds a lookup. The daemon is local, so a slow reply means it
// is unhealthy, and blocking discovery on it would be worse than going without
// the metadata.
const dockerTimeout = 3 * time.Second

// Docker label keys, as Compose and Kubernetes write them.
const (
	labelComposeProject = "com.docker.compose.project"
	labelComposeService = "com.docker.compose.service"
	labelPodName        = "io.kubernetes.pod.name"
	labelPodNamespace   = "io.kubernetes.pod.namespace"
	labelPodUID         = "io.kubernetes.pod.uid"
)

// appLabelKeys are the conventional Kubernetes labels naming a workload, most
// specific first.
var appLabelKeys = []string{
	"app.kubernetes.io/name",
	"app.kubernetes.io/instance",
	"app",
	"k8s-app",
}

// dockerRuntime reads metadata from a Docker-compatible daemon socket.
type dockerRuntime struct {
	socket string
	client *http.Client
}

// NewDocker returns a Runtime backed by a Docker socket.
func NewDocker(socket string) Runtime {
	if socket == "" {
		socket = DefaultDockerSocket
	}
	return &dockerRuntime{
		socket: socket,
		client: &http.Client{
			Timeout: dockerTimeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

func (d *dockerRuntime) Name() string { return "docker" }

// Available reports whether the socket exists. It deliberately does not connect:
// this is called once at start-up, and a daemon that is briefly down should not
// disable metadata for the process's lifetime.
func (d *dockerRuntime) Available() bool {
	info, err := os.Stat(d.socket)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}

// dockerInspect is the subset of the container inspect response apm2go reads.
type dockerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// Lookup fetches a container's metadata by id.
func (d *dockerRuntime) Lookup(ctx context.Context, containerID string) (*Info, error) {
	if containerID == "" {
		return nil, fmt.Errorf("no container id")
	}

	url := fmt.Sprintf("http://docker/containers/%s/json", containerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query docker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The daemon explains refusals in the body, and its explanation is far
		// more useful than the status alone — an API version mismatch reads as
		// a plain 400 otherwise.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		message := strings.TrimSpace(string(detail))
		if message != "" {
			return nil, fmt.Errorf("docker returned %s for container %s: %s",
				resp.Status, short(containerID), message)
		}
		return nil, fmt.Errorf("docker returned %s for container %s", resp.Status, short(containerID))
	}

	var inspected dockerInspect
	if err := json.NewDecoder(resp.Body).Decode(&inspected); err != nil {
		return nil, fmt.Errorf("decode docker response: %w", err)
	}

	return infoFromDocker(&inspected), nil
}

// infoFromDocker maps an inspect response onto Info.
func infoFromDocker(inspected *dockerInspect) *Info {
	labels := inspected.Config.Labels

	info := &Info{
		ID: inspected.ID,
		// Docker returns the name with a leading slash.
		Name:           strings.TrimPrefix(inspected.Name, "/"),
		Image:          inspected.Config.Image,
		ComposeProject: labels[labelComposeProject],
		ComposeService: labels[labelComposeService],
		PodName:        labels[labelPodName],
		PodNamespace:   labels[labelPodNamespace],
		PodUID:         labels[labelPodUID],
		Source:         "docker",
	}

	for _, key := range appLabelKeys {
		if value := labels[key]; value != "" {
			info.AppLabel = value
			break
		}
	}
	return info
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
