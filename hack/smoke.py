#!/usr/bin/env python3
"""Opt-in local containers: real Trusted Artifacts, script execution, and packaging."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import uuid

import yaml

ROOT = Path(__file__).resolve().parents[1]
LOCK = yaml.safe_load((ROOT / "catalog-lock.yaml").read_text())


def run(command, **kwargs):
    return subprocess.run(command, check=True, text=True, **kwargs)


def main():
    network = "byob-smoke-" + uuid.uuid4().hex[:12]
    registry = network + "-registry"
    run(["podman", "network", "create", network], stdout=subprocess.DEVNULL)
    try:
        run(["podman", "run", "-d", "--name", registry, "--network", network, "--network-alias", "registry", "docker.io/library/registry:2"], stdout=subprocess.DEVNULL)
        with tempfile.TemporaryDirectory(prefix="byob-smoke-") as temporary:
            work = Path(temporary)
            shutil.copytree(ROOT / "examples/component", work / "source")
            (work / "inputs/0").mkdir(parents=True)
            common = ["podman", "run", "--rm", "--network", network, "--userns", "keep-id:uid=1000,gid=0", "--user", "1000:0", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", f"{work}:/var/workdir:Z"]
            for image in (LOCK["task-runner"], LOCK["trusted-artifacts"]):
                run(["podman", "pull", "--quiet", image])
            print("Running the default developer image as UID 1000", flush=True)
            local_task = yaml.safe_load((ROOT / "task/run-build/0.1/run-build.yaml").read_text())
            build = next(step for step in local_task["spec"]["steps"] if step["name"] == "build")
            args = [value.replace("$(params.script-path)", "build.sh") for value in build["args"]]
            run(common + ["--workdir", build["workingDir"], "-e", "HOME=/tmp/byob-home", "--entrypoint", build["command"][0], LOCK["task-runner"]] + args)
            print("Round-tripping source through the real Trusted Artifacts tools", flush=True)
            artifact = common + ["-e", "ORAS_OPTIONS=--plain-http", LOCK["trusted-artifacts"]]
            run(artifact + ["create", "--store", "registry:5000/byob:source", "/var/workdir/source.ref=/var/workdir/source"])
            reference = (work / "source.ref").read_text()
            run(artifact + ["use", f"{reference}=/var/workdir/inputs/0"])
            assert (work / "inputs/0/out/hello.txt").read_bytes() == (work / "source/out/hello.txt").read_bytes()
            run(["python3", str(ROOT / "scripts/prepare-package.py")], env={**os.environ, "INPUT_ROOT": str(work / "inputs"), "PACKAGE_ROOT": str(work / "package"), "ARCHIVE_PATH": str(work / "payload.tar.gz"), "LAYOUT": "flat"})
            run(common + ["--entrypoint", "syft", LOCK["task-runner"], "dir:/var/workdir/package", "-o", "spdx-json=/var/workdir/sbom.spdx.json"])
            print("Publishing the payload and SBOM to the disposable HTTP registry", flush=True)
            script = (ROOT / "scripts/publish-package.sh").read_text()
            # Only the test registry transport changes; production keeps TLS enabled.
            script = script.replace("oras push", "oras push --plain-http").replace("cosign attach sbom", "cosign attach sbom --allow-http-registry")
            for name in ("IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL"):
                script = script.replace(f"$(step.results.{name}.path)", f"/var/workdir/{name}")
            (work / "publish.sh").write_text(script)
            run(common + ["--workdir", "/var/workdir", "-e", "IMAGE=registry:5000/byob:output", "-e", "ARTIFACT_TYPE=application/vnd.konflux.build-output.v1", "-e", "REGISTRY_AUTH_DIR=/no-auth", "--entrypoint", "/bin/bash", LOCK["task-runner"], "/var/workdir/publish.sh"])
            digest = (work / "IMAGE_DIGEST").read_text()
            manifest = run(common + ["--entrypoint", "oras", LOCK["task-runner"], "manifest", "fetch", "--plain-http", f"registry:5000/byob@{digest}"], capture_output=True)
            assert json.loads(manifest.stdout)["artifactType"] == "application/vnd.konflux.build-output.v1"
            with tarfile.open(work / "payload.tar.gz") as archive:
                assert archive.extractfile("hello.txt").read().startswith(b"Built on ")
            print("Testing remote source transfer with real rootless Podman and a local SSH transport fixture", flush=True)
            remote_root = work / "vm"
            remote_root.mkdir()
            ssh_dir = work / "ssh"
            ssh_dir.mkdir()
            (ssh_dir / "host").write_text("builder@fixture.invalid")
            (ssh_dir / "id_rsa").write_text("transport-fixture-key")
            (ssh_dir / "user-dir").write_text(str(remote_root))
            bin_dir = work / "bin"
            bin_dir.mkdir()
            transport = bin_dir / "ssh"
            transport.write_text("#!/bin/bash\nset -eu\nwhile [[ $1 != builder@fixture.invalid ]]; do shift; done\nshift\nexec bash -c \"$1\"\n")
            transport.chmod(0o755)
            remote_env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "SSH_DIR": str(ssh_dir), "SOURCE_ROOT": str(work / "source"), "CONTROL_ROOT": str(work / "control"), "BUILDER_IMAGE": LOCK["task-runner"], "SCRIPT_PATH": "build.sh"}
            run(["bash", str(ROOT / "scripts/run-build-remote.sh")], env=remote_env)
            assert (work / "source/out/hello.txt").read_text().startswith("Built on ")
            assert "@sha256:" in (work / "control/builder-image").read_text()
            assert not list(remote_root.iterdir()), "remote scratch directory leaked"
            (work / "source/build.sh").write_text("#!/bin/sh\nexit 17\n")
            failed = subprocess.run(["bash", str(ROOT / "scripts/run-build-remote.sh")], env=remote_env)
            assert failed.returncode != 0, "failed remote script reported success"
            assert not list(remote_root.iterdir()), "failed build leaked remote scratch directory"
            print("Container smoke tests passed", flush=True)
    finally:
        subprocess.run(["podman", "rm", "-f", registry], stdout=subprocess.DEVNULL)
        subprocess.run(["podman", "network", "rm", network], stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
