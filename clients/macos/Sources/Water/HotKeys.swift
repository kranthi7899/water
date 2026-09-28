import AppKit
import ApplicationServices
import Carbon.HIToolbox
import WaterClientCore

// MARK: - Change the hotkeys here.
//
// Key codes are hardware virtual key codes (Carbon's kVK_* values):
// Space = 49, V = 9, W = 13, K = 40, M = 46. Modifiers must match exactly.
enum HotKeyConfig {
    /// Opens the text pop-up bar.
    static let textBar = HotKey(keyCode: 49, modifiers: [.control, .option], label: "⌃⌥Space")
    /// Voice mode on/off (owner brief 2026-09-26). While it's on, Space is
    /// push-to-talk and Esc leaves — see `CaptureKeys` and `VoiceMode`.
    static let voice = HotKey(keyCode: 9, modifiers: [.control, .option], label: "⌃⌥V")
    /// Starts or stops capturing a meeting (manual only, never automatic).
    static let meeting = HotKey(keyCode: 46, modifiers: [.control, .option], label: "⌃⌥M")
    /// Opens (or hides) the workspace window (Slice V-ui).
    static let workspace = HotKey(keyCode: 13, modifiers: [.control, .option], label: "⌃⌥W")

    static let all = [textBar, voice, meeting, workspace]
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
    /// press of the full hotkey. Unused since voice mode (⌃⌥V toggles; Space
    /// is the hold); the tracker behind it still swallows ⌃⌥V's auto-repeat.
    /// The release is matched by key code alone, not modifiers. A plain 'v'
    /// released with no press in progress is not ours and passes through.
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

/// The talk key (⌃V) and Esc, captured system-wide only while voice mode is
/// on.
///
/// Carbon hot keys (`RegisterEventHotKey`), not the NSEvent monitors above:
/// a global NSEvent monitor can only observe, and the talk key must be
/// swallowed so it never types into the focused app while voice mode holds
/// it. A Carbon hot key does exactly that, needs no Accessibility grant, and
/// reports both pressed and released. The talk key is registered with the
/// Control modifier (⌃V, not bare Space as it was until 2026-09-28 — see
/// `register()`'s own doc comment); Esc is registered with no modifiers, so
/// ⌃⌥Space (the text bar), ⌘Esc and the rest still pass through.
///
/// Every press and release goes to the one handler passed to `init` — a
/// required parameter, so the keys can't be registered with nobody
/// listening. (Before the 2026-09-26 fixes the handler was two optional
/// properties AppDelegate never set: voice mode swallowed the talk key and
/// Esc system-wide and did nothing with them until its 60s idle timeout.)
///
/// Nothing is registered until `register()`, and `unregister()` is
/// idempotent. AppDelegate calls it on every voice-mode exit and on
/// termination; the OS also drops a process's hot keys when it dies, so a
/// crash can't leave the talk key captured either.
final class CaptureKeys {
    /// Carbon hot key ids.
    private enum Key: UInt32 {
        case space = 1, escape = 2
        var captureKey: CaptureKey { self == .space ? .space : .escape }
    }

    /// Main thread: (key, pressed). A held key's auto-repeat may arrive as
    /// more presses: `VoiceMode` ignores those.
    private let onKey: (CaptureKey, Bool) -> Void

    init(onKey: @escaping (CaptureKey, Bool) -> Void) {
        self.onKey = onKey
    }

    private var handler: EventHandlerRef?
    private var refs: [Key: EventHotKeyRef] = [:]
    /// 'WtrV': tells our hot keys from anyone else's in the dispatcher.
    private static let signature: OSType = 0x5774_7256

    var isRegistered: Bool { !refs.isEmpty }

