#!/usr/bin/env python3
"""Stage validated outputs and produce a deterministic OCI payload."""
import gzip
import os
from pathlib import Path
import re
import shutil
import sys
import tarfile

from filesystem import check_tree, safe_relative


def main():
    inputs = Path(os.environ["INPUT_ROOT"])
    package = Path(os.environ["PACKAGE_ROOT"])
    archive = Path(os.environ["ARCHIVE_PATH"])
    layout = os.environ["LAYOUT"]
    sources = sorted(inputs.iterdir())
    if not sources or (layout == "flat" and len(sources) != 1):
        raise ValueError("flat layout requires one source; platform layout requires at least one")
    if layout not in ("flat", "platform"):
        raise ValueError(f"unknown layout: {layout}")
    destinations = set()
    selected = []
    for source in sources:
        if source.is_symlink() or not source.is_dir():
            raise ValueError("source must be a real directory")
        output = source / "out"
        check_tree(output)
        if not any(p.is_file() and not p.is_symlink() for p in output.rglob("*")):
            raise ValueError(f"output has no regular files: {output}")
        name = ""
        if layout == "platform":
            marker = source / ".konflux-platform"
            if marker.is_symlink() or not marker.is_file():
                raise ValueError("missing trusted platform marker")
            platform = marker.read_text().strip()
            safe_relative(platform)
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_./-]*", platform):
                raise ValueError("invalid platform label")
            name = re.sub(r"[^A-Za-z0-9]", "-", platform)
            if name in destinations:
                raise ValueError(f"duplicate platform directory: {name}")
        destinations.add(name)
        selected.append((output, name))
    if package.exists():
        shutil.rmtree(package)
    package.mkdir(parents=True)
    for output, name in selected:
        shutil.copytree(output, package / name, symlinks=True, dirs_exist_ok=True)
    archive.parent.mkdir(parents=True, exist_ok=True)
    with archive.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as tar:
                for path in sorted(package.rglob("*")):
                    info = tar.gettarinfo(str(path), arcname=str(path.relative_to(package)))
                    info.uid = info.gid = info.mtime = 0
                    info.uname = info.gname = ""
                    if info.isfile():
                        with path.open("rb") as data:
                            tar.addfile(info, data)
                    else:
                        tar.addfile(info)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError) as error:
        sys.exit(f"packaging failed: {error}")
