package store

import (
	"context"
	"testing"
	"time"

	"github.com/apm2go/apm2go/internal/model"
)

func manyMetrics(n int, from time.Time) []*model.Metric {
	out := make([]*model.Metric, n)
	for i := 0; i < n; i++ {
		out[i] = &model.Metric{
			Timestamp: from.Add(time.Duration(i) * time.Second),
			Name:      "process.cpu.utilization",
			Kind:      model.KindGauge,
			Service:   "svc",
			Value:     0.5,
			Unit:      "1",
		}
	}
	return out
}

// Stats().SizeBytes used to be left at its zero value unconditionally -- the
// field existed on the struct and in the API response, but nothing ever wrote
// to it, so every install reported a 0-byte database regardless of how much it
// held. This is what an operator reads when deciding whether storage.*
// retention needs tightening, so a silent zero there is a silent lie.
func TestStatsReportsTheDatabasesRealSize(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	empty, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if empty.SizeBytes <= 0 {
		t.Fatalf("SizeBytes = %d for a freshly opened database, want > 0 (the schema itself occupies space)", empty.SizeBytes)
	}

	if err := s.WriteMetrics(ctx, manyMetrics(2000, time.Now().UTC().Add(-2000*time.Second))); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	if _, err := s.DB().Exec("CHECKPOINT"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	after, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if after.SizeBytes <= empty.SizeBytes {
		t.Errorf("SizeBytes did not grow after writing data: before=%d after=%d", empty.SizeBytes, after.SizeBytes)
	}
}
