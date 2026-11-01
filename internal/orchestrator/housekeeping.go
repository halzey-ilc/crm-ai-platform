package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"crmai/internal/metrics"
	"crmai/internal/store/postgres"
)

// Store is what housekeeping needs from the database.
type Store interface {
	QueueStats(ctx context.Context) ([]postgres.QueueStat, error)
	PurgeFinished(ctx context.Context, days, limit int, runBy string) (int, error)
}

// AudioPurger deletes call recordings past their retention.
type AudioPurger interface {
	PurgeAudio(ctx context.Context, hours, limit int) (int, error)
}

// Housekeeping refreshes queue gauges and applies the retention periods.
type Housekeeping struct {
	Store         Store
	WorkerID      string
	RetentionDays int // 0 disables the cleanup
	// Audio and AudioRetentionHours delete recordings; nil Audio disables it.
	Audio               AudioPurger
	AudioRetentionHours int
	StatsEvery          time.Duration
	PurgeEvery          time.Duration
	BatchSize           int
	Log                 *slog.Logger
}

// Run loops until ctx is cancelled.
func (h *Housekeeping) Run(ctx context.Context) {
	stats := time.NewTicker(h.StatsEvery)
	defer stats.Stop()
	purge := time.NewTicker(h.PurgeEvery)
	defer purge.Stop()
	h.RefreshStats(ctx)
	h.Purge(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-stats.C:
			h.RefreshStats(ctx)
		case <-purge.C:
			h.Purge(ctx)
		}
	}
}

// RefreshStats updates the queue gauges.
func (h *Housekeeping) RefreshStats(ctx context.Context) {
	rows, err := h.Store.QueueStats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			h.Log.Warn("queue stats failed", "err", err)
		}
		return
	}
	for _, r := range rows {
		metrics.QueueJobs.Set(float64(r.Jobs), r.Status)
		if r.Status == "queued" {
			metrics.QueueOldestQueued.Set(r.OldestQueuedSeconds)
		}
	}
}

// Purge deletes recordings and finished jobs past their retention, batch by batch.
func (h *Housekeeping) Purge(ctx context.Context) {
	h.purgeAudio(ctx)
	if h.RetentionDays <= 0 {
		return
	}
	total := 0
	for ctx.Err() == nil {
		n, err := h.Store.PurgeFinished(ctx, h.RetentionDays, h.BatchSize, h.WorkerID)
		if err != nil {
			metrics.RetentionRuns.Inc("error")
			h.Log.Error("retention cleanup failed", "err", err)
			return
		}
		total += n
		metrics.RetentionDeleted.Add(float64(n))
		if n < h.BatchSize {
			break
		}
	}
	metrics.RetentionRuns.Inc("ok")
	if total > 0 {
		h.Log.Info("retention cleanup", "jobs_deleted", total, "older_than_days", h.RetentionDays)
	}
}

func (h *Housekeeping) purgeAudio(ctx context.Context) {
	if h.Audio == nil || h.AudioRetentionHours <= 0 {
		return
	}
	total := 0
	for ctx.Err() == nil {
		n, err := h.Audio.PurgeAudio(ctx, h.AudioRetentionHours, h.BatchSize)
		if err != nil {
			h.Log.Error("audio cleanup failed", "err", err)
			return
		}
		total += n
		if n < h.BatchSize {
			break
		}
	}
	if total > 0 {
		h.Log.Info("audio cleanup", "recordings_deleted", total, "older_than_hours", h.AudioRetentionHours)
	}
}
