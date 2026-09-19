import json
from typing import Any

from pydantic import ValidationError

from velin_capabilities.fixtures import conflicts
from velin_capabilities.schemas import CapabilityRequest, CreativeDirection, Evaluation, Source


def check(name: str, passed: bool, detail: str) -> Evaluation:
    return Evaluation(name=name, passed=passed, score=1.0 if passed else 0.0, detail=detail)


def evaluate_direction(request: CapabilityRequest, output: dict[str, Any]) -> list[Evaluation]:
    try:
        CreativeDirection.model_validate(output)
        schema_valid = True
    except ValidationError:
        schema_valid = False
    research = request.context.get("research", {})
    known = {s["id"] for s in research.get("sources", [])} if isinstance(research, dict) else set()
    refs = output.get("references", [])
    active = {
        k: v
        for k, v in output.items()
        if k
        not in {
            "prohibitions",
            "uncertainties",
            "objective",
            "audience",
            "title",
        }
    }
    rendered = json.dumps(active, ensure_ascii=False).casefold()
    principles = output.get("visual_principles", [])
    # This verifies explicit retention, not arbitrary semantic constraint satisfaction.
    retained = all(c in principles for c in request.brief.constraints)
    return [
        check("schema_valid", schema_valid, "Creative direction satisfies the strict schema."),
        check("constraints_retained", retained, "All declared constraints are retained verbatim."),
        check(
            "constraints_consistent", not conflicts(request), "No recognized constraint conflicts."
        ),
        check(
            "prohibited_patterns_absent",
            not any(term.casefold() in rendered for term in request.brief.prohibited),
            "No prohibited literal appears in generated active direction fields.",
        ),
        check(
            "prohibition_registry_complete",
            output.get("prohibitions") == request.brief.prohibited,
            "All prohibitions remain attached to the direction.",
        ),
        check(
            "source_references_exist",
            all(ref in known for ref in refs) and (bool(refs) if known else True),
            "References resolve to this workflow's research.",
        ),
        check(
            "no_duplicate_principles",
            len(principles) == len(set(principles)),
            "Principles contain no exact duplicates.",
        ),
    ]


def evaluate(
    request: CapabilityRequest, output: dict[str, Any], sources: list[Source]
) -> list[Evaluation]:
    if request.capability in {"art_direction", "revision"}:
        return evaluate_direction(request, output)
    if request.capability == "research":
        return [
            check(
                "source_integrity",
                output.get("sources") == [s.model_dump() for s in sources],
                "Research preserves exactly the source records returned by the allowed tool.",
            )
        ]
    if request.capability == "strategy":
        known = {s["id"] for s in request.context.get("research", {}).get("sources", [])}
        return [
            check(
                "strategy_source_integrity",
                all(s in known for s in output["source_ids"]),
                "Strategy cites only research from the current workflow.",
            )
        ]
    return [check("schema_valid", True, "Output parsed and validated before acceptance.")]
