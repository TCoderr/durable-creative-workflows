FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
WORKDIR /src
ENV GOFLAGS=-p=1 GOMAXPROCS=1 GOMEMLIMIT=512MiB
COPY services/control/go.mod services/control/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY services/control/ ./
ARG APP_VERSION=local
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/velin ./cmd/velin

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/velin /app/velin
USER 65532:65532
EXPOSE 8080 8081
HEALTHCHECK --interval=10s --timeout=4s --start-period=20s CMD ["/app/velin", "health", "http://127.0.0.1:8080/health/ready"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/velin"]
CMD ["api"]
