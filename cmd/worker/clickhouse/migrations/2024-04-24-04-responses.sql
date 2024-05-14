CREATE TABLE responses (

  http_id UUID PRIMARY KEY,

  conn_id UUID,
  http_version Enum('HTTP_1_0', 'HTTP_1_1', 'HTTP_2', 'HTTP_3'),

  begin_time UInt64,
  end_time UInt64,
  status_code UInt32,

) ENGINE = MergeTree PARTITION BY bitShiftRight(begin_time, 20);
