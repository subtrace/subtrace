FROM golang:1.22.0 AS build
WORKDIR /src/subtrace
COPY . .
ARG SUBTRACE_CONTROL_URL=https://control.subtrace.dev
RUN SUBTRACE_CONTROL_URL=${SUBTRACE_CONTROL_URL} make

FROM debian:12
RUN apt-get update && apt-get install -y ca-certificates
COPY --from=build /src/subtrace/subtrace /usr/local/bin/subtrace
ENTRYPOINT ["/usr/local/bin/subtrace"]
