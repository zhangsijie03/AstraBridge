#!/usr/bin/env python3
"""Compile the shipped Swift updater; exercise proxy handshakes only against loopback."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading

root = Path(__file__).resolve().parents[1]
source = (root / 'app/main.swift').read_text()
product = source[source.index('private enum Product'):source.index('private struct GatewayResult')]
manifest = json.loads((root / 'upstream-manifest.json').read_text())
swift = r'''
import Foundation
import CFNetwork

func require(_ condition: Bool, _ message: String) {
    if !condition { fatalError(message) }
}
func checkProxy(_ raw: String, _ host: String, _ port: Int, socks: Bool = false) throws {
    let proxy = try UpdateChecker.environmentProxy(["HTTPS_PROXY": raw])!
    if socks {
        require(proxy[kCFNetworkProxiesSOCKSEnable as String] as? Bool == true, "SOCKS disabled")
        require(proxy[kCFNetworkProxiesSOCKSProxy as String] as? String == host, "SOCKS host")
        require(proxy[kCFNetworkProxiesSOCKSPort as String] as? Int == port, "SOCKS port")
        require(proxy[kCFNetworkProxiesHTTPSEnable as String] == nil, "SOCKS mapped to HTTP")
    } else {
        require(proxy[kCFNetworkProxiesHTTPProxy as String] as? String == host, "HTTP host")
        require(proxy[kCFNetworkProxiesHTTPPort as String] as? Int == port, "HTTP port")
        require(proxy[kCFNetworkProxiesHTTPSProxy as String] as? String == host, "HTTPS host")
        require(proxy[kCFNetworkProxiesHTTPSPort as String] as? Int == port, "HTTPS port")
    }
}
let mode = CommandLine.arguments[1]
if mode == "config" {
    try checkProxy("http://127.0.0.1", "127.0.0.1", 80)
    try checkProxy("http://127.0.0.1:6789/", "127.0.0.1", 6789)
    try checkProxy("socks5://127.0.0.1", "127.0.0.1", 1080, socks: true)
    try checkProxy("socks5h://127.0.0.1:6789", "127.0.0.1", 6789, socks: true)
    let lower = try UpdateChecker.environmentProxy(["HTTPS_PROXY": "", "https_proxy": "http://localhost:6789", "HTTP_PROXY": "http://wrong:1234"])!
    require(lower[kCFNetworkProxiesHTTPSProxy as String] as? String == "localhost", "proxy priority")
    let absent = try UpdateChecker.environmentProxy([:])
    require(absent == nil, "empty environment")
    let system: [String: Any] = [kCFNetworkProxiesHTTPSEnable as String: 1, kCFNetworkProxiesHTTPSProxy as String: "system.invalid", kCFNetworkProxiesHTTPSPort as String: 18902]
    let base = ["CODEX_HOME": "/synthetic/codex", "NO_PROXY": "example.invalid", "EXTRA_SETTING": "preserved"]
    // 显式配置、大小写及 HTTP 后备都必须压过系统代理，且两条功能链选择一致。
    for key in ["HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"] {
        var inherited = base
        inherited[key] = "http://explicit.invalid:18901"
        let engine = ProxyEnvironment.forEngine(inherited, systemSettings: system)
        require(engine["HTTPS_PROXY"] == "http://explicit.invalid:18901", "system overrode explicit proxy")
        require(base.allSatisfy { engine[$0.key] == $0.value }, "unrelated environment changed")
        let updater = try UpdateChecker.environmentProxy(inherited)!
        require(updater[kCFNetworkProxiesHTTPSProxy as String] as? String == "explicit.invalid", "engine/updater mismatch")
    }
    let mixed = ["HTTPS_PROXY": "http://first.invalid:18901", "https_proxy": "http://second.invalid:18902", "HTTP_PROXY": "http://third.invalid:18903"]
    require(ProxyEnvironment.forEngine(mixed, systemSettings: system)["HTTPS_PROXY"] == mixed["HTTPS_PROXY"], "proxy priority changed")
    require(ProxyEnvironment.forEngine(["HTTPS_PROXY": " ", "https_proxy": "http://lower.invalid:18901"], systemSettings: system)["HTTPS_PROXY"] == "http://lower.invalid:18901", "empty upper-case masks lower-case")
    require(ProxyEnvironment.forEngine(["HTTPS_PROXY": "unsupported://explicit.invalid"], systemSettings: system)["HTTPS_PROXY"] == "unsupported://explicit.invalid", "invalid explicit proxy silently fell back")
    require(ProxyEnvironment.forEngine(base, systemSettings: system)["HTTPS_PROXY"] == "http://system.invalid:18902", "system fallback missing")
    require(ProxyEnvironment.forEngine(base, systemSettings: nil) == base, "proxy injected without configuration")
    var disabled = system; disabled[kCFNetworkProxiesHTTPSEnable as String] = 0
    require(ProxyEnvironment.forEngine(base, systemSettings: disabled) == base, "disabled system proxy used")
    var ipv6 = system; ipv6[kCFNetworkProxiesHTTPSProxy as String] = "::1"
    require(ProxyEnvironment.forEngine(base, systemSettings: ipv6)["HTTPS_PROXY"] == "http://[::1]:18902", "invalid IPv6 authority")
    for raw in ["garbage", "https://localhost", "ftp://localhost", "http://user:secret@localhost", "http://localhost/path", "http://localhost:0", "http://localhost:65536", "http://localhost?token=secret"] {
        do { _ = try UpdateChecker.environmentProxy(["HTTPS_PROXY": raw]); fatalError("Accepted invalid proxy") }
        catch UpdateError.invalidProxy { }
    }
    print("PASS: proxy protocols, default/explicit ports, precedence and rejection")
    print("PASS: engine/updater explicit proxy precedence, system fallback and environment preservation")
} else if mode == "source" {
    let tag = CommandLine.arguments[2], commit = CommandLine.arguments[3]
    require(Product.sourceDescription.contains(tag), "stale tag")
    require(Product.sourceDescription.contains(String(commit.prefix(7))), "stale commit")
    require(Product.sourceURL?.absoluteString == "https://github.com/ranxi2001/sub2api/blob/\(commit)/docs/excel-bps.md", "stale source URL")
    print("PASS: About and source link match bundled provenance")
} else if mode == "missing-source" {
    require(Product.sourceURL == nil, "missing manifest has URL")
    require(Product.sourceDescription.contains("来源清单不可读取"), "missing manifest hidden")
    print("PASS: missing source manifest reported")
} else {
    let checker = UpdateChecker()
    var callbacks = 0
    let expected = mode == "invalid" ? 2 : 1
    func result<T>(_ result: Result<T, Error>) {
        require(Thread.isMainThread, "callback off main thread")
        guard case .failure(let error) = result else { fatalError("Unexpected success") }
        if mode == "invalid" {
            guard case UpdateError.invalidProxy = error else { fatalError("Wrong configuration failure") }
        } else {
            guard case UpdateError.downloadFailed = error else { fatalError("Wrong transport failure") }
        }
        callbacks += 1
    }
    checker.check(currentVersion: "0.0.0", completion: result)
    if mode == "invalid" {
        let url = URL(string: "https://example.invalid/synthetic")!
        checker.stage(AppUpdate(version: "1.0.0", notes: "", archiveURL: url, checksumURL: url), completion: result)
    }
    let deadline = Date().addingTimeInterval(8)
    while callbacks < expected && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.05)) }
    require(callbacks == expected, "completion stuck")
    print("PASS: \(mode) completes on main thread")
}
'''

def read_exact(connection, count):
    data = b''
    while len(data) < count:
        chunk = connection.recv(count - len(data))
        assert chunk, 'Proxy handshake truncated'
        data += chunk
    return data


with tempfile.TemporaryDirectory(prefix='AstraBridge proxy test ') as directory:
    base = Path(directory)
    bundle = base / 'Probe.app/Contents'
    (bundle / 'MacOS').mkdir(parents=True)
    (bundle / 'Resources').mkdir()
    resource = bundle / 'Resources/upstream-manifest.json'
    resource.write_text(json.dumps(manifest))
    (bundle / 'Info.plist').write_text('<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>Probe</string><key>CFBundleIdentifier</key><string>test.astrabridge.proxy</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>')
    main = base / 'main.swift'
    main.write_text('import Foundation\n' + product + swift)
    executable = bundle / 'MacOS/Probe'
    subprocess.run(['swiftc', '-module-cache-path', str(root / '.build/swift-cache'),
                    str(root / 'app/UpdateChecker.swift'), str(main), '-o', str(executable)], check=True)
    clean_env = {k: v for k, v in os.environ.items() if k.lower() not in ('https_proxy', 'http_proxy', 'all_proxy', 'no_proxy')}
    for args in [['config'], ['source', manifest['tag'], manifest['commit']]]:
        subprocess.run([str(executable), *args], env=clean_env, check=True, timeout=15)
    resource.unlink()
    subprocess.run([str(executable), 'missing-source'], env=clean_env, check=True, timeout=15)
    subprocess.run([str(executable), 'invalid'], env=dict(clean_env, HTTPS_PROXY='unsupported://127.0.0.1'), check=True, timeout=15)
    for scheme in ['http', 'socks5', 'socks5h']:
        errors, requests = [], []
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            listener.listen()
            listener.settimeout(10)

            def serve():
                try:
                    with listener.accept()[0] as connection:
                        connection.settimeout(5)
                        if scheme == 'http':
                            header = b''
                            while b'\r\n\r\n' not in header:
                                header += read_exact(connection, 1)
                            assert header.startswith(b'CONNECT api.github.com:443 '), header
                            requests.append('HTTP CONNECT')
                            connection.sendall(b'HTTP/1.1 502 Synthetic test failure\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
                        else:
                            version, methods = read_exact(connection, 2)
                            assert version == 5
                            assert 0 in read_exact(connection, methods)
                            connection.sendall(b'\x05\x00')
                            version, command, reserved, address = read_exact(connection, 4)
                            assert (version, command, reserved, address) == (5, 1, 0, 3)
                            host = read_exact(connection, read_exact(connection, 1)[0])
                            port = int.from_bytes(read_exact(connection, 2), 'big')
                            assert (host, port) == (b'api.github.com', 443)
                            requests.append('SOCKS5 domain CONNECT')
                            connection.sendall(b'\x05\x02\x00\x01\x00\x00\x00\x00\x00\x00')
                except Exception as error:
                    errors.append(error)

            server = threading.Thread(target=serve, name='synthetic-update-proxy')
            server.start()
            result = subprocess.run([str(executable), scheme], env=dict(clean_env, HTTPS_PROXY=f'{scheme}://127.0.0.1:{listener.getsockname()[1]}'), timeout=15)
            server.join(timeout=11)
            assert result.returncode == 0 and requests and not errors and not server.is_alive(), (scheme, errors, result.returncode)
            print(f'PASS: {scheme} uses {requests[0]} on loopback; no upstream request')
