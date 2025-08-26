FROM --platform=$BUILDPLATFORM golang:1.24.2 AS build
WORKDIR /go/src/subtrace
COPY . .
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg --mount=type=cache,target=/root/.cache GOARCH=$TARGETARCH make

FROM clickhouse/clickhouse-server:24.8-alpine
RUN mv /entrypoint.sh /clickhouse_entrypoint.sh
COPY --from=build /go/src/subtrace/subtrace /usr/local/bin/subtrace
COPY worker/clickhouse_system_ttl.xml /etc/clickhouse-server/config.d/
COPY worker/subtrace_entrypoint.sh /
ENTRYPOINT ["bash", "-c"]
CMD ["/subtrace_entrypoint.sh"]
