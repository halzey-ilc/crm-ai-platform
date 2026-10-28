// Package orchestrator runs claimed jobs: timeouts, retries with backoff,
// cancellation and idempotent completion. Delivery is at-least-once; a job
// re-claimed after a crash never produces a second result because results
// are keyed by job id and completion requires holding the lease.
package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
)

// Worker polls the queue and executes jobs.
type Worker struct {
	Queue        jobs.Queue
	Types        *jobtypes.Registry
	Models       *modelgw.Gateway
	ID           string
	Concurrency  int
	PollInterval time.Duration
	Lease        time.Duration
	Backoff      jobs.Backoff
	Log          *slog.Logger
	Now          func() time.Time
}

// Run polls until ctx is cancelled, then waits for in-flight jobs. Jobs still
// running at shutdown keep their lease and are re-claimed after it expires.
func (w *Worker) Run(ctx context.Context) {
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		free := w.Concurrency - len(sem)
		if free > 0 {
			claimed, err := w.Queue.Claim(ctx, w.ID, free, w.Lease)
			if err != nil && ctx.Err() == nil {
				w.Log.Error("claim failed", "err", err)
			}
			for _, j := range claimed {
				sem <- struct{}{}
				wg.Add(1)
				go func(j jobs.Job) {
					defer func() { <-sem; wg.Done() }()
					// Finish the job even if shutdown starts: the store writes
					// are short, and abandoning them would only cost a retry.
					w.Process(context.WithoutCancel(ctx), j)
				}(j)
			}
			if len(claimed) == free {
				continue // queue may have more; poll again immediately
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.PollInterval):
		}
	}
}

// Process executes one claimed job and records the outcome.
func (w *Worker) Process(ctx context.Context, j jobs.Job) {
	log := w.Log.With("job_id", j.ID, "tenant", j.TenantID, "job_type", j.Type,
		"attempt", j.Attempts, "correlation_id", j.CorrelationID)

	if j.Attempts > j.MaxAttempts {
		// Re-claimed after its lease expired on the last allowed attempt.
		w.record(log, w.Queue.Fail(ctx, j, w.ID, "attempts_exhausted", "lease expired on the final attempt", nil))
		return
	}
	if j.CancelRequested {
		w.record(log, w.Queue.MarkCancelled(ctx, j, w.ID))
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(j.TimeoutSeconds)*time.Second)
	started := time.Now()
	res, err := w.Types.Execute(runCtx, w.Models, j)
	cancel()
	log = log.With("duration_ms", time.Since(started).Milliseconds())

	if requested, cerr := w.Queue.CancelRequested(ctx, j); cerr == nil && requested {
		log.Info("job cancelled during execution")
		w.record(log, w.Queue.MarkCancelled(ctx, j, w.ID))
		return
	}

	switch {
	case err == nil:
		log.Info("job succeeded", "provider", res.Provider, "model", res.Model)
		w.record(log, w.Queue.Complete(ctx, j, w.ID, res))
	case jobtypes.IsPermanent(err):
		var p *jobtypes.PermanentError
		errors.As(err, &p)
		log.Warn("job failed permanently", "error_code", p.Code)
		w.record(log, w.Queue.Fail(ctx, j, w.ID, p.Code, safeMessage(p.Code), nil))
	case modelgw.IsTransient(err) && j.Attempts < j.MaxAttempts:
		retryAt := w.now().Add(w.Backoff.Delay(j.Attempts))
		log.Warn("job failed transiently, will retry", "retry_at", retryAt)
		w.record(log, w.Queue.Fail(ctx, j, w.ID, "transient_error", "temporary failure, retry scheduled", &retryAt))
	default:
		code := "attempts_exhausted"
		if !modelgw.IsTransient(err) {
			code = "internal_error"
		}
		log.Warn("job failed", "error_code", code)
		w.record(log, w.Queue.Fail(ctx, j, w.ID, code, safeMessage(code), nil))
	}
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) record(log *slog.Logger, err error) {
	switch {
	case err == nil:
	case errors.Is(err, jobs.ErrLeaseLost):
		log.Warn("lease lost; another worker owns the job now, outcome discarded")
	default:
		log.Error("recording job outcome failed", "err", err)
	}
}

// safeMessage maps error codes to client-facing text. Provider error details
// can contain input fragments, so they are logged by code only.
func safeMessage(code string) string {
	switch code {
	case "output_contract_violation":
		return "model output did not satisfy the output contract"
	case "output_not_grounded":
		return "model output referenced text not present in the input"
	case "provider_error":
		return "model provider rejected the request"
	case "unknown_job_type":
		return "job type is not supported by this worker"
	case "attempts_exhausted":
		return "all attempts failed"
	default:
		return "job failed"
	}
}
