import Foundation

/// The two keys voice mode captures (HotKeys.swift's `CaptureKeys`). `.space`
/// names the talk key by its historical role, not its physical key: it was
/// bare Space until 2026-09-28, when it moved to ⌃V (see `HotKeys.swift`'s
/// `CaptureKeys.register()`) because bare Space ate every ordinary space
/// keystroke elsewhere if voice mode was left on.
public enum CaptureKey: Equatable { case space, escape }

/// Voice mode (owner brief 2026-09-26): ⌃⌥V turns it on and off. While it
/// is on the globe stays up, holding ⌃V talks (release sends, exactly
/// like the old ⌃⌥V hold), and Esc leaves. The talk key and Esc are captured
/// only while it is on: entry emits `registerCaptureKeys` and every way out
/// (toggle, Esc, idle timeout, a failed registration, app shutdown) emits
/// `unregisterCaptureKeys`.
///
/// Pure: no AppKit, no timers, no keys. AppDelegate feeds it the inputs
/// and carries out the returned effects in order (VoiceModeTests).
///
/// Esc is voice mode's one hard stop (2026-09-26 fixes, docs/slices/V.md
/// §10): from any state it cancels the capture (held, or still finishing
/// after release), the voice turn in flight and any speech, then leaves. It
/// never touches an approval: that stays open in the glass tab and the
/// workspace.
public struct VoiceMode: Equatable {
    public enum State: Equatable {
        case off
        /// On and waiting for Space.
        case armed
        /// Space is down: the mic is (being) opened.
        case holding
        /// On, not holding, and something is still going on: a turn in
        /// flight, a reply being spoken, or an approval pinned.
        case processing
    }

    public enum ExitReason: Equatable {
        case toggle, escape, idleTimeout
        /// Space couldn't be captured (another app owns it).
        case captureUnavailable
        /// The app is quitting.
        case shutdown
    }

    public enum Effect: Equatable {
        case registerCaptureKeys
        case unregisterCaptureKeys
        /// Start a push-to-talk hold (VoiceSession.startHold).
        case startHold
        /// End it: the transcript is sent as a voice turn.
        case endHold
        /// Drop the hold without sending anything: a key still held, or a
        /// capture still finishing after release (AppDelegate only cancels
        /// a capture Space started).
        case cancelHold
        /// Cancel the voice turn in flight (its stream is closed; nothing
        /// more is shown or spoken). Esc only.
        case cancelTurn
        /// Silence the reply being spoken. Esc only.
        case stopSpeech
        case showGlobe
        case hideGlobe
        case exited(ExitReason)
    }

    /// Seconds of true idleness (not holding, no capture still finishing,
    /// no turn, not speaking, no approval pinned) before voice mode turns
    /// itself off.
    public static let idleTimeout: TimeInterval = 60

    public private(set) var isOn = false
    public private(set) var isHolding = false
    private var activeTurn: Int?
    private var speaking = false
    private var approvalPinned = false
    private var capturePending = false
    /// When the idle countdown last restarted.
    private var idleSince: Date?

    public init() {}

    public var state: State {
        if !isOn { return .off }
        if isHolding { return .holding }
        return isBusy ? .processing : .armed
    }

    /// Whether the talk key and Esc must be captured right now.
    public var capturesKeys: Bool { isOn }

    var isBusy: Bool { activeTurn != nil || speaking || approvalPinned || capturePending }

    // MARK: user inputs

    /// One captured key event, as CaptureKeys reports it: the talk key
    /// (`.space`, physically ⌃V) down/up is the hold, Esc down leaves. Esc's
    /// release and anything while off do nothing.
    public mutating func key(_ key: CaptureKey, pressed: Bool, now: Date) -> [Effect] {
        switch (key, pressed) {
        case (.space, true): return spaceDown(now: now)
        case (.space, false): return spaceUp(now: now)
        case (.escape, true): return esc(now: now)
        case (.escape, false): return []
        }
    }

    /// ⌃⌥V (or the menu item).
    public mutating func toggle(now: Date) -> [Effect] {
        if isOn { return exit(.toggle) }
        isOn = true
        isHolding = false
        idleSince = now
        return [.registerCaptureKeys, .showGlobe]
    }

