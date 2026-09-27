import Foundation

// The globe's one state machine (docs/slices/V.md §7.4 V-hud, §8), kept
// free of AppKit so every rule is testable on a fake clock
// (ActivityModelTests). Sources/Water/GlobeHUD.swift feeds it and shows the
// globe from its visibility, phase and level; it decides nothing itself.
// `steps` is still tracked but no longer drawn (owner brief 2026-09-26), and
// `isExpanded` is kept only for the tests that pin that bookkeeping.
//
// Rules:
// - It is visible only during an interaction (from hold or text submit
//   until the reply ends plus `dismissDelay`) or while an approval is
//   pinned (owner decision D7).
// - It expands when `steps` or `approvals` is non-empty. A turn with no
//   tool_* and no approval_required never expands.
// - An open approval pins it: no auto-dismiss until every card is resolved.
// - After the turn ends (done/error, and any speech has drained) with no
//   approvals, it collapses and hides at end + `dismissDelay`.
// - Events from any turn but the latest are ignored.
// - A `tool_start` for a web search (`searchTools`) while the turn is open
//   shows `searching` (2026-09-26) until the next `tool_end`, sentence or
//   delta, or the turn's end; after a `tool_end` it goes back to thinking,
//   or to responding if a reply is still being spoken.

/// One tool call the model made during the current turn, as the daemon
/// labelled it (code-built text, never the call's arguments).
public struct ActivityStep: Equatable {
    public enum State: Equatable {
        case running, ok, queued, denied, error
        /// A status a newer daemon sends that this client doesn't know.
        case other(String)

        /// Maps a `tool_end` status. A missing status reads as ok.
        public init(status: String?) {
            switch status?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "" {
            case "", "ok": self = .ok
            case "queued": self = .queued
            case "denied": self = .denied
            case "error": self = .error
            case let s: self = .other(s)
            }
        }
    }

    public var id: String
    public var label: String
    public var state: State

    public init(id: String, label: String, state: State) {
        self.id = id
        self.label = label
        self.state = state
    }
}

/// An open approval envelope, as `approval_required` described it. The
/// `payloadHash` is what a click posts back to
/// POST /v1/approvals/{id}/decision; `readBack` is the daemon's code-built
/// text, shown exactly (as plain text) before anyone clicks.
public struct ApprovalCard: Equatable {
    public var id: String
    public var action: String?
    public var risk: String?
    public var readBack: String?
    public var payloadHash: String?
    /// True while a click's decision request is in flight.
    public var submitting = false
    /// A short, client-written note after a failed click.
    public var note: String?

    public init(id: String, action: String?, risk: String?, readBack: String?, payloadHash: String?) {
        self.id = id
        self.action = action
        self.risk = risk
        self.readBack = readBack
        self.payloadHash = payloadHash
    }
}

public struct ActivityModel: Equatable {
    public enum Phase: Equatable {
        case idle, listening, thinking, responding, needsYou
        /// A web search is running (the globe's searching look).
        case searching
    }

    /// Tools whose run shows as `searching`.
    public static let searchTools: Set<String> = ["research.web"]

    /// Seconds the HUD stays after an interaction ends with nothing pending.
    public static let dismissDelay: TimeInterval = 4
    /// A hold that ended but never produced a turn or a failure stops
    /// "thinking" after this long.
    public static let stallTimeout: TimeInterval = 15
    /// The same while the speech models are still warming up: the
    /// transcript is queued behind the load (VoiceSession.warmupBackstop).
    public static let warmupStallTimeout: TimeInterval = 95
    /// Shown for a step whose label came through empty.
    public static let genericStepLabel = "Working"
    /// How fast an amplitude fades when it stops being updated (seconds
    /// for it to fall to about a third).
    static let levelDecay: TimeInterval = 0.25
    /// The thinking pulse's period.
    static let pulsePeriod: TimeInterval = 1.4

    private enum Activity: Equatable { case idle, listening, thinking, responding, searching }

    public private(set) var isVisible = false
    public private(set) var steps: [ActivityStep] = []
    public private(set) var approvals: [ApprovalCard] = []
    /// When the HUD hides, if nothing else happens first.
    public private(set) var dismissAt: Date?

    private var activity: Activity = .idle
    private var turn = 0
    /// True between `turnSent` and that turn's terminal event.
    private var turnOpen = false
    private var speaking = false
    private var level: Float = 0
    private var levelAt: Date?
    private var thinkingSince: Date?
    /// Set at `holdEnded`, cleared once a turn is sent: the stall guard.
    private var holdEndedAt: Date?
    /// Set by `transcriptWarmingUp`: the stall guard waits longer.
    private var warmingUp = false

    public init() {}

