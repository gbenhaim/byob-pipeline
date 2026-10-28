import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]


class ExampleTests(unittest.TestCase):
    def test_pac_example_has_pinned_tasks_and_literal_matrix_after_configuration(self):
        path = ROOT / "examples/pac/push.yaml"
        self.assertTrue(path.exists(), "missing static PaC example")
        # Stand in for the real digests an operator copies from tkn bundle push.
        contents = path.read_text()
        for name in ("RUN_BUILD_REMOTE", "PACK"):
            contents = contents.replace(f"REPLACE_WITH_{name}_BUNDLE_DIGEST", "a" * 64)
        run = yaml.safe_load(contents)
        self.assertEqual(run["kind"], "PipelineRun")
        spec = run["spec"]
        self.assertNotIn("podTemplate", spec)
        self.assertNotIn("taskRunSpecs", spec)
        tasks = spec["pipelineSpec"]["tasks"]
        for task in tasks:
            self.assertNotIn("taskSpec", task)
            ref = task["taskRef"]
            self.assertEqual(ref["resolver"], "bundles")
            self.assertRegex(next(p["value"] for p in ref["params"] if p["name"] == "bundle"), r"@sha256:[a-f0-9]{64}$")
        build = next(t for t in tasks if t["name"] == "build")
        self.assertEqual(build["matrix"]["params"], [{"name": "PLATFORM", "value": ["linux/amd64", "linux/arm64"]}])
        pack = next(t for t in tasks if t["name"] == "pack")
        self.assertEqual(next(p["value"] for p in pack["params"] if p["name"] == "BUILD_ARTIFACT"), ["$(tasks.build.results.BUILD_ARTIFACT[*])"])

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
