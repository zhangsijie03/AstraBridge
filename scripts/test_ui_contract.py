#!/usr/bin/env python3
"""Compile the real Swift event structs and verify the Go-to-UI JSON contract."""
from pathlib import Path
import subprocess
import tempfile
root=Path(__file__).resolve().parents[1]
source=(root/'app/main.swift').read_text()
structs=source[source.index('private enum EnginePhase'):source.index('final class AppDelegate')]
with tempfile.TemporaryDirectory(prefix='bps-ui-contract-') as directory:
    path=Path(directory)/'contract.swift'
    path.write_text('import Foundation\n'+structs+'''
let json = #"{"type":"state","phase":"enabled","base_url":"http://127.0.0.1:17861/v1","api_key":"fake-local-key","requests":0}"#.data(using:.utf8)!
private let event = try JSONDecoder().decode(EngineEvent.self,from:json)
guard event.baseURL == "http://127.0.0.1:17861/v1", event.apiKey == "fake-local-key" else {
    print("FAIL: Go connection fields were lost while decoding UI state"); exit(1)
}
print("PASS: UI decodes Base URL and local API Key")
''')
    executable=Path(directory)/'contract'
    subprocess.run(['swiftc','-module-cache-path',str(root/'.build/swift-cache'),str(path),'-o',str(executable)],check=True)
    subprocess.run([str(executable)],check=True)
