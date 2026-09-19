FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
WORKDIR /src
ENV GOFLAGS=-p=2 GOMAXPROCS=2 GOMEMLIMIT=512MiB CGO_ENABLED=0 GOEXPERIMENT=jsonv2
ADD --checksum=sha256:04268af574690b84bc3474a5f19e002cd6da3e16899fac9fd39c6e84e7843940 https://github.com/aquasecurity/trivy/archive/refs/tags/v0.74.0.tar.gz /tmp/upstream.tar.gz
RUN tar -xzf /tmp/upstream.tar.gz --strip-components=1 -C /src
RUN --mount=type=cache,target=/go/pkg/mod go get google.golang.org/grpc@v1.83.2
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -buildvcs=false -trimpath -ldflags="-s -w -X github.com/aquasecurity/trivy/pkg/version/app.ver=0.74.0" -o /out/trivy ./cmd/trivy

FROM aquasec/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969
RUN apk upgrade --no-cache
COPY --from=build /out/trivy /usr/local/bin/trivy
COPY --from=build /src/go.mod /src/go.sum /usr/share/velin-upstream/
LABEL org.opencontainers.image.source="https://github.com/aquasecurity/trivy" \
      org.opencontainers.image.version="0.74.0-velin-security.1"
