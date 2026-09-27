import Foundation

// The glass tab (owner brief 2026-09-26): a floating translucent panel that
// shows what a VOICE request produced — an email draft the model just made,
// or an approval waiting on the CEO. Everything here is pure (no AppKit), so
// the rules about what shows, what an email looks like and when it hides are
// tested (GlassTabTests). Sources/Water/Glass/ draws it.
//
// Rules:
// - Only the voice channel shows it; a typed turn already has its text on
//   screen, and its approval is a line in the text bar (and the workspace).
// - Only these events produce an item: `approval_required`, an `artifact`
//   whose type is `email_draft`, and an `artifact` whose type is `display`
//   (the model called display.show because the CEO would benefit from
//   SEEING something: a list, figures, steps). Everything else is nil. The
//   app itself may also show a short `notice` (a voice-mode hint or error),
//   since the text bar no longer pops up in voice mode.
// - An approval for an outward message (gmail.send_message,
//   twinlink.send_message) is laid out as a message once GET
//   /v1/approvals/{id} returns its payload — and only if that payload has
//   nothing beyond the fields the layout shows. Anything else (an extra
//   field like html_attachment or bcc, or a payload that isn't a message)
//   shows the daemon's exact read-back, which lists every bound field, so
//   the CEO never approves something the tab hid. The same goes for a
//   recipient list too long to draw in full (`maxLaidOutRecipients`): an
//   approval's To/Cc rows are never cut short with "…".
// - An approval item carries the payload_hash its text came with; the
//   decision is bound to that hash (ApprovalActions.decide).
// - Every item has a close button. A draft, display or notice hides on
//   close or after `linger` (paused while the pointer is over the tab); an
//   approval stays until it resolves (decided here, elsewhere, or gone) or
//   is closed — closing only hides it, it stays pending in the workspace.
// - All strings are plain text. The view never interprets them as markup.
// - An approval's recipient `warnings` (Slice W, D4c: a near-miss domain the
//   CEO confirmed, a domain with no mail server) show as a banner above the
//   read-back, one line each, never truncated and never dismissable on their
//   own. They change nothing about Approve: the daemon decides whether a
//   voice yes is enough, and an envelope with warnings is tap-only there, so
//   the tap here is exactly the intended path.
// - `confirmPhrase` (D5b) is set when a spoken yes armed the two-step send;
//   the tab then shows `Say “confirm send” or tap Approve`. It is kept across
//   a re-read only while the envelope is still pending with the same hash,
//   and never shown next to warnings (those envelopes are tap-only).

/// An email's visible fields, as the glass tab lays them out.
public struct EmailFields: Equatable {
    public var to: [String]
    public var cc: [String]
    public var subject: String
    public var body: String

    public init(to: [String], cc: [String] = [], subject: String, body: String) {
        self.to = GlassItem.capList(to)
        self.cc = GlassItem.capList(cc)
        self.subject = GlassItem.cap(subject)
        self.body = GlassItem.cap(body)
    }
}

/// One thing the glass tab shows.
public struct GlassItem: Equatable {
    public enum Kind: Equatable {
        /// An approval envelope waiting on the CEO (`approval_required`).
        case approval(id: String, action: String?, risk: String?, readBack: String, payloadHash: String?)
        /// A draft the model made (an `artifact`); read-only.
        case draft
        /// Something the model chose to show (a `display` artifact from
        /// display.show): a title and plain-text body; read-only.
        case display(title: String)
        /// A short client-written note (voice-mode hint, error); read-only.
        case notice
    }

    public var kind: Kind
    /// Set when the item is laid out as a message.
    public var email: EmailFields?
    /// What to show when `email` is nil (and a one-line summary otherwise).
    public var readBack: String
    /// For a draft: the function that made it (e.g. "gmail.draft_for_review").
    public var tool: String?
    /// True while a Approve/Reject click is in flight.
    public var submitting = false
    /// A short client-written note after a failed click. Plain text.
    public var note: String?
    /// For an approval: the daemon's recipient warnings (plain text, capped,
    /// blanks dropped). Empty for every other item.
    public var warnings: [String]
    /// For an approval: the spoken phrase that completes a two-step send
    /// (one line, capped). Nil otherwise.
    public var confirmPhrase: String?

