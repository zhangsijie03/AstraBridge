#!/usr/bin/env python3
"""Exercise the shipped PowerShell updater with real temp directories and injected faults."""
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
source_code = (root / 'windows/AstraBridge/UpdateChecker.cs').read_text()
script = source_code.split('File.WriteAllText(script, """', 1)[1].split('""", Encoding.UTF8)', 1)[0].strip()
parameters, body = script.split('\n', 1)
# Stub process lookup/relaunch and delays only; file operations remain real except at the failure site.
stubs = r'''
function Get-Process { param($Id, $ErrorAction) if ($env:FAILURE -eq 'waiting') { return $true } }
function Start-Sleep { param($Milliseconds) }
function Start-Process {
    param($FilePath)
    $version = Get-Content -LiteralPath (Join-Path (Split-Path -Parent $FilePath) 'version')
    $version | Add-Content -LiteralPath $env:OPEN_LOG
    if ($env:FAILURE -eq 'open' -and $version -eq 'new') { throw 'Injected launch failure' }
}
function Copy-Item {
    param($LiteralPath, $Destination, [switch]$Recurse, [switch]$Force)
    if ($env:FAILURE -eq 'copy') { throw 'Injected copy failure' }
    Microsoft.PowerShell.Management\Copy-Item -LiteralPath $LiteralPath -Destination $Destination -Recurse:$Recurse -Force:$Force
}
function Move-Item {
    param($LiteralPath, $Destination, [switch]$Force)
    if ($env:FAILURE -eq 'move' -and $LiteralPath -like '*.update-*') { throw 'Injected install failure' }
    Microsoft.PowerShell.Management\Move-Item -LiteralPath $LiteralPath -Destination $Destination -Force:$Force
}
'''
with tempfile.TemporaryDirectory(prefix='AstraBridge update review ') as directory:
    base = Path(directory)
    for failure in ['none', 'copy', 'move', 'open', 'waiting']:
        case = base / failure
        case.mkdir()
        target, source, staging = case / 'Astra Bridge', case / 'new app', case / 'staging'
        staging.mkdir()
        for package, version in [(target, 'old'), (source, 'new')]:
            package.mkdir()
            (package / 'AstraBridge.exe').write_text('synthetic executable')
            (package / 'version').write_text(version)
        updater = staging / 'apply-update.ps1'
        updater.write_text(parameters + '\n' + stubs + body, encoding='utf-8-sig')
        open_log = case / 'opened.log'
        env = dict(os.environ, FAILURE=failure, OPEN_LOG=str(open_log))
        # Match AppContext.BaseDirectory including its trailing separator, spaces and current platform paths.
        shell = 'powershell.exe' if os.name == 'nt' else 'pwsh'
        result = subprocess.run([shell, '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(updater),
                                 str(target) + os.sep, str(source), '123456', str(staging)],
                                env=env, cwd=base, timeout=15, capture_output=True)
        assert (result.returncode == 0) == (failure == 'none'), (failure, result.stderr)
        assert (target / 'version').read_text() == ('new' if failure == 'none' else 'old'), failure
        opened = open_log.read_text().splitlines() if open_log.exists() else []
        assert opened == {'none': ['new'], 'copy': ['old'], 'move': ['old'], 'open': ['new', 'old'], 'waiting': []}[failure], (failure, opened)
        if failure != 'none':
            assert 'Update failed' in (staging / 'update.log').read_text(encoding='utf-8-sig'), failure
        assert not list(case.glob('*.previous-*')), failure
        assert not list(case.glob('*.update-*')), failure
        print('PASS: Windows updater', failure)
