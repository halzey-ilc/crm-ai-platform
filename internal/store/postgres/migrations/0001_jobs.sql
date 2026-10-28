-- AI job system: jobs, results, tenant row-level security, queue claim.
--
-- Tenant isolation: every row carries tenant_id. The application role
-- (ai_app) is not the table owner, so row-level security applies to it: it
-- sees and writes only rows whose tenant_id equals the transaction-local
-- setting app.tenant_id. With no setting it sees nothing (fail closed).

CREATE TABLE ai_job (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         text        NOT NULL CHECK (tenant_id ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    job_type          text        NOT NULL CHECK (length(job_type) BETWEEN 1 AND 128),
    status            text        NOT NULL DEFAULT 'queued'
                                  CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    input             jsonb       NOT NULL,
    idempotency_key   text        NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
    request_hash      text        NOT NULL CHECK (length(request_hash) = 64),
    created_by        text        NOT NULL CHECK (length(created_by) BETWEEN 1 AND 128),
    correlation_id    text        NOT NULL,
    attempts          integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts      integer     NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
    timeout_seconds   integer     NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 86400),
    cancel_requested  boolean     NOT NULL DEFAULT false,
    error_code        text,
    error_message     text        CHECK (length(error_message) <= 500),
    next_run_at       timestamptz NOT NULL DEFAULT now(),
    lease_owner       text,
    lease_expires_at  timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,
    finished_at       timestamptz,
    CONSTRAINT ai_job_idempotency UNIQUE (tenant_id, created_by, idempotency_key),
    CONSTRAINT ai_job_lease_consistent CHECK ((status = 'running') = (lease_owner IS NOT NULL))
);

CREATE INDEX ai_job_runnable ON ai_job (next_run_at) WHERE status = 'queued';
CREATE INDEX ai_job_leases   ON ai_job (lease_expires_at) WHERE status = 'running';
CREATE INDEX ai_job_tenant   ON ai_job (tenant_id, created_at DESC);

CREATE TABLE ai_job_result (
    job_id      uuid        PRIMARY KEY REFERENCES ai_job (id),
    tenant_id   text        NOT NULL,
    schema_id   text        NOT NULL,
    provider    text        NOT NULL,
    model       text        NOT NULL,
    output      jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE ai_job        ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_job_result ENABLE ROW LEVEL SECURITY;

CREATE POLICY ai_job_tenant_isolation ON ai_job
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE POLICY ai_job_result_tenant_isolation ON ai_job_result
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- The only cross-tenant read the worker needs: lease runnable jobs. It runs
-- with the owner's rights (bypassing RLS) but returns just the claimed rows;
-- all further work on a job happens under that job's tenant setting.
-- Jobs whose lease expired (crashed worker) are claimable again.
CREATE FUNCTION ai_claim_jobs(p_worker text, p_limit integer, p_lease_seconds integer)
RETURNS SETOF ai_job
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    UPDATE ai_job j
       SET status = 'running',
           attempts = j.attempts + 1,
           lease_owner = p_worker,
           lease_expires_at = now() + make_interval(secs => p_lease_seconds),
           started_at = coalesce(j.started_at, now()),
           updated_at = now()
     WHERE j.id IN (
           SELECT id FROM ai_job
            WHERE (status = 'queued' AND next_run_at <= now())
               OR (status = 'running' AND lease_expires_at < now())
            ORDER BY next_run_at
            LIMIT least(greatest(p_limit, 1), 100)
            FOR UPDATE SKIP LOCKED)
    RETURNING j.*;
$$;

REVOKE ALL ON FUNCTION ai_claim_jobs(text, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ai_claim_jobs(text, integer, integer) TO ai_app;

-- No DELETE: retention is a separate, audited operation (see docs/AI_DATA_GOVERNANCE.md).
GRANT SELECT, INSERT, UPDATE ON ai_job        TO ai_app;
GRANT SELECT, INSERT          ON ai_job_result TO ai_app;
