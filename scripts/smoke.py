#!/usr/bin/env python3
"""Exercise the packaged engine with temporary configuration and no credentials."""
import json
import os
import pathlib
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
binary = root / os.environ.get("BPS_APP_PATH", "dist/AstraBridge.app") / "Contents/Resources/bps-local"
with tempfile.TemporaryDirectory(prefix="bps-local-smoke-") as temporary:
    folder = pathlib.Path(temporary)
    codex = folder / "codex"
    codex.mkdir()
    original = b"# isolated smoke test\nmodel='gpt-6-astra'\n"
    (codex / "config.toml").write_bytes(original)
    # An unreadable-as-JSON placeholder proves idle startup needs no credential parsing.
    (codex / "auth.json").write_text("NOT_A_CREDENTIAL", encoding="utf-8")
    process = subprocess.Popen(
        [str(binary), "--codex-home", str(codex), "--state-dir", str(folder / "state")],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    try:
        initial = json.loads(process.stdout.readline())
        assert initial["phase"] == "idle", initial
        assert (codex / "config.toml").read_bytes() == original
        assert not (folder / "state/config-backup.json").exists()
        out, err = process.communicate('{"action":"quit"}\n', timeout=8)
        assert process.returncode == 0, err
        events = [json.loads(line) for line in out.splitlines()]
        assert any(event.get("phase") == "stopped" for event in events), events
        assert not any(event.get("phase") == "error" for event in events), events
        assert (codex / "config.toml").read_bytes() == original
        assert (codex / "auth.json").read_text() == "NOT_A_CREDENTIAL"
        print("PASS: packaged engine idle startup, no credentials required, config unchanged, graceful exit")
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
