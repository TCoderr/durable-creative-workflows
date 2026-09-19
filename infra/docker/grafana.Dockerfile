FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
WORKDIR /src
ENV GOFLAGS=-p=1 GOMAXPROCS=1 GOMEMLIMIT=512MiB CGO_ENABLED=0
ADD --checksum=sha256:eb5c8001e18b3e587bdda93c2fff925301d46c61ba1688c97585ccbda3848e03 https://github.com/grafana/grafana/archive/refs/tags/v13.2.2.tar.gz /tmp/upstream.tar.gz
RUN tar -xzf /tmp/upstream.tar.gz --strip-components=1 -C /src
RUN --mount=type=cache,target=/go/pkg/mod go get github.com/apache/thrift@v0.24.0 google.golang.org/grpc@v1.83.2
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -buildvcs=false -tags=oss -trimpath -ldflags="-s -w -X main.version=13.2.2 -X main.buildBranch=security" -o /out/grafana ./pkg/cmd/grafana

# VELIN uses only Prometheus. Keep its official signed plugin, remove unused
# bundled datasource executables, and leave signature enforcement enabled.
FROM grafana/grafana:13.2.2@sha256:ac461fb352abc50da10a51c7d02462e9c05488f11f53f14b3ad79a8145f638a0
ENV GF_PLUGINS_PREINSTALL_DISABLED=true \
    GF_PLUGINS_PREINSTALL_AUTO_UPDATE=false \
    GF_PLUGINS_PLUGIN_ADMIN_ENABLED=false
USER root
ADD --checksum=sha256:33c5316c52e8745e38ea844d5357723c0ebe15ce28cdaf6000cdffe95a01af72 https://github.com/grafana/grafana-prometheus-datasource/releases/download/v13.1.9/prometheus-13.1.9.linux_amd64.zip /tmp/prometheus.zip
RUN apk upgrade --no-cache \
    && rm -rf /usr/share/grafana/data/plugins-bundled/* \
    && unzip /tmp/prometheus.zip -d /usr/share/grafana/data/plugins-bundled \
    && rm /tmp/prometheus.zip \
    && chown -R 472:0 /usr/share/grafana/data/plugins-bundled
COPY --from=build /out/grafana /usr/share/grafana/bin/grafana
COPY --from=build /src/go.mod /src/go.sum /src/go.work /src/go.work.sum /usr/share/velin-upstream/
USER 472
LABEL org.opencontainers.image.source="https://github.com/grafana/grafana" \
      org.opencontainers.image.version="13.2.2-velin-security.1"
