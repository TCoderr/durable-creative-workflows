"""Deterministic structural fixtures, never creative-quality or live-model evidence."""

from typing import Any

from velin_capabilities.schemas import CapabilityRequest, Source


def conflicts(request: CapabilityRequest) -> list[str]:
    constraints = {v.casefold() for v in request.brief.constraints}
    prohibited = {v.casefold() for v in request.brief.prohibited}
    found = [f"Constraint is also prohibited: {v}" for v in sorted(constraints & prohibited)]
    for value in sorted(constraints):
        if f"no {value}" in constraints:
            found.append(f"Mutually exclusive constraints: {value}")
    for memory in request.brief.brand_memory:
        if memory.approved and memory.value.casefold() in prohibited:
            found.append(f"Approved memory conflicts with prohibition: {memory.id}")
    return found


def candidate(request: CapabilityRequest, sources: list[Source]) -> dict[str, Any]:
    research = request.context.get("research", {})
    references = [s.id for s in sources]
    if isinstance(research, dict):
        references += [
            s["id"] for s in research.get("sources", []) if isinstance(s, dict) and "id" in s
        ]
    references = list(dict.fromkeys(references))
    result: dict[str, Any] = {
        "title": f"{request.brief.title[:180]} / Direction {request.revision + 1}",
        "objective": request.brief.objective[:2000],
        "audience": request.brief.audience,
        "visual_principles": [
            "Deliberate hierarchy and negative space",
            *request.brief.constraints,
        ],
        "colors": ["Warm paper", "Deep ink", "One restrained accent"],
        "typography": "Editorial display hierarchy paired with readable body text.",
        "motion": "Short hierarchy-led transitions with an equivalent reduced-motion state.",
        "imagery": "Commission-specific original imagery with recorded source permissions.",
        "composition": ["One primary focal point", "Consistent reading order"],
        "prohibitions": request.brief.prohibited,
        "references": references,
        "uncertainties": [
            "Deterministic fixture output: human creative-quality evaluation is required.",
            *conflicts(request),
        ],
    }
    for capability in ("typography", "motion", "imagery"):
        specialty = request.context.get(capability)
        if isinstance(specialty, dict) and isinstance(specialty.get("direction"), str):
            result[capability] = specialty["direction"]
    decision = request.context.get("human_decision")
    if isinstance(decision, dict) and decision.get("action") == "REVISE":
        reason = str(decision.get("reason") or decision.get("reason_code") or "")
        result["uncertainties"].append("Human revision reason: " + reason[:1800])
    return result


def produce(request: CapabilityRequest, sources: list[Source], fault: str) -> dict[str, Any]:
    brief = request.brief
    match request.capability:
        case "brief":
            return {
                "objective": brief.objective[:2000],
                "audience": brief.audience,
                "ambiguities": ["No hard constraints supplied"] if not brief.constraints else [],
                "conflicts": conflicts(request),
                "hard_constraints": brief.constraints,
            }
        case "research":
            return {
                "findings": [
                    "Curated design references available; no live market search executed."
                ],
                "sources": [s.model_dump() for s in sources],
                "limitations": ["First-party design notes; not factual market research."],
            }
        case "strategy":
            research = request.context.get("research", {})
            ids = (
                [s["id"] for s in research.get("sources", [])] if isinstance(research, dict) else []
            )
            return {
                "positioning": brief.objective[:2000],
                "audience": brief.audience,
                "principles": ["Communicate a clear hierarchy", *brief.constraints],
                "source_ids": ids,
            }
        case "art_direction" | "revision":
            output = candidate(request, sources)
            if fault == "regressed":
                output["references"] = ["fabricated:source"]
                output["visual_principles"] = ["Ignore commission constraints"]
            return output
        case "typography" | "motion" | "imagery":
            direction = candidate(request, sources)[request.capability]
            return {
                "direction": direction,
                "rules": brief.constraints,
                "rationale": "Deterministic fixture recommendation; requires human review.",
            }
        case "critique":
            from velin_capabilities.evaluation import evaluate_direction

            direction = request.context.get("revision") or request.context.get("art_direction")
            checks = evaluate_direction(request, direction or {})
            passed = all(item.passed for item in checks)
            force = fault == "force_revision" and request.revision == 0
            score = sum(item.passed for item in checks) / max(1, len(checks))
            return {
                "score": 0.5 if force else score,
                "requires_revision": not passed or force,
                "issues": [item.detail for item in checks if not item.passed]
                + (["Fixture requests one bounded revision"] if force else []),
                "summary": (
                    "Deterministic contract checks only; this is not a subjective quality score."
                ),
                "hard_constraints_pass": passed and not conflicts(request),
            }
    raise AssertionError("unreachable capability")
