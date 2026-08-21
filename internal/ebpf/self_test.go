package ebpf

import (
	"reflect"
	"testing"
)

// recordingSink is a TargetSink that remembers the last set it was given, so
// a test can assert on exactly what WithSelf forwarded.
type recordingSink struct {
	got []Target
}

func (r *recordingSink) SetTargets(targets []Target) { r.got = targets }

func TestWithSelfAppendsTheFixedTarget(t *testing.T) {
	inner := &recordingSink{}
	self := Target{PID: 1234, Name: "apm2go", Runtime: RuntimeGo}
	sink := WithSelf(inner, self)

	sink.SetTargets([]Target{{Name: "graylog", Runtime: RuntimeGo}})

	if len(inner.got) != 2 {
		t.Fatalf("got %d targets, want 2: %v", len(inner.got), inner.got)
	}
	if inner.got[0].Name != "graylog" {
		t.Errorf("first target = %+v, want the discovered one untouched", inner.got[0])
	}
	if !reflect.DeepEqual(inner.got[1], self) {
		t.Errorf("second target = %+v, want the self target %+v", inner.got[1], self)
	}
}

// An empty scan is ordinary right after start-up, before /proc has anything
// else worth watching. Self must still show up — that is the entire point.
func TestWithSelfSurvivesAnEmptyScan(t *testing.T) {
	inner := &recordingSink{}
	self := Target{PID: 1, Name: "apm2go", Runtime: RuntimeGo}
	sink := WithSelf(inner, self)

	sink.SetTargets(nil)

	if len(inner.got) != 1 || !reflect.DeepEqual(inner.got[0], self) {
		t.Errorf("got %v, want only the self target", inner.got)
	}
}

// The slice handed to SetTargets is shared with every other sink in the same
// scan. WithSelf must not mutate it in place — appending to it directly could
// grow into, or reallocate out from under, another sink's read of the same
// backing array.
func TestWithSelfDoesNotMutateTheInputSlice(t *testing.T) {
	inner := &recordingSink{}
	self := Target{PID: 1, Name: "apm2go", Runtime: RuntimeGo}
	sink := WithSelf(inner, self)

	// Built with spare capacity, the way disambiguate's output or a
	// pre-sized slice from Scan might be, so an in-place append would
	// silently succeed without reallocating and corrupt this backing array.
	in := make([]Target, 1, 4)
	in[0] = Target{Name: "graylog", Runtime: RuntimeGo}

	sink.SetTargets(in)

	if len(in) != 1 {
		t.Fatalf("caller's slice length changed to %d, want 1 (unmutated)", len(in))
	}
	if cap(in) < 2 {
		t.Fatalf("test invariant broken: need spare capacity to exercise the mutation risk")
	}
	// If SetTargets had appended in place, this write would land in the
	// caller's backing array at index 1, exactly where WithSelf's own
	// (correctly, separately allocated) output also placed the self target.
	in = in[:2]
	if reflect.DeepEqual(in[1], self) {
		t.Error("WithSelf wrote the self target into the caller's backing array")
	}
}
