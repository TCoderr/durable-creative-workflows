import json
from typing import Annotated, Any, Literal

from pydantic import BaseModel, ConfigDict, Field, StringConstraints, model_validator

Capability = Literal[
    "brief",
    "research",
    "strategy",
    "art_direction",
    "typography",
    "motion",
    "imagery",
    "critique",
    "revision",
]
Text = Annotated[str, StringConstraints(min_length=1, max_length=2000, strip_whitespace=True)]
ShortText = Annotated[str, StringConstraints(min_length=1, max_length=300, strip_whitespace=True)]
Texts = Annotated[list[Text], Field(max_length=30)]
Identifier = Annotated[str, StringConstraints(pattern=r"^[a-zA-Z0-9_.:-]{1,128}$")]


class StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)


class Memory(StrictModel):
    id: Identifier
    source: ShortText
    value: Annotated[str, StringConstraints(min_length=1, max_length=500)]
    approved: bool


class Brief(StrictModel):
    title: Annotated[str, StringConstraints(min_length=3, max_length=160, strip_whitespace=True)]
    objective: Annotated[
        str, StringConstraints(min_length=15, max_length=2000, strip_whitespace=True)
    ]
    audience: Annotated[str, StringConstraints(min_length=3, max_length=300, strip_whitespace=True)]
    constraints: list[Annotated[str, StringConstraints(min_length=1, max_length=500)]] = Field(
        default_factory=list,
        max_length=20,
    )
    prohibited: list[Annotated[str, StringConstraints(min_length=1, max_length=500)]] = Field(
        default_factory=list,
        max_length=20,
    )
    brand_memory: Annotated[list[Memory], Field(max_length=20)] = Field(default_factory=list)


class CapabilityRequest(StrictModel):
    step_id: Identifier
    commission_id: Identifier
    capability: Capability
    brief: Brief
    context: dict[str, Any] = Field(default_factory=dict)
    revision: int = Field(default=0, ge=0, le=5)

    @model_validator(mode="after")
    def bounded_context(self) -> "CapabilityRequest":
        if len(json.dumps(self.context, ensure_ascii=False).encode()) > 65536:
            raise ValueError("context exceeds 64 KiB")
        permitted = {
            "brief",
            "research",
            "strategy",
            "art_direction",
            "typography",
            "motion",
            "imagery",
            "critique",
            "revision",
            "human_decision",
            "review_round",
        }
        if self.context.keys() - permitted:
            raise ValueError("unknown context capabilities")
        for capability, value in self.context.items():
            if capability in OUTPUT_SCHEMAS:
                OUTPUT_SCHEMAS[capability].model_validate(value)
        if "human_decision" in self.context:
            HumanDecision.model_validate(self.context["human_decision"])
        return self


class HumanDecision(StrictModel):
    decision_id: Identifier
    approval_id: Identifier
    revision_id: Identifier
    action: Literal["APPROVE", "REVISE", "REJECT"]
    reason_code: ShortText
    reason: str = Field(max_length=2000)
    actor_id: Annotated[str, StringConstraints(min_length=1, max_length=256)] | None = None


class BriefAnalysis(StrictModel):
    objective: Text
    audience: ShortText
    ambiguities: Texts
    conflicts: Texts
    hard_constraints: Texts


class Source(StrictModel):
    id: Identifier
    title: ShortText
    url: ShortText
    excerpt: Text


class Research(StrictModel):
    findings: Texts
    sources: Annotated[list[Source], Field(max_length=10)]
    limitations: Texts


class Strategy(StrictModel):
    positioning: Text
    audience: ShortText
    principles: Texts
    source_ids: Annotated[list[Identifier], Field(max_length=10)]


class CreativeDirection(StrictModel):
    title: ShortText
    objective: Text
    audience: ShortText
    visual_principles: Annotated[list[Text], Field(min_length=1, max_length=30)]
    colors: Annotated[list[ShortText], Field(min_length=1, max_length=10)]
    typography: Text
    motion: Text
    imagery: Text
    composition: Annotated[list[Text], Field(min_length=1, max_length=30)]
    prohibitions: Texts
    references: Annotated[list[Identifier], Field(max_length=20)]
    uncertainties: Texts


class Specialty(StrictModel):
    direction: Text
    rules: Texts
    rationale: Text


class Critique(StrictModel):
    score: float = Field(ge=0, le=1)
    requires_revision: bool
    issues: Texts
    summary: Text
    hard_constraints_pass: bool


OUTPUT_SCHEMAS: dict[str, type[StrictModel]] = {
    "brief": BriefAnalysis,
    "research": Research,
    "strategy": Strategy,
    "art_direction": CreativeDirection,
    "typography": Specialty,
    "motion": Specialty,
    "imagery": Specialty,
    "critique": Critique,
    "revision": CreativeDirection,
}


class Evaluation(StrictModel):
    name: ShortText
    kind: Literal["deterministic"] = "deterministic"
    passed: bool
    mandatory: bool = True
    score: float = Field(ge=0, le=1)
    detail: Text


class ToolCall(StrictModel):
    id: Identifier
    tool: ShortText
    arguments_hash: ShortText
    timestamp: ShortText
    outcome: Literal["success", "blocked", "failed"]
    latency_ms: float
    source_ids: list[Identifier]


class Invocation(StrictModel):
    provider: ShortText
    model: ShortText
    attempt: int
    repair: bool
    outcome: ShortText
    latency_ms: float
    live: bool


class Provenance(StrictModel):
    prompt_id: ShortText
    prompt_version: ShortText
    prompt_hash: ShortText
    schema_version: ShortText
    provider: ShortText
    model: ShortText
    live: bool
    input_tokens: int | None = None
    output_tokens: int | None = None
    cost_usd: float | None = None
    latency_ms: float


class CapabilityResponse(StrictModel):
    step_id: Identifier
    capability: Capability
    output: dict[str, Any]
    provenance: Provenance
    tool_calls: list[ToolCall]
    evaluations: list[Evaluation]
    invocations: list[Invocation]
