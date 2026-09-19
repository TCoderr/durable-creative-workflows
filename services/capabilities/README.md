# VELIN capability service

A stateless FastAPI service that turns one typed request into one typed proposal plus deterministic checks. It is called only by control-plane activities, holds no database credentials, and cannot approve, transition or finalize a commission.

## Contract

`POST /v1/capabilities/run` takes a `CapabilityRequest` (`step_id`, `commission_id`, `capability`, `brief`, `context`, `revision`) and returns a `CapabilityResponse` (`step_id`, `capability`, `output`, `provenance`, `tool_calls`, `evaluations`, `invocations`). Every capability has a strict Pydantic output schema in `velin_capabilities/schemas.py` and a versioned, hash-pinned prompt under `prompts/`. Unknown fields, unknown capabilities and oversized context are rejected. A failed call returns a safe error envelope with a known code and bounded attempt evidence, never an accepted output.

## Providers

`VELIN_PROVIDER=deterministic` (default) uses structural fixtures that report `live: false`; they establish structure, never creative quality, and never claim live model execution. `VELIN_PROVIDER=http` selects an operator-configured HTTPS chat-completions adapter (`LLM_ENDPOINT`, `LLM_API_KEY`, `LLM_MODEL`, optional `LLM_FALLBACK_*`). Live and fixture providers cannot be fallback peers. Two transient failures open a circuit for fifteen seconds with one half-open probe; one structured-output repair is permitted; the whole call is bounded by a 45-second deadline.

## Faults

With `VELIN_TEST_MODE=1` the header `X-Velin-Test-Fault` (or `VELIN_TEST_FAULT`) injects `malformed_once`, `malformed_always`, `primary_timeout`, `primary_failure`, `force_revision`, `tool_failure`, `unauthorized_tool`, `slow_activity`, `prompt_injection` or `regressed`. Test mode refuses `APP_ENV=production`.

## Checks

```sh
uv sync --frozen
uv run ruff check velin_capabilities tests
uv run ruff format --check velin_capabilities tests
uv run mypy velin_capabilities
uv run pytest -q
uv run python -m velin_capabilities.eval_cli              # structural corpus gate, exit 0
uv run python -m velin_capabilities.eval_cli --regressed  # must exit 1
```
