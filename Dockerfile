FROM golang:1.22.0 AS build
WORKDIR /src/subtrace
COPY . .
RUN make

FROM debian:12
RUN apt-get update && apt-get install -y ca-certificates
COPY --from=build /src/subtrace/subtrace /usr/local/bin/subtrace
ENTRYPOINT ["/usr/local/bin/subtrace"]
