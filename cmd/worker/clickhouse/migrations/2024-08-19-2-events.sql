CREATE TABLE events (
  `time` DateTime64(9) CODEC(Delta, ZSTD(1)),                                                                                                                                  
  `event_id` UUID,
  `service` LowCardinality(String),
  INDEX `index_time` `time` TYPE minmax,
  INDEX `index_event_id` `event_id` TYPE bloom_filter(0.01) GRANULARITY 1,
  INDEX `index_service` `service` TYPE bloom_filter(0.01) GRANULARITY 1,
)
ENGINE = MergeTree
PARTITION BY toDate(`time`)
ORDER BY (`service`, `time`)
TTL toDate(`time`) + toIntervalDay(30)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;
