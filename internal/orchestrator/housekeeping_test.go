package orchestrator_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"crmai/internal/metrics"
	"crmai/internal/orchestrator"
	"crmai/internal/store/postgres"
)

type fakeStore struct {
	batches []int // jobs deleted per call
	calls   int
	err     error
}

func (f *fakeStore) QueueStats(context.Context) ([]postgres.QueueStat, error) {
	return []postgres.QueueStat{{Status: "queued", Jobs: 3, OldestQueuedSeconds: 12}, {Status: "running", Jobs: 1}}, nil
}

func (f *fakeStore) PurgeFinished(_ context.Context, days, limit int, _ string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if days != 30 || limit != 2 {
		panic("unexpected arguments")
	}
	n := f.batches[f.calls]
	f.calls++
	return n, nil
}

func housekeeping(s *fakeStore, days int) *orchestrator.Housekeeping {
	return &orchestrator.Housekeeping{
		Store: s, WorkerID: "w", RetentionDays: days, BatchSize: 2,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestPurgeRunsBatchesUntilShort(t *testing.T) {
	before := metrics.RetentionDeleted.Value()
	s := &fakeStore{batches: []int{2, 2, 1}}
	housekeeping(s, 30).Purge(context.Background())
	if s.calls != 3 || metrics.RetentionDeleted.Value()-before != 5 {
		t.Fatalf("calls=%d deleted=%v", s.calls, metrics.RetentionDeleted.Value()-before)
	}
}

func TestPurgeDisabledAndErrors(t *testing.T) {
	s := &fakeStore{}
	housekeeping(s, 0).Purge(context.Background())
	if s.calls != 0 {
		t.Fatal("retention 0 must not delete anything")
	}
	before := metrics.RetentionRuns.Value("error")
	housekeeping(&fakeStore{err: errors.New("db down")}, 30).Purge(context.Background())
	if metrics.RetentionRuns.Value("error")-before != 1 {
		t.Fatal("a failed run must be counted")
	}
}

func TestQueueGaugesRefresh(t *testing.T) {
	housekeeping(&fakeStore{}, 30).RefreshStats(context.Background())
	// Gauges have no getter; render and look for the series.
	var b strings.Builder
	metrics.Default.Write(&b)
	for _, want := range []string{`ai_queue_jobs{status="queued"} 3`, "ai_queue_oldest_queued_seconds 12"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
}
