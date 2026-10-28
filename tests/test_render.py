import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]


class RenderTests(unittest.TestCase):
    def test_cluster_helper_rejects_empty_context_before_rendering_or_connecting(self):
        command = ["python3", str(ROOT / "hack/test-cluster.py")]
        for option, value in (("context", ""), ("namespace", "test"), ("git-url", "https://example.invalid/source"), ("revision", "a" * 40), ("output-image", "registry.example/out:test"), ("oci-storage", "registry.example/ta"), ("bundles", "/missing-bundle-file")):
            command += ["--" + option, value]
        result = subprocess.run(command, text=True, capture_output=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn("--context must not be empty", result.stderr)

    def test_production_run_has_pinned_tasks_and_literal_matrix(self):
        helper = ROOT / "hack/render-run.py"
        self.assertTrue(helper.exists(), "missing PaC renderer")
        with tempfile.TemporaryDirectory() as temporary:
            lock = Path(temporary) / "bundles.yaml"
            lock.write_text(yaml.safe_dump({name: f"registry.example/catalog/{name}@sha256:" + "a" * 64 for name in ("run-build", "run-build-remote", "pack")}))
            result = subprocess.run(["python3", str(helper), "--pipeline", "byob-matrix", "--bundles", str(lock), "--output-image", "registry.example/team/app:test", "--oci-storage", "registry.example/team/ta", "--platform", "linux/amd64", "--platform", "linux/arm64"], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            run = yaml.safe_load(result.stdout)
            self.assertEqual(run["kind"], "PipelineRun")
            spec = run["spec"]
            self.assertNotIn("podTemplate", spec)
            self.assertNotIn("taskRunSpecs", spec)
            for task in spec["pipelineSpec"]["tasks"]:
                self.assertNotIn("taskSpec", task)
                ref = task["taskRef"]
                self.assertEqual(ref["resolver"], "bundles")
                self.assertRegex(next(p["value"] for p in ref["params"] if p["name"] == "bundle"), r"@sha256:[a-f0-9]{64}$")

    def test_local_task_propagates_script_failure_and_treats_path_as_data(self):
        path = ROOT / "task/run-build/0.1/run-build.yaml"
        self.assertTrue(path.exists(), "missing local Task")
        spec = yaml.safe_load(path.read_text())["spec"]
        step = next(s for s in spec["steps"] if s["name"] == "build")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            name = "build; touch injected.sh"
            (source / name).write_text("#!/bin/sh\nexit 17\n")
            (source / name).chmod(0o755)
            args = [v.replace("$(params.script-path)", name) for v in step["args"]]
            result = subprocess.run(step["command"] + args, cwd=root, env={**os.environ, "HOME": str(root / "home")}, capture_output=True)
            self.assertEqual(result.returncode, 17)
            self.assertFalse((source / "injected.sh").exists())


if __name__ == "__main__":
    unittest.main()
