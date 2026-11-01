-- Call recordings the CRM uploads for transcription (call.transcribe.v1).
-- Tenant isolation by row-level security, as for jobs and documents.
--
-- Recordings are personal data: they are kept only until transcribed and are
-- deleted after a few hours by ai_purge_audio(), run by the orchestrator. ai_app
-- may DELETE its own tenant's rows (the CRM can withdraw a recording).

CREATE TABLE ai_audio (
    tenant_id    text        NOT NULL CHECK (tenant_id ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    ref          text        NOT NULL CHECK (ref ~ '^[A-Za-z0-9._:-]{1,64}$'),
    content_type text        NOT NULL CHECK (content_type ~ '^audio/[a-z0-9.+-]{1,40}$'),
    bytes        bytea       NOT NULL CHECK (octet_length(bytes) BETWEEN 1 AND 104857600),
    sha256       text        NOT NULL CHECK (length(sha256) = 64),
    created_by   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, ref)
);

CREATE INDEX ai_audio_created ON ai_audio (created_at);

ALTER TABLE ai_audio ENABLE ROW LEVEL SECURITY;

CREATE POLICY ai_audio_tenant_isolation ON ai_audio
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON ai_audio TO ai_app;

-- Deletes recordings older than p_hours over all tenants, at most p_limit per
-- call. Returns the number deleted, never their content. p_hours below 1 is refused.
CREATE FUNCTION ai_purge_audio(p_hours integer, p_limit integer)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    deleted integer;
BEGIN
    IF p_hours IS NULL OR p_hours < 1 THEN
        RAISE EXCEPTION 'audio retention must be at least 1 hour';
    END IF;
    WITH victims AS (
        SELECT tenant_id, ref FROM ai_audio
         WHERE created_at < now() - make_interval(hours => p_hours)
         ORDER BY created_at
         LIMIT least(greatest(p_limit, 1), 10000)
         FOR UPDATE SKIP LOCKED
    ), gone AS (
        DELETE FROM ai_audio a USING victims v
         WHERE a.tenant_id = v.tenant_id AND a.ref = v.ref
        RETURNING 1
    )
    SELECT count(*) INTO deleted FROM gone;
    RETURN deleted;
END;
$$;

REVOKE ALL ON FUNCTION ai_purge_audio(integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ai_purge_audio(integer, integer) TO ai_app;
