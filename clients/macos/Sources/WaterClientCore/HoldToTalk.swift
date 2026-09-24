import Foundation

// The push-to-talk state machine and hotkey hold tracking, kept free of
// AppKit/AVFoundation/Speech so they can be driven from tests. The Water app
// supplies the real permission prompt, microphone+recognizer and timer
// behind the small protocols below (Voice.swift).

/// Asks for whatever access capture needs. Calls back once, on the caller's
/// (main) thread: nil when granted, else a user-facing message.
public protocol VoicePermissionGate: AnyObject {
    func request(_ done: @escaping (String?) -> Void)
}

/// One recognition result, delivered on the main thread.
public enum SpeechResult: Equatable {
    case partial(String)
    case final(String)
    case error(String)
}

/// The microphone plus an on-device recognizer, as one capture at a time.
public protocol SpeechCapture: AnyObject {
    /// Opens the mic and starts recognizing. `onResult` runs on the main
    /// thread and may keep firing after `cancel()` (a late callback from the
    /// recognizer); VoiceSession filters those out. Throws a user-facing error.
    func start(onResult: @escaping (SpeechResult) -> Void) throws
    /// Closes the mic; the recognizer finishes what it already has.
    func endAudio()
    /// Stops everything now.
    func cancel()
}

/// Runs `work` on the main thread after `seconds`.
public protocol VoiceScheduler: AnyObject {
    func after(_ seconds: TimeInterval, _ work: @escaping () -> Void)
}

public struct CaptureStartError: Error, LocalizedError, Equatable {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

/// Push-to-talk: hold the hotkey to record, release to send. `startHold`/
/// `endHold` are the hold gesture; `toggle` is a plain click (the menu item),
/// where there's no hold to track. Main thread only.
///
/// Every capture gets a generation number. Recognizer callbacks and the
/// finish backstop carry the generation they were made for and are ignored
/// once it's stale, so a quick second hold can never be delivered, cut short
/// or failed by the first one's leftovers.
public final class VoiceSession {
    public enum State: Equatable {
        case idle
        /// Waiting on the permission step; the mic is not open yet.
        case starting
        case listening
        /// Mic closed, waiting for the recognizer's final result.
        case finishing
    }
    public enum Mode { case hold, toggle }

    public private(set) var state: State = .idle
    public private(set) var mode: Mode = .hold

    /// How long after release to deliver whatever was recognized if no final
    /// result has arrived. endAudio() leaves only buffered audio to decode,
    /// so isFinal normally lands well inside this.
    public var backstop: TimeInterval = 1.5
    /// Reported (through onFailure) when the key came up before the mic
    /// opened — e.g. the user let go to answer a permission prompt.
    public var releasedEarlyMessage = "Hold the voice hotkey while you talk, and release it to send."

    public var onListening: (() -> Void)?
    public var onPartial: ((String) -> Void)?
    /// The final transcript (non-empty), ready to send as a voice turn.
    public var onTranscript: ((String) -> Void)?
    /// A user-facing failure; the session is back to idle.
    public var onFailure: ((String) -> Void)?

    private let permissions: VoicePermissionGate
    private let capture: SpeechCapture
    private let scheduler: VoiceScheduler
    private var generation = 0
    private var releasedWhileStarting = false
    private var transcript = ""
    private var delivered = false

    public init(permissions: VoicePermissionGate, capture: SpeechCapture, scheduler: VoiceScheduler) {
        self.permissions = permissions
        self.capture = capture
        self.scheduler = scheduler
    }

    /// Menu click: start, or stop a running capture.
    public func toggle() {
        switch state {
        case .idle: begin(.toggle)
        case .listening: finish()
        case .starting, .finishing: break
        }
    }

    /// Hotkey went down. Starts a hold from idle. A press while a
    /// menu-started capture is listening stops it (its release is then a
    /// no-op); anything else — a repeat, a press mid-hold — is ignored.
    public func startHold() {
        switch state {
        case .idle: begin(.hold)
        case .listening where mode == .toggle: finish()
        default: break
        }
    }

