#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
GO_BIN="${GO_BIN:-go}"
python3 scripts/check_upstream.py
VERSION="$(tr -d '[:space:]' < VERSION)"
export MACOSX_DEPLOYMENT_TARGET=13.0
export GOOS=darwin GOARCH=arm64 CGO_ENABLED=0
APP="${BPS_APP_PATH:-dist/AstraBridge.app}"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" .build
"$GO_BIN" build -mod=vendor -trimpath -o "$APP/Contents/Resources/bps-local" ./cmd/bps-local
swiftc -target arm64-apple-macosx13.0 -swift-version 5 -O -module-cache-path .build/swift-cache app/Interface.swift app/main.swift -o "$APP/Contents/MacOS/AstraBridge" -framework AppKit -framework CFNetwork
swiftc -module-cache-path .build/swift-cache scripts/make_icon.swift -o .build/make-icon
.build/make-icon .build/AstraBridge.iconset
iconutil -c icns .build/AstraBridge.iconset -o "$APP/Contents/Resources/AstraBridge.icns"
cp README.md LICENSE NOTICE VERSION "$APP/Contents/Resources/"
cp upstream-manifest.json "$APP/Contents/Resources/"
mkdir -p "$APP/Contents/Resources/docs"
cp docs/source-provenance.md docs/native-images.md "$APP/Contents/Resources/docs/"
cp -R licenses "$APP/Contents/Resources/"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>AstraBridge</string>
<key>CFBundleIdentifier</key><string>local.bps.desktop</string>
<key>CFBundleName</key><string>AstraBridge</string>
<key>CFBundleDisplayName</key><string>AstraBridge</string>
<key>CFBundleVersion</key><string>12</string>
<key>CFBundleShortVersionString</key><string>${VERSION}</string>
<key>CFBundleIconFile</key><string>AstraBridge</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
codesign --force --sign - "$APP/Contents/Resources/bps-local"
codesign --force --sign - "$APP"
printf 'Built: %s/%s\n' "$PWD" "$APP"
