#!/bin/bash
# Builds Wet Paint for pilots (Windows) and for Fridge (macOS).
# Needs Go 1.23+ on PATH (https://go.dev/dl/). Output goes to dist/.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${1:-1.3.1}"
LD="-s -w -X main.version=$VERSION"
rm -rf dist && mkdir -p dist

echo "→ Icons"
if [ -f "Wet Paint Icon.png" ] && command -v sips >/dev/null; then
  mkdir -p assets
  for s in 16 24 32 48 64 128 256; do sips -z $s $s "Wet Paint Icon.png" --out assets/icon-$s.png >/dev/null; done
  go run ./tools/mkicon -version "$VERSION" -out rsrc_windows_amd64.syso assets/icon-{16,24,32,48,64,128,256}.png
fi

echo "→ Windows (pilots)"
# Plain console program: it shows a small status window and closing it quits.
# Not stripped and no hidden-window GUI mode — both look suspicious to antivirus.
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-X main.version=$VERSION" -o dist/WetPaint.exe .
(cd dist && shasum -a 256 WetPaint.exe > WetPaint.exe.sha256.txt)

echo "→ macOS (Fridge)"
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LD" -o dist/wetpaint-arm64 .
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$LD" -o dist/wetpaint-amd64 .
APP="dist/Wet Paint.app/Contents"
mkdir -p "$APP/MacOS"
if command -v lipo >/dev/null; then
  lipo -create -output "$APP/MacOS/WetPaint" dist/wetpaint-arm64 dist/wetpaint-amd64
else
  cp dist/wetpaint-arm64 "$APP/MacOS/WetPaint"
fi
rm dist/wetpaint-arm64 dist/wetpaint-amd64
if command -v iconutil >/dev/null && [ -f "Wet Paint Icon.png" ]; then
  IS=dist/WetPaint.iconset; mkdir -p "$IS" "$APP/Resources"
  for s in 16 32 128 256 512; do
    sips -z $s $s "Wet Paint Icon.png" --out "$IS/icon_${s}x${s}.png" >/dev/null
    sips -z $((s*2)) $((s*2)) "Wet Paint Icon.png" --out "$IS/icon_${s}x${s}@2x.png" >/dev/null
  done
  iconutil -c icns "$IS" -o "$APP/Resources/WetPaint.icns" && rm -rf "$IS"
fi
cat > "$APP/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>Wet Paint</string>
  <key>CFBundleIdentifier</key><string>com.flywetboys.wetpaint</string>
  <key>CFBundleExecutable</key><string>WetPaint</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleIconFile</key><string>WetPaint</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
</dict></plist>
EOF
codesign --force --deep -s - "dist/Wet Paint.app" 2>/dev/null || true   # ad-hoc sign so Apple Silicon will run it
(cd dist && zip -qry "WetPaint-mac.zip" "Wet Paint.app")
ls -lh dist
