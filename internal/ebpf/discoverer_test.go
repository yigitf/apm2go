package ebpf

import "testing"

func TestDisambiguateRenamesCollisions(t *testing.T) {
	in := []Target{
		{Name: "app", Ports: []int{8000}, Runtime: RuntimePython},
		{Name: "app", Ports: []int{8001}, Runtime: RuntimeNode},
		{Name: "unique", Ports: []int{9000}, Runtime: RuntimeGo},
	}
	out := disambiguate(in)

	names := map[string]bool{}
	for _, t := range out {
		names[t.Name] = true
	}
	if len(names) != 3 {
		t.Fatalf("got %d distinct names after disambiguation, want 3: %v", len(names), out)
	}
	if !names["app-8000"] || !names["app-8001"] {
		t.Errorf("colliding targets were not disambiguated by port: %v", out)
	}
	if !names["unique"] {
		t.Errorf("a non-colliding target was renamed unnecessarily: %v", out)
	}
}

func TestDisambiguateLeavesUniqueNamesAlone(t *testing.T) {
	in := []Target{
		{Name: "gateway", Ports: []int{8081}},
		{Name: "orders", Ports: []int{8082}},
	}
	out := disambiguate(in)
	if out[0].Name != "gateway" || out[1].Name != "orders" {
		t.Errorf("names changed unexpectedly: %v", out)
	}
}

// A port number two targets both listen on cannot select either of them: OBI
// matches on the number alone and has no idea the two are in different network
// namespaces. Two web servers each keeping their distribution's :80 alongside
// their own port is the ordinary way this happens.
func TestDisambiguateDropsPortsTwoTargetsShare(t *testing.T) {
	got := disambiguate([]Target{
		{Name: "nginx", Ports: []int{80, 8100}, Runtime: RuntimeNginx},
		{Name: "httpd", Ports: []int{80, 8101}, Runtime: RuntimeHTTPD},
		{Name: "api", Ports: []int{9000}, Runtime: RuntimeGo},
	})

	want := map[string][]int{
		"nginx": {8100},
		"httpd": {8101},
		"api":   {9000},
	}
	for _, target := range got {
		expected, ok := want[target.Name]
		if !ok {
			t.Fatalf("unexpected target %q in %+v", target.Name, got)
		}
		if len(target.Ports) != len(expected) {
			t.Errorf("%s ports = %v, want %v", target.Name, target.Ports, expected)
			continue
		}
		for i := range expected {
			if target.Ports[i] != expected[i] {
				t.Errorf("%s ports = %v, want %v", target.Name, target.Ports, expected)
				break
			}
		}
	}
}

// When the shared port is the only one either target has, nothing can separate
// them and OBI will merge them however it is configured. Dropping the ports
// would leave a rule selecting everything; dropping the targets would make two
// services vanish. Both keep what they had, which is what apm2go did before
// ports were plural at all.
func TestDisambiguateKeepsTheOnlyPortEvenWhenShared(t *testing.T) {
	got := disambiguate([]Target{
		{Name: "nginx", Ports: []int{80}, Runtime: RuntimeNginx},
		{Name: "nginx", Ports: []int{80}, Runtime: RuntimeNginx},
	})

	for _, target := range got {
		if len(target.Ports) != 1 || target.Ports[0] != 80 {
			t.Errorf("ports = %v, want [80] kept", target.Ports)
		}
		if target.Name != "nginx-80" {
			t.Errorf("name = %q, want the colliding name made unique", target.Name)
		}
	}
}
