// swift-tools-version:5.10
//
// Water's macOS client: a menu-bar app (text pop-up bar + push-to-talk voice)
// that is a pure client of the water daemon. It has no agent logic; every
// capability lives in the daemon behind its Unix socket.
//
// Built without Xcode: `./build.sh` runs `swift build -c release` and
// hand-assembles Water.app. One third-party package: FluidAudio (on-device
// ASR/TTS, Apache-2.0), pinned to an exact version — see slice V's plan
// (docs/slices/V.md) for why exact and not a range. Scoped to the "Water"
// executable target only; WaterClientCore stays free of it.
import PackageDescription

let package = Package(
    name: "WaterClient",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "Water", targets: ["Water"]),
    ],
    dependencies: [
        .package(url: "https://github.com/FluidInference/FluidAudio", exact: "0.17.4"),
    ],
    targets: [
        // Everything testable without a GUI, permissions, or a real daemon:
        // the HTTP-over-Unix-socket client, NDJSON parsing, token storage,
        // and the push-to-talk state machine + hotkey hold tracking
        // (HoldToTalk.swift; the app plugs in the real mic/recognizer).
        .target(name: "WaterClientCore"),
        // The AppKit app: status item, pop-up panel, hotkeys, voice.
        .executableTarget(
            name: "Water",
            dependencies: [
                "WaterClientCore",
                .product(name: "FluidAudio", package: "FluidAudio"),
            ]
        ),
        // Run with ./test.sh (swift-testing; see the note there on why a
        // plain `swift test` needs extra flags without Xcode).
        .testTarget(name: "WaterClientCoreTests", dependencies: ["WaterClientCore"]),
    ]
)