    public var phase: Phase {
        switch activity {
        case .idle: return approvals.isEmpty ? .idle : .needsYou
        case .listening: return .listening
        case .thinking: return .thinking
        case .responding: return .responding
        case .searching: return .searching
        }
    }

    public var isExpanded: Bool { !steps.isEmpty || !approvals.isEmpty }
    public var isPinned: Bool { !approvals.isEmpty }

    // MARK: inputs

    /// The mic opened for push-to-talk.
    public mutating func holdStarted(now: Date) {
        // A hold is barge-in: the app cancels any turn in flight and
        // silences its speech, so that turn's late events are stale now.
        turn += 1
        turnOpen = false
        speaking = false
        begin(now: now)
        activity = .listening
        steps = []
        holdEndedAt = nil
        warmingUp = false
    }

    /// The hold ended while the recognizer is still loading: stay
    /// "thinking" until the (late) transcript arrives, up to
    /// `warmupStallTimeout` instead of `stallTimeout`.
    public mutating func transcriptWarmingUp(now: Date) {
        guard activity == .thinking, !turnOpen else { return }
        warmingUp = true
    }

    /// The hotkey came up; the transcript is on its way.
    public mutating func holdEnded(now: Date) {
        guard activity == .listening else { return }
        activity = .thinking
        thinkingSince = now
        holdEndedAt = now
    }

    /// A hold or toggle ended without a turn (nothing heard, a permission
    /// or mic failure).
    public mutating func cancelled(now: Date) {
        holdEndedAt = nil
        warmingUp = false
        guard !turnOpen else { return }
        activity = .idle
        speaking = false
        settle(now: now)
    }

    /// A turn was posted. Returns its id, which every later `event` and
    /// `turnEnded` for it must carry; anything tagged with an older id is
    /// ignored.
    @discardableResult
    public mutating func turnSent(now: Date) -> Int {
        turn += 1
        begin(now: now)
        activity = .thinking
        thinkingSince = now
        holdEndedAt = nil
        warmingUp = false
        turnOpen = true
        speaking = false
        steps = []
        return turn
    }

    public mutating func event(_ e: TurnEvent, turn t: Int, now: Date) {
        guard t == turn else { return }
        switch e.kind {
        case .delta, .sentence:
            guard turnOpen else { return }
            activity = .responding
        case .toolStart:
            guard let id = Self.nonEmpty(e.stepID) else { return }
            if !steps.contains(where: { $0.id == id }) {
                steps.append(ActivityStep(id: id, label: Self.label(e.label), state: .running))
            }
            isVisible = true
            if turnOpen, activity == .thinking || activity == .responding,
               let tool = Self.nonEmpty(e.tool), Self.searchTools.contains(tool) {
                activity = .searching
            }
        case .toolEnd:
            guard let id = Self.nonEmpty(e.stepID) else { return }
            let state = ActivityStep.State(status: e.status)
            if let i = steps.firstIndex(where: { $0.id == id }) {
                steps[i].state = state
            } else {
                steps.append(ActivityStep(id: id, label: Self.label(e.label), state: state))
            }
            isVisible = true
            if activity == .searching {
                activity = speaking ? .responding : .thinking
                if activity == .thinking { thinkingSince = now }
            }
        case .approvalRequired:
            guard let id = Self.nonEmpty(e.approvalID) else { return }
            let card = ApprovalCard(id: id, action: e.approvalAction, risk: Self.nonEmpty(e.risk),
                                    readBack: Self.nonEmpty(e.readBack), payloadHash: Self.nonEmpty(e.payloadHash))
            if let i = approvals.firstIndex(where: { $0.id == id }) {
                approvals[i].merge(card)
            } else {
                approvals.append(card)
            }
            isVisible = true
            dismissAt = nil
        case .done, .error:
            end(now: now)
        case .ack, .queued, .handoff, .artifact, .unknown:
            break
        }
    }

    /// The stream ended (cleanly or not). Harmless after done/error.
    public mutating func turnEnded(turn t: Int, now: Date) {
        guard t == turn else { return }
        end(now: now)
    }

    /// The speech queue handed a sentence to the engine.
    public mutating func speechStarted(now: Date) {
        guard isVisible, activity != .listening else { return }
        speaking = true
        activity = .responding
    }

    /// The speech queue drained (or was cut off).
    public mutating func speechIdle(now: Date) {
        guard speaking else { return }
        speaking = false
        level = 0
        levelAt = nil
        if !turnOpen, activity == .responding {
            activity = .idle
            settle(now: now)
        }
    }

    /// Microphone amplitude, 0...1 (clamped).
    public mutating func micLevel(_ v: Float, now: Date) {
        guard activity == .listening else { return }
        setLevel(v, now: now)
    }