    public init(kind: Kind, email: EmailFields?, readBack: String, tool: String? = nil,
                warnings: [String] = [], confirmPhrase: String? = nil) {
        self.kind = kind
        self.email = email
        self.readBack = Self.cap(readBack)
        self.tool = tool
        let approval: Bool
        if case .approval = kind { approval = true } else { approval = false }
        self.warnings = approval ? Self.capWarnings(warnings) : []
        self.confirmPhrase = approval ? Self.capPhrase(confirmPhrase) : nil
    }

    // MARK: limits (the daemon caps too; this is the client's own bound)

    public static let maxString = 20_000
    public static let maxList = 50
    /// Seconds a draft, display or notice stays up with nobody touching it
    /// (the count pauses while the pointer is over the tab).
    public static let linger: TimeInterval = 10
    /// After the pointer leaves, at least this long before it hides.
    public static let lingerAfterHover: TimeInterval = 3
    /// The client's own bound on a display title (display.show caps at 120).
    public static let maxTitle = 200
    public static let displayType = "display"
    /// The most recipients (to + cc) an approval is laid out as a message
    /// with, and the most characters they may add up to. The view draws an
    /// approval's recipients in full, never cut with "…"; a longer list
    /// falls back to the read-back, which names every address, so the CEO
    /// never approves a send to someone the tab didn't show.
    public static let maxLaidOutRecipients = 8
    public static let maxLaidOutRecipientChars = 400
    /// The most warnings an approval carries, and the most characters each
    /// may have (the daemon writes a handful of short lines; this is the
    /// client's own bound, so the banner can always draw them in full).
    public static let maxWarnings = 8
    public static let maxWarningChars = 400
    /// The confirm phrase is a few words; anything longer is cut to this.
    public static let maxPhraseChars = 40

    /// The view may cut the To/Cc rows short with "…": only for a read-only
    /// draft. An approval's recipients are always drawn in full.
    public var truncatesRecipients: Bool { !isApproval }

    /// True when every recipient fits the full (untruncated) layout: at most
    /// `maxLaidOutRecipients` non-blank entries adding up to at most
    /// `maxLaidOutRecipientChars` characters.
    static func recipientsFit(_ lists: [String]...) -> Bool {
        let all = lists.joined().map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
        return all.count <= maxLaidOutRecipients && all.reduce(0) { $0 + $1.count } <= maxLaidOutRecipientChars
    }

    static func cap(_ s: String) -> String {
        guard s.utf8.count > maxString else { return s }
        var out = Substring(s)
        while out.utf8.count > maxString { out = out.dropLast(max(1, (out.utf8.count - maxString) / 4)) }
        return String(out)
    }

    static func capWarnings(_ l: [String]) -> [String] {
        Array(l.lazy.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
            .map { $0.count > maxWarningChars ? String($0.prefix(maxWarningChars - 1)) + "…" : $0 }
            .prefix(maxWarnings))
    }

    static func capPhrase(_ s: String?) -> String? {
        guard let line = s?.split(whereSeparator: \.isNewline).first?
            .trimmingCharacters(in: .whitespacesAndNewlines), !line.isEmpty else { return nil }
        return line.count > maxPhraseChars ? String(line.prefix(maxPhraseChars)) : line
    }

    static func capList(_ l: [String]) -> [String] {
        Array(l.lazy.map { cap($0.trimmingCharacters(in: .whitespacesAndNewlines)) }.filter { !$0.isEmpty }.prefix(maxList))
    }

    // MARK: derived

    public var approvalID: String? {
        if case .approval(let id, _, _, _, _) = kind { return id }
        return nil
    }

    public var payloadHash: String? {
        if case .approval(_, _, _, _, let h) = kind { return h }
        return nil
    }

    public var action: String? {
        switch kind {
        case .approval(_, let a, _, _, _): return a
        case .draft, .display: return tool
        case .notice: return nil
        }
    }

    public var risk: String? {
        if case .approval(_, _, let r, _, _) = kind { return r }
        return nil
    }

    public var isApproval: Bool { approvalID != nil }

    /// The hint under an approval armed for a spoken two-step send:
    /// `Say “confirm send” or tap Approve`. Nil without a confirm phrase,
    /// and nil when there are warnings (those envelopes need a tap).
    public var confirmHint: String? {
        guard isApproval, warnings.isEmpty, let p = confirmPhrase else { return nil }
        return "Say \u{201C}\(p)\u{201D} or tap Approve"
    }

