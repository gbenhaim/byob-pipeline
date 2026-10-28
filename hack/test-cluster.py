#!/usr/bin/env python3
"""Run a catalog integration scenario in an explicitly selected test namespace."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import time

import yaml

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for option in ("context", "namespace", "git-url", "revision", "output-image", "oci-storage", "bundles"):
        parser.add_argument("--" + option, required=True)
    parser.add_argument("--pipeline", choices=["byob", "byob-remote", "byob-matrix", "byob-chained"], default="byob")
    parser.add_argument("--platform", action="append", default=[])
    parser.add_argument("--timeout", type=int, default=1800)
    args = parser.parse_args()
    for option in ("context", "namespace", "git_url", "revision", "output_image", "oci_storage", "bundles"):
        if not getattr(args, option).strip():
            parser.error(f"--{option.replace('_', '-')} must not be empty")
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    command = [sys.executable, str(ROOT / "hack/render-run.py"), "--pipeline", args.pipeline, "--bundles", args.bundles, "--output-image", args.output_image, "--oci-storage", args.oci_storage]
    for platform in args.platform:
        command += ["--platform", platform]
    rendered = subprocess.run(command, check=True, capture_output=True, text=True)
    run = yaml.safe_load(rendered.stdout)
    for param in run["spec"]["params"]:
        if param["name"] == "git-url":
            param["value"] = args.git_url
        elif param["name"] == "revision":
            param["value"] = args.revision
    run["metadata"].pop("annotations", None)
    kubectl = ["kubectl", "--context", args.context, "--namespace", args.namespace]
    created = subprocess.run(kubectl + ["create", "-f", "-", "-o", "json"], input=yaml.safe_dump(run), check=True, capture_output=True, text=True)
    name = json.loads(created.stdout)["metadata"]["name"]
    print(f"Created {args.namespace}/{name} in {args.context}", flush=True)
    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        response = subprocess.run(kubectl + ["get", "pipelinerun", name, "-o", "json"], check=True, capture_output=True, text=True)
        status = json.loads(response.stdout).get("status", {})
        condition = next((c for c in status.get("conditions", []) if c["type"] == "Succeeded"), {})
        if condition.get("status") == "False":
            parser.exit(1, f"{name} failed: {condition.get('message', '')}\n")
        if condition.get("status") == "True":
            results = {r["name"]: r["value"] for r in status.get("results", [])}
            if not all(results.get(key) for key in ("IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL", "CHAINS-GIT_COMMIT")):
                parser.exit(1, f"{name} is missing expected build results\n")
            print(json.dumps(results, indent=2))
            return
        time.sleep(5)
    parser.exit(1, f"Timed out; {name} was retained for investigation\n")


if __name__ == "__main__":
    main()
