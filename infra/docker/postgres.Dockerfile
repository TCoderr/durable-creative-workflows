# Same upstream PostgreSQL 17 server, with supported Alpine security updates.
FROM postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73
# su-exec provides the same setgroups/setuid/exec operation used by this image's
# entrypoint; removes the unused vulnerable Go TLS implementation bundled in gosu.
RUN apk add --no-cache su-exec 'libcrypto3>=3.5.8-r0' 'libssl3>=3.5.8-r0' 'libuuid>=2.42.3-r0' \
    && rm /usr/local/bin/gosu \
    && sed -i 's/exec gosu postgres/exec su-exec postgres/' /usr/local/bin/docker-entrypoint.sh