    /// Registers the talk key (⌃V) and Esc. False (with nothing left
    /// registered) when the talk key can't be taken — another app already
    /// holds it as a hot key. Esc failing alone is tolerated: ⌃⌥V and the
    /// idle timeout still exit.
    ///
    /// The talk key was bare Space until 2026-09-28: while voice mode was
    /// on, Space was captured system-wide with no modifier, which meant it
    /// silently ate every ordinary space keystroke in whatever app was
    /// frontmost if voice mode was left on. ⌃V (Control held with V) needs
    /// both keys down together, which nothing types by accident, and Ctrl+V
    /// is not a standard macOS shortcut (paste is ⌘V), so it was free to
    /// take.
    @discardableResult
    func register() -> Bool {
        installHandler()
        guard handler != nil else { return false }
        for (key, code, modifiers) in [(Key.space, kVK_ANSI_V, UInt32(controlKey)), (Key.escape, kVK_Escape, 0)] where refs[key] == nil {
            var ref: EventHotKeyRef?
            let id = EventHotKeyID(signature: Self.signature, id: key.rawValue)
            if RegisterEventHotKey(UInt32(code), modifiers, id, GetEventDispatcherTarget(), 0, &ref) == noErr, let ref {
                refs[key] = ref
            }
        }
        guard refs[.space] != nil else {
            unregister()
            return false
        }
        return true
    }

    /// Releases both keys; safe to call any number of times.
    func unregister() {
        for ref in refs.values { UnregisterEventHotKey(ref) }
        refs.removeAll()
    }

    deinit {
        unregister()
        if let handler { RemoveEventHandler(handler) }
    }

    /// `--voice-keys-selftest` only: sends one synthetic hot-key event for
    /// `key` through the Carbon event dispatcher to this object's handler —
    /// the same path a real press takes after `RegisterEventHotKey` — with
    /// nothing registered, so no real key is ever taken.
    func simulate(_ key: CaptureKey, pressed: Bool) -> OSStatus {
        installHandler()
        var event: EventRef?
        let kind = UInt32(pressed ? kEventHotKeyPressed : kEventHotKeyReleased)
        var st = CreateEvent(nil, OSType(kEventClassKeyboard), kind, 0, EventAttributes(kEventAttributeNone), &event)
        guard st == noErr, let event else { return st }
        defer { ReleaseEvent(event) }
        var id = EventHotKeyID(signature: Self.signature, id: (key == .space ? Key.space : Key.escape).rawValue)
        st = SetEventParameter(event, EventParamName(kEventParamDirectObject), EventParamType(typeEventHotKeyID),
                               MemoryLayout<EventHotKeyID>.size, &id)
        guard st == noErr else { return st }
        return SendEventToEventTarget(event, GetEventDispatcherTarget())
    }

    private func installHandler() {
        guard handler == nil else { return }
        var types = [
            EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed)),
            EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyReleased)),
        ]
        let me = Unmanaged.passUnretained(self).toOpaque()
        InstallEventHandler(GetEventDispatcherTarget(), { _, event, userData in
            guard let event, let userData else { return OSStatus(eventNotHandledErr) }
            return Unmanaged<CaptureKeys>.fromOpaque(userData).takeUnretainedValue().handle(event)
        }, types.count, &types, me, &handler)
    }

    private func handle(_ event: EventRef) -> OSStatus {
        var id = EventHotKeyID()
        let st = GetEventParameter(event, EventParamName(kEventParamDirectObject), EventParamType(typeEventHotKeyID),
                                   nil, MemoryLayout<EventHotKeyID>.size, nil, &id)
        guard st == noErr, id.signature == Self.signature, let key = Key(rawValue: id.id) else {
            return OSStatus(eventNotHandledErr)
        }
        let pressed: Bool
        switch GetEventKind(event) {
        case UInt32(kEventHotKeyPressed): pressed = true
        case UInt32(kEventHotKeyReleased): pressed = false
        default: return OSStatus(eventNotHandledErr)
        }
        // Handled after this Carbon callback returns (a press may unregister
        // the very key being dispatched); the main queue keeps the order.
        DispatchQueue.main.async { [weak self] in
            self?.onKey(key.captureKey, pressed)
        }
        return noErr
    }
}
