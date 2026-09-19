"""Validate references and exact Go route coverage; --standards also runs OpenAPI validation."""

import argparse
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def validate(document):
    references = 0
    identifiers = set()
    operations = set()

    def visit(value):
        nonlocal references
        if isinstance(value, dict):
            if "$ref" in value:
                reference = value["$ref"]
                assert reference.startswith("#/"), "Only self-contained local references expected"
                target = document
                for key in reference[2:].split("/"):
                    target = target[key.replace("~1", "/").replace("~0", "~")]
                references += 1
            for child in value.values():
                visit(child)
        elif isinstance(value, list):
            for child in value:
                visit(child)

    assert document["openapi"].startswith("3.1.")
    visit(document)
    for path, item in document["paths"].items():
        for method, operation in item.items():
            if method not in {"get", "post", "put", "patch", "delete", "head", "options"}:
                continue
            identifier = operation["operationId"]
            assert identifier not in identifiers, "Duplicate operationId"
            identifiers.add(identifier)
            operations.add((method.upper(), path))
            assert operation["responses"], "Responses required"
            declared = {p["name"] for p in operation.get("parameters", []) if p["in"] == "path" and p.get("required") is True}
            assert declared == set(re.findall(r"\{([^}]+)\}", path)), "Path parameter mismatch"
    return {"operations": len(operations), "resolved_local_references": references}, operations


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--standards", action="store_true", help="Require full OpenAPI standards validation")
    args = parser.parse_args()
    results = {}
    for name in ["openapi.json", "capabilities-openapi.json"]:
        document = json.loads((ROOT / "docs/api" / name).read_text("utf-8"))
        summary, operations = validate(document)
        if args.standards:
            from openapi_spec_validator import validate as validate_spec

            validate_spec(document)
            summary["standards_validator"] = "openapi-spec-validator: OK"
        if name == "openapi.json":
            source = (ROOT / "services/control/internal/platform/api.go").read_text("utf-8")
            actual = set(re.findall(r'Handle(?:Func)?\("(GET|POST) ([^\"]+)"', source))
            assert actual == operations, f"Go route drift: {actual ^ operations}"
            summary["exact_go_route_coverage"] = True
        results[name] = summary
    print(json.dumps({"passed": True, "documents": results}, indent=2))


if __name__ == "__main__":
    main()
