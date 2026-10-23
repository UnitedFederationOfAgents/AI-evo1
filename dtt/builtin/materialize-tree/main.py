#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["pyyaml>=6"]
# ///
"""materialize-tree -- the reference T3 (see AI-evo1/dtt/README.md).

spec is a flat YAML mapping of relative path -> file content; a path ending
in "/" is a directory (its value is ignored). The tree is built under root
(default: tree/ in the run's working directory) and its entries are packed
into tree.zip in the working directory, the archive output.

Inputs arrive as JSON on stdin; results go to $DTT_OUTPUT as JSON; stderr is
the log.
"""
import json
import os
import sys
import zipfile
from pathlib import Path, PurePosixPath

import yaml


def fail(msg: str) -> None:
    print(f"materialize-tree: {msg}", file=sys.stderr)
    sys.exit(1)


def parse_spec(text: str) -> list[tuple[str, bool, str]]:
    """Returns (relative path, is_dir, content) entries in document order."""
    try:
        spec = yaml.safe_load(text) if text.strip() else {}
    except yaml.YAMLError as e:
        fail(f"spec isn't valid YAML: {e}")
    if spec is None:
        spec = {}
    if not isinstance(spec, dict):
        fail("spec should be a mapping of relative path: content")
    entries = []
    for key, value in spec.items():
        rel = str(key)
        is_dir = rel.endswith("/")
        p = PurePosixPath(rel)
        if rel.strip() in ("", "/") or p.is_absolute() or ".." in p.parts:
            fail(f"{rel!r}: paths are relative and stay inside the tree")
        if not is_dir and isinstance(value, (dict, list)):
            fail(f"{rel!r}: a file's content is text, not a mapping or list (end the path with / for a directory)")
        content = "" if value is None or is_dir else str(value)
        entries.append((str(p), is_dir, content))
    return entries


def main() -> int:
    inputs = json.load(sys.stdin)
    entries = parse_spec(inputs.get("spec") or "")
    overwrite = bool(inputs.get("overwrite"))
    root = Path(os.path.expanduser(inputs.get("root") or "tree"))
    if not root.is_absolute():
        root = Path.cwd() / root
    if root.exists() and not root.is_dir():
        fail(f"root {root} exists and isn't a directory")

    files = dirs = 0
    for rel, is_dir, content in entries:
        target = root / rel
        if is_dir:
            if target.exists() and not target.is_dir():
                fail(f"{rel}: a file is in the way of this directory")
            if not target.is_dir():
                dirs += 1
            target.mkdir(parents=True, exist_ok=True)
            print(f"dir   {rel}/", file=sys.stderr)
            continue
        if target.is_dir():
            fail(f"{rel}: a directory is in the way of this file")
        if target.exists() and not overwrite:
            fail(f"{rel} already exists under {root} (set overwrite to replace it)")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)
        files += 1
        print(f"file  {rel} ({len(content.encode())} bytes)", file=sys.stderr)

    archive = Path.cwd() / "tree.zip"
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
        for rel, is_dir, _ in entries:
            if is_dir:
                z.writestr(rel.rstrip("/") + "/", "")
            else:
                z.write(root / rel, rel)

    with open(os.environ["DTT_OUTPUT"], "w") as f:
        json.dump({
            "values": {"root": str(root.resolve()), "file_count": files, "dir_count": dirs},
            "files": {"archive": str(archive)},
        }, f)
    print(f"built {files} file(s) and {dirs} new directory(ies) under {root}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
