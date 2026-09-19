FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
WORKDIR /src
ENV GOFLAGS=-p=1 GOMAXPROCS=1 GOMEMLIMIT=512MiB
ADD --checksum=sha256:9294e72722fe8f90e54994ce36f331ea6176cb0edc4149cbf1d023bc89536505 https://github.com/prometheus/prometheus/archive/refs/tags/v3.14.0.tar.gz /tmp/upstream.tar.gz
RUN tar -xzf /tmp/upstream.tar.gz --strip-components=1 -C /src
RUN go get golang.org/x/crypto@v0.55.0 google.golang.org/grpc@v1.83.2
# API/scraping/TSDB/query behavior is retained. Grafana is the operator UI;
# the upstream npm-built native Prometheus UI is deliberately not bundled here.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/prometheus ./cmd/prometheus \
    && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/promtool ./cmd/promtool

FROM prom/prometheus:v3.14.0@sha256:5ce7540c3c00ef4ab0c9d2c995c6a5b9c421f44b4a115d97a2c7af3b1c21cbb0 AS runtime
COPY --from=build /out/prometheus /bin/prometheus
COPY --from=build /out/promtool /bin/promtool
COPY --from=build /src/go.mod /src/go.sum /usr/share/velin-upstream/
LABEL org.opencontainers.image.source="https://github.com/prometheus/prometheus" \
      org.opencontainers.image.version="3.14.0-velin-security.1"
