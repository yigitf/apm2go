package discovery

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Sources reported in JVM.ServiceNameSource, in the order deriveServiceName
// tries them.
const (
	SourceOtelProperty   = "otel.service.name"
	SourceSpringProperty = "spring.application.name"
	SourceKubernetes     = "kubernetes-label"
	SourceCompose        = "compose-service"
	SourceContainerName  = "container-name"
	SourceJarFile        = "jar"
	SourceSystemdUnit    = "systemd-unit"
	SourceMainClass      = "main-class"
	SourceFallback       = "pid"
)

// deriveServiceName picks the most human-meaningful name available for a JVM.
//
// The order matters: an explicit OpenTelemetry or Spring name is what operators
// already call the service, a jar file name is usually the artifact name, and a
// main class is a last resort because it is long and repetitive across a fleet.
func deriveServiceName(jvm *JVM) {
	if v := jvm.SystemProps["otel.service.name"]; v != "" {
		jvm.ServiceName, jvm.ServiceNameSource = v, SourceOtelProperty
		return
	}
	if v := jvm.SystemProps["spring.application.name"]; v != "" {
		jvm.ServiceName, jvm.ServiceNameSource = v, SourceSpringProperty
		return
	}
	// A container's own identity outranks the jar file, because whoever
	// deployed the workload chose it: a Kubernetes app label or a Compose
	// service name is what the team calls the thing, while the jar is often
	// just "app.jar" across an entire fleet.
	if name := jvm.Container.ServiceName(); name != "" {
		jvm.ServiceName, jvm.ServiceNameSource = name, containerNameSource(jvm)
		return
	}

	if jvm.JarPath != "" {
		if name := jarServiceName(jvm.JarPath); name != "" {
			jvm.ServiceName, jvm.ServiceNameSource = name, SourceJarFile
			return
		}
	}
	if jvm.SystemdUnit != "" {
		jvm.ServiceName, jvm.ServiceNameSource = jvm.SystemdUnit, SourceSystemdUnit
		return
	}
	if jvm.MainClass != "" {
		jvm.ServiceName, jvm.ServiceNameSource = mainClassServiceName(jvm.MainClass), SourceMainClass
		return
	}
	jvm.ServiceName, jvm.ServiceNameSource = "java-"+strconv.Itoa(jvm.PID), SourceFallback
}

// containerNameSource reports which part of the container's identity supplied
// the name, so the UI can explain a surprising one.
func containerNameSource(jvm *JVM) string {
	switch {
	case jvm.Container == nil:
		return SourceContainerName
	case jvm.Container.AppLabel != "":
		return SourceKubernetes
	case jvm.Container.ComposeService != "":
		return SourceCompose
	case jvm.Container.PodName != "":
		return SourceKubernetes
	default:
		return SourceContainerName
	}
}

// jarServiceName turns "/opt/app/orders-service-1.4.2-SNAPSHOT.jar" into
// "orders-service" by dropping the extension and any trailing version segment.
func jarServiceName(jarPath string) string {
	base := strings.TrimSuffix(filepath.Base(jarPath), ".jar")
	base = strings.TrimSuffix(base, ".war")
	if base == "" {
		return ""
	}

	// Common Maven-style suffixes carry no information about the service.
	for _, suffix := range []string{"-SNAPSHOT", "-RELEASE", "-exec", "-boot", "-all", "-fat", "-shaded"} {
		base = strings.TrimSuffix(base, suffix)
	}

	// Drop trailing version segments such as "-1.4.2" or "-v2".
	parts := strings.Split(base, "-")
	for len(parts) > 1 && isVersionSegment(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, "-")
}

// isVersionSegment reports whether s looks like a version component: it starts
// with a digit, or with "v" followed by a digit, and holds nothing but digits
// and dots after that.
func isVersionSegment(s string) bool {
	if s == "" {
		return false
	}
	s = strings.TrimPrefix(s, "v")
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// mainClassServiceName shortens "com.acme.orders.OrdersApplication" to
// "OrdersApplication", which is what an operator would call it.
func mainClassServiceName(mainClass string) string {
	if i := strings.LastIndexByte(mainClass, '.'); i >= 0 && i+1 < len(mainClass) {
		return mainClass[i+1:]
	}
	return mainClass
}
