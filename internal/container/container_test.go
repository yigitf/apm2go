package container

import "testing"

func TestPodUIDFromCgroup(t *testing.T) {
	tests := []struct {
		name   string
		cgroup string
		want   string
	}{
		{
			// The systemd cgroup driver writes underscores where the UID has
			// dashes; the result has to be normalised back so an operator can
			// paste it into kubectl.
			name:   "systemd driver",
			cgroup: "0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod3f7a1b2c_4d5e_6f70_8192_a3b4c5d6e7f8.slice/cri-containerd-abc.scope",
			want:   "3f7a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8",
		},
		{
			name:   "cgroupfs driver",
			cgroup: "0::/kubepods/burstable/pod3f7a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8/abc123",
			want:   "3f7a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8",
		},
		{
			name:   "plain docker, not kubernetes",
			cgroup: "0::/docker/04c50619d785eafb6d7af9674af7f43c980f2211a882ca140b418584fd44158a",
			want:   "",
		},
		{
			name:   "not containerized at all",
			cgroup: "0::/system.slice/chain-gateway-service.service",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PodUIDFromCgroup(tt.cgroup); got != tt.want {
				t.Errorf("PodUIDFromCgroup() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceNamePrefersTheMostDeliberateSource(t *testing.T) {
	tests := []struct {
		name string
		info *Info
		want string
	}{
		{
			name: "an app label beats everything",
			info: &Info{AppLabel: "checkout", ComposeService: "web", PodName: "web-abc123-x9k2p", Name: "k8s_web_1"},
			want: "checkout",
		},
		{
			name: "compose service beats the container name",
			info: &Info{ComposeService: "orders", Name: "myproject-orders-1"},
			want: "orders",
		},
		{
			// A deployment's pod adds a replicaset hash and a pod suffix; both
			// change on every rollout and would make each restart a new service.
			name: "pod name is stripped of generated suffixes",
			info: &Info{PodName: "checkout-7d9f8b6c4d-x2k9p"},
			want: "checkout",
		},
		{
			name: "statefulset ordinals are stripped too",
			info: &Info{PodName: "kafka-0"},
			want: "kafka",
		},
		{
			// "service" and "api" are words, not hashes, and must survive.
			name: "real words in a name are kept",
			info: &Info{PodName: "orders-api-service"},
			want: "orders-api-service",
		},
		{
			name: "container name is the last resort",
			info: &Info{Name: "nostalgic_hopper"},
			want: "nostalgic_hopper",
		},
		{
			name: "an id alone is not a name",
			info: &Info{ID: "04c50619d785"},
			want: "",
		},
		{
			name: "nil is safe",
			info: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.ServiceName(); got != tt.want {
				t.Errorf("ServiceName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInfoFromDockerReadsComposeAndKubernetesLabels(t *testing.T) {
	inspected := &dockerInspect{ID: "abc123", Name: "/myproject-orders-1"}
	inspected.Config.Image = "registry.example.com/orders:1.4.2"
	inspected.Config.Labels = map[string]string{
		labelComposeProject:      "myproject",
		labelComposeService:      "orders",
		labelPodName:             "orders-7d9f8b6c4d-x2k9p",
		labelPodNamespace:        "production",
		"app.kubernetes.io/name": "orders",
	}

	info := infoFromDocker(inspected)

	// The leading slash Docker puts on names is not part of the name.
	if info.Name != "myproject-orders-1" {
		t.Errorf("Name = %q, want the leading slash stripped", info.Name)
	}
	if info.ComposeService != "orders" || info.ComposeProject != "myproject" {
		t.Errorf("compose labels not read: project=%q service=%q", info.ComposeProject, info.ComposeService)
	}
	if info.PodNamespace != "production" {
		t.Errorf("PodNamespace = %q, want %q", info.PodNamespace, "production")
	}
	if info.AppLabel != "orders" {
		t.Errorf("AppLabel = %q, want %q", info.AppLabel, "orders")
	}
	if info.Image != "registry.example.com/orders:1.4.2" {
		t.Errorf("Image = %q", info.Image)
	}
}
