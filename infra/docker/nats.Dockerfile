FROM nats:2.15.0-alpine@sha256:ac8f88a6494bffc2c2a5289a0ca61cb28a9145c11ba5677cf24265d07f46d8d4
USER root
RUN apk add --no-cache 'libcrypto3>=3.5.8-r0' 'libssl3>=3.5.8-r0'
USER 65532:65532
