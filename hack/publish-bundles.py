#!/usr/bin/env python3
"""Explicit operator command: publish Task bundles and write their immutable refs."""
import argparse
from pathlib import Path
import re
import subprocess
import uuid

import yaml

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--registry-prefix", required=True, help="Writable repository prefix, for example quay.io/my-org/byob")
    parser.add_argument("--output", type=Path, required=True, help="Bundle lock file for render-run.py")
    args = parser.parse_args()
    bundles = {}
    tag = "0.1-" + uuid.uuid4().hex
    for name in ("run-build", "run-build-remote", "pack"):
        reference = f"{args.registry_prefix.rstrip('/')}/{name}:{tag}"
        result = subprocess.run(["tkn", "--no-color", "bundle", "push", reference, "-f", str(ROOT / "task" / name / "0.1" / f"{name}.yaml")], check=True, capture_output=True, text=True)
        # Capture the digest from the actual push, never a later lookup of a tag.
        digests = re.findall(r"sha256:[a-f0-9]{64}", result.stdout + result.stderr)
        if not digests:
            raise RuntimeError(f"tkn did not report the pushed bundle digest for {name}")
        bundles[name] = reference + "@" + digests[-1]
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(yaml.safe_dump(bundles, sort_keys=False))


if __name__ == "__main__":
    main()
