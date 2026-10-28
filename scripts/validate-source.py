#!/usr/bin/env python3
"""Validate source in a trusted image, never in the developer image."""
import os
from pathlib import Path
import re
import sys

from filesystem import check_tree, safe_relative


def main():
    source = Path(os.environ["SOURCE_ROOT"])
    mode = os.environ["MODE"]
    marker = source / ".konflux-platform"
    if mode == "export" and (marker.exists() or marker.is_symlink()):
        # Never follow an attacker-supplied marker, including a dangling symlink.
        marker.unlink()
    check_tree(source)
    if (source / ".skip-trusted-artifacts").exists():
        raise ValueError("source must not disable Trusted Artifact publication")
    if mode == "prepare":
        relative = safe_relative(os.environ["SCRIPT_PATH"])
        script = (source / relative).resolve(strict=True)
        if not script.is_relative_to(source.resolve()) or not script.is_file():
            raise ValueError("script must be a file inside the source tree")
        if not os.access(script, os.X_OK):
            raise ValueError("script must be executable and have an interpreter shebang")
        with script.open("rb") as contents:
            if contents.read(2) != b"#!":
                raise ValueError("script must have an interpreter shebang")
    elif mode == "export":
        platform = os.environ.get("BUILD_PLATFORM", "")
        if platform:
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_./-]*", platform):
                raise ValueError("invalid platform label")
            safe_relative(platform)
            marker.write_text(platform + "\n")
    else:
        raise ValueError(f"unknown validation mode: {mode}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError) as error:
        sys.exit(f"source validation failed: {error}")
