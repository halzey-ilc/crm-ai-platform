-- Knowledge Assistant: documents the CRM shares with the AI Platform, split
-- into passages, with access tags. Tenant isolation by row-level security, as
-- for jobs.
--
-- ai_app may DELETE here (and only here): a document removed or re-sent by the
-- CRM must disappear, and RLS still limits it to its own tenant's rows.

CREATE TABLE kb_document (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   text        NOT NULL CHECK (tenant_id ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    ref         text        NOT NULL CHECK (ref ~ '^[A-Za-z0-9._:-]{1,64}$'),
    title       text        NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    language    text        NOT NULL CHECK (language IN ('ru', 'ky', 'en')),
    access_tags text[]      NOT NULL CHECK (cardinality(access_tags) BETWEEN 1 AND 50),
    sha256      text        NOT NULL CHECK (length(sha256) = 64),
    created_by  text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT kb_document_ref UNIQUE (tenant_id, ref)
);

CREATE TABLE kb_passage (
    document_id uuid        NOT NULL REFERENCES kb_document (id) ON DELETE CASCADE,
    tenant_id   text        NOT NULL,
    n           integer     NOT NULL CHECK (n >= 0),
    body        text        NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    tsv         tsvector    GENERATED ALWAYS AS (to_tsvector('simple', body)) STORED,
    PRIMARY KEY (document_id, n)
);

CREATE INDEX kb_document_tags ON kb_document USING gin (access_tags);
CREATE INDEX kb_passage_tsv   ON kb_passage  USING gin (tsv);

ALTER TABLE kb_document ENABLE ROW LEVEL SECURITY;
ALTER TABLE kb_passage  ENABLE ROW LEVEL SECURITY;

CREATE POLICY kb_document_tenant_isolation ON kb_document
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE POLICY kb_passage_tenant_isolation ON kb_passage
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON kb_document TO ai_app;
GRANT SELECT, INSERT, DELETE ON kb_passage TO ai_app;
