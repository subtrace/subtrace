FROM --platform=$BUILDPLATFORM golang:1.23.3 AS build
WORKDIR /go/src/subtrace
COPY . .
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg --mount=type=cache,target=/root/.cache GOARCH=$TARGETARCH make

FROM clickhouse/clickhouse-server:24.8-alpine
RUN mv /entrypoint.sh /clickhouse_entrypoint.sh
COPY --from=build /go/src/subtrace/subtrace /usr/local/bin/subtrace
RUN cat >/subtrace_entrypoint.sh <<EOF
  bash /clickhouse_entrypoint.sh &
  export SUBTRACE_CLICKHOUSE_HOST=localhost
  export SUBTRACE_CLICKHOUSE_DATABASE=default
  subtrace worker -v "\$@"
EOF
RUN chmod +x /subtrace_entrypoint.sh
ENTRYPOINT ["bash", "-c"]
CMD ["/subtrace_entrypoint.sh"]
