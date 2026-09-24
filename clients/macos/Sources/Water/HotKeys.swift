import AppKit
import ApplicationServices

// MARK: - Change the hotkeys here.
//
// Key codes are hardware virtual key codes (Carbon's kVK_* values):
// Space = 49, V = 9, W = 13, K = 40. Modifiers must match exactly.
enum HotKeyConfig {
    /// Opens the text pop-up bar.
    static let textBar = HotKey(keyCode: 49, modifiers: [.control, .option], label: "⌃⌥Space")
    /// Push-to-talk: press once to start listening, again to send.
    static let voice = HotKey(keyCode: 9, modifiers: [.control, .option], label: "⌃⌥V")
}

struct HotKey {
    let keyCode: UInt16
    let modifiers: NSEvent.ModifierFlags
    let label: String

    func matches(_ e: NSEvent) -> Bool {
        let relevant: NSEvent.ModifierFlags = [.command, .option, .control, .shift]
        return e.keyCode == keyCode && e.modifierFlags.intersection(relevant) == modifiers && !e.isARepeat
    }
}

/// Watches for the two hotkeys. The global monitor (keys pressed while
/// another app is frontmost) only receives events once the user has granted
/// Accessibility access; the local monitor (while Water's own panel is key)
/// needs no permission.
final class HotKeyMonitor {
    private var global: Any?
    private var local: Any?
    private var trustPoll: Timer?
    private let handler: (HotKey) -> Void
    var onTrustChange: ((Bool) -> Void)?

    init(handler: @escaping (HotKey) -> Void) {
        self.handler = handler
    }

    static var isTrusted: Bool { AXIsProcessTrusted() }

    func start() {
        local = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] e in
            guard let self, let k = self.match(e) else { return e }
            self.handler(k)
            return nil // swallow it
        }
        installGlobal()
        if !Self.isTrusted { pollForTrust() }
    }

    private func match(_ e: NSEvent) -> HotKey? {
        [HotKeyConfig.textBar, HotKeyConfig.voice].first { $0.matches(e) }
    }

    private func installGlobal() {
        if let global { NSEvent.removeMonitor(global) }
        global = NSEvent.addGlobalMonitorForEvents(matching: .keyDown) { [weak self] e in
            guard let self, let k = self.match(e) else { return }
            self.handler(k)
        }
    }

    /// A monitor installed before the grant doesn't start receiving events by
    /// itself, so once trust flips on, reinstall it — no relaunch needed.
    private func pollForTrust() {
        trustPoll = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] t in
            guard Self.isTrusted else { return }
            t.invalidate()
            self?.installGlobal()
            self?.onTrustChange?(true)
        }
    }
}
