import hashlib
import json
from pathlib import Path

from pydantic import Field

from velin_capabilities.errors import CapabilityError
from velin_capabilities.schemas import Capability, StrictModel

PROMPT_ROOT = Path(__file__).resolve().parent.parent / "prompts"


class Prompt(StrictModel):
    id: str
    version: str
    capability: Capability
    schema_version: str
    created_at: str
    status: str
    content: str
    content_hash: str = Field(pattern=r"^[a-f0-9]{64}$")


def load_prompt(capability: str, root: Path = PROMPT_ROOT) -> Prompt:
    if capability not in {
        "brief",
        "research",
        "strategy",
        "art_direction",
        "typography",
        "motion",
        "imagery",
        "critique",
        "revision",
    }:
        raise CapabilityError("PROMPT_NOT_FOUND", "No approved prompt for capability.", 422)
    try:
        prompt = Prompt.model_validate(
            json.loads((root / f"{capability}.v1.json").read_text("utf-8"))
        )
        manifest = json.loads((root / "manifest.json").read_text("utf-8"))
        digest = hashlib.sha256(prompt.content.encode()).hexdigest()
        if (
            digest != prompt.content_hash
            or manifest[capability] != digest
            or prompt.capability != capability
            or prompt.status != "active"
        ):
            raise ValueError("prompt integrity mismatch")
    except (OSError, ValueError, KeyError) as exc:
        raise CapabilityError(
            "PROMPT_REGISTRY_INVALID", "Prompt registry failed validation."
        ) from exc
    return prompt
