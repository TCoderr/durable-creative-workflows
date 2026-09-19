FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
WORKDIR /src
ENV GOFLAGS=-p=2 GOMAXPROCS=2
ADD --checksum=sha256:14280dbc5f157373a2b34d7d333dd0c8f1b8506fa9ff5b332edc9048527af6f8 https://github.com/temporalio/cli/archive/refs/tags/v1.8.3.tar.gz /tmp/upstream.tar.gz
RUN tar -xzf /tmp/upstream.tar.gz --strip-components=1 -C /src
RUN go get golang.org/x/crypto@v0.55.0 google.golang.org/grpc@v1.83.2 github.com/apache/thrift@v0.24.0
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/temporal ./cmd/temporal

FROM temporalio/temporal:1.8.3@sha256:cea463d98a8d6def4420f903ea5c3fcd0d85c8d10fbcc2770a50c12fff2eb26d AS runtime
USER root
RUN apk add --no-cache 'libcrypto3>=3.5.8-r0' 'libssl3>=3.5.8-r0'
COPY --from=build /out/temporal /usr/local/bin/temporal
COPY --from=build /src/go.mod /src/go.sum /usr/share/velin-upstream/
USER temporal
LABEL org.opencontainers.image.source="https://github.com/temporalio/cli" \
      org.opencontainers.image.version="1.8.3-velin-security.1"
