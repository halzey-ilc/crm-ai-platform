package jobs

import (
	"context"
	"time"
)

// Repository is the request-side port (used by the gateway). Every method is
// scoped to one tenant; implementations must make rows of other tenants
// invisible, not merely filtered by the caller.
type Repository interface {
	// Create persists a job, or returns the existing job when the same
	// (tenant, creator, idempotency key) was already used. created reports
	// which happened. The caller compares request hashes.
	Create(ctx context.Context, j NewJob) (job Job, created bool, err error)
	// Get returns a job and its result (nil until succeeded).
	Get(ctx context.Context, tenantID, id string) (Job, *Result, error)
	// RequestCancel cancels a queued job immediately or flags a running one.
	RequestCancel(ctx context.Context, tenantID, id string) (Job, error)
}

// Queue is the worker-side port (used by the orchestrator). Delivery is
// at-least-once: a job whose lease expires is claimed again, so completion
// must be idempotent.
type Queue interface {
	// Claim leases up to limit runnable jobs to worker.
	Claim(ctx context.Context, worker string, limit int, lease time.Duration) ([]Job, error)
	// Complete stores the result and marks the job succeeded, only while
	// worker still holds the lease (ErrLeaseLost otherwise). A second
	// completion never creates a second result.
	Complete(ctx context.Context, j Job, worker string, r Result) error
	// Fail records an error. With retryAt the job is re-queued; without it
	// the job becomes failed.
	Fail(ctx context.Context, j Job, worker, code, message string, retryAt *time.Time) error
	// MarkCancelled finishes a job whose cancellation was requested.
	MarkCancelled(ctx context.Context, j Job, worker string) error
	// CancelRequested reports whether cancellation was requested meanwhile.
	CancelRequested(ctx context.Context, j Job) (bool, error)
}
