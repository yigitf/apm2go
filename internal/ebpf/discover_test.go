package ebpf

import "testing"

func TestScriptName(t *testing.T) {
	tests := []struct {
		cmdline  []string
		fallback string
		want     string
	}{
		{[]string{"node", "caller.js"}, "node", "caller"},
		{[]string{"node", "/opt/node/express-app.js"}, "node", "express-app"},
		{[]string{"node", "--max-old-space-size=512", "server.js"}, "node", "server"},
		{[]string{"python3", "app.py"}, "python3", "app"},
		{[]string{"python3", "-m", "gunicorn", "wsgi:app"}, "python3", "python3"},
		{[]string{"node"}, "node", "node"},
	}
	for _, tt := range tests {
		got := scriptName(tt.cmdline, tt.fallback)
		if got != tt.want {
			t.Errorf("scriptName(%v, %q) = %q, want %q", tt.cmdline, tt.fallback, got, tt.want)
		}
	}
}

func TestPHPFPMPoolName(t *testing.T) {
	tests := []struct {
		cmdline []string
		want    string
	}{
		{[]string{"php-fpm:", "pool", "www"}, "www"},
		{[]string{"php-fpm:", "pool", "api-backend"}, "api-backend"},
		// The master process fronts every pool without handling requests
		// itself; it must be recognised as "not a pool" rather than named.
		{[]string{"php-fpm:", "master", "process", "(/etc/php-fpm.conf)"}, ""},
		{[]string{"php-fpm"}, ""},
	}
	for _, tt := range tests {
		got := phpFPMPoolName(tt.cmdline)
		if got != tt.want {
			t.Errorf("phpFPMPoolName(%v) = %q, want %q", tt.cmdline, got, tt.want)
		}
	}
}

// The safety property this allow-list exists for: no JVM executable name, in
// any spelling apm2go's own discovery package recognises, may ever classify
// as a Runtime target.
func TestClassifyExeNeverMatchesJava(t *testing.T) {
	for _, exe := range []string{"java", "javaw", "java8", "openjdk"} {
		if _, ok := classifyExe(exe); ok {
			t.Errorf("classifyExe(%q) matched a runtime; a JVM must never be a Runtime target", exe)
		}
	}
}

// /proc/<pid>/exe resolves through the distribution's real, versioned binary
// name, not the unversioned symlink a script's shebang names — this is
// exactly what going from an exact-match map to a prefix match fixed after it
// broke silently against a real python3.9 process in the acceptance
// environment.
func TestClassifyExeMatchesVersionedInterpreters(t *testing.T) {
	tests := []struct {
		exe  string
		want Runtime
	}{
		{"node", RuntimeNode},
		{"nodejs", RuntimeNode},
		{"python3", RuntimePython},
		{"python3.9", RuntimePython},
		{"python3.11", RuntimePython},
		{"php-fpm", RuntimePHP},
		{"php-fpm8.1", RuntimePHP},
		{"php-fpm7.4", RuntimePHP},
	}
	for _, tt := range tests {
		got, ok := classifyExe(tt.exe)
		if !ok || got != tt.want {
			t.Errorf("classifyExe(%q) = %q, %v; want %q, true", tt.exe, got, ok, tt.want)
		}
	}
}

// Docker's userland proxy holds every published port on the host, while the
// container behind it holds the same number in its own namespace. Since OBI
// selects by port and cannot see namespaces, a target built from the proxy
// captures the service behind it and files that service's traffic under the
// proxy's name.
func TestClassifyExeNeverMatchesDockerProxy(t *testing.T) {
	if _, ok := classifyExe("docker-proxy"); ok {
		t.Error("docker-proxy matched an interpreter prefix")
	}
	if !goDaemonDenylist["docker-proxy"] {
		t.Error("docker-proxy is not on the Go daemon denylist, so every published port becomes a service")
	}
}
