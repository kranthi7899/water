#!/bin/sh
# Builds Water.app without Xcode: `swift build -c release`, then a
# hand-assembled bundle, code-signed so macOS treats it as a real app for
# privacy permissions (Accessibility, Microphone, Speech Recognition, System
# Audio Recording).
#
#   ./build.sh            -> clients/macos/build/Water.app
#   ./build.sh --install  -> also copies it to ~/Applications/Water.app
#
# Safe to re-run: the bundle is rebuilt from scratch each time.
#
# Signing uses a stable local identity, "Water Local Code Signing": a
# self-signed code-signing certificate created on the first run and reused
# after that. It lives in its own keychain (SIGN_DIR below, with a random
# password kept next to it, both owner-only), not the login keychain, so
# creating and using it never prompts. The certificate is never marked
# trusted; it doesn't need to be. What matters is that macOS pins privacy
# grants to the app's designated requirement ("identifier com.water.client
# and certificate leaf = <this cert>"), which no longer changes between
# builds — so Accessibility and the other grants survive a rebuild. (An
# ad-hoc signature changed on every build and forced a re-grant.)
#
# To start over with a new identity: rm -rf "$SIGN_DIR" (then re-grant once).
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
    <string>Water listens only when you ask it to: during a push-to-talk turn (after you press its voice hotkey), to turn what you say into a request, and while you have started meeting capture (its meeting hotkey), to transcribe your side of the meeting on this Mac. Audio is never saved or sent anywhere.</string>
    <key>NSSpeechRecognitionUsageDescription</key>
    <string>Water transcribes your push-to-talk speech, and meetings you choose to capture, on this Mac only. On-device recognition is required; your audio is never sent to Apple or anyone else.</string>
    <key>NSAudioCaptureUsageDescription</key>
    <string>While you have started meeting capture (Water's meeting hotkey, never automatically), Water listens to your Mac's audio output — the other people on a call — and transcribes it on this Mac only. Only the text goes to your local water daemon, as meeting notes it treats as untrusted, never as instructions; the audio is never saved or sent anywhere. A red icon in the menu bar shows whenever it is listening.</string>
</dict>
</plist>
EOF
plutil -lint "$APP/Contents/Info.plist" >/dev/null

SIGN_NAME="Water Local Code Signing"
SIGN_DIR="${WATER_SIGN_DIR:-$HOME/Library/Application Support/Water/codesign}"
KC="$SIGN_DIR/water-codesign.keychain-db"
PASS_FILE="$SIGN_DIR/keychain-password"

sign_hash() {
    security find-identity -p codesigning "$KC" 2>/dev/null |
        awk -v n="\"$SIGN_NAME\"" 'index($0, n) { print $2; exit }'
}

# codesign finds identities only through the user's keychain search list,
# and `security create-keychain` adds itself to that list, so the signing
# keychain is on it only while signing: the list is restored exactly as it
# was (minus the signing keychain) on every exit path.
ORIG_LIST="$(security list-keychains -d user | grep -vF "$KC" | tr '\n' ' ')"
TMP=""
cleanup() {
    eval "security list-keychains -d user -s $ORIG_LIST"
    [ -z "$TMP" ] || rm -rf "$TMP"
}
trap cleanup EXIT

if [ ! -f "$KC" ] || [ ! -s "$PASS_FILE" ]; then
    echo "creating the local code-signing identity \"$SIGN_NAME\" (one time)"
    rm -rf "$SIGN_DIR"
    mkdir -p "$SIGN_DIR"
    chmod 700 "$SIGN_DIR"
    (umask 077; /usr/bin/openssl rand -hex 32 > "$PASS_FILE")
    PW="$(cat "$PASS_FILE")"
    TMP="$(mktemp -d)"
    /usr/bin/openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
        -subj "/CN=$SIGN_NAME" -keyout "$TMP/key.pem" -out "$TMP/cert.pem" \
        -addext "basicConstraints=critical,CA:FALSE" \
        -addext "keyUsage=critical,digitalSignature" \
        -addext "extendedKeyUsage=critical,codeSigning" 2>/dev/null
    /usr/bin/openssl pkcs12 -export -inkey "$TMP/key.pem" -in "$TMP/cert.pem" \
        -name "$SIGN_NAME" -passout "pass:$PW" -out "$TMP/id.p12"
    security create-keychain -p "$PW" "$KC"
    chmod 600 "$KC"
    security set-keychain-settings "$KC" # no auto-lock timeout
    security unlock-keychain -p "$PW" "$KC"
    security import "$TMP/id.p12" -k "$KC" -P "$PW" -T /usr/bin/codesign >/dev/null
    # Let codesign use the key without a GUI "allow" prompt.
    security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$PW" "$KC" >/dev/null
    rm -rf "$TMP"
    TMP=""
fi
security unlock-keychain -p "$(cat "$PASS_FILE")" "$KC"
HASH="$(sign_hash)"
[ -n "$HASH" ] || { echo "no \"$SIGN_NAME\" identity in $KC; remove $SIGN_DIR and re-run" >&2; exit 1; }

eval "security list-keychains -d user -s $ORIG_LIST \"\$KC\""
codesign --force --deep -s "$HASH" "$APP"
cleanup
trap - EXIT
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
