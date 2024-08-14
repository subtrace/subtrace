FROM golang:1.22.0 AS build
WORKDIR /go/src/subtrace
COPY . .
RUN --mount=type=cache,target=/go/pkg --mount=type=cache,target=/root/.cache make

FROM debian:12

RUN apt-get update && apt-get install -y ca-certificates curl

ARG TARGETARCH
RUN cat | bash - <<EOF
  curl -sLO "https://packages.clickhouse.com/deb/pool/main/c/clickhouse/clickhouse-common-static_23.12.6.19_${TARGETARCH}.deb"
  curl -sLO "https://packages.clickhouse.com/deb/pool/main/c/clickhouse/clickhouse-server_23.12.6.19_${TARGETARCH}.deb"
  DEBIAN_FRONTEND=noninteractive apt install -y ./clickhouse-common-static_23.12.6.19_${TARGETARCH}.deb ./clickhouse-server_23.12.6.19_${TARGETARCH}.deb
  rm ./clickhouse-common-static_23.12.6.19_${TARGETARCH}.deb ./clickhouse-server_23.12.6.19_${TARGETARCH}.deb
EOF

COPY --from=build /go/src/subtrace/subtrace /usr/local/bin/subtrace

RUN cat >/usr/local/bin/start_worker.sh <<EOF
  clickhouse-server &
  export SUBTRACE_CLICKHOUSE_HOST=localhost
  export SUBTRACE_CLICKHOUSE_DATABASE=default
  export SUBTRACE_CLICKHOUSE_FORMAT_SCHEMAS=/var/lib/clickhouse/format_schemas
  subtrace worker \$*
EOF

ENTRYPOINT ["/usr/bin/bash", "/usr/local/bin/start_worker.sh"]
