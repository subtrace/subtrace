-- Dummy placeholder for ingestion materialized view. The real view is built
-- at runtime time. This exists so that the runtime build can use a simple
-- ALTER MATERIALIZED VIEW consistently.
CREATE MATERIALIZED VIEW ingest_mv TO `events` AS SELECT true FROM ingest;
