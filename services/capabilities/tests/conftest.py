from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient

from velin_capabilities.app import create_app
from velin_capabilities.engine import Engine
from velin_capabilities.providers import DeterministicProvider, Router
from velin_capabilities.schemas import Brief, CapabilityRequest

TOKEN = "test-only-internal-token-0000000000000000"  # noqa: S105


@pytest.fixture
def request_data() -> CapabilityRequest:
    return CapabilityRequest(
        step_id="commission-1:art_direction:0",
        commission_id="commission-1",
        capability="art_direction",
        brief=Brief(
            title="Editorial commission",
            objective="Create an editorial direction for a cultural journal.",
            audience="Curious readers",
            constraints=["Clear reading hierarchy"],
            prohibited=["neon gradients"],
        ),
    )


@pytest.fixture
def engine() -> Engine:
    return Engine(
        Router(DeterministicProvider(), DeterministicProvider("deterministic-fallback"), 0)
    )


@pytest.fixture
def client(monkeypatch: pytest.MonkeyPatch) -> Iterator[TestClient]:
    monkeypatch.setenv("CAPABILITIES_TOKEN", TOKEN)
    monkeypatch.setenv("VELIN_TEST_MODE", "1")
    monkeypatch.setenv("VELIN_PROVIDER", "deterministic")
    with TestClient(create_app()) as session:
        session.headers["Authorization"] = "Bearer " + TOKEN
        yield session
