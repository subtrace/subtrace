FROM --platform=$BUILDPLATFORM golang:1.22.0 AS build
WORKDIR /go/src/subtrace
COPY . .
COPY .git .git
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg --mount=type=cache,target=/root/.cache GOARCH=$TARGETARCH make

FROM clickhouse/clickhouse-server:24.8-alpine
COPY --from=build /go/src/subtrace/subtrace /usr/local/bin/subtrace
RUN cat >/usr/local/bin/start_worker.sh <<EOF
  clickhouse-server &
  export SUBTRACE_CLICKHOUSE_HOST=localhost
  export SUBTRACE_CLICKHOUSE_DATABASE=default
  export SUBTRACE_CLICKHOUSE_FORMAT_SCHEMAS=/var/lib/clickhouse/format_schemas
  subtrace worker \$*
EOF
ENTRYPOINT ["/usr/bin/bash", "/usr/local/bin/start_worker.sh"]
