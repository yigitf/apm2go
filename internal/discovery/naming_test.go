package discovery

import "testing"

func TestJarServiceName(t *testing.T) {
	tests := []struct {
		jarPath string
		want    string
	}{
		{"/opt/app/orders-service-1.4.2-SNAPSHOT.jar", "orders-service"},
		{"/opt/app/orders-service.jar", "orders-service"},
		{"payments-2.0.jar", "payments"},
		{"/srv/gateway-v2.jar", "gateway"},
		{"/srv/api-1.0.0-exec.jar", "api"},
		{"app.war", "app"},
		// A version-looking name with no other segment must survive: dropping
		// it would leave nothing to call the service.
		{"7.jar", "7"},
		// "oauth2" is a name, not a version, and must not be trimmed.
		{"oauth2-server.jar", "oauth2-server"},
	}

	for _, tt := range tests {
		if got := jarServiceName(tt.jarPath); got != tt.want {
			t.Errorf("jarServiceName(%q) = %q, want %q", tt.jarPath, got, tt.want)
		}
	}
}

func TestDeriveServiceNamePrefersExplicitNames(t *testing.T) {
	tests := []struct {
		name       string
		jvm        *JVM
		wantName   string
		wantSource string
	}{
		{
			name: "otel property wins over everything",
			jvm: &JVM{
				PID:         10,
				SystemProps: map[string]string{"otel.service.name": "checkout", "spring.application.name": "spring-checkout"},
				JarPath:     "/opt/other.jar",
			},
			wantName:   "checkout",
			wantSource: SourceOtelProperty,
		},
		{
			name: "spring property beats the jar name",
			jvm: &JVM{
				PID:         11,
				SystemProps: map[string]string{"spring.application.name": "orders"},
				JarPath:     "/opt/orders-1.0.jar",
			},
			wantName:   "orders",
			wantSource: SourceSpringProperty,
		},
		{
			name:       "jar name beats the systemd unit",
			jvm:        &JVM{PID: 12, SystemProps: map[string]string{}, JarPath: "/opt/billing-3.1.jar", SystemdUnit: "billing-prod"},
			wantName:   "billing",
			wantSource: SourceJarFile,
		},
		{
			name:       "systemd unit beats a main class",
			jvm:        &JVM{PID: 13, SystemProps: map[string]string{}, SystemdUnit: "search-api", MainClass: "com.acme.Main"},
			wantName:   "search-api",
			wantSource: SourceSystemdUnit,
		},
		{
			name:       "main class is shortened to its simple name",
			jvm:        &JVM{PID: 14, SystemProps: map[string]string{}, MainClass: "com.acme.orders.OrdersApplication"},
			wantName:   "OrdersApplication",
			wantSource: SourceMainClass,
		},
		{
			name:       "pid is the last resort",
			jvm:        &JVM{PID: 4242, SystemProps: map[string]string{}},
			wantName:   "java-4242",
			wantSource: SourceFallback,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deriveServiceName(tt.jvm)
			if tt.jvm.ServiceName != tt.wantName {
				t.Errorf("ServiceName = %q, want %q", tt.jvm.ServiceName, tt.wantName)
			}
			if tt.jvm.ServiceNameSource != tt.wantSource {
				t.Errorf("ServiceNameSource = %q, want %q", tt.jvm.ServiceNameSource, tt.wantSource)
			}
		})
	}
}

func TestParseEntryPoint(t *testing.T) {
	tests := []struct {
		name          string
		cmdline       []string
		wantMainClass string
		wantJar       string
	}{
		{
			name:    "jar wins when -jar is present",
			cmdline: []string{"java", "-Xmx2g", "-jar", "/opt/app.jar", "--port=80"},
			wantJar: "/opt/app.jar",
		},
		{
			name:          "first non-flag token is the main class",
			cmdline:       []string{"java", "-Xmx2g", "-Dfoo=bar", "com.acme.Main", "arg"},
			wantMainClass: "com.acme.Main",
		},
		{
			// A classpath value is not a main class, even though it does not
			// start with a dash.
			name:          "classpath value is skipped",
			cmdline:       []string{"java", "-cp", "/opt/lib/*:/opt/classes", "com.acme.Main"},
			wantMainClass: "com.acme.Main",
		},
		{
			name:    "no entry point at all",
			cmdline: []string{"java", "-version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jvm := &JVM{SystemProps: map[string]string{}}
			parseEntryPoint(tt.cmdline, jvm)
			if jvm.MainClass != tt.wantMainClass {
				t.Errorf("MainClass = %q, want %q", jvm.MainClass, tt.wantMainClass)
			}
			if jvm.JarPath != tt.wantJar {
				t.Errorf("JarPath = %q, want %q", jvm.JarPath, tt.wantJar)
			}
		})
	}
}

func TestFilterAcceptsAndExcludes(t *testing.T) {
	jvm := &JVM{
		ServiceName: "orders-api",
		Cmdline:     []string{"java", "-jar", "/opt/orders.jar"},
		SystemdUnit: "orders",
	}

	tests := []struct {
		name    string
		include []string
		exclude []string
		want    bool
	}{
		{name: "empty filter accepts everything", want: true},
		{name: "matching include accepts", include: []string{"orders"}, want: true},
		{name: "non-matching include rejects", include: []string{"payments"}, want: false},
		{name: "exclude rejects even when included", include: []string{"orders"}, exclude: []string{"api"}, want: false},
		{name: "match is case insensitive", include: []string{"ORDERS"}, want: true},
		{name: "match covers the command line", include: []string{"/opt/orders.jar"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewFilter(tt.include, tt.exclude).Accept(jvm); got != tt.want {
				t.Errorf("Accept() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMajorJavaVersion(t *testing.T) {
	tests := []struct {
		version string
		want    int
	}{
		{"21.0.11", 21},
		{"17.0.9+9", 17},
		{"1.8.0_402", 8},
		{"11", 11},
		{"", 0},
		{"not-a-version", 0},
	}

	for _, tt := range tests {
		if got := majorJavaVersion(tt.version); got != tt.want {
			t.Errorf("majorJavaVersion(%q) = %d, want %d", tt.version, got, tt.want)
		}
	}
}
