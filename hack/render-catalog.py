#!/usr/bin/env python3
"""Build self-contained Tekton YAML from platform-owned scripts and definitions."""
import argparse
import copy
import os
from pathlib import Path
import subprocess
import tempfile

import yaml

ROOT = Path(__file__).resolve().parents[1]
LOCK = yaml.safe_load((ROOT / "catalog-lock.yaml").read_text())
RUNNER = LOCK["task-runner"]
TA = LOCK["trusted-artifacts"]
AUTH = "/var/run/byob-registry-auth"


class Dumper(yaml.SafeDumper):
    def ignore_aliases(self, data):
        return True


def represent_string(dumper, value):
    return dumper.represent_scalar("tag:yaml.org,2002:str", value, style="|" if "\n" in value else None)


Dumper.add_representer(str, represent_string)


def script(name):
    text = (ROOT / "scripts" / name).read_text()
    return text.replace("from filesystem import check_tree, safe_relative", (ROOT / "scripts/filesystem.py").read_text())


def env(**values):
    return [{"name": name, "value": value} for name, value in values.items()]


def param(name, description, default=None, type="string"):
    result = {"name": name, "type": type, "description": description}
    if default is not None:
        result["default"] = default
    return result


def mounts(auth=False):
    result = [{"name": "workdir", "mountPath": "/var/workdir"}]
    if auth:
        result.append({"name": "registry-auth", "mountPath": AUTH, "readOnly": True})
    return result


