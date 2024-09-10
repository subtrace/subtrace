-- Dummy placeholder for ingestion materialized view. The real view is built
-- at runtime time. This exists so that the runtime build can use a simple
-- ALTER MATERIALIZED VIEW consistently.
CREATE MATERIALIZED VIEW subtrace_ingest_mv_{{.Suffix}} TO subtrace_events_{{.Suffix}} AS SELECT true FROM subtrace_ingest_{{.Suffix}};
