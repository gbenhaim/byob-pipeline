import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

from task_helpers import step


class FilesystemTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def run_script(self, task_name, step_name, **env):
        script = step(task_name, step_name)["script"]
        return subprocess.run(["python3", "-c", script], env={**os.environ, **{k: str(v) for k, v in env.items()}}, capture_output=True, text=True)

    def source(self, name="source"):
        source = self.root / name
        source.mkdir()
        (source / "build.sh").write_text("#!/bin/sh\nmkdir -p out\nprintf hello > out/hello\n")
        (source / "build.sh").chmod(0o755)
        return source

    def test_script_path_accepts_spaces_and_internal_symlink(self):
        source = self.source()
        (source / "build script.sh").symlink_to("build.sh")
        result = self.run_script("run-build", "prepare", SOURCE_ROOT=source, SCRIPT_PATH="./build script.sh", MODE="prepare")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_script_rejects_absolute_traversal_and_missing_paths(self):
        source = self.source()
        for path in ("/bin/sh", "../build.sh", "foo/../build.sh", "missing", ""):
            with self.subTest(path=path):
                result = self.run_script("run-build", "prepare", SOURCE_ROOT=source, SCRIPT_PATH=path, MODE="prepare")
                self.assertNotEqual(result.returncode, 0)

    def test_source_rejects_sibling_prefix_symlink(self):
        source = self.source()
        sibling = self.root / "source-evil"
        sibling.mkdir()
        (source / "escape").symlink_to(sibling)
        result = self.run_script("run-build", "prepare", SOURCE_ROOT=source, SCRIPT_PATH="build.sh", MODE="prepare")
        self.assertNotEqual(result.returncode, 0)

    def export(self, source, platform="", name="artifact"):
        artifact = self.root / name
        artifact.mkdir(exist_ok=True)
        result = self.run_script("run-build", "validate-output", SOURCE_ROOT=source, ARTIFACT_ROOT=artifact, BUILD_PLATFORM=platform)
        return result, artifact

    def test_output_artifact_contains_only_deliverables(self):
        source = self.source()
        (source / "out").mkdir()
        (source / "out/hello").write_text("deliverable")
        (source / "out/link").symlink_to("hello")
        (source / "changed-source").write_text("must not be exported")
        (source / ".skip-trusted-artifacts").touch()
        os.mkfifo(source / "build-cache-fifo")
        result, artifact = self.export(source)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sorted(p.name for p in artifact.iterdir()), ["out"])
        self.assertEqual((artifact / "out/hello").read_text(), "deliverable")
        self.assertEqual(os.readlink(artifact / "out/link"), "hello")

    def test_output_rejects_missing_empty_special_and_escaping_paths(self):
        for case in ("missing", "empty", "fifo", "escape", "absolute", "output-symlink", "source-symlink"):
            with self.subTest(case=case):
                source = self.source(case)
                output = source / "out"
                if case != "missing":
                    output.mkdir()
                if case not in ("missing", "empty"):
                    (output / "hello").write_text("hello")
                if case == "fifo":
                    os.mkfifo(output / "fifo")
                elif case == "escape":
                    (output / "escape").symlink_to("../build.sh")
                elif case == "absolute":
                    (output / "link").symlink_to(output / "hello")
                elif case == "output-symlink":
                    output.rename(source / "other")
                    output.symlink_to("other")
                elif case == "source-symlink":
                    link = self.root / "source-link"
                    link.symlink_to(source)
                    source = link
                result, artifact = self.export(source, name=case + "-artifact")
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(list(artifact.iterdir()), [])

    def test_platform_metadata_is_separate_from_user_outputs(self):
        source = self.source()
        (source / "out").mkdir()
        (source / "out/hello").write_text("deliverable")
        (source / ".konflux-platform").write_text("forged")
        result, artifact = self.export(source, "linux/arm64")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((artifact / ".konflux-platform").read_text(), "linux/arm64\n")
        self.assertEqual((source / ".konflux-platform").read_text(), "forged")
        self.assertEqual(sorted(p.name for p in artifact.iterdir()), [".konflux-platform", "out"])

    def input_tree(self, index, platform, contents):
        source = self.root / "inputs" / str(index)
        (source / "out").mkdir(parents=True)
        (source / "out" / "hello").write_text(contents)
        (source / ".konflux-platform").write_text(platform + "\n")
        return source

    def package(self, layout):
        return self.run_script("pack", "prepare", INPUT_ROOT=self.root / "inputs", PACKAGE_ROOT=self.root / "package", ARCHIVE_PATH=self.root / "payload.tar.gz", LAYOUT=layout)

    def test_matrix_package_maps_platforms_without_assuming_result_order(self):
        self.input_tree(0, "linux/arm64", "arm")
        self.input_tree(1, "linux/amd64", "amd")
        result = self.package("platform")
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.root / "payload.tar.gz") as archive:
            self.assertEqual(archive.extractfile("linux-arm64/hello").read(), b"arm")
            self.assertEqual(archive.extractfile("linux-amd64/hello").read(), b"amd")
            self.assertNotIn(".konflux-platform", archive.getnames())

    def test_flat_package_preserves_safe_symlinks_and_is_reproducible(self):
        source = self.input_tree(0, "", "hello")
        (source / "out" / "link").symlink_to("hello")
        result = self.package("flat")
        self.assertEqual(result.returncode, 0, result.stderr)
        first = (self.root / "payload.tar.gz").read_bytes()
        os.utime(source / "out" / "hello", (123, 123))
        result = self.package("flat")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / "payload.tar.gz").read_bytes(), first)
        with tarfile.open(self.root / "payload.tar.gz") as archive:
            self.assertTrue(archive.getmember("link").issym())

    def test_matrix_rejects_duplicate_normalized_platforms(self):
        self.input_tree(0, "linux/arm64", "one")
        self.input_tree(1, "linux-arm64", "two")
        self.assertNotEqual(self.package("platform").returncode, 0)
        self.assertFalse((self.root / "payload.tar.gz").exists())

    def test_pack_rejects_empty_output_and_escaping_symlinks(self):
        source = self.input_tree(0, "linux/amd64", "one")
        (source / "out" / "hello").unlink()
        self.assertNotEqual(self.package("flat").returncode, 0)
        (source / "out" / "escape").symlink_to("../../outside")
        self.assertNotEqual(self.package("flat").returncode, 0)

    def test_flat_rejects_multiple_sources(self):
        self.input_tree(0, "linux/amd64", "one")
        self.input_tree(1, "linux/arm64", "two")
        self.assertNotEqual(self.package("flat").returncode, 0)


if __name__ == "__main__":
    unittest.main()
