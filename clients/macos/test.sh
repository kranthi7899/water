#!/bin/sh
# Runs the unit tests: ./test.sh [extra swift test args]
#
# The tests use swift-testing (`import Testing`), because XCTest ships only
# with full Xcode and this package is built with the Command Line Tools alone.
# The CLT's swift-testing framework isn't on SwiftPM's default search path,
# and its `_Testing_Foundation` cross-import overlay ships without module
# files, so both are handled with flags here. With Xcode installed, a plain
# `swift test` works and this script just adds harmless flags.
#
# Opt-in tests:
#   WATER_KEYCHAIN_TEST=1     round-trip against the real login keychain
#   WATER_LIVE_DAEMON_TEST=1  probe the running daemon's /v1/health
set -eu
cd "$(dirname "$0")"

F=/Library/Developer/CommandLineTools/Library/Developer/Frameworks
if [ -d "$F/Testing.framework" ] && [ ! -d /Applications/Xcode.app ]; then
    exec swift test \
        -Xswiftc -F -Xswiftc "$F" \
        -Xswiftc -Xfrontend -Xswiftc -disable-cross-import-overlays \
        -Xlinker -F -Xlinker "$F" -Xlinker -rpath -Xlinker "$F" \
        "$@"
fi
exec swift test "$@"
