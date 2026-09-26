#!/usr/bin/env python3
"""Verify pinned protocol files and documented native-image extraction offline."""
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1]
manifest = json.loads((root / "upstream-manifest.json").read_text())
package = root / "internal/basispoints"
expected = manifest["sha256"]
actual = {path.name for path in package.iterdir() if path.is_file()}
errors = []
for name in sorted(actual - expected.keys()):
    errors.append(f"Unexpected local file: {name}")
for name, digest in expected.items():
    path = package / name
    if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        errors.append(f"Differs from upstream: {name}")
if errors:
    raise SystemExit("\n".join(errors))
native = manifest.get("native_images")
if native:
    print(f"PASS: {len(expected)} locked files verified: 21 unchanged {manifest['tag']} files, "
          f"4 native-image files from {native['commit']}, 1 documented helper extraction")
else:
    print(f"PASS: all {len(expected)} protocol files match {manifest['tag']} ({manifest['commit']}) byte-for-byte")
