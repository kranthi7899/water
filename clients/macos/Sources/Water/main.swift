import AppKit

// A plain NSApplication (not a SwiftUI App) so a menu-bar-only, LSUIElement
// app behaves predictably. `--selftest` and `--voice-bench` both run
// headless instead (see SelfTest, VoiceBench). `--render-globe` and
// `--render-glass` render one offscreen frame to a PNG and exit, before any
// app or daemon setup (see GlobeRender, GlassRender). `--globe-selftest`
// drives an unshown globe with synthetic mouse events (GlobeSelfTest).
if CommandLine.arguments.contains("--render-globe") {
    exit(GlobeRender.run(arguments: CommandLine.arguments))
}
if CommandLine.arguments.contains("--render-glass") {
    exit(GlassRender.run(arguments: CommandLine.arguments))
}
if CommandLine.arguments.contains("--voice-keys-selftest") { exit(VoiceKeysSelfTest.run()) }
if CommandLine.arguments.contains("--globe-selftest") { exit(GlobeSelfTest.run()) }
if CommandLine.arguments.contains("--asr-selftest") { exit(AsrSelfTest.run(arguments: CommandLine.arguments)) }
if CommandLine.arguments.contains("--selftest") {
    exit(SelfTest.run(arguments: CommandLine.arguments))
}
if CommandLine.arguments.contains("--voice-bench") {
    exit(VoiceBench.run(arguments: CommandLine.arguments))
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory) // no Dock icon, even when run unbundled
app.run()
