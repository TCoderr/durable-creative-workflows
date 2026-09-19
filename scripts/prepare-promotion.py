"""Validate a reviewed immutable release manifest; never deploy or rebuild."""

import json
import os
import re
from pathlib import Path

root = Path(__file__).resolve().parents[1]
environment = os.environ["TARGET_ENVIRONMENT"]
if environment not in {"dev", "staging", "prod"}:
    raise SystemExit("Invalid target environment")
source = (root / os.environ["RELEASE_MANIFEST"]).resolve()
if not source.is_relative_to(root / "infra" / "releases") or source.suffix != ".json":
    raise SystemExit("Release manifests must be reviewed JSON files under infra/releases")
data = json.loads(source.read_text())
if not re.fullmatch(r"[a-f0-9]{40}", data.get("commit", "")):
    raise SystemExit("A real source commit is required")
if set(data.get("images", {})) != {"control", "capabilities", "frontend", "nats"}:
    raise SystemExit("Exactly the four platform images must be supplied")
for image in data["images"].values():
    if not re.fullmatch(r"[a-z0-9][a-z0-9./_-]+@sha256:[a-f0-9]{64}", image):
        raise SystemExit("Every image must name an immutable registry digest")
order = {"dev": None, "staging": "dev", "prod": "staging"}
required_previous = order[environment]
if required_previous:
    previous = data.get("verified_environments", {}).get(required_previous, {})
    if previous.get("images") != data["images"] or not previous.get("evidence_uri"):
        raise SystemExit("Previous environment must verify these exact image digests")
output = root / ".local" / "promotion.json"
output.parent.mkdir(exist_ok=True)
output.write_text(json.dumps({"target": environment, "release": data}, indent=2) + "\n")
print("Immutable promotion candidate validated; deployment requires operator review.")
