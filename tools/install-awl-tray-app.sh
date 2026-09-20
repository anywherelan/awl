#!/usr/bin/env bash
set -euo pipefail

# Builds/installs "AWL Tray.app" in /Applications, wrapping the awl-tray binary.
# Usage: ./install-awl-tray-app.sh [/path/to/awl-tray]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_BIN="${1:-./awl-tray}"
ICON_PNG="$SCRIPT_DIR/Icon.png"
APP_NAME="AWL Tray"
APP_DIR="/Applications/${APP_NAME}.app"
BUNDLE_ID="com.anywherelan.awltray"
VERSION="0.19.0"

if [ ! -f "$SRC_BIN" ]; then
  echo "error: binary not found at $SRC_BIN" >&2
  exit 1
fi

mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"

# App icon: Icon.png (the Anywherelan logo, extracted from the binary's own
# embedded web UI assets) ships next to this script and gets converted to .icns
# with the standard macOS icon sizes via the built-in sips/iconutil tools.
if [ -f "$ICON_PNG" ]; then
  ICONSET=$(mktemp -d)/AppIcon.iconset
  mkdir -p "$ICONSET"
  for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$ICON_PNG" --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
    sips -z $((size * 2)) $((size * 2)) "$ICON_PNG" --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$ICONSET" -o "$APP_DIR/Contents/Resources/AppIcon.icns"
  rm -rf "$(dirname "$ICONSET")"
fi

cat > "$APP_DIR/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>awl-tray</string>
	<key>CFBundleIconFile</key>
	<string>AppIcon</string>
	<key>CFBundleIdentifier</key>
	<string>${BUNDLE_ID}</string>
	<key>CFBundleName</key>
	<string>${APP_NAME}</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>${VERSION}</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>LSUIElement</key>
	<true/>
</dict>
</plist>
PLIST

cp "$SRC_BIN" "$APP_DIR/Contents/MacOS/awl-tray-bin"
chmod +x "$APP_DIR/Contents/MacOS/awl-tray-bin"
xattr -d com.apple.quarantine "$APP_DIR/Contents/MacOS/awl-tray-bin" 2>/dev/null || true

# CFBundleExecutable is a plain shell script, not the Go binary itself: the binary's
# embedded LC_BUILD_VERSION (minos 26.0, a toolchain artifact) makes LaunchServices
# refuse to launch it directly as a bundle's main executable ("can't use this version
# of the application..."), even though running it standalone works fine. A script has
# no such version stamp, so LaunchServices has nothing to reject.
#
# It also does its own root elevation via plain `sudo` instead of letting the binary
# self-elevate via `osascript ... with administrator privileges`: that API runs the
# child in a separate authorization session that can't reconnect to the WindowServer,
# so the re-launched root process dies silently before it can draw the menu bar icon.
# `sudo` keeps the same session (confirmed working: `sudo ./awl-tray` from Terminal
# shows the tray icon fine), so we prompt for the password with a plain GUI dialog and
# pipe it into `sudo` ourselves. Once already root, the binary skips its own broken
# elevation path entirely.
cat > "$APP_DIR/Contents/MacOS/awl-tray" <<'LAUNCHER'
#!/bin/bash
DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$DIR/awl-tray-bin"

if [ "$(id -u)" -eq 0 ]; then
  exec "$BIN" "$@"
fi

PASSWORD=$(osascript <<'APPLESCRIPT'
display dialog "AWL Tray needs administrator access to start." default answer "" with title "AWL Tray" with icon caution with hidden answer
text returned of result
APPLESCRIPT
)
if [ -z "$PASSWORD" ]; then
  exit 0
fi

echo "$PASSWORD" | sudo -S -k "$BIN" "$@"
RC=$?
if [ $RC -ne 0 ]; then
  osascript -e 'display alert "AWL Tray" message "Incorrect password, or the app failed to start." as critical' >/dev/null 2>&1
fi
exit $RC
LAUNCHER
chmod +x "$APP_DIR/Contents/MacOS/awl-tray"

# Re-sign ad-hoc for the bundle: the binary's original signature was made for a
# standalone executable and is invalid once moved into a .app bundle structure.
codesign --force --deep -s - "$APP_DIR"

echo "Installed: $APP_DIR"