    /// Space went down. A second press while held (auto-repeat) is ignored.
    public mutating func spaceDown(now: Date) -> [Effect] {
        guard isOn, !isHolding else { return [] }
        isHolding = true
        idleSince = now
        return [.startHold]
    }

    /// Space came up. Only the release of a press this mode saw counts.
    public mutating func spaceUp(now: Date) -> [Effect] {
        guard isOn, isHolding else { return [] }
        isHolding = false
        idleSince = now
        return [.endHold]
    }

    /// Esc: leaves voice mode at once, from any state. It cancels the
    /// capture (even one that is finishing after Space came up), the turn in
    /// flight and the speech, so nothing more is heard or sent.
    public mutating func esc(now: Date) -> [Effect] {
        guard isOn else { return [] }
        activeTurn = nil
        speaking = false
        capturePending = false
        return exit(.escape)
    }

    // MARK: activity inputs (tracked whether on or off)

    /// A turn was sent (any channel). A new one replaces the old: the
    /// runner cancels the old stream, whose finish then never arrives.
    public mutating func turnStarted(_ id: Int, now: Date) {
        activeTurn = id
    }

    /// A turn's stream ended. Ignored unless it's the one in flight.
    public mutating func turnFinished(_ id: Int, now: Date) {
        guard activeTurn == id else { return }
        activeTurn = nil
        becameQuieter(now)
    }

    /// The runner cancelled whatever was in flight (a new hold barged in).
    public mutating func turnsCancelled(now: Date) {
        guard activeTurn != nil else { return }
        activeTurn = nil
        becameQuieter(now)
    }

    public mutating func speechStarted(now: Date) { speaking = true }

    public mutating func speechIdle(now: Date) {
        guard speaking else { return }
        speaking = false
        becameQuieter(now)
    }

    public mutating func setApprovalPinned(_ pinned: Bool, now: Date) {
        guard pinned != approvalPinned else { return }
        approvalPinned = pinned
        if !pinned { becameQuieter(now) }
    }

    /// A Space capture is still finishing after release: its transcript is
    /// on its way (queued behind a model warm-up, up to
    /// `VoiceSession.warmupBackstop`, longer than `idleTimeout`). That is
    /// not idleness. AppDelegate reports it from the capture's real state
    /// on every tick, so it can't outlive the capture.
    public mutating func setCapturePending(_ pending: Bool, now: Date) {
        guard pending != capturePending else { return }
        capturePending = pending
        if !pending { becameQuieter(now) }
    }

    // MARK: clock and teardown

    /// Turns voice mode off after `idleTimeout` of true idleness. Never
    /// mid-hold, mid-turn, mid-reply or while an approval is pinned.
    public mutating func tick(now: Date) -> [Effect] {
        guard isOn, !isHolding, !isBusy, let since = idleSince,
              now.timeIntervalSince(since) >= Self.idleTimeout else { return [] }
        return exit(.idleTimeout)
    }

    /// Registering Space failed: voice mode can't work, so it leaves.
    public mutating func captureUnavailable() -> [Effect] {
        guard isOn else { return [] }
        return exit(.captureUnavailable)
    }

    /// App termination: release everything.
    public mutating func shutdown() -> [Effect] {
        guard isOn else { return [] }
        return exit(.shutdown)
    }

    // MARK: -

    /// The idle countdown restarts from the moment the last busy thing ends,
    /// so a long turn doesn't end straight into a timeout.
    private mutating func becameQuieter(_ now: Date) {
        if !isBusy { idleSince = now }
    }

    private mutating func exit(_ reason: ExitReason) -> [Effect] {
        var fx: [Effect] = []
        if reason == .escape {
            fx += [.cancelHold, .cancelTurn, .stopSpeech]
        } else if isHolding {
            fx.append(.cancelHold)
        }
        isOn = false
        isHolding = false
        idleSince = nil
        fx += [.unregisterCaptureKeys, .hideGlobe, .exited(reason)]
        return fx
    }
}
