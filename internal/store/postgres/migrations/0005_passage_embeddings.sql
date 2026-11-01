-- Semantic search for the Knowledge Assistant: one vector per passage, made by
-- the embedding model (multilingual-e5-small, 384 numbers) when the document is
-- stored. NULL when no embedding server was configured; such passages are still
-- found by full-text search. Plain real[] instead of pgvector: the knowledge
-- base of one company is small enough to rank in the application after the
-- access filter, and the database image stays unchanged.

ALTER TABLE kb_passage ADD COLUMN embedding real[]
    CHECK (embedding IS NULL OR cardinality(embedding) BETWEEN 1 AND 4096);

GRANT UPDATE (embedding) ON kb_passage TO ai_app;
