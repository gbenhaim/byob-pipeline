#!/usr/bin/env python3
"""Render a committed PaC PipelineRun using actually published Task bundles."""
import argparse
from pathlib import Path
import re
import sys

import yaml

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pipeline", choices=["byob", "byob-remote", "byob-matrix", "byob-chained"], default="byob")
    parser.add_argument("--bundles", type=Path, required=True)
    parser.add_argument("--output-image", required=True)
    parser.add_argument("--oci-storage", required=True)
    parser.add_argument("--platform", action="append", help="Written as a literal into the committed PipelineRun.")
    args = parser.parse_args()
    bundles = yaml.safe_load(args.bundles.read_text())
    pipeline = yaml.safe_load((ROOT / "pipelines" / f"{args.pipeline}.yaml").read_text())
    spec = pipeline["spec"]
    for task in spec["tasks"]:
        ref = task["taskRef"]
        if "name" in ref:
            name = ref["name"]
            bundle = bundles.get(name, "")
            if not re.fullmatch(r"[^\s]+@sha256:[a-f0-9]{64}", bundle):
                parser.error(f"a published digest-pinned bundle is required for {name}")
            task["taskRef"] = {"resolver": "bundles", "params": [{"name": "bundle", "value": bundle}, {"name": "name", "value": name}, {"name": "kind", "value": "task"}]}
        if task["name"] == "build" and args.platform:
            if "matrix" in task:
                task["matrix"]["params"][0]["value"] = args.platform
            elif args.pipeline == "byob-remote" and len(args.platform) == 1:
                next(p for p in task["params"] if p["name"] == "PLATFORM")["value"] = args.platform[0]
            else:
                parser.error("--platform requires byob-matrix or one platform with byob-remote")
    run = {"apiVersion": "tekton.dev/v1", "kind": "PipelineRun", "metadata": {"generateName": args.pipeline + "-", "labels": {"pipelines.appstudio.openshift.io/type": "build"}, "annotations": {"pipelinesascode.tekton.dev/on-event": '["push"]', "pipelinesascode.tekton.dev/on-target-branch": '["main"]'}}, "spec": {"taskRunTemplate": {"serviceAccountName": "byob-build"}, "params": [{"name": "git-url", "value": "{{repo_url}}"}, {"name": "revision", "value": "{{revision}}"}, {"name": "output-image", "value": args.output_image}, {"name": "oci-storage", "value": args.oci_storage}], "pipelineSpec": spec}}
    sys.stdout.write(yaml.safe_dump(run, sort_keys=False, width=100))


if __name__ == "__main__":
    main()
