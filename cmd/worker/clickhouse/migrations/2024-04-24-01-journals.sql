CREATE TABLE journals (

  journal_id UUID PRIMARY KEY,

  insert_time UInt64,

) ENGINE = MergeTree PARTITION BY bitShiftRight(insert_time, 20);
