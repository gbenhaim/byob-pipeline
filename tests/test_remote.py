import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from task_helpers import step


class RemoteTests(unittest.TestCase):
    def test_attached_container_cannot_consume_remaining_remote_shell_program(self):
        script = step("run-build-remote", "build-remote")["script"].split("<<'REMOTE'\n", 1)[1].split("\nREMOTE\n", 1)[0]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "source/out").mkdir(parents=True)
            (root / "source/out/output.txt").write_text("expected output")
            (root / "source/private-source.txt").write_text("must not be returned")
            (root / "source/out/same-link").symlink_to("./output.txt")
            (root / "source/out/sub").mkdir()
            (root / "source/out/sub/parent-link").symlink_to("../output.txt")
            for index, content in enumerate(("first build output", "second build output")):
                output = root / "inputs" / str(index) / "out"
                output.mkdir(parents=True)
                (output / "generated.txt").write_text(content)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            podman = bin_dir / "podman"
            podman.write_text('''#!/bin/bash
set -eu
[[ "$1" == --default-mounts-file=/dev/null ]] || exit 88
shift
case "$1" in
 info) printf true ;;
 pull) : ;;
 image) printf 'sha256:%064d' 0 ;;
 create) printf fixture-container ;;
 cp) if [[ "$2" == - ]]; then mkdir -p "$FIXTURE_ROOT/received"; tar -xf - -C "$FIXTURE_ROOT/received"; else cp -a "$FIXTURE_ROOT/received${2#fixture-container:}" "$3"; fi ;;
 start) cat >/dev/null ;;
 inspect) printf 0 ;;
 rm) : ;;
 *) exit 99 ;;
esac
''')
            podman.chmod(0o755)
            result = subprocess.run(["bash", "-s", "--", str(root), "registry.example/builder@sha256:" + "a" * 64, "build.sh"], input=script, env={**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "FIXTURE_ROOT": str(root), "REQUIRE_MOUNT_ISOLATION": "true"}, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((root / "output/out/output.txt").exists(), "attached container consumed the copy-back commands")
            self.assertEqual(os.readlink(root / "output/out/same-link"), "./output.txt")
            self.assertEqual(os.readlink(root / "output/out/sub/parent-link"), "../output.txt")
            self.assertEqual(sorted(p.name for p in (root / "output").iterdir()), ["out"])
            self.assertEqual((root / "received/var/workdir/inputs/0/out/generated.txt").read_text(), "first build output")
            self.assertEqual((root / "received/var/workdir/inputs/1/out/generated.txt").read_text(), "second build output")

    def test_supplied_known_hosts_uses_strict_verification(self):
        script = step("run-build-remote", "build-remote")["script"].split("remote()", 1)[0]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "host").write_text("fixture.invalid")
            (root / "id_rsa").write_text("fixture key")
            (root / "known_hosts").write_text("fixture.invalid ssh-ed25519 fixture\n")
            # Exercise the actual OpenSSH option precedence without connecting.
            script += '\nssh -G "${ssh_options[@]}" "$host"\nrm -rf "$private"\n'
            result = subprocess.run(["bash", "-c", script], env={**os.environ, "SSH_DIR": str(root), "CONTROL_ROOT": str(root / "control"), "BUILDER_IMAGE": "registry.example/builder@sha256:" + "a" * 64}, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("stricthostkeychecking true\n", result.stdout)


if __name__ == "__main__":
    unittest.main()
