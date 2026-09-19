import asyncio
import json
import time
from dataclasses import dataclass
from typing import Any, Protocol
from urllib.parse import urlparse

import httpx

from velin_capabilities.errors import ProviderError
from velin_capabilities.fixtures import produce
from velin_capabilities.prompts import Prompt
from velin_capabilities.schemas import CapabilityRequest, Invocation, Source
from velin_capabilities.telemetry import (
    CIRCUIT,
    FALLBACKS,
    PROVIDER_CALLS,
    PROVIDER_LATENCY,
    TRACER,
)


@dataclass
class ModelResult:
    content: str
    provider: str
    model: str
    live: bool
    input_tokens: int | None = None
    output_tokens: int | None = None


class Provider(Protocol):
    name: str
    model: str
    live: bool

    async def generate(
        self,
        request: CapabilityRequest,
        prompt: Prompt,
        sources: list[Source],
        schema: dict[str, Any],
        repair: bool,
        fault: str,
    ) -> ModelResult: ...


class DeterministicProvider:
    live = False

    def __init__(self, name: str = "deterministic-primary") -> None:
        self.name = name
        self.model = "velin-structural-fixture-v1"
        self.calls = 0

    async def generate(
        self,
        request: CapabilityRequest,
        prompt: Prompt,
        sources: list[Source],
        schema: dict[str, Any],
        repair: bool,
        fault: str,
    ) -> ModelResult:
        self.calls += 1
        if self.name == "deterministic-primary":
            if fault == "primary_timeout":
                raise ProviderError("PROVIDER_TIMEOUT", True)
            if fault == "primary_failure":
                raise ProviderError("PROVIDER_UNAVAILABLE", True)
        if fault == "slow_activity" and request.capability == "strategy":
            await asyncio.sleep(25)  # Deliberate crash-injection window, test-mode only.
        if fault == "malformed_always" or (fault == "malformed_once" and not repair):
            return ModelResult('{"invalid":', self.name, self.model, False)
        model = "velin-regressed-fixture-v1" if fault == "regressed" else self.model
        return ModelResult(json.dumps(produce(request, sources, fault)), self.name, model, False)


class HTTPProvider:
    """Explicit OpenAI-compatible chat-completions contract, arbitrary configured provider host.

    Endpoint and credentials are operator configuration, never model/user input. Redirects are
    forbidden. No SDK-specific workflow coupling and no automatic fixture fallback for live calls.
    """

    live = True

    def __init__(self, endpoint: str, key: str, model: str, name: str = "configured-http") -> None:
        parsed = urlparse(endpoint)
        if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password:
            raise ValueError("Live provider endpoint requires HTTPS without URL credentials")
        if not key or not model:
            raise ValueError("Live provider key and model are required")
        self.endpoint, self.key, self.model, self.name = endpoint, key, model, name

    async def generate(
        self,
        request: CapabilityRequest,
        prompt: Prompt,
        sources: list[Source],
        schema: dict[str, Any],
        repair: bool,
        fault: str,
    ) -> ModelResult:
        brief_data = request.brief.model_dump()
        brief_data["brand_memory"] = [
            item.model_dump() for item in request.brief.brand_memory if item.approved
        ]
        body = {
            "model": self.model,
            "messages": [
                {
                    "role": "system",
                    "content": prompt.content
                    + (
                        " Previous output failed schema validation."
                        " Return only one valid JSON object."
                        if repair
                        else ""
                    ),
                },
                {
                    "role": "user",
                    "content": json.dumps(
                        {
                            "untrusted_brief": brief_data,
                            "untrusted_context": request.context,
                            "untrusted_sources": [s.model_dump() for s in sources],
                            "revision": request.revision,
                        }
                    ),
                },
            ],
            "response_format": {
                "type": "json_schema",
                "json_schema": {
                    "name": request.capability,
                    "strict": True,
                    "schema": schema,
                },
            },
            "max_completion_tokens": 3500,
        }
        try:
            async with httpx.AsyncClient(
                timeout=10, follow_redirects=False, trust_env=False
            ) as client:
                async with client.stream(
                    "POST",
                    self.endpoint,
                    json=body,
                    headers={"Authorization": f"Bearer {self.key}"},
                ) as response:
                    if response.status_code == 408:
                        raise ProviderError("PROVIDER_TIMEOUT", True)
                    if response.status_code == 429:
                        raise ProviderError("PROVIDER_RATE_LIMIT", True)
                    if response.status_code >= 500:
                        raise ProviderError("PROVIDER_UNAVAILABLE", True)
                    if response.status_code != 200:
                        raise ProviderError("PROVIDER_REJECTED", False)
                    raw = bytearray()
                    async for chunk in response.aiter_bytes():
                        raw.extend(chunk)
                        if len(raw) > 262144:
                            raise ProviderError("PROVIDER_RESPONSE_TOO_LARGE", False)
            parsed = json.loads(raw)
            content = parsed["choices"][0]["message"]["content"]
            if not isinstance(content, str):
                raise ValueError("content is not text")
            usage = parsed.get("usage", {})
            actual_model = parsed.get("model", self.model)
            if not isinstance(usage, dict) or not isinstance(actual_model, str):
                raise ValueError("provider metadata shape is invalid")
            if not 1 <= len(actual_model) <= 300:
                raise ValueError("provider model metadata exceeds limit")
            for kind in ("prompt_tokens", "completion_tokens"):
                count = usage.get(kind)
                if count is not None and (type(count) is not int or count < 0):
                    raise ValueError("provider token metadata is invalid")
            return ModelResult(
                content,
                self.name,
                actual_model,
                True,
                usage.get("prompt_tokens"),
                usage.get("completion_tokens"),
            )
        except httpx.TimeoutException as exc:
            raise ProviderError("PROVIDER_TIMEOUT", True) from exc
        except httpx.HTTPError as exc:
            raise ProviderError("PROVIDER_NETWORK", True) from exc
        except (ValueError, KeyError, TypeError, IndexError) as exc:
            raise ProviderError("PROVIDER_ENVELOPE_INVALID", False) from exc


