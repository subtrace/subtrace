CREATE TABLE conns (

  conn_id UUID PRIMARY KEY,

  tcp_socket_type Enum('Dial', 'Accept'),
  tcp_create_time UInt64,
  tcp_connect_time UInt64,
  tcp_tracee_bind_addr String,
  tcp_remote_peer_addr String,
  tcp_close_time UInt64,

  tls_handshake_begin_time UInt64,
  tls_handshake_end_time UInt64,
  tls_server_name String,
  tls_negotiated_version UInt16,

) ENGINE = MergeTree PARTITION BY bitShiftRight(tcp_create_time, 20);