    /// Approve/Reject are possible: an approval with a hash, not in flight.
    public var canDecide: Bool {
        guard let h = payloadHash, !h.isEmpty else { return false }
        return !submitting
    }

    /// For an approval whose layout depends on GET /v1/approvals/{id}.
    public var wantsPayload: Bool {
        guard isApproval, email == nil, let a = action else { return false }
        return Self.isOutwardMessage(a)
    }

    /// The tab's header line.
    public var title: String {
        switch kind {
        case .approval(_, let action, _, _, _):
            switch action {
            case "gmail.send_message": return "Send this email?"
            case "twinlink.send_message": return "Send this message?"
            case "gmail.draft_message", "gmail.draft_for_review": return "Create this draft?"
            default: return "Needs your approval"
            }
        case .draft:
            return tool == "gmail.draft_for_review" ? "Draft for your review" : "Email draft"
        case .display(let t):
            return t.isEmpty ? "Water" : t
        case .notice:
            return "Water"
        }
    }

    // MARK: the decision

    public static let outwardMessageActions: Set<String> = ["gmail.send_message", "twinlink.send_message"]
    public static let draftTools: Set<String> = ["gmail.draft_message", "gmail.draft_for_review"]

    public static func isOutwardMessage(_ action: String) -> Bool { outwardMessageActions.contains(action) }

    /// `shouldShow(channel:event:)` from plain parameters: an item only for
    /// a voice turn's `approval_required` (with a valid id), or its
    /// `artifact` of type `email_draft` or `display` (`title`/`body`).
    public static func decide(channel: Channel, kind: TurnEvent.Kind,
                              approvalID: String? = nil, action: String? = nil, risk: String? = nil,
                              readBack: String? = nil, payloadHash: String? = nil,
                              tool: String? = nil, artifactType: String? = nil,
                              email: EmailFields? = nil,
                              title: String? = nil, body: String? = nil,
                              warnings: [String] = [], confirmPhrase: String? = nil) -> GlassItem? {
        guard channel == .voice else { return nil }
        switch kind {
        case .approvalRequired:
            guard let id = nonEmpty(approvalID), WorkspaceAllowlist.isID(id) else { return nil }
            let rb = nonEmpty(readBack) ?? "Approve \(nonEmpty(action) ?? "this action")?"
            return GlassItem(kind: .approval(id: id, action: nonEmpty(action), risk: nonEmpty(risk),
                                             readBack: cap(rb), payloadHash: nonEmpty(payloadHash)),
                             email: nil, readBack: rb, warnings: warnings, confirmPhrase: confirmPhrase)
        default:
            guard isArtifact(kind) else { return nil }
            if artifactType == displayType {
                let t = capTitle(nonEmpty(title) ?? "")
                let b = body?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                guard !t.isEmpty || !b.isEmpty else { return nil }
                return GlassItem(kind: .display(title: t), email: nil, readBack: b, tool: nonEmpty(tool))
            }
            guard artifactType == TurnArtifact.emailDraftType, let email else { return nil }
            return GlassItem(kind: .draft, email: email, readBack: summary(email, draft: true), tool: nonEmpty(tool))
        }
    }

