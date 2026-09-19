import json
from typing import Any

import httpx
import pytest

from velin_capabilities.errors import ProviderError
from velin_capabilities.prompts import load_prompt
from velin_capabilities.providers import HTTPProvider
from velin_capabilities.schemas import CapabilityRequest, Memory
from velin_capabilities.tools import SOURCES


def mock_client(monkeypatch: pytest.MonkeyPatch, handler: Any) -> None:
    client = httpx.AsyncClient
    monkeypatch.setattr(
        httpx,
        "AsyncClient",
        lambda **kwargs: client(transport=httpx.MockTransport(handler), **kwargs),
    )


async def test_adapter_contract_and_accounting(
    monkeypatch: pytest.MonkeyPatch, request_data: CapabilityRequest
) -> None:
    request_data.brief.brand_memory = [
        Memory(id="memory-1", source="decision:1", value="UNAPPROVED-SENTINEL", approved=False)
    ]
    captured: list[dict[str, Any]] = []

    def provider(request: httpx.Request) -> httpx.Response:
        captured.append(json.loads(request.content))
        return httpx.Response(
            200,
            json={
                "model": "actual-deployment-version",
                "choices": [{"message": {"content": '{"direction":"fixture"}'}}],
                "usage": {"prompt_tokens": 18, "completion_tokens": 7},
            },
        )

    mock_client(monkeypatch, provider)
    adapter = HTTPProvider("https://provider.example/v1/chat/completions", "fake-key", "requested")
    result = await adapter.generate(
        request_data, load_prompt("art_direction"), SOURCES, {}, False, ""
    )
    assert result.model == "actual-deployment-version"
    assert result.input_tokens == 18 and result.output_tokens == 7
    assert captured[0]["response_format"]["type"] == "json_schema"
    assert "UNAPPROVED-SENTINEL" not in json.dumps(captured)
    assert "tools" not in captured[0]
    assert "fake-key" not in json.dumps(captured)


@pytest.mark.parametrize(
    "status,code,retryable",
    [
        (401, "PROVIDER_REJECTED", False),
        (302, "PROVIDER_REJECTED", False),
        (408, "PROVIDER_TIMEOUT", True),
        (429, "PROVIDER_RATE_LIMIT", True),
        (503, "PROVIDER_UNAVAILABLE", True),
    ],
)
async def test_http_failure_classification(
    monkeypatch: pytest.MonkeyPatch,
    request_data: CapabilityRequest,
    status: int,
    code: str,
    retryable: bool,
) -> None:
    mock_client(monkeypatch, lambda request: httpx.Response(status, text="sensitive upstream body"))
    adapter = HTTPProvider("https://provider.example/v1/chat/completions", "fake-key", "model")
    with pytest.raises(ProviderError) as exc:
        await adapter.generate(request_data, load_prompt("art_direction"), [], {}, False, "")
    assert exc.value.code == code and exc.value.retryable == retryable
    assert "sensitive" not in str(exc.value)


async def test_real_timeout_exception_classified(
    monkeypatch: pytest.MonkeyPatch, request_data: CapabilityRequest
) -> None:
    def timeout(request: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("provider timed out", request=request)

    mock_client(monkeypatch, timeout)
    adapter = HTTPProvider("https://provider.example/v1/chat/completions", "fake-key", "model")
    with pytest.raises(ProviderError) as exc:
        await adapter.generate(request_data, load_prompt("art_direction"), [], {}, False, "")
    assert exc.value.code == "PROVIDER_TIMEOUT"


@pytest.mark.parametrize("usage", [{"prompt_tokens": -1}, {"completion_tokens": "20"}, []])
async def test_invalid_provider_accounting_rejected(
    monkeypatch: pytest.MonkeyPatch,
    request_data: CapabilityRequest,
    usage: Any,
) -> None:
    mock_client(
        monkeypatch,
        lambda request: httpx.Response(
            200,
            json={
                "choices": [{"message": {"content": "{}"}}],
                "usage": usage,
            },
        ),
    )
    adapter = HTTPProvider("https://provider.example/v1/chat/completions", "fake-key", "model")
    with pytest.raises(ProviderError) as exc:
        await adapter.generate(request_data, load_prompt("art_direction"), [], {}, False, "")
    assert exc.value.code == "PROVIDER_ENVELOPE_INVALID"
    assert exc.value.retryable is False
