import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from task_helpers import step

DIGEST = "sha256:" + "a" * 64


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        (self.root / "payload.tar.gz").write_bytes(b"payload")
        (self.root / "sbom.spdx.json").write_text('{"spdxVersion":"SPDX-2.3"}')
        self.command("oras", '#!/bin/sh\nprintf \'{"digest":"' + DIGEST + '"}\\n\'\n')
        self.command("cosign", '#!/bin/sh\nexit "${ATTACH_STATUS:-0}"\n')

    def command(self, name, text):
        path = self.bin / name
        path.write_text(text)
        path.chmod(0o755)

    def publish(self, **extra):
        script = step("pack", "publish")["script"]
        for name in ("IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL"):
            script = script.replace(f"$(step.results.{name}.path)", str(self.root / name))
        return subprocess.run(["bash", "-c", script], cwd=self.root, env={**os.environ, "PATH": f"{self.bin}:{os.environ['PATH']}", "IMAGE": "localhost:5000/team/output:test", "ARTIFACT_TYPE": "application/vnd.konflux.build-output.v1", "REGISTRY_AUTH_DIR": str(self.root / "no-auth"), **extra}, capture_output=True, text=True)

    def test_results_use_manifest_digest_and_preserve_registry_port(self):
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / "IMAGE_DIGEST").read_text(), DIGEST)
        self.assertEqual((self.root / "IMAGE_URL").read_text(), "localhost:5000/team/output:test")
        self.assertTrue((self.root / "SBOM_BLOB_URL").read_text().startswith("localhost:5000/team/output@sha256:"))

    def test_failed_sbom_attachment_emits_no_subject_results(self):
        result = self.publish(ATTACH_STATUS="7")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / "IMAGE_DIGEST").exists())
        self.assertFalse((self.root / "IMAGE_URL").exists())

    def test_registry_push_failure_does_not_use_stale_result(self):
        self.command("oras", "#!/bin/sh\nexit 9\n")
        self.assertNotEqual(self.publish().returncode, 0)
        self.assertFalse((self.root / "IMAGE_DIGEST").exists())


if __name__ == "__main__":
    unittest.main()