    /// Speech output amplitude, 0...1 (clamped).
    public mutating func ttsLevel(_ v: Float, now: Date) {
        guard speaking, activity == .responding else { return }
        setLevel(v, now: now)
    }

    /// The envelope is no longer pending (a click, or a re-read showing it
    /// was decided elsewhere, e.g. by a spoken yes).
    public mutating func approvalResolved(_ id: String, now: Date) {
        guard let i = approvals.firstIndex(where: { $0.id == id }) else { return }
        approvals.remove(at: i)
        if approvals.isEmpty, activity == .idle { settle(now: now) }
    }

    public mutating func approvalSubmitting(_ id: String) {
        guard let i = approvals.firstIndex(where: { $0.id == id }) else { return }
        approvals[i].submitting = true
        approvals[i].note = nil
    }

    public mutating func approvalFailed(_ id: String, message: String) {
        guard let i = approvals.firstIndex(where: { $0.id == id }) else { return }
        approvals[i].submitting = false
        approvals[i].note = message
    }

    /// A re-read found the envelope still pending: take its current hash
    /// and read-back. Never adds a card.
    public mutating func approvalRefreshed(_ card: ApprovalCard) {
        guard let i = approvals.firstIndex(where: { $0.id == card.id }) else { return }
        approvals[i].merge(card)
        approvals[i].submitting = false
    }

    /// Esc left voice mode (2026-09-26 fixes): the app cancelled the turn in
    /// flight (its stream never reports an end) and silenced the speech, so
    /// hide now instead of waiting on either, and treat anything still
    /// arriving for that turn as stale. An approval still pinned stays: Esc
    /// never closes one.
    public mutating func dismissNow(now: Date) {
        turn += 1
        turnOpen = false
        speaking = false
        holdEndedAt = nil
        warmingUp = false
        activity = .idle
        steps = []
        level = 0
        levelAt = nil
        dismissAt = nil
        if approvals.isEmpty { isVisible = false }
    }

    /// The clock: runs the auto-dismiss and the stall guard.
    public mutating func tick(now: Date) {
        if activity == .thinking, !turnOpen, let ended = holdEndedAt,
           now.timeIntervalSince(ended) >= (warmingUp ? Self.warmupStallTimeout : Self.stallTimeout) {
            holdEndedAt = nil
            warmingUp = false
            activity = .idle
            settle(now: now)
        }
        if let at = dismissAt, now >= at, approvals.isEmpty, activity == .idle {
            isVisible = false
            dismissAt = nil
            steps = []
        }
    }

    // MARK: output

    /// What the blob should show right now, 0...1: amplitude while
    /// listening or speaking (fading if it stops updating), a time-driven
    /// pulse while thinking, and nothing at rest.
    public func displayLevel(now: Date) -> Float {
        switch activity {
        case .idle:
            return 0
        case .thinking, .searching:
            return Self.thinkingPulse(elapsed: now.timeIntervalSince(thinkingSince ?? now))
        case .listening, .responding:
            guard let at = levelAt else { return 0 }
            let age = max(0, now.timeIntervalSince(at))
            return level * Float(exp(-age / Self.levelDecay))
        }
    }

    /// A smooth 0.2...0.8 breath, independent of any audio.
    public static func thinkingPulse(elapsed: TimeInterval) -> Float {
        let x = sin(2 * Double.pi * elapsed / pulsePeriod)
        return Float(0.5 + 0.3 * x)
    }

    // MARK: -

    private mutating func begin(now: Date) {
        isVisible = true
        dismissAt = nil
        level = 0
        levelAt = nil
    }

    private mutating func end(now: Date) {
        guard turnOpen else { return }
        turnOpen = false
        if speaking { return } // speechIdle finishes it
        activity = .idle
        settle(now: now)
    }

    /// At rest: schedule the dismiss unless an approval pins it.
    private mutating func settle(now: Date) {
        level = 0
        levelAt = nil
        dismissAt = approvals.isEmpty ? now.addingTimeInterval(Self.dismissDelay) : nil
    }

    private mutating func setLevel(_ v: Float, now: Date) {
        level = v.isFinite ? min(1, max(0, v)) : 0
        levelAt = now
    }

    private static func nonEmpty(_ s: String?) -> String? {
        guard let t = s?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        return t
    }

    private static func label(_ s: String?) -> String { nonEmpty(s) ?? genericStepLabel }
}

extension ApprovalCard {
    /// Takes every field `other` knows, keeping what it doesn't.
    mutating func merge(_ other: ApprovalCard) {
        if let v = other.action { action = v }
        if let v = other.risk { risk = v }
        if let v = other.readBack { readBack = v }
        if let v = other.payloadHash { payloadHash = v }
    }
}
