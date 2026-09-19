#!/usr/bin/env python3
"""Bind final Trivy reports and CycloneDX SBOMs to actual local image IDs."""

import hashlib
import json
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REPORTS = ROOT / ".local/security"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inspect(image):
    return json.loads(subprocess.check_output(["docker", "image", "inspect", image], text=True))[0]


def main():
    if len(sys.argv) < 2:
        raise SystemExit("Pass the exact final image references scanned by scan-images.sh.")
    scanner = inspect("velin-trivy:0.74.0-security.1")
    evidence = {
        "timestamp_utc": datetime.now(timezone.utc).isoformat(),
        "scanner": {"name": "Trivy", "version": "0.74.0-velin-security.1", "image_id": scanner["Id"],
                    "repo_digests": scanner.get("RepoDigests", [])},
        "database": json.loads((ROOT / ".local/trivy-cache/db/metadata.json").read_text()),
        "severity_gate": ["HIGH", "CRITICAL"],
        "suppressed_findings": 0,
        "limits": ["Image package scanning is not a reachability or penetration test.",
                   "Raw reports and SBOMs are retained under ignored .local/security; hashes bind this summary to them.",
                   "No cloud deployment or live external model execution is established by image scanning."],
        "images": [],
    }
    for image in sys.argv[1:]:
        prefix = re.sub(r"[/@:]", "_", image)
        report_path = REPORTS / f"{prefix}.vulnerabilities.json"
        sbom_path = REPORTS / f"{prefix}.sbom.cdx.json"
        log_path = REPORTS / f"{prefix}.scan.log"
        exit_code = int((REPORTS / f"{prefix}.scan-exit-code").read_text().strip())
        sbom_exit_code = int((REPORTS / f"{prefix}.sbom-exit-code").read_text().strip())
        report, sbom = json.loads(report_path.read_text()), json.loads(sbom_path.read_text())
        image_id = inspect(image)["Id"]
        properties = sbom["metadata"]["component"]["properties"]
        sbom_id = next(p["value"] for p in properties if p["name"] == "aquasecurity:trivy:ImageID")
        if not (image_id == report["Metadata"]["ImageID"] == sbom_id == (REPORTS / f"{prefix}.image-id").read_text().strip()):
            raise RuntimeError(f"Stale image/report/SBOM identity: {image}")
        if sbom.get("bomFormat") != "CycloneDX":
            raise RuntimeError(f"Missing real CycloneDX document: {image}")
        components = sbom.get("components", [])
        findings = [{"target": result["Target"], "id": vuln["VulnerabilityID"],
                     "severity": vuln["Severity"], "package": vuln["PkgName"],
                     "installed": vuln["InstalledVersion"], "fixed": vuln.get("FixedVersion", "")}
                    for result in report.get("Results", []) for vuln in result.get("Vulnerabilities", [])]
        counts = {level: sum(item["severity"] == level for item in findings) for level in ("HIGH", "CRITICAL")}
        warnings = sorted({line.split("\tWARN\t", 1)[1][:600] for line in log_path.read_text().splitlines()
                           if "\tWARN\t" in line})
        evidence["images"].append({"reference": image, "image_id": image_id,
                                  "scan_created_at": report["CreatedAt"], "counts": counts,
                                  "scan_exit_code": exit_code,
                                  "sbom_exit_code": sbom_exit_code,
                                  "gate": "PASS" if exit_code == sbom_exit_code == 0 and not any(counts.values()) else "FAIL",
                                  "scanner_warnings": warnings,
                                  "scan_log": str(log_path.relative_to(ROOT)), "scan_log_sha256": digest(log_path),
                                  "report": str(report_path.relative_to(ROOT)), "report_sha256": digest(report_path),
                                  "sbom": str(sbom_path.relative_to(ROOT)), "sbom_sha256": digest(sbom_path),
                                  "sbom_created_at": sbom["metadata"]["timestamp"],
                                  "sbom_component_count": len(components),
                                  "inventory_coverage": "PACKAGE_INVENTORY" if components else "CONTAINER_METADATA_ONLY",
                                  "coverage_warning": None if components else
                                  "Scanner recognized no OS/package database or language packages; an empty finding list is not complete package vulnerability coverage.",
                                  "findings": findings})
    evidence["status"] = "PASS" if all(item["gate"] == "PASS" for item in evidence["images"]) else "FAIL"
    evidence["all_images_have_package_inventory"] = all(item["sbom_component_count"] > 0 for item in evidence["images"])
    target = REPORTS / "security-verification.json"
    target.write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps({"status": evidence["status"], "image_count": len(evidence["images"]), "evidence": str(target)}, indent=2))
    raise SystemExit(0 if evidence["status"] == "PASS" else 1)


if __name__ == "__main__":
    main()
