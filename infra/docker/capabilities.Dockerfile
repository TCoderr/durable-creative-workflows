FROM ghcr.io/astral-sh/uv:0.12.17@sha256:10787c682e4184e4f290de1171fd4703dc63de99221f10fe1c99002ce7fa9acc AS uv
FROM python:3.14-alpine@sha256:c6ead215bfd31f1e433d968853b7a769989117115b728874824e6c0a27cb96fc AS build
COPY --from=uv /uv /usr/local/bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy
WORKDIR /app
COPY services/capabilities/pyproject.toml services/capabilities/uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project

FROM python:3.14-alpine@sha256:c6ead215bfd31f1e433d968853b7a769989117115b728874824e6c0a27cb96fc AS runtime
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PATH="/app/.venv/bin:$PATH"
WORKDIR /app
RUN apk upgrade --no-cache \
    && addgroup -g 10001 velin && adduser -D -H -u 10001 -G velin velin \
    && rm -rf /usr/local/lib/python3.14/ensurepip /usr/local/lib/python3.14/site-packages/pip /usr/local/lib/python3.14/site-packages/pip-*.dist-info /usr/local/bin/pip*
COPY --from=build --chown=10001:10001 /app/.venv /app/.venv
COPY --chown=10001:10001 services/capabilities/velin_capabilities ./velin_capabilities
COPY --chown=10001:10001 services/capabilities/prompts ./prompts
USER 10001:10001
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/health/ready', timeout=2)"]
STOPSIGNAL SIGTERM
CMD ["python", "-m", "uvicorn", "velin_capabilities.app:app", "--host", "0.0.0.0", "--port", "8000", "--no-access-log"]
