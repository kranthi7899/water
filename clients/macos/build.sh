#!/bin/sh
# Builds Water.app without Xcode: `swift build -c release`, then a
# hand-assembled bundle, ad-hoc code-signed so macOS treats it as a real app
# for privacy permissions (Accessibility, Microphone, Speech Recognition).
#
#   ./build.sh            -> clients/macos/build/Water.app
#   ./build.sh --install  -> also copies it to ~/Applications/Water.app
#
# Safe to re-run: the bundle is rebuilt from scratch each time.
#
# Note: an ad-hoc signature is tied to the exact binary, so after a rebuild
# macOS may treat Water as a new app — re-check its Accessibility switch
# (toggle it off and on), and expect one keychain "allow" prompt.
set -eu
cd "$(dirname "$0")"

INSTALL=0
[ "${1:-}" = "--install" ] && INSTALL=1

swift build -c release
BIN="$(swift build -c release --show-bin-path)/Water"

OUT=build
APP="$OUT/Water.app"
VERSION="0.1.0"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/Water"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>Water</string>
    <key>CFBundleIdentifier</key>
    <string>com.water.client</string>
    <key>CFBundleName</key>
    <string>Water</string>
    <key>CFBundleDisplayName</key>
    <string>Water</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>$VERSION</string>
    <key>CFBundleVersion</key>
    <string>$VERSION</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>LSMinimumSystemVersion</key>
    <string>14.0</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSMicrophoneUsageDescription</key>
    <string>Water listens only while you hold a push-to-talk turn (after you press its voice hotkey), so it can turn what you say into a request for your water daemon.</string>
    <key>NSSpeechRecognitionUsageDescription</key>
    <string>Water transcribes your push-to-talk speech on this Mac only. On-device recognition is required; your audio is never sent to Apple or anyone else.</string>
</dict>
</plist>
EOF
plutil -lint "$APP/Contents/Info.plist" >/dev/null

codesign --force --deep -s - "$APP"
codesign --verify --strict "$APP"
echo "built $(cd "$OUT" && pwd)/Water.app"

if [ "$INSTALL" = 1 ]; then
    DEST="$HOME/Applications"
    mkdir -p "$DEST"
    # Quit a running copy first so the replaced binary isn't in use.
    pkill -x Water 2>/dev/null || true
    rm -rf "$DEST/Water.app"
    cp -R "$APP" "$DEST/Water.app"
    echo "installed $DEST/Water.app (launch: open \"$DEST/Water.app\")"
fi
