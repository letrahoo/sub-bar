#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${SUBBAR_DIST_DIR:-$ROOT_DIR/dist}"
APP_BUNDLE="$DIST_DIR/SubBar.app"
DMG_OUT="$DIST_DIR/SubBar-macOS.dmg"

echo "==> [verify-app] Checking bundle layout at $APP_BUNDLE"
test -d "$APP_BUNDLE"
test -x "$APP_BUNDLE/Contents/MacOS/SubBar"
test -x "$APP_BUNDLE/Contents/Resources/subbar-server"
test -f "$APP_BUNDLE/Contents/Resources/AppIcon.icns"
test -f "$APP_BUNDLE/Contents/Resources/material-icons.LICENSE.txt"

echo "==> [verify-app] Validating Info.plist"
plutil -lint "$APP_BUNDLE/Contents/Info.plist" >/dev/null
bundle_id="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$APP_BUNDLE/Contents/Info.plist")"
short_ver="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP_BUNDLE/Contents/Info.plist")"
if [ "$bundle_id" != "io.github.letrahoo.subbar" ]; then
  echo "ERROR: unexpected CFBundleIdentifier: $bundle_id" >&2
  exit 1
fi
if [ -z "$short_ver" ]; then
  echo "ERROR: empty CFBundleShortVersionString" >&2
  exit 1
fi
echo "    Bundle ID: $bundle_id, Version: $short_ver"

echo "==> [verify-app] Verifying code signature"
codesign --verify --deep --strict "$APP_BUNDLE"

if [ "${SUBBAR_EXPECT_UNIVERSAL:-0}" = "1" ]; then
  echo "==> [verify-app] Checking universal (arm64 + x86_64) architectures"
  for bin in "$APP_BUNDLE/Contents/MacOS/SubBar" "$APP_BUNDLE/Contents/Resources/subbar-server"; do
    archs="$(lipo -archs "$bin")"
    echo "    $(basename "$bin"): $archs"
    case " $archs " in
      *" arm64 "*) ;;
      *) echo "ERROR: $bin missing arm64 slice ($archs)" >&2; exit 1 ;;
    esac
    case " $archs " in
      *" x86_64 "*) ;;
      *) echo "ERROR: $bin missing x86_64 slice ($archs)" >&2; exit 1 ;;
    esac
  done
fi

if [ -f "$DMG_OUT" ]; then
  echo "==> [verify-app] Verifying DMG checksum"
  hdiutil verify "$DMG_OUT" >/dev/null
fi

echo "==> [verify-app] Running E2E acceptance against bundled subbar-server"
SUBBAR_SERVER_BIN="$APP_BUNDLE/Contents/Resources/subbar-server" "$ROOT_DIR/scripts/verify-e2e.sh"

echo "==> [verify-app] App bundle verification passed."
