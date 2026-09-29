#!/usr/bin/env python3
"""Compile the real Swift event structs and verify the Go-to-UI JSON contract."""
from pathlib import Path
import subprocess
import tempfile
root=Path(__file__).resolve().parents[1]
source=(root/'app/main.swift').read_text()
structs=source[source.index('private enum EnginePhase'):source.index('final class AppDelegate')]
trace=(root/'app/TransferLog.swift').read_text()
structs=trace[trace.index('enum TransferStage'):trace.index('final class TransferLogWindow')]+structs
with tempfile.TemporaryDirectory(prefix='bps-ui-contract-') as directory:
    path=Path(directory)/'contract.swift'
    path.write_text('import Foundation\n'+structs+'''
let json = #"{"type":"state","phase":"enabled","base_url":"http://127.0.0.1:17861/v1","api_key":"fake-local-key","requests":0}"#.data(using:.utf8)!
private let event = try JSONDecoder().decode(EngineEvent.self,from:json)
guard event.baseURL == "http://127.0.0.1:17861/v1", event.apiKey == "fake-local-key" else {
    print("FAIL: Go connection fields were lost while decoding UI state"); exit(1)
}
let diagnostic = #"{"type":"trace","requests":0,"trace":{"request_id":"R000001","time":"2026-09-29T12:00:00Z","stage":"streaming","message":"正在接收","elapsed_ms":35000,"quiet_ms":32000,"upstream_bytes":1024,"client_events":0,"tool_calls":0,"attempt":1,"http_status":200}}"#.data(using:.utf8)!
private let log = try JSONDecoder().decode(EngineEvent.self, from: diagnostic)
guard let trace = log.trace, trace.stage == .streaming, trace.quietMS == 32000, trace.line.contains("上游暂无新数据"), !trace.terminal else { exit(1) }
print("PASS: UI decodes connection and trace fields, including upstream silence")
''')
    executable=Path(directory)/'contract'
    subprocess.run(['swiftc','-module-cache-path',str(root/'.build/swift-cache'),str(path),'-o',str(executable)],check=True)
    subprocess.run([str(executable)],check=True)
