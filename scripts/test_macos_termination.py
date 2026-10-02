#!/usr/bin/env python3
"""Run the real AppKit termination delegate with isolated, synthetic engines."""
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time

root = Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory(prefix='AstraBridge-exit-review-') as directory:
    folder = Path(directory)
    bundle = folder / 'Test.app'
    mac = bundle / 'Contents/MacOS'
    resources = bundle / 'Contents/Resources'
    mac.mkdir(parents=True)
    resources.mkdir()
    code = (root / 'app/main.swift').read_text()
    # Replace only launch orchestration: no network check, credential read or visible window.
    code = code.replace('checkForUpdates(manual: false)', '''DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) {
        if let path = ProcessInfo.processInfo.environment["ASTRA_TEST_UPDATE_STAGE"] {
            let staging = URL(fileURLWithPath: path)
            let staged = StagedAppUpdate(version: "test", appURL: staging.appendingPathComponent("New.app"), stagingDirectory: staging)
            do { self.updaterProcess = try self.updateChecker.launchUpdater(staged: staged, replacing: Bundle.main.bundleURL, waitingFor: ProcessInfo.processInfo.processIdentifier) }
            catch { fatalError("Synthetic updater could not launch: \\(error)") }
        }
        self.updateInProgress = true; self.setBusy(true); NSApp.terminate(nil)
    }''')
    code = code.replace('NSApp.activate(ignoringOtherApps: true)', '').replace('window.makeKeyAndOrderFront(nil)', '').replace('app.setActivationPolicy(.regular)', 'app.setActivationPolicy(.accessory)')
    source = folder / 'main.swift'
    source.write_text(code)
    subprocess.run(['swiftc', '-swift-version', '5', '-module-cache-path', str(root / '.build/swift-cache'), str(root / 'app/Interface.swift'), str(root / 'app/TransferLog.swift'), str(root / 'app/UpdateChecker.swift'), str(source), '-o', str(mac / 'AstraBridge'), '-framework', 'AppKit', '-framework', 'CFNetwork'], check=True)
    (bundle / 'Contents/Info.plist').write_text('<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>AstraBridge</string><key>CFBundleIdentifier</key><string>local.astrabridge.exit-test</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>')
    # 最后一项用实际 helper、ditto/mv 和 LaunchServices，把退出、替换、重启串起来。
    staging = folder / 'staging'
    replacement = staging / 'New.app'
    shutil.copytree(bundle, replacement)
    launch_source = folder / 'relaunch.swift'
    launch_source.write_text('import Foundation\ntry "new".write(to: Bundle.main.bundleURL.appendingPathComponent("relaunch.marker"), atomically: true, encoding: .utf8)\n')
    subprocess.run(['swiftc', '-module-cache-path', str(root / '.build/swift-cache'), str(launch_source), '-o', str(replacement / 'Contents/MacOS/AstraBridge')], check=True)
    for scenario in ['graceful', 'unresponsive', 'update']:
        stuck = scenario == 'unresponsive'
        engine = resources / 'bps-local'
        pid_file = folder / 'engine.pid'
        engine.write_text('#!/bin/sh\n' + ("trap '' INT TERM\n" if stuck else '') + 'echo $$ > "$ASTRA_TEST_ENGINE_PID"\n' + "echo '{\"type\":\"state\",\"phase\":\"idle\",\"requests\":0}'\n" + ('while IFS= read -r line; do :; done\n' if stuck else 'read -r line\nexit 0\n'))
        engine.chmod(0o755)
        start = time.monotonic()
        try:
            environment = dict(os.environ, ASTRA_TEST_ENGINE_PID=str(pid_file))
            if scenario == 'update':
                environment['ASTRA_TEST_UPDATE_STAGE'] = str(staging)
            subprocess.run([str(mac / 'AstraBridge')], env=environment, timeout=14, check=True, capture_output=True)
            elapsed = time.monotonic() - start
            assert elapsed < (13 if stuck else 4), (stuck, elapsed)
            if stuck:
                assert elapsed >= 8, 'Unresponsive engine was killed without grace period'
            if scenario == 'update':
                deadline = time.monotonic() + 10
                while not (bundle / 'relaunch.marker').exists() and time.monotonic() < deadline:
                    time.sleep(0.1)
                assert (bundle / 'relaunch.marker').read_text() == 'new', 'Replacement app did not launch'
                print('PASS: actual AppKit exit, app replacement and LaunchServices relaunch')
            else:
                print('PASS: actual AppKit exit', scenario, round(elapsed, 2), 'seconds')
        except BaseException:
            if pid_file.exists():
                try:
                    os.kill(int(pid_file.read_text()), signal.SIGKILL)
                except ProcessLookupError:
                    pass
            raise
