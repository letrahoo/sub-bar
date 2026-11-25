#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MACOS_DIR="$ROOT_DIR/macos"
DIST_DIR="${SUBBAR_DIST_DIR:-$ROOT_DIR/dist}"
APP_BUNDLE="$DIST_DIR/SubBar.app"
ZIP_OUT="$DIST_DIR/SubBar-macOS.zip"

mkdir -p "$DIST_DIR"

echo "==> Building SubBar release binary..."
swift build --disable-sandbox --package-path "$MACOS_DIR" -c release ${SUBBAR_SWIFT_ARCHS:-} --scratch-path "${SUBBAR_SWIFT_SCRATCH:-$MACOS_DIR/.build}"

BIN_PATH="$(swift build --disable-sandbox --package-path "$MACOS_DIR" -c release ${SUBBAR_SWIFT_ARCHS:-} --scratch-path "${SUBBAR_SWIFT_SCRATCH:-$MACOS_DIR/.build}" --show-bin-path)/SubBar"

rm -rf "$APP_BUNDLE" "$ZIP_OUT"
mkdir -p "$APP_BUNDLE/Contents/MacOS" "$APP_BUNDLE/Contents/Resources"

cp "$BIN_PATH" "$APP_BUNDLE/Contents/MacOS/SubBar"
chmod +x "$APP_BUNDLE/Contents/MacOS/SubBar"

# Bundle the Go server so the app is fully standalone (build it first: make server)
if [ -x "$DIST_DIR/subbar-server" ]; then
  cp "$DIST_DIR/subbar-server" "$APP_BUNDLE/Contents/Resources/subbar-server"
  chmod +x "$APP_BUNDLE/Contents/Resources/subbar-server"
else
  echo "warning: dist/subbar-server not found; app will be a client only" >&2
fi

cp "$ROOT_DIR/server/internal/web/static/material-icons.LICENSE.txt" "$APP_BUNDLE/Contents/Resources/"

# Everforest app tile with the same three-column bar_chart mark as the popover.
ICONSET_DIR="$(mktemp -d /tmp/SubBarIcon.XXXXXX)/AppIcon.iconset"
mkdir -p "$ICONSET_DIR"
swift - "$ICONSET_DIR" <<'SWIFT_ICON'
import AppKit

let outDir = CommandLine.arguments[1]
let specs: [(String, Int)] = [
    ("icon_16x16.png", 16),
    ("icon_16x16@2x.png", 32),
    ("icon_32x32.png", 32),
    ("icon_32x32@2x.png", 64),
    ("icon_128x128.png", 128),
    ("icon_128x128@2x.png", 256),
    ("icon_256x256.png", 256),
    ("icon_256x256@2x.png", 512),
    ("icon_512x512.png", 512),
    ("icon_512x512@2x.png", 1024)
]

for (name, px) in specs {
    let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
        isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0
    )!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    let s = CGFloat(px)
    let pad = s * 0.08
    let card = NSRect(x: pad, y: pad, width: s - pad * 2, height: s - pad * 2)
    let bgPath = NSBezierPath(roundedRect: card, xRadius: s * 0.22, yRadius: s * 0.22)
    NSColor(srgbRed: 230/255, green: 195/255, blue: 132/255, alpha: 1).setFill()
    bgPath.fill()

    NSColor(srgbRed: 45/255, green: 53/255, blue: 59/255, alpha: 1).setFill()
    let unit = card.width / 24
    for (x, width, height): (CGFloat, CGFloat, CGFloat) in [(5, 3, 9.8), (10.6, 2.8, 14), (16.2, 2.8, 6)] {
        NSBezierPath(rect: NSRect(x: card.minX + x * unit, y: card.minY + 5 * unit,
                                  width: width * unit, height: height * unit)).fill()
    }
    NSGraphicsContext.restoreGraphicsState()
    if let png = rep.representation(using: .png, properties: [:]) {
        try? png.write(to: URL(fileURLWithPath: outDir).appendingPathComponent(name))
    }
}
SWIFT_ICON

/usr/bin/iconutil -c icns "$ICONSET_DIR" -o "$APP_BUNDLE/Contents/Resources/AppIcon.icns"
rm -rf "$(dirname "$ICONSET_DIR")"

APP_VERSION="${SUBBAR_VERSION:-0.2.0}"
APP_BUILD="${SUBBAR_BUILD:-4}"

cat > "$APP_BUNDLE/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>zh_CN</string>
  <key>CFBundleDisplayName</key>
  <string>SubBar</string>
  <key>CFBundleExecutable</key>
  <string>SubBar</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
  <key>CFBundleIdentifier</key>
  <string>io.github.letrahoo.subbar</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>SubBar</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>${APP_VERSION}</string>
  <key>CFBundleVersion</key>
  <string>${APP_BUILD}</string>
  <key>LSMinimumSystemVersion</key>
  <string>14.0</string>
  <key>LSUIElement</key>
  <true/>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>NSAppTransportSecurity</key>
  <dict>
    <key>NSAllowsLocalNetworking</key>
    <true/>
  </dict>
</dict>
</plist>
EOF

codesign --force --deep --sign - "$APP_BUNDLE"
codesign --verify --deep --strict "$APP_BUNDLE"
/usr/bin/ditto -c -k --sequesterRsrc --keepParent "$APP_BUNDLE" "$ZIP_OUT"

# Stage only installable files (not the server build or ZIP) inside the disk image.
DMG_STAGE="$(mktemp -d)"
trap 'rm -rf "$DMG_STAGE"' EXIT
/usr/bin/ditto "$APP_BUNDLE" "$DMG_STAGE/SubBar.app"
ln -s /Applications "$DMG_STAGE/Applications"
DMG_OUT="$DIST_DIR/SubBar-macOS.dmg"
hdiutil create -ov -volname SubBar -srcfolder "$DMG_STAGE" -fs HFS+ -format UDZO "$DMG_OUT"
hdiutil verify "$DMG_OUT"

du -sh "$APP_BUNDLE" "$ZIP_OUT" "$DMG_OUT"
