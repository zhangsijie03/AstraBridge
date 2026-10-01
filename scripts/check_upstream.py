#!/usr/bin/env python3
"""Verify pinned protocol files and explicitly documented local adaptations."""
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1]
manifest = json.loads((root / "upstream-manifest.json").read_text())
package = root / "internal/basispoints"
expected = manifest["sha256"]
patches = manifest.get("local_patches", {})
local_files = manifest.get("local_files", {})
actual = {path.name for path in package.iterdir() if path.is_file()}
allowed = set(expected) | set(patches) | set(local_files)
errors = []
for name in sorted(actual - allowed):
    errors.append(f"Unexpected local file: {name}")
for name, digest in expected.items():
    if name in patches:
        continue
    path = package / name
    if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        errors.append(f"Differs from upstream: {name}")
for name, spec in patches.items():
    path = package / name
    if name not in expected:
        errors.append(f"Local patch is missing upstream digest: {name}")
        continue
    if spec.get("upstream_sha256") != expected[name]:
        errors.append(f"Local patch upstream digest mismatch: {name}")
        continue
    local_digest = spec.get("local_sha256")
    if not path.is_file() or not local_digest or hashlib.sha256(path.read_bytes()).hexdigest() != local_digest:
        errors.append(f"Local patch changed without manifest update: {name}")
for name, digest in local_files.items():
    path = package / name
    if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        errors.append(f"Local file changed without manifest update: {name}")
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
unchanged = len(expected) - len(patches)
print(f"PASS: {unchanged} unchanged and {len(patches)} locally adapted {manifest['tag']} protocol files "
      f"({manifest['commit']}), {len(local_files)} local files, 1 documented image helper extraction; "
      f"{len(transport['sha256'])} unchanged native transport files")
