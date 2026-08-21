package receiver

import "strings"

// OpenTelemetry renamed most of its semantic conventions between the 1.x and
// 1.2x releases, and instrumentation libraries migrated at different times. A
// collector that reads only one spelling silently loses fields, so apm2go reads
// both and prefers the current one.
const (
	// Resource attributes.
	attrServiceName    = "service.name"
	attrServiceVersion = "service.version"
	attrHostName       = "host.name"
	attrProcessPID     = "process.pid"
	attrContainerID    = "container.id"
	// The language the emitting process runs. Every SDK apm2go receives from
	// sets one of these: the OpenTelemetry Java agent reports
	// telemetry.sdk.language=java, and OBI reports the language its eBPF probes
	// detected for the process it is watching.
	attrTelemetrySDKLanguage = "telemetry.sdk.language"
	attrProcessRuntimeName   = "process.runtime.name"

	// HTTP, current conventions.
	attrHTTPRequestMethod      = "http.request.method"
	attrHTTPResponseStatusCode = "http.response.status_code"
	attrURLPath                = "url.path"
	attrURLFull                = "url.full"
	// HTTP, legacy conventions.
	attrHTTPMethod     = "http.method"
	attrHTTPStatusCode = "http.status_code"
	attrHTTPTarget     = "http.target"
	attrHTTPURL        = "http.url"
	// Stable across both.
	attrHTTPRoute = "http.route"

	// Database, current conventions.
	attrDBSystemName = "db.system.name"
	attrDBQueryText  = "db.query.text"
	attrDBNamespace  = "db.namespace"
	// Database, legacy conventions.
	attrDBSystem    = "db.system"
	attrDBStatement = "db.statement"
	attrDBName      = "db.name"
	attrDBOperation = "db.operation"

	// Remote peer identification, used to draw the service map.
	attrPeerService   = "peer.service"
	attrServerAddress = "server.address"
	attrNetPeerName   = "net.peer.name"

	// Messaging.
	attrMessagingSystem      = "messaging.system"
	attrMessagingDestination = "messaging.destination.name"

	// Exception events.
	eventException          = "exception"
	attrExceptionType       = "exception.type"
	attrExceptionMessage    = "exception.message"
	attrExceptionStacktrace = "exception.stacktrace"

	// Set by apm2go's injector so the UI can flag traces that came from a
	// runtime attach, where coverage may be incomplete.
	attrApm2goInjected = "apm2go.injected"
)

// runtimeAliases maps the spellings different producers use onto one name per
// language. OBI says "nodejs" where a Node SDK says "node", and the Java
// agent's process.runtime.name is a whole sentence ("OpenJDK Runtime
// Environment") rather than a token. Normalising at ingest means every reader —
// the API, the UI's icons — matches on one spelling instead of each keeping its
// own list of synonyms.
// uninformativeRuntimes are the values a producer sends when it could not work
// out what it was watching. OBI reports one of these for a native binary — an
// nginx worker is a C program, and it has no runtime symbols to read further —
// and treating them as an answer would be worse than having none: it would
// mask the answer apm2go's own discovery already has, which is that the process
// is nginx. Mapped to empty so the pipeline's resolver gets its turn.
var uninformativeRuntimes = map[string]bool{
	"generic": true,
	"unknown": true,
	"c":       true,
	"cpp":     true,
	"c++":     true,
	"native":  true,
}

var runtimeAliases = map[string]string{
	"node":   "nodejs",
	"js":     "nodejs",
	"dotnet": "dotnet",
	"py":     "python",
}

// detectRuntime names the language a resource belongs to.
//
// telemetry.sdk.language is the attribute meant for this and is preferred.
// process.runtime.name is the fallback, and is matched by substring because it
// carries a product name rather than a language: an OpenJ9 JVM reports "Eclipse
// OpenJ9 VM" and a HotSpot one "OpenJDK Runtime Environment", which share only
// the letters "J". An unrecognised value yields no runtime rather than a guess,
// since a wrong language icon is worse than none.
func detectRuntime(resource map[string]string) string {
	if language := strings.ToLower(strings.TrimSpace(resource[attrTelemetrySDKLanguage])); language != "" {
		if uninformativeRuntimes[language] {
			return ""
		}
		if alias, ok := runtimeAliases[language]; ok {
			return alias
		}
		return language
	}

	name := strings.ToLower(resource[attrProcessRuntimeName])
	switch {
	case name == "":
		return ""
	case strings.Contains(name, "jdk"), strings.Contains(name, "jvm"),
		strings.Contains(name, "java"), strings.Contains(name, "openj9"):
		return "java"
	case strings.Contains(name, "node"):
		return "nodejs"
	case strings.Contains(name, "python"), strings.Contains(name, "cpython"):
		return "python"
	case strings.Contains(name, "go"):
		return "go"
	default:
		return ""
	}
}

// firstNonEmpty returns the first non-empty value, which is how the conversion
// code expresses "current convention, else legacy".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstNonZero returns the first non-zero value.
func firstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}
