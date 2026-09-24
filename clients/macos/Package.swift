// swift-tools-version:5.10
//
// Water's macOS client: a menu-bar app (text pop-up bar + push-to-talk voice)
// that is a pure client of the water daemon. It has no agent logic; every
// capability lives in the daemon behind its Unix socket.
//
// Built without Xcode: `./build.sh` runs `swift build -c release` and
// hand-assembles Water.app. No third-party packages — Apple frameworks only.
import PackageDescription

let package = Package(
    name: "WaterClient",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "Water", targets: ["Water"]),
    ],
    targets: [
        // Everything testable without a GUI, permissions, or a real daemon:
        // the HTTP-over-Unix-socket client, NDJSON parsing, token storage.
        .target(name: "WaterClientCore"),
        // The AppKit app: status item, pop-up panel, hotkeys, voice.
        .executableTarget(name: "Water", dependencies: ["WaterClientCore"]),
        // Run with ./test.sh (swift-testing; see the note there on why a
        // plain `swift test` needs extra flags without Xcode).
        .testTarget(name: "WaterClientCoreTests", dependencies: ["WaterClientCore"]),
    ]
)