    /// A short client-written note (plain text), e.g. voice mode's first-use
    /// hint or a capture error, shown instead of popping the text bar.
    public static func notice(_ text: String) -> GlassItem {
        GlassItem(kind: .notice, email: nil, readBack: text.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// One line, at most `maxTitle` characters.
    static func capTitle(_ s: String) -> String {
        let line = s.split(whereSeparator: \.isNewline).first.map(String.init) ?? ""
        return line.count > maxTitle ? String(line.prefix(maxTitle)) : line
    }

    static func isArtifact(_ kind: TurnEvent.Kind) -> Bool { kind == .artifact }

    /// The glass tab's one rule on a turn event: an item only for a voice
    /// turn's `approval_required`, or its `artifact` of type `email_draft`
    /// or `display`.
    public static func shouldShow(channel: Channel, event e: TurnEvent) -> GlassItem? {
        let a = e.artifact
        return decide(channel: channel, kind: e.kind, approvalID: e.approvalID, action: e.approvalAction,
                      risk: e.risk, readBack: e.readBack, payloadHash: e.payloadHash, tool: e.tool,
                      artifactType: a?.type,
                      email: a.map { EmailFields(to: $0.to, cc: $0.cc, subject: $0.subject, body: $0.body) },
                      title: a?.title, body: a?.body,
                      warnings: e.warnings, confirmPhrase: e.confirmPhrase)
    }

    /// One line naming a message: "Draft to a@b.c — Re: timing".
    public static func summary(_ e: EmailFields, draft: Bool) -> String {
        let to = e.to.isEmpty ? "no recipient" : e.to.joined(separator: ", ")
        let subj = e.subject.isEmpty ? "(no subject)" : e.subject
        return "\(draft ? "Draft to" : "Email to") \(to), subject '\(subj)'"
    }

    // MARK: approval payloads

    /// Payload keys each outward message's layout shows. A payload with any
    /// other key falls back to the read-back (which lists every field).
    static let gmailKeys: Set<String> = ["to", "cc", "subject", "body"]
    static let twinKeys: Set<String> = ["to_twin", "subject", "payload"]

    /// Lays out an outward message's approval payload (the `payload` object
    /// of GET /v1/approvals/{id}). Nil when the action isn't one, a field has
    /// the wrong shape, or the payload carries anything the layout would hide.
    public static func emailFields(action: String, payload: [String: Any]) -> EmailFields? {
        switch action {
        case "gmail.send_message":
            guard Set(payload.keys).isSubset(of: gmailKeys),
                  let to = strings(payload["to"]), !to.isEmpty,
                  let cc = payload["cc"] == nil ? [] : strings(payload["cc"]),
                  let subject = string(payload["subject"], optional: true),
                  let body = string(payload["body"], optional: true),
                  recipientsFit(to, cc) else { return nil }
            return EmailFields(to: to, cc: cc, subject: subject, body: body)
        case "twinlink.send_message":
            guard Set(payload.keys).isSubset(of: twinKeys),
                  let to = string(payload["to_twin"], optional: false),
                  let subject = string(payload["subject"], optional: true),
                  let body = string(payload["payload"], optional: true),
                  recipientsFit([to]) else { return nil }
            return EmailFields(to: ["twin: \(to)"], cc: [], subject: subject, body: body)
        default:
            return nil
        }
    }

    /// Applies a GET /v1/approvals/{id} body to this approval item: nil when
    /// it resolved (gone, decided, or not this envelope); otherwise the
    /// item with the envelope's current hash and read-back, laid out as a
    /// message when the payload allows. Text and hash always come from the
    /// same response, so a click is bound to what the tab shows.
    ///
    /// Warnings come from the body's `warnings` when it has that field (the
    /// daemon recomputes them on every read); a body without it keeps the
    /// item's own. The confirm phrase survives only while the hash is
    /// unchanged: an edited envelope is a new decision, so the hint goes.
    public func applyingApproval(body: Data?) -> GlassItem? {
        guard let id = approvalID else { return self }
        guard let body,
              let o = (try? JSONSerialization.jsonObject(with: body)) as? [String: Any],
              let state = ApprovalState.parse(o), state.id == id, state.isPending else { return nil }
        let action = state.action ?? self.action
        let rb = state.readBack ?? readBack
        var next = self
        next.kind = .approval(id: id, action: action, risk: state.risk ?? risk,
                              readBack: Self.cap(rb), payloadHash: state.payloadHash)
        next.readBack = Self.cap(rb)
        if let w = o["warnings"] {
            next.warnings = Self.capWarnings((w as? [Any] ?? []).compactMap { $0 as? String })
        }
        if state.payloadHash == nil || state.payloadHash != payloadHash { next.confirmPhrase = nil }
        if let action, let payload = o["payload"] as? [String: Any] {
            next.email = Self.emailFields(action: action, payload: payload)
        } else {
            next.email = nil
        }
        return next
    }

    // MARK: helpers

    static func nonEmpty(_ s: String?) -> String? {
        guard let t = s?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        return t
    }

    static func string(_ v: Any?, optional: Bool) -> String? {
        if v == nil || v is NSNull { return optional ? "" : nil }
        guard let s = v as? String else { return nil }
        if !optional && nonEmpty(s) == nil { return nil }
        return s
    }

    /// A string or a list of strings; nil for any other shape.
    static func strings(_ v: Any?) -> [String]? {
        if let s = v as? String { return s.split(separator: ",").map(String.init) }
        guard let a = v as? [Any] else { return nil }
        var out: [String] = []
        for x in a {
            guard let s = x as? String else { return nil }
            out.append(s)
        }
        return out
    }
}

/// When the tab hides (pure; the controller asks it on a timer).
public struct GlassTabState: Equatable {
    public private(set) var item: GlassItem?
    public private(set) var shownAt: Date?
    /// When a non-approval item hides, unless the pointer is over the tab.
    public private(set) var hideAt: Date?
    /// Seconds left on `hideAt` while the pointer is over the tab.
    private var pausedRemaining: TimeInterval?
    public private(set) var hovering = false

    public init() {}

    public var isVisible: Bool { item != nil }

    /// A new item replaces whatever was showing — except that a draft,
    /// display or notice never covers an approval still waiting (the
    /// approval matters more).
    public mutating func show(_ next: GlassItem, now: Date) {
        if let cur = item, cur.isApproval, !next.isApproval { return }
        var next = next
        // The same envelope again (a spoken yes armed the confirm step): keep
        // its message layout while the hash is unchanged, so the tab doesn't
        // flash back to the read-back until the re-read lands.
        if let cur = item, let id = next.approvalID, cur.approvalID == id, next.email == nil,
           let h = next.payloadHash, cur.payloadHash == h {
            next.email = cur.email
        }
        item = next
        shownAt = now
        pausedRemaining = nil
        hideAt = nil
        if !next.isApproval {
            if hovering { pausedRemaining = GlassItem.linger } else { hideAt = now.addingTimeInterval(GlassItem.linger) }
        }
    }

    /// The current approval, re-read: nil resolves it (the tab hides).
    public mutating func approvalReread(id: String, item next: GlassItem?) {
        guard let cur = item, cur.approvalID == id else { return }
        var n = next
        n?.submitting = cur.submitting
        n?.note = cur.note
        if n == nil { clear() } else { item = n }
    }

    public mutating func resolved(id: String) {
        if item?.approvalID == id { clear() }
    }

    public mutating func submitting(id: String) {
        guard item?.approvalID == id else { return }
        item?.submitting = true
        item?.note = nil
    }

    public mutating func failed(id: String, message: String) {
        guard item?.approvalID == id else { return }
        item?.submitting = false
        item?.note = message
    }

    /// The close button (any item). For an approval this only hides the
    /// tab: nothing is decided, and it stays pending in the workspace.
    public mutating func close() { clear() }

    /// The pointer entered or left the tab: the linger count pauses while
    /// it's inside, and resumes (at least `lingerAfterHover`) when it leaves.
    public mutating func hover(_ inside: Bool, now: Date) {
        guard inside != hovering else { return }
        hovering = inside
        if inside {
            if let at = hideAt {
                pausedRemaining = max(0, at.timeIntervalSince(now))
                hideAt = nil
            }
        } else if let left = pausedRemaining {
            hideAt = now.addingTimeInterval(max(left, GlassItem.lingerAfterHover))
            pausedRemaining = nil
        }
    }

    /// Non-approval items hide at `hideAt`; approvals stay until resolved.
    public mutating func tick(now: Date) {
        guard let cur = item, !cur.isApproval, let at = hideAt else { return }
        if now >= at { clear() }
    }

    private mutating func clear() {
        item = nil
        shownAt = nil
        hideAt = nil
        pausedRemaining = nil
        // A hidden panel reports no mouse-exit: start the next item fresh.
        hovering = false
    }
}

extension UnixSocketClient {
    /// GET /v1/approvals/{id}'s raw body for the glass tab (nil on 404),
    /// through the same request builder the HUD uses.
    public func fetchApprovalBody(id: String, token: String) throws -> Data? {
        let r = try send(try ApprovalActions.get(id: id, token: token))
        if r.status == 404 { return nil }
        guard r.status == 200 else {
            throw WaterClientError.http(status: r.status, body: String(decoding: r.body.prefix(4096), as: UTF8.self))
        }
        return r.body
    }
}
