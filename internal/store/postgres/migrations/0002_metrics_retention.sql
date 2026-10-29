-- Queue statistics for metrics and the retention cleanup.
--
-- ai_app still has no DELETE grant. Both functions below run with the owner's
-- rights but do one narrow thing each, and return no tenant data.

-- Counts by status over all tenants, and the age of the oldest runnable job.
CREATE FUNCTION ai_queue_stats()
RETURNS TABLE (status text, jobs bigint, oldest_queued_seconds double precision)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT s.status,
           count(j.id),
           CASE WHEN s.status = 'queued'
                THEN coalesce(extract(epoch FROM now() - min(j.next_run_at)
                              FILTER (WHERE j.next_run_at <= now())), 0)
                ELSE 0 END
      FROM unnest(ARRAY['queued', 'running', 'succeeded', 'failed', 'cancelled']) AS s(status)
      LEFT JOIN ai_job j ON j.status = s.status
     GROUP BY s.status;
$$;

-- One row per cleanup run: when, how old, how many. Never contains job data.
CREATE TABLE ai_retention_run (
    id               bigserial   PRIMARY KEY,
    ran_at           timestamptz NOT NULL DEFAULT now(),
    older_than_days  integer     NOT NULL,
    jobs_deleted     integer     NOT NULL,
    run_by           text        NOT NULL
);

-- Deletes finished jobs (succeeded, failed, cancelled) and their results whose
-- finish is older than p_days, at most p_limit per call. Queued and running
-- jobs are never touched. p_days below 1 is refused.
CREATE FUNCTION ai_purge_finished(p_days integer, p_limit integer, p_run_by text)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    deleted integer;
BEGIN
    IF p_days IS NULL OR p_days < 1 THEN
        RAISE EXCEPTION 'retention must be at least 1 day';
    END IF;
    WITH victims AS (
        SELECT id FROM ai_job
         WHERE status IN ('succeeded', 'failed', 'cancelled')
           AND finished_at < now() - make_interval(days => p_days)
         ORDER BY finished_at
         LIMIT least(greatest(p_limit, 1), 10000)
         FOR UPDATE SKIP LOCKED
    ), gone_results AS (
        DELETE FROM ai_job_result r USING victims v WHERE r.job_id = v.id RETURNING r.job_id
    ), gone AS (
        DELETE FROM ai_job j USING victims v WHERE j.id = v.id RETURNING j.id
    )
    SELECT count(*) INTO deleted FROM gone;
    INSERT INTO ai_retention_run (older_than_days, jobs_deleted, run_by)
    VALUES (p_days, deleted, left(coalesce(p_run_by, 'unknown'), 128));
    RETURN deleted;
END;
$$;

REVOKE ALL ON FUNCTION ai_queue_stats() FROM PUBLIC;
REVOKE ALL ON FUNCTION ai_purge_finished(integer, integer, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ai_queue_stats() TO ai_app;
GRANT EXECUTE ON FUNCTION ai_purge_finished(integer, integer, text) TO ai_app;
GRANT SELECT ON ai_retention_run TO ai_app;