class CircuitBreaker:
    def __init__(self, threshold: int = 2, cooldown: float = 15.0) -> None:
        self.threshold, self.cooldown = threshold, cooldown
        self.failures = 0
        self.opened_at: float | None = None
        self.probe_in_flight = False

    def allow(self, now: float) -> bool:
        if self.opened_at is None:
            return True
        if now - self.opened_at < self.cooldown or self.probe_in_flight:
            return False
        self.probe_in_flight = True
        return True

    def success(self) -> None:
        self.failures, self.opened_at, self.probe_in_flight = 0, None, False

    def failure(self, now: float) -> None:
        self.failures += 1
        self.probe_in_flight = False
        if self.failures >= self.threshold:
            self.opened_at = now


class Router:
    def __init__(self, primary: Provider, fallback: Provider | None, backoff: float = 0.1) -> None:
        if fallback and fallback.live != primary.live:
            raise ValueError("Live and fixture providers cannot be fallback peers")
        self.primary, self.fallback, self.backoff = primary, fallback, backoff
        self.circuits = {
            provider.name: CircuitBreaker() for provider in [primary, fallback] if provider
        }

    async def generate(
        self,
        request: CapabilityRequest,
        prompt: Prompt,
        sources: list[Source],
        schema: dict[str, Any],
        repair: bool,
        fault: str,
        invocations: list[Invocation],
    ) -> ModelResult:
        last = ProviderError("PROVIDER_CIRCUIT_OPEN", True)
        for provider_index, provider in enumerate([self.primary, self.fallback]):
            if provider is None:
                continue
            if provider_index:
                FALLBACKS.inc()
            circuit = self.circuits[provider.name]
            for attempt in range(1, 3):
                if not circuit.allow(time.monotonic()):
                    CIRCUIT.labels(provider=provider.name).set(1)
                    invocations.append(
                        Invocation(
                            provider=provider.name,
                            model=provider.model,
                            attempt=attempt,
                            repair=repair,
                            outcome="PROVIDER_CIRCUIT_OPEN",
                            latency_ms=0.0,
                            live=provider.live,
                        )
                    )
                    break
                started = time.monotonic()
                outcome = "success"
                with TRACER.start_as_current_span("provider.invoke") as span:
                    span.set_attribute("gen_ai.provider.name", provider.name)
                    span.set_attribute("gen_ai.request.model", provider.model)
                    span.set_attribute("velin.fixture", not provider.live)
                    try:
                        result = await provider.generate(
                            request, prompt, sources, schema, repair, fault
                        )
                        circuit.success()
                        CIRCUIT.labels(provider=provider.name).set(0)
                        return result
                    except ProviderError as exc:
                        outcome, last = exc.code, exc
                        if not exc.retryable:
                            circuit.probe_in_flight = False
                            raise
                        circuit.failure(time.monotonic())
                    except asyncio.CancelledError:
                        outcome = "PROVIDER_CANCELLED"
                        circuit.probe_in_flight = False
                        raise
                    finally:
                        elapsed = time.monotonic() - started
                        PROVIDER_CALLS.labels(provider=provider.name, outcome=outcome).inc()
                        PROVIDER_LATENCY.labels(provider=provider.name).observe(elapsed)
                        invocations.append(
                            Invocation(
                                provider=provider.name,
                                model=provider.model,
                                attempt=attempt,
                                repair=repair,
                                outcome=outcome,
                                latency_ms=elapsed * 1000,
                                live=provider.live,
                            )
                        )
                        if outcome != "success":
                            last.invocations = invocations[-8:]
                if attempt < 2:
                    await asyncio.sleep(self.backoff * 2 ** (attempt - 1))
        last.invocations = invocations[-8:]
        raise last
