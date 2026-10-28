import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from task_helpers import step


class BuildInputTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.refs = ["oci:registry.example/build@sha256:" + letter * 64 for letter in "ab"]
        for letter, content in (("a", "first"), ("b", "second")):
            output = self.root / "fixtures" / (letter * 64) / "out"
            output.mkdir(parents=True)
            (output / "shared.txt").write_text(content)
        self.restore = self.root / "use-archive"
        self.restore.write_text('''#!/bin/bash
set -eu
reference=${1%%=*}
destination=${1#*=}
if [[ "$reference" == "${FAIL_REF:-}" ]]; then exit 17; fi
cp -a "$FIXTURES/${reference##*@sha256:}/." "$destination"
''')
        self.restore.chmod(0o755)

    def restore_inputs(self, refs, **extra):
        script = step("run-build", "restore-inputs")["script"]
        script = script.replace("/var/workdir/inputs", str(self.root / "inputs"))
        script = script.replace("/usr/local/bin/use-archive", str(self.restore))
        return subprocess.run(["bash", "-c", script, "restore-inputs", *refs], env={**os.environ, "FIXTURES": str(self.root / "fixtures"), **extra}, capture_output=True, text=True)

    def test_empty_array_creates_an_empty_input_directory(self):
        result = self.restore_inputs([])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(list((self.root / "inputs").iterdir()), [])

    def test_multiple_artifacts_keep_colliding_filenames_separate(self):
        result = self.restore_inputs(self.refs)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / "inputs/0/out/shared.txt").read_text(), "first")
        self.assertEqual((self.root / "inputs/1/out/shared.txt").read_text(), "second")

    def test_empty_and_invalid_items_are_rejected(self):
        for value in ("", "not-an-artifact", "oci:registry.example/build@sha256:short"):
            with self.subTest(value=value):
                result = self.restore_inputs([value])
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Invalid build artifact reference", result.stderr)
                self.assertFalse((self.root / "inputs/0").exists())

    def test_restore_failure_stops_before_the_next_artifact(self):
        result = self.restore_inputs(self.refs, FAIL_REF=self.refs[0])
        self.assertEqual(result.returncode, 17, result.stderr)
        self.assertFalse((self.root / "inputs/1").exists())


if __name__ == "__main__":
    unittest.main()
