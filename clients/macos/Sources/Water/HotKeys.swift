import AppKit
import ApplicationServices
import WaterClientCore

// MARK: - Change the hotkeys here.
//
// Key codes are hardware virtual key codes (Carbon's kVK_* values):
// Space = 49, V = 9, W = 13, K = 40, M = 46. Modifiers must match exactly.
enum HotKeyConfig {
    /// Opens the text pop-up bar.
    static let textBar = HotKey(keyCode: 49, modifiers: [.control, .option], label: "⌃⌥Space")
    /// Push-to-talk: hold to record, release to send.
    static let voice = HotKey(keyCode: 9, modifiers: [.control, .option], label: "⌃⌥V")
    /// Starts or stops capturing a meeting (manual only, never automatic).
    static let meeting = HotKey(keyCode: 46, modifiers: [.control, .option], label: "⌃⌥M")

    static let all = [textBar, voice, meeting]
}

struct HotKey: Equatable {
    let keyCode: UInt16
    let modifiers: NSEvent.ModifierFlags
    let label: String

    func matches(_ e: NSEvent) -> Bool {
        let relevant: NSEvent.ModifierFlags = [.command, .option, .control, .shift]
        return e.keyCode == keyCode && e.modifierFlags.intersection(relevant) == modifiers && !e.isARepeat
    }

    static func == (a: HotKey, b: HotKey) -> Bool { a.keyCode == b.keyCode && a.modifiers == b.modifiers }
}

/// Watches for the hotkeys. The global monitor (keys pressed while
/// another app is frontmost) only receives events once the user has granted
/// Accessibility access; the local monitor (while Water's own panel is key)
/// needs no permission.
final class HotKeyMonitor {
    private var global: Any?
    private var local: Any?
    private var trustPoll: Timer?
    private let handler: (HotKey) -> Void
    var onTrustChange: ((Bool) -> Void)?
    /// Fires when the voice hotkey's key is physically released after a
    /// press of the full hotkey, for push-to-talk. The release is matched by
    /// key code alone, not modifiers: a user commonly releases ⌃/⌥ a beat
    /// before or after the letter key, and it must still register as "stop
    /// recording". A plain 'v' released with no hold in progress is not ours
    /// and passes through untouched.
    var onVoiceKeyUp: (() -> Void)?
    /// Shared by both monitors: the press can arrive through the global one
    /// and its release through the local one once Water's panel is key.
    private var voiceHold = HoldKeyTracker(keyCode: HotKeyConfig.voice.keyCode)

    init(handler: @escaping (HotKey) -> Void) {
        self.handler = handler
    }

    static var isTrusted: Bool { AXIsProcessTrusted() }

    func start() {
        local = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .keyUp]) { [weak self] e in
            guard let self else { return e }
            return self.handle(e, swallow: true)
        }
        installGlobal()
        if !Self.isTrusted { pollForTrust() }
    }

    private func match(_ e: NSEvent) -> HotKey? {
        HotKeyConfig.all.first { $0.matches(e) }
    }

    /// Returns the event to pass through, or nil to swallow it (local monitor only).
    @discardableResult
    private func handle(_ e: NSEvent, swallow: Bool) -> NSEvent? {
        if e.type == .keyUp {
            guard voiceHold.keyUp(keyCode: e.keyCode) == .release else { return e }
            onVoiceKeyUp?()
            return swallow ? nil : e
        }
        let k = match(e)
        if voiceHold.keyDown(keyCode: e.keyCode, isRepeat: e.isARepeat, matchesHotKey: k == HotKeyConfig.voice) == .swallow {
            // Auto-repeat while the voice hotkey is held: keep it out of the
            // panel's text field (a beep per repeat, into the open mic).
            return swallow ? nil : e
        }
        guard let k else { return e }
        handler(k)
        return swallow ? nil : e
    }

    private func installGlobal() {
        if let global { NSEvent.removeMonitor(global) }
        global = NSEvent.addGlobalMonitorForEvents(matching: [.keyDown, .keyUp]) { [weak self] e in
            self?.handle(e, swallow: false)
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
