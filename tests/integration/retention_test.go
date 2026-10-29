//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func ownerExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func jobExists(t *testing.T, id string) bool {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var n int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM ai_job WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestRetentionDeletesOnlyOldFinishedJobs(t *testing.T) {
	ctx := context.Background()
	tenant := newID("ret")
	w := newWorker(t, "ret-worker")

	old := createJob(t, tenant, "ret-key-old-1")
	w.Process(ctx, claimJob(t, w, old.ID, time.Minute))
	recent := createJob(t, tenant, "ret-key-new-1")
	w.Process(ctx, claimJob(t, w, recent.ID, time.Minute))
	queued := createJob(t, tenant, "ret-key-queued")
	if resultCount(t, old.ID) != 1 || resultCount(t, recent.ID) != 1 {
		t.Fatal("both processed jobs must have results")
	}
	// Age the first job (and a queued one, which must never be deleted).
	ownerExec(t, `UPDATE ai_job SET finished_at = now() - interval '40 days' WHERE id = $1`, old.ID)
	ownerExec(t, `UPDATE ai_job SET created_at = now() - interval '40 days' WHERE id = $1`, queued.ID)

	// The application role has no DELETE grant of its own.
	appConn, err := pgx.Connect(ctx, appDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appConn.Close(ctx)
	if _, err := appConn.Exec(ctx, `DELETE FROM ai_job WHERE id = $1`, recent.ID); err == nil {
		t.Fatal("ai_app must not be able to DELETE directly")
	}
	if _, err := store.PurgeFinished(ctx, 0, 100, "it"); err == nil {
		t.Fatal("a retention of 0 days must be refused")
	}

	n, err := store.PurgeFinished(ctx, 30, 100, "it")
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 || jobExists(t, old.ID) || resultCount(t, old.ID) != 0 {
		t.Fatalf("the old finished job and its result must be deleted (n=%d)", n)
	}
	if !jobExists(t, recent.ID) || !jobExists(t, queued.ID) {
		t.Fatal("recent and unfinished jobs must stay")
	}

	var runs int
	if err := appConn.QueryRow(ctx, `SELECT count(*) FROM ai_retention_run WHERE run_by = 'it' AND jobs_deleted >= 1`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("each cleanup run must be logged: runs=%d err=%v", runs, err)
	}
}

func TestQueueStatsCoverAllStatuses(t *testing.T) {
	createJob(t, newID("stats"), "stats-key-0001")
	stats, err := store.QueueStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, s := range stats {
		got[s.Status] = s.Jobs
	}
	if len(got) != 5 || got["queued"] < 1 {
		t.Fatalf("expected all five statuses with at least one queued job: %v", got)
	}
}
