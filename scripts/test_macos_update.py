#!/usr/bin/env python3
"""Fault-inject the shipped updater in temporary app bundles; never touch an installed app."""
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap

root = Path(__file__).resolve().parents[1]
swift = (root / 'app/UpdateChecker.swift').read_text()
script = textwrap.dedent(swift.split('let contents = """', 1)[1].split('"""', 1)[0]).strip() + '\n'

with tempfile.TemporaryDirectory(prefix='AstraBridge updater test ') as directory:
    base = Path(directory)
    commands = base / 'commands'
    commands.mkdir()
    # 真实复制/重命名，故障只注入指定阶段；open 使用替身，避免打开测试窗口。
    stubs = {
        'ditto': 'if [ "$FAILURE" = copy ]; then exit 21; fi\nexec /usr/bin/ditto "$@"',
        'mv': 'case "$1" in *.update-*) if [ "$FAILURE" = move ]; then exit 22; fi ;; esac\nexec /bin/mv "$@"',
        'open': 'version=$(cat "$1/version")\necho "$version" >> "$OPEN_LOG"\nif [ "$FAILURE" = open ] && [ "$version" = new ]; then exit 23; fi',
        'sleep': 'exit 0',
    }
    for name, contents in stubs.items():
        path = commands / name
        path.write_text('#!/bin/sh\nset -eu\n' + contents + '\n')
        path.chmod(0o755)
    for failure in ['none', 'copy', 'move', 'open', 'waiting']:
        case = base / failure
        case.mkdir()
        target, source, staging = case / 'Astra Bridge.app', case / 'new app', case / 'staging'
        staging.mkdir()
        for bundle, version in [(target, 'old'), (source, 'new')]:
            executable = bundle / 'Contents/MacOS/AstraBridge'
            executable.parent.mkdir(parents=True)
            executable.write_text('#!/bin/sh\nexit 0\n')
            executable.chmod(0o755)
            (bundle / 'version').write_text(version)
        updater = staging / 'apply-update.sh'
        updater.write_text(script)
        open_log = case / 'opened.log'
        env = dict(os.environ, PATH=str(commands) + ':/usr/bin:/bin', FAILURE=failure, OPEN_LOG=str(open_log))
        # 已回收子进程表示正常关闭；等待测试用当前测试 PID，确认不会误杀旧应用。
        exited = subprocess.Popen(['/usr/bin/true'])
        exited.wait()
        pid = os.getpid() if failure == 'waiting' else exited.pid
        result = subprocess.run(['/bin/sh', str(updater), str(target), str(source), str(pid), str(staging)], env=env, timeout=10)
        expected = 'new' if failure == 'none' else 'old'
        assert (target / 'version').read_text() == expected, failure
        assert (result.returncode == 0) == (failure == 'none'), failure
        opened = open_log.read_text().splitlines() if open_log.exists() else []
        assert opened == ({'none': ['new'], 'copy': ['old'], 'move': ['old'], 'open': ['new', 'old'], 'waiting': []}[failure]), (failure, opened)
        if failure != 'none':
            assert 'update failed' in (staging / 'update.log').read_text(), failure
        assert not list(case.glob('*.previous-*')), failure
        assert not list(case.glob('*.update-*')), failure
        print('PASS: macOS updater', failure)
