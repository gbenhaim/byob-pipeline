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

from task_helpers import ROOT, step, task

RUNNER = step("pack", "prepare")["image"]
TRUSTED_ARTIFACTS = step("run-build", "restore")["image"]
BUILDER = next(p["default"] for p in task("run-build")["spec"]["params"] if p["name"] == "image")


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
            (work / "source/private-source.txt").write_text("source must never be republished")
            (work / "inputs").mkdir()
            (work / "pack-inputs/0").mkdir(parents=True)
            common = ["podman", "run", "--rm", "--network", network, "--userns", "keep-id:uid=1000,gid=0", "--user", "1000:0", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", f"{work}:/var/workdir:Z"]
            for image in set((BUILDER, RUNNER, TRUSTED_ARTIFACTS)):
                run(["podman", "pull", "--quiet", image])
            build = step("run-build", "build")
            artifact = common + ["-e", "ORAS_OPTIONS=--plain-http", TRUSTED_ARTIFACTS]
            # Model checkout's source artifact once, before any developer code runs.
            run(artifact + ["create", "--store", "registry:5000/byob:checkout", "/var/workdir/checkout.ref=/var/workdir/source"])
            checkout = (work / "checkout.ref").read_text()

            def build_script(path):
                args = [value.replace("$(params.script-path)", path) for value in build["args"]]
                run(common + ["--workdir", build["workingDir"], "-e", "HOME=/tmp/byob-home", "-e", "BUILD_INPUTS=/var/workdir/inputs", "--entrypoint", build["command"][0], BUILDER] + args)

            def stage_outputs(name):
                staged = work / name
                staged.mkdir()
                stage_mount = ["-v", f"{staged}:/var/build-output:Z"]
                run(common + stage_mount + ["-e", "SOURCE_ROOT=/var/workdir/source", "-e", "ARTIFACT_ROOT=/var/build-output", "--entrypoint", "python3", RUNNER, "-c", step("run-build", "validate-output")["script"]])
                assert sorted(p.name for p in staged.iterdir()) == ["out"]
                return stage_mount

            def export_outputs(name):
                stage_mount = stage_outputs(name)
                args = [value.replace("$(params.ociStorage)", "registry:5000/byob").replace("$(context.taskRun.name)", name).replace("$(step.results.BUILD_ARTIFACT.path)", f"/var/workdir/{name}.ref") for value in step("run-build", "export")["args"]]
                export_mount = ["-v", stage_mount[1].removesuffix(":Z") + ":ro,Z"]
                run(common + export_mount + ["-e", "ORAS_OPTIONS=--plain-http", TRUSTED_ARTIFACTS] + args)
                return (work / f"{name}.ref").read_text()

            print("Testing chained builds with a fresh checkout and output-only handoffs", flush=True)
            build_script("scripts/generate.sh")
            codegen = export_outputs("codegen")
            shutil.rmtree(work / "source")
            run(artifact + ["use", f"{checkout}=/var/workdir/source"])
            assert not (work / "source/out").exists(), "checkout must not include preceding build output"
            run(common + ["-e", "ORAS_OPTIONS=--plain-http", "-e", f"BUILD_ARTIFACT={codegen}", "--entrypoint", "/bin/bash", TRUSTED_ARTIFACTS, "-c", step("run-build", "restore-inputs")["script"]])
            assert sorted(p.name for p in (work / "inputs").iterdir()) == ["out"]
            assert (work / "inputs/out/generated.txt").read_text() == "Generated from reviewed source\n"
            build_script("build.sh")
            reference = export_outputs("build")
            run(artifact + ["use", f"{reference}=/var/workdir/pack-inputs/0"])
            assert sorted(p.name for p in (work / "pack-inputs/0").iterdir()) == ["out"]
            assert (work / "pack-inputs/0/out/generated.txt").read_bytes() == (work / "inputs/out/generated.txt").read_bytes()
            run(["python3", "-c", step("pack", "prepare")["script"]], env={**os.environ, "INPUT_ROOT": str(work / "pack-inputs"), "PACKAGE_ROOT": str(work / "package"), "ARCHIVE_PATH": str(work / "payload.tar.gz"), "LAYOUT": "flat"})
            run(common + ["--entrypoint", "syft", RUNNER, "dir:/var/workdir/package", "-o", "spdx-json=/var/workdir/sbom.spdx.json"])
            print("Publishing the payload and SBOM to the disposable HTTP registry", flush=True)
            script = step("pack", "publish")["script"]
            # Only the test registry transport changes; production keeps TLS enabled.
            script = script.replace("oras push", "oras push --plain-http").replace("cosign attach sbom", "cosign attach sbom --allow-http-registry")
            for name in ("IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL"):
                script = script.replace(f"$(step.results.{name}.path)", f"/var/workdir/{name}")
            (work / "publish.sh").write_text(script)
            run(common + ["--workdir", "/var/workdir", "-e", "IMAGE=registry:5000/byob:output", "-e", "ARTIFACT_TYPE=application/vnd.konflux.build-output.v1", "-e", "REGISTRY_AUTH_DIR=/no-auth", "--entrypoint", "/bin/bash", RUNNER, "/var/workdir/publish.sh"])
            digest = (work / "IMAGE_DIGEST").read_text()
            manifest = run(common + ["--entrypoint", "oras", RUNNER, "manifest", "fetch", "--plain-http", f"registry:5000/byob@{digest}"], capture_output=True)
            assert json.loads(manifest.stdout)["artifactType"] == "application/vnd.konflux.build-output.v1"
            with tarfile.open(work / "payload.tar.gz") as archive:
                assert archive.extractfile("hello.txt").read().startswith(b"Built on ")
                assert sorted(archive.getnames()) == ["generated.txt", "hello.txt"]
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
            with (work / "source/build.sh").open("a") as script:
                script.write("printf changed > private-source.txt\nprintf extra > remote-source-change.txt\n")
            remote_env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "SSH_DIR": str(ssh_dir), "SOURCE_ROOT": str(work / "source"), "INPUT_ROOT": str(work / "inputs"), "CONTROL_ROOT": str(work / "control"), "BUILDER_IMAGE": BUILDER, "SCRIPT_PATH": "build.sh"}
            remote_command = ["bash", "-c", step("run-build-remote", "build-remote")["script"]]
            run(remote_command, env=remote_env)
            assert (work / "source/out/hello.txt").read_text().startswith("Built on ")
            assert (work / "source/out/generated.txt").read_text() == "Generated from reviewed source\n"
            assert (work / "source/private-source.txt").read_text() == "source must never be republished"
            assert not (work / "source/remote-source-change.txt").exists()
            assert "@sha256:" in (work / "control/builder-image").read_text()
            assert not list(remote_root.iterdir()), "remote scratch directory leaked"
            export_outputs("remote-build")
            (work / "source/build.sh").write_text("#!/bin/sh\nrm -rf out\nln -s . out\n")
            rejected = subprocess.run(remote_command, env=remote_env, capture_output=True, text=True)
            assert rejected.returncode != 0, "symlinked output must not be published"
            assert "out/ must be a real directory" in rejected.stderr, rejected.stderr
            assert not list(remote_root.iterdir()), "invalid output leaked remote scratch directory"
            (work / "source/build.sh").write_text("#!/bin/sh\nexit 17\n")
            failed = subprocess.run(remote_command, env=remote_env)
            assert failed.returncode != 0, "failed remote script reported success"
            assert not list(remote_root.iterdir()), "failed build leaked remote scratch directory"
            print("Container smoke tests passed", flush=True)
    finally:
        subprocess.run(["podman", "rm", "-f", registry], stdout=subprocess.DEVNULL)
        subprocess.run(["podman", "network", "rm", network], stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
