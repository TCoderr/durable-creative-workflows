FROM node:25-bookworm-slim@sha256:81db02c4b671288a03915da9534dbd54f96d0e7c24d80ccc54f5b36b2e684370 AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY . .
RUN npm run build

FROM gcr.io/distroless/nodejs24-debian13:nonroot@sha256:774b7d020b24214835769e24c3544835526cd0288f0b094eae48e8b2c2429a79 AS runtime
ENV NODE_ENV=production PORT=4173 HOST=0.0.0.0
WORKDIR /app
COPY --from=build --chown=65532:65532 /app/dist ./dist
COPY --chown=65532:65532 scripts/serve.mjs ./scripts/serve.mjs
USER 65532:65532
EXPOSE 4173
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s CMD ["/nodejs/bin/node", "-e", "fetch('http://127.0.0.1:4173/health/live').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"]
STOPSIGNAL SIGTERM
CMD ["scripts/serve.mjs"]
