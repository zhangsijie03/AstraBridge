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
transport = manifest["native_transport"]
transport_root = root / transport["local_path"]
transport_files = {p.name for p in transport_root.iterdir() if p.is_file()}
if transport_files != set(transport["sha256"]):
    errors.append("Native transport file list differs from manifest")
for name, digest in transport["sha256"].items():
    path = transport_root / name
    if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        errors.append(f"Native transport differs from upstream: {name}")
if errors:
    raise SystemExit("\n".join(errors))
print(f"PASS: {len(expected)} locked files verified: "
      f"{len(manifest['unmodified_files'])} unchanged {manifest['tag']} files "
      f"({manifest['commit']}), 1 documented image helper extraction; "
      f"{len(transport['sha256'])} unchanged native transport files")
