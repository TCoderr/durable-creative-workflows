import asyncio
import hmac
import logging
import os
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, Response
from opentelemetry import propagate, trace
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from velin_capabilities.engine import Engine
from velin_capabilities.errors import CapabilityError
from velin_capabilities.prompts import load_prompt
from velin_capabilities.providers import DeterministicProvider, HTTPProvider, Provider, Router
from velin_capabilities.schemas import OUTPUT_SCHEMAS, CapabilityRequest, CapabilityResponse
from velin_capabilities.telemetry import TRACER, event, setup_tracing

FAULTS = {
    "",
    "malformed_once",
    "malformed_always",
    "primary_timeout",
    "primary_failure",
    "force_revision",
    "tool_failure",
    "slow_activity",
    "prompt_injection",
    "regressed",
    "unauthorized_tool",
}


def create_app() -> FastAPI:
    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        token = os.getenv("CAPABILITIES_TOKEN", "")
        if len(token) < 32:
            raise RuntimeError("CAPABILITIES_TOKEN must contain at least 32 characters")
        if os.getenv("APP_ENV") == "production" and os.getenv("VELIN_TEST_MODE") == "1":
            raise RuntimeError("Test mode cannot run in production")
        provider_mode = os.getenv("VELIN_PROVIDER", "deterministic")
        primary: Provider
        fallback: Provider | None
        if provider_mode == "deterministic":
            primary = DeterministicProvider()
            fallback = DeterministicProvider("deterministic-fallback")
        elif provider_mode == "http":
            primary = HTTPProvider(
                os.getenv("LLM_ENDPOINT", ""),
                os.getenv("LLM_API_KEY", ""),
                os.getenv("LLM_MODEL", ""),
            )
            fallback = None
            if os.getenv("LLM_FALLBACK_ENDPOINT"):
                fallback = HTTPProvider(
                    os.environ["LLM_FALLBACK_ENDPOINT"],
                    os.getenv("LLM_FALLBACK_API_KEY", ""),
                    os.getenv("LLM_FALLBACK_MODEL", ""),
                    "configured-http-fallback",
                )
        else:
            raise RuntimeError("VELIN_PROVIDER must be deterministic or http")
        for capability in OUTPUT_SCHEMAS:
            load_prompt(capability)
        app.state.engine = Engine(Router(primary, fallback))
        app.state.token = token
        app.state.capacity = asyncio.Semaphore(16)
        logging.basicConfig(level=logging.INFO, format="%(message)s")
        telemetry = setup_tracing()
        app.state.ready = True
        event("service_started", provider_mode=provider_mode, fixture=not primary.live)
        yield
        app.state.ready = False
        telemetry.shutdown()
        event("service_stopped")

    app = FastAPI(
        title="VELIN capability service",
        version="1.0.0",
        lifespan=lifespan,
        docs_url=None,
        redoc_url=None,
        openapi_url=None,
    )

    @app.middleware("http")
    async def boundary(request: Request, call_next: Any) -> Response:
        context = propagate.extract(dict(request.headers))
        with TRACER.start_as_current_span(
            "http." + request.method, context=context, kind=trace.SpanKind.SERVER
        ):
            trace_id = f"{trace.get_current_span().get_span_context().trace_id:032x}"
            request.state.trace_id = trace_id
            # Authenticate before parsing private request bodies. No browser CORS is enabled.
            if request.url.path not in {"/health/live", "/health/ready"}:
                expected = "Bearer " + getattr(app.state, "token", "unconfigured")
                if not hmac.compare_digest(request.headers.get("authorization", ""), expected):
                    return JSONResponse(
                        {
                            "code": "UNAUTHORIZED",
                            "message": "Authentication required.",
                            "traceId": trace_id,
                        },
                        status_code=401,
                    )
            length = request.headers.get("content-length")
            if length and (not length.isdigit() or int(length) > 131072):
                return JSONResponse(
                    {
                        "code": "PAYLOAD_TOO_LARGE",
                        "message": "Request exceeds limit.",
                        "traceId": trace_id,
                    },
                    status_code=413,
                )
            # Stream bounded input: content-length is untrusted and may be absent.
            if request.method in {"POST", "PUT", "PATCH"}:
                body = bytearray()
                async for chunk in request.stream():
                    body.extend(chunk)
                    if len(body) > 131072:
                        return JSONResponse(
                            {
                                "code": "PAYLOAD_TOO_LARGE",
                                "message": "Request exceeds limit.",
                                "traceId": trace_id,
                            },
                            status_code=413,
                        )
                request._body = bytes(body)
            response: Response = await call_next(request)
            response.headers["X-Content-Type-Options"] = "nosniff"
            response.headers["Cache-Control"] = "no-store"
            response.headers["X-Trace-Id"] = trace_id
            return response

    @app.exception_handler(RequestValidationError)
    async def validation_error(request: Request, exc: RequestValidationError) -> JSONResponse:
        return JSONResponse(
            {
                "code": "VALIDATION_ERROR",
                "message": "Request schema is invalid.",
                "traceId": request.state.trace_id,
            },
            status_code=422,
        )

    @app.exception_handler(CapabilityError)
    async def capability_error(request: Request, exc: CapabilityError) -> JSONResponse:
        event("capability_failed", code=exc.code)
        envelope: dict[str, Any] = {
            "code": exc.code,
            "message": exc.message,
            "traceId": request.state.trace_id,
        }
        if exc.tool_calls:
            envelope["tool_calls"] = [item.model_dump() for item in exc.tool_calls]
        if exc.invocations:
            envelope["invocations"] = [item.model_dump() for item in exc.invocations]
        return JSONResponse(
            envelope,
            status_code=exc.status,
        )

    @app.exception_handler(Exception)
    async def internal_error(request: Request, exc: Exception) -> JSONResponse:
        event("capability_internal_error", error_type=type(exc).__name__)
        return JSONResponse(
            {
                "code": "INTERNAL_ERROR",
                "message": "Internal capability error.",
                "traceId": request.state.trace_id,
            },
            status_code=500,
        )

    @app.get("/health/live")
    async def live() -> dict[str, str]:
        return {"status": "alive"}

    @app.get("/health/ready")
    async def ready() -> Response:
        ready = getattr(app.state, "ready", False)
        return JSONResponse(
            {"status": "ready" if ready else "not_ready"}, status_code=200 if ready else 503
        )

    @app.get("/metrics")
    async def metrics() -> Response:
        return Response(generate_latest(), headers={"Content-Type": CONTENT_TYPE_LATEST})

    @app.post("/v1/capabilities/run", response_model=CapabilityResponse)
    async def run(request: Request, payload: CapabilityRequest) -> CapabilityResponse:
        fault = ""
        header = request.headers.get("x-velin-test-fault", "")
        if header and os.getenv("VELIN_TEST_MODE") != "1":
            raise CapabilityError("TEST_MODE_DISABLED", "Fault injection is disabled.", 403)
        if os.getenv("VELIN_TEST_MODE") == "1":
            fault = header or os.getenv("VELIN_TEST_FAULT", "")
            if fault not in FAULTS:
                raise CapabilityError("INVALID_TEST_FAULT", "Unknown test fault.", 422)
        if app.state.capacity.locked():
            raise CapabilityError("CAPACITY_EXCEEDED", "Capability concurrency limit reached.", 429)
        async with app.state.capacity:
            result: CapabilityResponse = await app.state.engine.run(payload, fault)
            return result

    return app


app = create_app()
