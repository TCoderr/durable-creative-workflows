"""Deterministic structural regression gate; never a subjective creative quality benchmark."""

import argparse
import asyncio
import json
from pathlib import Path
from typing import Any

from pydantic import ValidationError

from velin_capabilities.engine import Engine
from velin_capabilities.providers import DeterministicProvider, Router
from velin_capabilities.schemas import CapabilityRequest

CORPUS = Path(__file__).resolve().parent.parent / "evals" / "corpus.json"


async def run_corpus(regressed: bool = False) -> dict[str, Any]:
    corpus = json.loads(CORPUS.read_text("utf-8"))
    results = []
    for case in corpus["cases"]:
        engine = Engine(
            Router(DeterministicProvider(), DeterministicProvider("deterministic-fallback"), 0)
        )
        payload = {
            "step_id": f"eval-{case['id']}",
            "commission_id": f"eval-{case['id']}",
            "capability": "research",
            "brief": {
                key: case[key]
                for key in [
                    "title",
                    "objective",
                    "audience",
                    "constraints",
                    "prohibited",
                    "brand_memory",
                ]
                if key in case
            },
            "context": {"research": "x" * 65537} if case.get("oversized") else {},
        }
        response = None
        try:
            request = CapabilityRequest.model_validate(payload)
            research = await engine.run(request, case.get("fault", ""))
            request = CapabilityRequest.model_validate(
                {**payload, "capability": "art_direction", "context": {"research": research.output}}
            )
            response = await engine.run(request, "regressed" if regressed else "")
            actual = "eligible" if all(e.passed for e in response.evaluations) else "requires_human"
            if case.get("fault") == "prompt_injection":
                if [tool.tool for tool in research.tool_calls] != ["curated_research"]:
                    actual = "unauthorized_tool"
        except ValidationError:
            actual = "invalid_request"
        results.append(
            {
                "id": case["id"],
                "expected": case["expected"],
                "actual": actual,
                "passed": actual == case["expected"],
                "checks": [item.model_dump() for item in response.evaluations] if response else [],
                "prompt_hash": response.provenance.prompt_hash if response else None,
            }
        )
    return {
        "corpus_version": corpus["version"],
        "configuration": "regressed" if regressed else "baseline",
        "provider": "deterministic-primary",
        "model": "velin-regressed-fixture-v1" if regressed else "velin-structural-fixture-v1",
        "live": False,
        "deterministic": {
            "cases": results,
            "passed": sum(item["passed"] for item in results),
            "total": len(results),
        },
        "model_based": {"executed": False, "scores": [], "reason": "No live judge executed."},
        "gate_passed": all(item["passed"] for item in results),
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--regressed", action="store_true")
    parser.add_argument("--output", type=Path)
    arguments = parser.parse_args()
    result = asyncio.run(run_corpus(arguments.regressed))
    content = json.dumps(result, indent=2) + "\n"
    if arguments.output:
        arguments.output.write_text(content, encoding="utf-8")
    print(content)
    raise SystemExit(0 if result["gate_passed"] else 1)


if __name__ == "__main__":
    main()
