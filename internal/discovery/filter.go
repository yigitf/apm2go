package discovery

import "strings"

// Filter decides which discovered JVMs apm2go should manage. An empty Filter
// accepts everything.
type Filter struct {
	// Include, when non-empty, keeps only JVMs matching at least one pattern.
	Include []string
	// Exclude drops matching JVMs and is applied after Include.
	Exclude []string
}

// NewFilter builds a Filter from the configured pattern lists.
func NewFilter(include, exclude []string) *Filter {
	return &Filter{Include: include, Exclude: exclude}
}

// Accept reports whether a JVM passes the filter. Patterns are matched
// case-insensitively against the service name, the full command line and the
// systemd unit, which is what an operator would expect from a substring rule.
func (f *Filter) Accept(jvm *JVM) bool {
	if f == nil {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		jvm.ServiceName,
		jvm.CommandLine(),
		jvm.SystemdUnit,
		jvm.ContainerID,
	}, " "))

	if len(f.Include) > 0 && !matchesAny(haystack, f.Include) {
		return false
	}
	return !matchesAny(haystack, f.Exclude)
}

func matchesAny(haystack string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
