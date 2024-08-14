FROM golang:1.22.0 AS build
WORKDIR /go/src/subtrace
COPY . .
RUN --mount=type=cache,target=/go/pkg --mount=type=cache,target=/root/.cache make

FROM debian:12
RUN apt-get update && apt-get install -y ca-certificates
COPY --from=build /go/src/subtrace/subtrace /usr/local/bin/subtrace
ENTRYPOINT ["/usr/local/bin/subtrace"]
