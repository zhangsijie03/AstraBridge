#!/usr/bin/env python3
"""Exercise the shipped PowerShell updater with real temp directories and injected faults."""
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
source_code = (root / 'windows/AstraBridge/UpdateChecker.cs').read_text(encoding='utf-8')
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
    if ($env:FAILURE -in @('open', 'rollback') -and $version -eq 'new') { throw 'Injected launch failure' }
}
function Copy-Item {
    param($LiteralPath, $Destination, [switch]$Recurse, [switch]$Force)
    if ($env:FAILURE -eq 'copy') { throw 'Injected copy failure' }
    if ($env:FAILURE -eq 'backup' -and $Destination -like '*.previous-*') { throw 'Injected backup failure' }
    if ($env:FAILURE -eq 'rollback' -and $LiteralPath -like '*.previous-*bps-local.exe') { throw 'Injected rollback failure' }
    Microsoft.PowerShell.Management\Copy-Item -LiteralPath $LiteralPath -Destination $Destination -Recurse:$Recurse -Force:$Force
}
function Move-Item {
    param($LiteralPath, $Destination, [switch]$Force)
    if ($env:FAILURE -eq 'move' -and $LiteralPath -like '*.update-*') { throw 'Injected install failure' }
    if ($LiteralPath -like '*.update-*') {
        if ($env:FAILURE -eq 'late_move' -and $script:installedMoves -ge 2) { throw 'Injected partial install failure' }
        $script:installedMoves++
    }
    Microsoft.PowerShell.Management\Move-Item -LiteralPath $LiteralPath -Destination $Destination -Force:$Force
}
'''
with tempfile.TemporaryDirectory(prefix='AstraBridge update review ') as directory:
    base = Path(directory)
    for failure in ['none', 'copy', 'backup', 'move', 'late_move', 'open', 'rollback', 'waiting', 'conflict']:
        case = base / failure
        case.mkdir()
        target, source, staging = case / 'Astra Bridge', case / 'new app', case / 'staging'
        staging.mkdir()
        for package, version in [(target, 'old'), (source, 'new')]:
            package.mkdir()
            (package / 'AstraBridge.exe').write_text('synthetic executable')
            (package / 'version').write_text(version)
            (package / 'engine').mkdir()
            (package / 'engine/bps-local.exe').write_text(version)
        # 用户文件位于根目录和程序子目录，成功/回滚都必须原样保留。
        for name in ['my-notes.txt', 'engine/用户备注.txt', 'logs/session.log']:
            extra = target / name
            extra.parent.mkdir(exist_ok=True)
            extra.write_text('USER_DATA', encoding='utf-8')
        (source / 'new-directory').mkdir()
        (source / 'new-directory/added.txt').write_text('new-file')
        if failure == 'conflict':
            (target / 'new-directory').write_text('USER_CONFLICT')
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
        assert opened == {'none': ['new'], 'copy': ['old'], 'backup': ['old'], 'move': ['old'], 'late_move': ['old'], 'open': ['new', 'old'], 'rollback': ['new'], 'waiting': [], 'conflict': ['old']}[failure], (failure, opened)
        for name in ['my-notes.txt', 'engine/用户备注.txt', 'logs/session.log']:
            assert (target / name).read_text(encoding='utf-8') == 'USER_DATA', (failure, name)
        assert (target / 'engine/bps-local.exe').read_text() == ('new' if failure in ['none', 'rollback'] else 'old'), failure
        if failure == 'conflict':
            assert (target / 'new-directory').read_text() == 'USER_CONFLICT'
        elif failure != 'none':
            assert not (target / 'new-directory').exists(), failure
        if failure != 'none':
            assert 'Update failed' in (staging / 'update.log').read_text(encoding='utf-8-sig'), failure
        backups = list(case.glob('*.previous-*'))
        if failure == 'rollback':
            assert len(backups) == 1, backups
            assert (backups[0] / 'engine/bps-local.exe').read_text() == 'old'
            assert 'Rollback failed' in (staging / 'update.log').read_text(encoding='utf-8-sig')
        else:
            assert not backups, failure
        assert not list(case.glob('*.update-*')), failure
        print('PASS: Windows updater', failure)
