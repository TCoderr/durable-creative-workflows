import logging

import httpx
import pytest
from fastapi.testclient import TestClient

from velin_capabilities.engine import Engine
from velin_capabilities.providers import HTTPProvider, Router
from velin_capabilities.schemas import CapabilityRequest


def test_health_auth_and_contract(client: TestClient, request_data: CapabilityRequest) -> None:
    assert client.get("/health/live").status_code == 200
    assert client.get("/health/ready").status_code == 200
    assert (
        client.post(
            "/v1/capabilities/run",
            json=request_data.model_dump(),
            headers={"Authorization": "Bearer wrong"},
        ).status_code
        == 401
    )
    response = client.post("/v1/capabilities/run", json=request_data.model_dump())
    assert response.status_code == 200
    assert response.json()["provenance"]["live"] is False
    assert response.headers["Cache-Control"] == "no-store"
    assert "velin_provider_calls_total" in client.get("/metrics").text


def test_validation_and_body_limit(client: TestClient) -> None:
    response = client.post("/v1/capabilities/run", json={"brief": "secret payload"})
    assert response.status_code == 422
    assert set(response.json()) == {"code", "message", "traceId"}
    assert "secret payload" not in response.text
    assert client.post("/v1/capabilities/run", content="x" * 131073).status_code == 413


def test_fault_security_and_failure_contract(
    client: TestClient, request_data: CapabilityRequest, monkeypatch: pytest.MonkeyPatch
) -> None:
    response = client.post(
        "/v1/capabilities/run",
        json=request_data.model_dump(),
        headers={"X-Velin-Test-Fault": "malformed_always"},
    )
    assert response.status_code == 422
    assert response.json()["code"] == "STRUCTURED_OUTPUT_INVALID"
    monkeypatch.setenv("VELIN_TEST_MODE", "0")
    assert (
        client.post(
            "/v1/capabilities/run",
            json=request_data.model_dump(),
            headers={"X-Velin-Test-Fault": "malformed_once"},
        ).status_code
        == 403
    )


def test_sensitive_payload_not_logged(
    client: TestClient, request_data: CapabilityRequest, caplog: pytest.LogCaptureFixture
) -> None:
    request_data.brief.objective = "Private brand secret SENTINEL-NOT-FOR-LOGS"
    with caplog.at_level(logging.INFO):
        assert (
            client.post("/v1/capabilities/run", json=request_data.model_dump()).status_code == 200
        )
    assert "SENTINEL-NOT-FOR-LOGS" not in caplog.text
    assert "test-only-internal-token" not in caplog.text
    assert "capability_completed" in caplog.text


@pytest.mark.parametrize(
    "fault,outcome,tool",
    [
        ("tool_failure", "failed", "curated_research"),
        ("unauthorized_tool", "blocked", "unauthorized"),
    ],
)
def test_failed_tool_evidence_is_safe_and_not_accepted(
    client: TestClient,
    request_data: CapabilityRequest,
    fault: str,
    outcome: str,
    tool: str,
) -> None:
    request_data.capability = "research"
    request_data.brief.title = "PRIVATE-TOOL-ARGUMENT-SENTINEL"
    original = request_data.model_dump()
    response = client.post(
        "/v1/capabilities/run", json=original, headers={"X-Velin-Test-Fault": fault}
    )
    assert response.status_code in {422, 503}
    body = response.json()
    assert "output" not in body and "provenance" not in body
    assert "PRIVATE-TOOL-ARGUMENT-SENTINEL" not in response.text
    assert "blocked-fixture" not in response.text
    assert len(body["tool_calls"]) == 1
    record = body["tool_calls"][0]
    assert record["tool"] == tool and record["outcome"] == outcome
    assert len(record["id"]) <= 128 and len(record["arguments_hash"]) == 64
    assert record["source_ids"] == [] and record["latency_ms"] >= 0
    assert request_data.model_dump() == original


def test_exhausted_provider_failure_preserves_safe_attempts(
    client: TestClient,
    request_data: CapabilityRequest,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    original_client = httpx.AsyncClient
    transport = httpx.MockTransport(
        lambda request: httpx.Response(
            408,
            text="PRIVATE-UPSTREAM-ERROR-SENTINEL",
        )
    )
    monkeypatch.setattr(
        httpx,
        "AsyncClient",
        lambda **kwargs: original_client(
            transport=transport,
            **kwargs,
        ),
    )
    client.app.state.engine = Engine(
        Router(  # type: ignore[attr-defined]
            HTTPProvider("https://primary.example/v1/chat/completions", "fake-key-1", "primary"),
            HTTPProvider(
                "https://fallback.example/v1/chat/completions",
                "fake-key-2",
                "fallback",
                "configured-http-fallback",
            ),
            0,
        )
    )
    response = client.post("/v1/capabilities/run", json=request_data.model_dump())
    assert response.status_code == 503
    body = response.json()
    assert body["code"] == "PROVIDER_TIMEOUT"
    assert len(body["invocations"]) == 4
    assert all(item["outcome"] == "PROVIDER_TIMEOUT" for item in body["invocations"])
    assert {item["provider"] for item in body["invocations"]} == {
        "configured-http",
        "configured-http-fallback",
    }
    assert "PRIVATE-UPSTREAM-ERROR-SENTINEL" not in response.text
    assert "fake-key" not in response.text
    assert "output" not in body and "provenance" not in body
