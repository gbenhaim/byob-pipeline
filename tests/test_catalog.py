import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]


class CatalogTests(unittest.TestCase):
    def task(self, name):
        path = ROOT / "task" / name / "0.1" / f"{name}.yaml"
        self.assertTrue(path.exists(), f"missing Task: {path}")
        return yaml.safe_load(path.read_text())

    def test_local_developer_step_cannot_mount_credentials(self):
        task = self.task("run-build")["spec"]
        step = next(s for s in task["steps"] if s["name"] == "build")
        volumes = {v["name"]: v for v in task["volumes"]}
        mounts = task.get("stepTemplate", {}).get("volumeMounts", []) + step.get("volumeMounts", [])
        for mount in mounts:
            self.assertNotIn("secret", volumes[mount["name"]])
        self.assertEqual(step["image"], "$(params.image)")
        self.assertTrue(step["command"])
        self.assertFalse(task.get("workspaces"))
        self.assertFalse(task.get("sidecars"))
        context = task["stepTemplate"]["securityContext"]
        self.assertTrue(context["runAsNonRoot"])
        self.assertFalse(context["allowPrivilegeEscalation"])
        self.assertIn("ALL", context["capabilities"]["drop"])

    def test_generated_remote_routes_localhost_to_developer_image_without_ssh_secret(self):
        spec = self.task("run-build-remote")["spec"]
        steps = {s["name"]: s for s in spec["steps"]}
        self.assertEqual(steps["build-local"]["image"], "$(params.image)")
        self.assertNotEqual(steps["build-remote"]["image"], "$(params.image)")
        self.assertEqual(steps["build-local"]["when"][0]["values"], ["local"])
        self.assertEqual(steps["build-remote"]["when"][0]["values"], ["remote"])
        self.assertNotIn("ssh", [m["name"] for m in steps["build-local"]["volumeMounts"]])
        self.assertIn("ssh", [m["name"] for m in steps["build-remote"]["volumeMounts"]])
        select = steps["select-platform"]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            result = root / "mode"
            (root / "host").write_text("localhost\n")
            script = select["script"].replace("$(step.results.MODE.path)", str(result))
            completed = subprocess.run(["bash", "-c", script], env={**os.environ, "SSH_DIR": str(root)}, capture_output=True, text=True)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertEqual(result.read_text(), "local")
            (root / "host").write_text("builder@remote.example\n")
            completed = subprocess.run(["bash", "-c", script], env={**os.environ, "SSH_DIR": str(root)}, capture_output=True, text=True)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertEqual(result.read_text(), "remote")

    def test_developer_container_can_be_created_before_source_restore(self):
        for name, step_name in (("run-build", "build"), ("run-build-remote", "build-local")):
            spec = self.task(name)["spec"]
            step = next(s for s in spec["steps"] if s["name"] == step_name)
            self.assertFalse(any("subPath" in mount for mount in step["volumeMounts"]), "kubelet precreates subPath as root before restore")
            self.assertIn(step["workingDir"], [mount["mountPath"] for mount in step["volumeMounts"]], "runtime cwd must exist before prior steps execute")

    def test_only_pack_declares_final_subject(self):
        for name in ("run-build", "run-build-remote"):
            results = {r["name"]: r for r in self.task(name)["spec"]["results"]}
            self.assertNotIn("IMAGE_URL", results)
            self.assertNotIn("IMAGE_DIGEST", results)
            self.assertIn("steps.export.results.SOURCE_ARTIFACT", results["SOURCE_ARTIFACT"]["value"])
        names = {r["name"] for r in self.task("pack")["spec"]["results"]}
        self.assertTrue({"IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL"}.issubset(names))

    def test_matrix_consumes_all_build_artifacts_and_has_no_runtime_build_overrides(self):
        path = ROOT / "pipelines" / "byob-matrix.yaml"
        self.assertTrue(path.exists(), "missing matrix pipeline")
        spec = yaml.safe_load(path.read_text())["spec"]
        tasks = {t["name"]: t for t in spec["tasks"]}
        self.assertEqual(tasks["build"]["matrix"]["params"][0]["name"], "PLATFORM")
        params = {p["name"]: p["value"] for p in tasks["pack"]["params"]}
        self.assertEqual(params["SOURCE_ARTIFACT"], ["$(tasks.build.results.SOURCE_ARTIFACT[*])"])
        self.assertEqual(params["layout"], "platform")
        self.assertEqual({p["name"] for p in spec["params"]}, {"git-url", "revision", "output-image", "oci-storage"})
        self.assertFalse(spec.get("workspaces"))
        for task in tasks.values():
            self.assertNotIn("taskSpec", task)


if __name__ == "__main__":
    unittest.main()
