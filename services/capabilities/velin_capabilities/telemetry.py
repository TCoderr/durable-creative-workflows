import json
import logging
import os
from datetime import UTC, datetime
from typing import Any

from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from prometheus_client import Counter, Gauge, Histogram

TRACER = trace.get_tracer("velin.ai", "0.1.0")
PROVIDER_CALLS = Counter("velin_provider_calls", "Provider attempts", ["provider", "outcome"])
PROVIDER_LATENCY = Histogram("velin_provider_seconds", "Provider latency", ["provider"])
FALLBACKS = Counter("velin_provider_fallbacks", "Approved provider fallback")
CIRCUIT = Gauge("velin_provider_circuit_open", "Provider circuit open", ["provider"])
SCHEMA_FAILURES = Counter("velin_schema_failures", "Rejected structured outputs", ["capability"])
REPAIRS = Counter("velin_structured_repairs", "Bounded repair attempts", ["capability"])
TOOL_FAILURES = Counter("velin_tool_failures", "Tool blocked or failed", ["tool"])
CAPABILITY_LATENCY = Histogram(
    "velin_capability_seconds", "Capability execution latency", ["capability"]
)
EVALS = Counter("velin_evaluation_checks", "Deterministic evaluation outcomes", ["name", "passed"])
TOKENS = Counter("velin_provider_tokens", "Provider reported tokens only", ["provider", "kind"])


def setup_tracing() -> TracerProvider:
    provider = TracerProvider(
        resource=Resource.create(
            {
                "service.name": "velin-ai",
                "service.version": os.getenv("APP_VERSION", "dev"),
                "deployment.environment.name": os.getenv("APP_ENV", "local"),
            }
        )
    )
    if os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT"):
        provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    trace.set_tracer_provider(provider)
    return provider


def event(name: str, **safe_fields: Any) -> None:
    # Callers supply only identifiers, classifications and numeric measures. Never bodies.
    span = trace.get_current_span().get_span_context()
    logging.getLogger("velin.ai").info(
        json.dumps(
            {
                "timestamp": datetime.now(UTC).isoformat(),
                "level": "info",
                "service": "velin-ai",
                "environment": os.getenv("APP_ENV", "local"),
                "version": os.getenv("APP_VERSION", "dev"),
                "event": name,
                "trace_id": f"{span.trace_id:032x}",
                **safe_fields,
            }
        )
    )
