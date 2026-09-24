import AppKit

// A plain NSApplication (not a SwiftUI App) so a menu-bar-only, LSUIElement
// app behaves predictably. `--selftest` runs headless instead (see SelfTest).
if CommandLine.arguments.contains("--selftest") {
    exit(SelfTest.run(arguments: CommandLine.arguments))
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory) // no Dock icon, even when run unbundled
app.run()
