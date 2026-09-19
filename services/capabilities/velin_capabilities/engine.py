import asyncio
import time

from pydantic import ValidationError

from velin_capabilities.errors import CapabilityError
from velin_capabilities.evaluation import evaluate
from velin_capabilities.prompts import load_prompt
from velin_capabilities.providers import Router
from velin_capabilities.schemas import (
    OUTPUT_SCHEMAS,
    CapabilityRequest,
    CapabilityResponse,
    Invocation,
    Provenance,
    Source,
    ToolCall,
)
from velin_capabilities.telemetry import (
    CAPABILITY_LATENCY,
    EVALS,
    REPAIRS,
    SCHEMA_FAILURES,
    TOKENS,
    TRACER,
    event,
)
from velin_capabilities.tools import call_tool


class Engine:
    def __init__(self, router: Router) -> None:
        self.router = router

    async def run(self, request: CapabilityRequest, fault: str = "") -> CapabilityResponse:
        start = time.monotonic()
        tools: list[ToolCall] = []
        invocations: list[Invocation] = []
        with TRACER.start_as_current_span("capability." + request.capability) as span:
            span.set_attribute("velin.step_id", request.step_id)
            span.set_attribute("velin.commission_id", request.commission_id)
            try:
                async with asyncio.timeout(45):
                    return await self._run(request, fault, start, tools, invocations)
            except CapabilityError as exc:
                exc.tool_calls = (tools + exc.tool_calls)[-16:]
                exc.invocations = invocations[-8:]
                raise
            except TimeoutError as exc:
                raise CapabilityError(
                    "CAPABILITY_TIMEOUT",
                    "Capability execution deadline exceeded.",
                    tool_calls=tools,
                    invocations=invocations,
                ) from exc
            finally:
                CAPABILITY_LATENCY.labels(capability=request.capability).observe(
                    time.monotonic() - start
                )

    async def _run(
        self,
        request: CapabilityRequest,
        fault: str,
        start: float,
        tools: list[ToolCall],
        invocations: list[Invocation],
    ) -> CapabilityResponse:
        prompt = load_prompt(request.capability)
        sources: list[Source] = []
        if fault == "unauthorized_tool":
            call_tool(request.capability, request.step_id, "shell", {"command": "blocked-fixture"})
        if request.capability == "research":
            sources, record = call_tool(
                request.capability,
                request.step_id,
                "curated_research",
                {"topic": request.brief.title},
                fault=fault,
            )
            tools.append(record)
        schema = OUTPUT_SCHEMAS[request.capability]
        for repair in (False, True):
            result = await self.router.generate(
                request, prompt, sources, schema.model_json_schema(), repair, fault, invocations
            )
            try:
                if len(result.content.encode()) > 65536:
                    raise ValueError("model output too large")
                output = schema.model_validate_json(result.content).model_dump()
                break
            except (ValidationError, ValueError) as exc:
                SCHEMA_FAILURES.labels(capability=request.capability).inc()
                if repair:
                    event(
                        "capability_schema_rejected",
                        step_id=request.step_id,
                        capability=request.capability,
                    )
                    raise CapabilityError(
                        "STRUCTURED_OUTPUT_INVALID",
                        "Output failed schema validation after one repair.",
                        422,
                    ) from exc
                REPAIRS.labels(capability=request.capability).inc()
                event(
                    "capability_repair_requested",
                    step_id=request.step_id,
                    capability=request.capability,
                )
        else:
            raise AssertionError("bounded repair loop exhausted unexpectedly")
        checks = evaluate(request, output, sources)
        for item in checks:
            EVALS.labels(name=item.name, passed=str(item.passed).lower()).inc()
        # Preserve failed deterministic checks for critique/human escalation. They are evidence,
        # never a workflow authorization; the control plane gates finalization on mandatory checks.
        for kind, count in [("input", result.input_tokens), ("output", result.output_tokens)]:
            if count is not None:
                TOKENS.labels(provider=result.provider, kind=kind).inc(count)
        event(
            "capability_completed",
            step_id=request.step_id,
            capability=request.capability,
            provider=result.provider,
            model=result.model,
            live=result.live,
            checks_passed=all(item.passed for item in checks),
            attempts=len(invocations),
        )
        return CapabilityResponse(
            step_id=request.step_id,
            capability=request.capability,
            output=output,
            provenance=Provenance(
                prompt_id=prompt.id,
                prompt_version=prompt.version,
                prompt_hash=prompt.content_hash,
                schema_version=prompt.schema_version,
                provider=result.provider,
                model=result.model,
                live=result.live,
                input_tokens=result.input_tokens,
                output_tokens=result.output_tokens,
                cost_usd=None,
                latency_ms=(time.monotonic() - start) * 1000,
            ),
            tool_calls=tools,
            evaluations=checks,
            invocations=invocations,
        )
