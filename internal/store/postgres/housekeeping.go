package postgres

import (
	"context"
	"fmt"
)

// QueueStat is the number of jobs in one status over all tenants.
type QueueStat struct {
	Status              string
	Jobs                int64
	OldestQueuedSeconds float64
}

// QueueStats reads queue counts through ai_queue_stats(), which returns no
// tenant data.
func (s *Store) QueueStats(ctx context.Context) ([]QueueStat, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, jobs, oldest_queued_seconds FROM ai_queue_stats()`)
	if err != nil {
		return nil, fmt.Errorf("postgres: queue stats: %w", err)
	}
	defer rows.Close()
	var out []QueueStat
	for rows.Next() {
		var q QueueStat
		if err := rows.Scan(&q.Status, &q.Jobs, &q.OldestQueuedSeconds); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// PurgeFinished deletes up to limit finished jobs (and results) older than
// days, through ai_purge_finished(), which also logs the run. Returns the
// number of jobs deleted.
func (s *Store) PurgeFinished(ctx context.Context, days, limit int, runBy string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT ai_purge_finished($1, $2, $3)`, days, limit, runBy).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: purge finished jobs: %w", err)
	}
	return n, nil
}
