"""Filesystem checks embedded in trusted Task scripts by hack/render-catalog.py."""
import os
from pathlib import Path
import stat


def safe_relative(value):
    path = Path(value)
    if not value or path.is_absolute() or ".." in path.parts:
        raise ValueError(f"expected a relative path without '..': {value!r}")
    return path


def check_tree(root):
    if root.is_symlink() or not root.is_dir():
        raise ValueError(f"expected a real directory: {root}")
    root = root.resolve(strict=True)
    for directory, dirs, files in os.walk(root, followlinks=False):
        for name in dirs + files:
            path = Path(directory) / name
            mode = path.lstat().st_mode
            if stat.S_ISLNK(mode):
                # Absolute links would point somewhere different after extraction.
                target = os.readlink(path)
                if os.path.isabs(target) or not path.resolve().is_relative_to(root):
                    raise ValueError(f"symlink escapes artifact: {path}")
            elif not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
                raise ValueError(f"unsupported special file: {path}")
