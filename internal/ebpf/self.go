package ebpf

// WithSelf wraps a TargetSink so every scan it receives also carries one
// fixed target that never comes from /proc: apm2go's own process.
//
// Scan's goDaemonDenylist excludes apm2go from ordinary discovery, because a
// target built the way any other Go service is — from every port Scan finds
// it listening on — would hand OBI a rule that also matches its OTLP
// receiver, and OBI would then trace its own export traffic into that
// receiver, generating a span that itself needs exporting, forever. That risk
// is specific to which ports a target claims, not to being watched at all:
// resource metrics carry none of it (CPU, memory and disk I/O are read
// straight from the kernel per pid, the same as for anything else apm2go
// watches), and neither does the watched-process listing. Callers of WithSelf
// decide the risk per sink by what they put in Target.Ports — a caller that
// wants apm2go's own HTTP handling traced too passes a target scoped to its
// API port alone, never to the receiver's; see cmd/apm2go/run.go for exactly
// that.
func WithSelf(sink TargetSink, self Target) TargetSink {
	return &selfSink{TargetSink: sink, self: self}
}

type selfSink struct {
	TargetSink
	self Target
}

// SetTargets appends the fixed target to every scan before forwarding it.
//
// The slice is copied rather than appended to in place: targets is handed to
// every sink from the same scan, and appending in place risks writing into,
// or reallocating out from under, another sink's read of the same backing
// array.
func (s *selfSink) SetTargets(targets []Target) {
	augmented := make([]Target, len(targets), len(targets)+1)
	copy(augmented, targets)
	augmented = append(augmented, s.self)
	s.TargetSink.SetTargets(augmented)
}
