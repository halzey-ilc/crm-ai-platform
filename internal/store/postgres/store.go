// Package postgres implements the jobs ports on PostgreSQL.
//
// Every tenant-scoped statement runs inside WithTenant, which sets the
// transaction-local app.tenant_id that the row-level security policies check.
// A forgotten WHERE tenant_id = ... therefore cannot leak another tenant's
// rows; the explicit filters in the queries are a second layer.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crmai/internal/jobs"
)

// Store is the PostgreSQL adapter for jobs.Repository and jobs.Queue.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects with the application role.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("postgres: invalid AI_DATABASE_URL")
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "crm-ai-platform"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Ready checks connectivity and that the schema is at the expected version.
func (s *Store) Ready(ctx context.Context) error {
	var v *string
	if err := s.pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
		return fmt.Errorf("database not reachable or not migrated")
	}
	if v == nil || *v != LatestMigration() {
		return fmt.Errorf("database schema not at expected version")
	}
	return nil
}

// WithTenant runs fn in a transaction bound to one tenant.
func (s *Store) WithTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if !jobs.ValidTenantID(tenantID) {
		return errors.New("postgres: invalid tenant id")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

const jobColumns = `id::text, tenant_id, job_type, status, input, idempotency_key, request_hash,
	created_by, correlation_id, attempts, max_attempts, timeout_seconds, cancel_requested,
	coalesce(error_code, ''), coalesce(error_message, ''), next_run_at, coalesce(lease_owner, ''),
	lease_expires_at, created_at, updated_at, started_at, finished_at`

func scanJob(row pgx.Row) (jobs.Job, error) {
	var j jobs.Job
	var status string
	err := row.Scan(&j.ID, &j.TenantID, &j.Type, &status, &j.Input, &j.IdempotencyKey, &j.RequestHash,
		&j.CreatedBy, &j.CorrelationID, &j.Attempts, &j.MaxAttempts, &j.TimeoutSeconds, &j.CancelRequested,
		&j.ErrorCode, &j.ErrorMessage, &j.NextRunAt, &j.LeaseOwner,
		&j.LeaseExpiresAt, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt)
	j.Status = jobs.Status(status)
	return j, err
}

// Create implements jobs.Repository.
func (s *Store) Create(ctx context.Context, n jobs.NewJob) (jobs.Job, bool, error) {
	var j jobs.Job
	created := false
	err := s.WithTenant(ctx, n.TenantID, func(tx pgx.Tx) error {
		var err error
		j, err = scanJob(tx.QueryRow(ctx, `
			INSERT INTO ai_job (tenant_id, job_type, input, idempotency_key, request_hash,
			                    created_by, correlation_id, max_attempts, timeout_seconds)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT ON CONSTRAINT ai_job_idempotency DO NOTHING
			RETURNING `+jobColumns,
			n.TenantID, n.Type, n.Input, n.IdempotencyKey, n.RequestHash,
			n.CreatedBy, n.CorrelationID, n.MaxAttempts, n.TimeoutSeconds))
		if err == nil {
			created = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		j, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM ai_job
			WHERE tenant_id = $1 AND created_by = $2 AND idempotency_key = $3`,
			n.TenantID, n.CreatedBy, n.IdempotencyKey))
		return err
	})
	return j, created, err
}

// Get implements jobs.Repository.
func (s *Store) Get(ctx context.Context, tenantID, id string) (jobs.Job, *jobs.Result, error) {
	var j jobs.Job
	var res *jobs.Result
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		j, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM ai_job WHERE id = $1 AND tenant_id = $2`, id, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.ErrNotFound
		}
		if err != nil {
			return err
		}
		r := jobs.Result{}
		err = tx.QueryRow(ctx, `SELECT job_id::text, tenant_id, schema_id, provider, model, output, created_at
			FROM ai_job_result WHERE job_id = $1 AND tenant_id = $2`, id, tenantID).
			Scan(&r.JobID, &r.TenantID, &r.SchemaID, &r.Provider, &r.Model, &r.Output, &r.CreatedAt)
		if err == nil {
			res = &r
			return nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return j, res, err
}

// RequestCancel implements jobs.Repository.
func (s *Store) RequestCancel(ctx context.Context, tenantID, id string) (jobs.Job, error) {
	var j jobs.Job
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM ai_job WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Status.Terminal() {
			return jobs.ErrNotCancellable
		}
		j, err = scanJob(tx.QueryRow(ctx, `
			UPDATE ai_job SET
			    cancel_requested = true,
			    status      = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
			    finished_at = CASE WHEN status = 'queued' THEN now() ELSE finished_at END,
			    updated_at  = now()
			WHERE id = $1 AND tenant_id = $2
			RETURNING `+jobColumns, id, tenantID))
		return err
	})
	return j, err
}

// Claim implements jobs.Queue.
func (s *Store) Claim(ctx context.Context, worker string, limit int, lease time.Duration) ([]jobs.Job, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+jobColumns+` FROM ai_claim_jobs($1, $2, $3)`,
		worker, limit, int(lease/time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jobs.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Complete implements jobs.Queue.
func (s *Store) Complete(ctx context.Context, j jobs.Job, worker string, r jobs.Result) error {
	return s.WithTenant(ctx, j.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE ai_job SET status = 'succeeded', finished_at = now(), updated_at = now(),
			       lease_owner = NULL, lease_expires_at = NULL, error_code = NULL, error_message = NULL
			WHERE id = $1 AND status = 'running' AND lease_owner = $2`, j.ID, worker)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return jobs.ErrLeaseLost
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ai_job_result (job_id, tenant_id, schema_id, provider, model, output)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (job_id) DO NOTHING`,
			j.ID, j.TenantID, r.SchemaID, r.Provider, r.Model, r.Output)
		return err
	})
}

// Fail implements jobs.Queue.
func (s *Store) Fail(ctx context.Context, j jobs.Job, worker, code, message string, retryAt *time.Time) error {
	if len(message) > 500 {
		message = message[:500]
	}
	return s.WithTenant(ctx, j.TenantID, func(tx pgx.Tx) error {
		var tag interface{ RowsAffected() int64 }
		var err error
		if retryAt != nil {
			tag, err = tx.Exec(ctx, `
				UPDATE ai_job SET status = 'queued', next_run_at = $3, error_code = $4, error_message = $5,
				       lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
				WHERE id = $1 AND status = 'running' AND lease_owner = $2`, j.ID, worker, *retryAt, code, message)
		} else {
			tag, err = tx.Exec(ctx, `
				UPDATE ai_job SET status = 'failed', finished_at = now(), error_code = $3, error_message = $4,
				       lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
				WHERE id = $1 AND status = 'running' AND lease_owner = $2`, j.ID, worker, code, message)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return jobs.ErrLeaseLost
		}
		return nil
	})
}

// MarkCancelled implements jobs.Queue.
func (s *Store) MarkCancelled(ctx context.Context, j jobs.Job, worker string) error {
	return s.WithTenant(ctx, j.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE ai_job SET status = 'cancelled', finished_at = now(),
			       lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
			WHERE id = $1 AND status = 'running' AND lease_owner = $2`, j.ID, worker)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return jobs.ErrLeaseLost
		}
		return nil
	})
}

// CancelRequested implements jobs.Queue.
func (s *Store) CancelRequested(ctx context.Context, j jobs.Job) (bool, error) {
	var requested bool
	err := s.WithTenant(ctx, j.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT cancel_requested FROM ai_job WHERE id = $1`, j.ID).Scan(&requested)
	})
	return requested, err
}

var (
	_ jobs.Repository = (*Store)(nil)
	_ jobs.Queue      = (*Store)(nil)
)