def task(name, description):
    return {"apiVersion": "tekton.dev/v1", "kind": "Task", "metadata": {"name": name, "labels": {"app.kubernetes.io/version": "0.1.0"}, "annotations": {"tekton.dev/pipelines.minVersion": "1.16.0"}}, "spec": {
        "description": description,
        "params": [], "results": [],
        "stepTemplate": {"securityContext": {"runAsUser": 1000, "runAsGroup": 0, "runAsNonRoot": True, "allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}, "seccompProfile": {"type": "RuntimeDefault"}}},
        "volumes": [{"name": "workdir", "emptyDir": {}}, {"name": "registry-auth", "secret": {"secretName": "byob-registry-auth", "optional": True, "items": [{"key": ".dockerconfigjson", "path": "config.json"}]}}], "steps": []}}


def run_build():
    result = task("run-build", "Execute a repository script in a non-root developer image and export the modified source as a Trusted Artifact.")
    spec = result["spec"]
    spec["params"] = [param("image", "Digest-pinned developer image, with /bin/sh and the script interpreter.", RUNNER), param("script-path", "Executable script path relative to the source tree.", "build.sh"), param("SOURCE_ARTIFACT", "Source Trusted Artifact from the preceding task."), param("ociStorage", "Platform-owned OCI repository (without a tag) for handoffs."), param("ociArtifactExpiresAfter", "Optional Trusted Artifact retention period.", "")]
    spec["results"] = [{"name": "SOURCE_ARTIFACT", "type": "string", "description": "Modified source tree.", "value": "$(steps.export.results.SOURCE_ARTIFACT)"}]
    spec["steps"] = [
        {"name": "restore", "image": TA, "args": ["use", "$(params.SOURCE_ARTIFACT)=/var/workdir/source"], "volumeMounts": mounts(True), "env": env(AUTHFILE=f"{AUTH}/config.json")},
        {"name": "prepare", "image": RUNNER, "script": script("validate-source.py"), "env": env(SOURCE_ROOT="/var/workdir/source", MODE="prepare", SCRIPT_PATH="$(params.script-path)"), "volumeMounts": mounts()},
        {"name": "build", "image": "$(params.image)", "command": ["/bin/sh"], "args": ["-eu", "-c", 'mkdir -p "$HOME"; cd source; exec "./$1"', "run-build", "$(params.script-path)"], "workingDir": "/var/workdir", "env": env(HOME="/tmp/byob-home"), "volumeMounts": mounts(), "computeResources": {"requests": {"cpu": "1", "memory": "1Gi"}, "limits": {"memory": "2Gi"}}},
        {"name": "validate-output", "image": RUNNER, "script": script("validate-source.py"), "env": env(SOURCE_ROOT="/var/workdir/source", MODE="export"), "volumeMounts": mounts()},
        {"name": "export", "image": TA, "args": ["create", "--store", "$(params.ociStorage):$(context.taskRun.name)", "$(step.results.SOURCE_ARTIFACT.path)=/var/workdir/source"], "results": [{"name": "SOURCE_ARTIFACT", "type": "string"}], "env": env(AUTHFILE=f"{AUTH}/config.json", IMAGE_EXPIRES_AFTER="$(params.ociArtifactExpiresAfter)"), "volumeMounts": mounts(True)},
    ]
    return result


def pack():
    result = task("pack", "Pack one or several build outputs into an OCI artifact and attach its SPDX SBOM.")
    spec = result["spec"]
    spec["params"] = [param("SOURCE_ARTIFACT", "Trusted source trees containing out/.", type="array"), param("IMAGE", "Tagged destination for the component build artifact."), param("artifactType", "OCI artifactType for the documented output format.", "application/vnd.konflux.build-output.v1"), param("layout", "Platform-owned output layout: flat or platform.", "flat")]
    results = [{"name": name, "type": "string"} for name in ("IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL")]
    spec["results"] = [{**item, "value": f"$(steps.publish.results.{item['name']})"} for item in results]
    spec["steps"] = [
        {"name": "restore", "image": TA, "script": script("restore-package.sh"), "args": ["$(params.SOURCE_ARTIFACT[*])"], "env": env(AUTHFILE=f"{AUTH}/config.json"), "volumeMounts": mounts(True)},
        {"name": "prepare", "image": RUNNER, "script": script("prepare-package.py"), "env": env(INPUT_ROOT="/var/workdir/inputs", PACKAGE_ROOT="/var/workdir/package", ARCHIVE_PATH="/var/workdir/payload.tar.gz", LAYOUT="$(params.layout)"), "volumeMounts": mounts()},
        {"name": "sbom", "image": RUNNER, "command": ["syft"], "args": ["dir:/var/workdir/package", "-o", "spdx-json=/var/workdir/sbom.spdx.json"], "volumeMounts": mounts()},
        {"name": "publish", "image": RUNNER, "workingDir": "/var/workdir", "script": script("publish-package.sh"), "env": env(IMAGE="$(params.IMAGE)", ARTIFACT_TYPE="$(params.artifactType)", REGISTRY_AUTH_DIR=AUTH), "volumeMounts": mounts(True), "results": results},
    ]
    return result


def bindings(**values):
    return [{"name": key, "value": value} for key, value in values.items()]


def pipeline(name, remote=False, matrix=False, chained=False):
    checkout = {"name": "checkout", "taskRef": {"resolver": "bundles", "params": bindings(bundle=LOCK["checkout-bundle"], name="git-clone-oci-ta", kind="task")}, "params": bindings(url="$(params.git-url)", revision="$(params.revision)", ociStorage="$(params.oci-storage):$(context.pipelineRun.name)-checkout")}
    build = {"name": "build", "taskRef": {"name": "run-build-remote" if remote else "run-build"}, "params": bindings(image=RUNNER, **{"script-path": "build.sh", "SOURCE_ARTIFACT": "$(tasks.checkout.results.SOURCE_ARTIFACT)", "ociStorage": "$(params.oci-storage)"})}
    tasks = [checkout]
    if chained:
        codegen = copy.deepcopy(build)
        codegen["name"] = "codegen"
        codegen["params"][1]["value"] = "scripts/generate.sh"
        tasks.append(codegen)
        build["params"][2]["value"] = "$(tasks.codegen.results.SOURCE_ARTIFACT)"
    if matrix:
        build["matrix"] = {"params": bindings(PLATFORM=["linux/amd64", "linux/arm64"])}
    elif remote:
        build["params"].append({"name": "PLATFORM", "value": "linux/arm64"})
    tasks.append(build)
    source = "$(tasks.build.results.SOURCE_ARTIFACT[*])" if matrix else "$(tasks.build.results.SOURCE_ARTIFACT)"
    tasks.append({"name": "pack", "taskRef": {"name": "pack"}, "params": bindings(SOURCE_ARTIFACT=[source], IMAGE="$(params.output-image)", artifactType="application/vnd.konflux.build-output.v1", layout="platform" if matrix else "flat")})
    return {"apiVersion": "tekton.dev/v1", "kind": "Pipeline", "metadata": {"name": name}, "spec": {"description": "BYOB checkout, script execution, and OCI packaging using Trusted Artifacts.", "params": [param("git-url", "Source repository URL."), param("revision", "Exact reviewed source commit."), param("output-image", "Tagged OCI output destination."), param("oci-storage", "Platform-owned Trusted Artifact repository, without tag.")], "results": [{"name": n, "value": f"$(tasks.pack.results.{n})"} for n in ("IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL")] + [{"name": "CHAINS-GIT_URL", "value": "$(tasks.checkout.results.CHAINS-GIT_URL)"}, {"name": "CHAINS-GIT_COMMIT", "value": "$(tasks.checkout.results.CHAINS-GIT_COMMIT)"}], "tasks": tasks}}


def write(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("# Generated by hack/render-catalog.py; do not edit.\n" + yaml.dump(data, Dumper=Dumper, sort_keys=False, width=100))


def generate(destination):
    for name, definition in (("run-build", run_build()), ("pack", pack())):
        write(destination / "task" / name / "0.1" / f"{name}.yaml", definition)
    subprocess.run(["go", "run", ".", "--task", str(destination / "task/run-build/0.1/run-build.yaml"), "--remote-task", str(destination / "task/run-build-remote/0.1/run-build-remote.yaml"), "--scripts", str(ROOT / "scripts")], cwd=ROOT / "task-generator/remote", env={**os.environ, "GOCACHE": os.environ.get("GOCACHE", "/tmp/byob-go-cache")}, check=True)
    for name, flags in (("byob", {}), ("byob-remote", {"remote": True}), ("byob-matrix", {"remote": True, "matrix": True}), ("byob-chained", {"chained": True})):
        write(destination / "pipelines" / f"{name}.yaml", pipeline(name, **flags))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if not args.check:
        generate(ROOT)
        return
    with tempfile.TemporaryDirectory() as temporary:
        destination = Path(temporary)
        generate(destination)
        stale = [str(path.relative_to(destination)) for path in destination.rglob("*.yaml") if not (ROOT / path.relative_to(destination)).exists() or path.read_bytes() != (ROOT / path.relative_to(destination)).read_bytes()]
        if stale:
            parser.exit(1, "Generated catalog is stale: " + ", ".join(stale) + "\n")


if __name__ == "__main__":
    main()