    /// Hotkey came up. Ends a hold; if the mic isn't open yet, remembers the
    /// release so it never opens. Ignored for menu-started captures, which
    /// no key release belongs to.
    public func endHold() {
        guard mode == .hold else { return }
        switch state {
        case .starting: releasedWhileStarting = true
        case .listening: finish()
        case .idle, .finishing: break
        }
    }

    /// Drops whatever is in progress without delivering anything.
    public func cancel() {
        guard state != .idle else { return }
        teardown()
    }

    // MARK: -

    private func begin(_ m: Mode) {
        mode = m
        state = .starting
        releasedWhileStarting = false
        generation += 1
        let gen = generation
        permissions.request { [weak self] problem in
            guard let self, self.generation == gen, self.state == .starting else { return }
            if let problem { return self.fail(problem) }
            if self.releasedWhileStarting {
                self.releasedWhileStarting = false
                return self.fail(self.releasedEarlyMessage)
            }
            self.startCapture()
        }
    }

    private func startCapture() {
        generation += 1
        let gen = generation
        transcript = ""
        delivered = false
        do {
            try capture.start { [weak self] result in
                guard let self, self.generation == gen else { return }
                self.handle(result)
            }
        } catch {
            capture.cancel()
            return fail((error as? LocalizedError)?.errorDescription ?? "\(error)")
        }
        state = .listening
        onListening?()
    }

    private func handle(_ result: SpeechResult) {
        switch result {
        case .partial(let t):
            transcript = t
            if !delivered { onPartial?(t) }
        case .final(let t):
            transcript = t
            if !delivered { onPartial?(t) }
            deliver()
        case .error(let m):
            if state == .finishing {
                deliver()
            } else if state == .listening {
                teardown()
                onFailure?("Speech recognition stopped: \(m)")
            }
        }
    }

    private func finish() {
        state = .finishing
        capture.endAudio()
        let gen = generation
        scheduler.after(backstop) { [weak self] in
            guard let self, self.generation == gen else { return }
            self.deliver()
        }
    }

    private func deliver() {
        guard !delivered, state == .finishing else { return }
        delivered = true
        let text = transcript.trimmingCharacters(in: .whitespacesAndNewlines)
        teardown()
        if text.isEmpty {
            onFailure?("Didn't catch anything — try again.")
        } else {
            onTranscript?(text)
        }
    }

    /// Back to idle; everything scheduled for the old capture goes stale.
    private func teardown() {
        capture.cancel()
        generation += 1
        releasedWhileStarting = false
        state = .idle
    }

    private func fail(_ message: String) {
        generation += 1
        releasedWhileStarting = false
        state = .idle
        onFailure?(message)
    }
}

/// Tracks a hold-to-talk hotkey across keyDown/keyUp, so only the release
/// of a press that actually started a hold counts, and the OS's auto-repeat
/// keyDowns during the hold are swallowed instead of reaching a text field.
/// The release is matched by key code alone: users often let go of the
/// modifiers a beat before the letter.
public struct HoldKeyTracker {
    public enum Decision: Equatable {
        /// Not ours: let the event through.
        case pass
        /// Ours, but nothing to do (an auto-repeat): swallow it where possible.
        case swallow
        /// The hotkey went down: start.
        case press
        /// The held key came up: stop.
        case release
    }

    public let keyCode: UInt16
    public private(set) var held = false

    public init(keyCode: UInt16) { self.keyCode = keyCode }

    /// `matchesHotKey`: key code and modifiers match and it isn't a repeat.
    public mutating func keyDown(keyCode k: UInt16, isRepeat: Bool, matchesHotKey: Bool) -> Decision {
        if matchesHotKey {
            held = true
            return .press
        }
        if k == keyCode, held, isRepeat { return .swallow }
        return .pass
    }

    public mutating func keyUp(keyCode k: UInt16) -> Decision {
        guard k == keyCode, held else { return .pass }
        held = false
        return .release
    }
}
