CREATE TABLE events (
  insert_time DateTime64(6, 'UTC') NOT NULL DEFAULT NOW()
) ENGINE = MergeTree ORDER BY insert_time;
