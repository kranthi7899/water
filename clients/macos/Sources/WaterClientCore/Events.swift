import Foundation

/// The delivery channel for a turn, exactly as the daemon's handleTurn
/// accepts it (internal/gateway/handlers.go). Only `voice` gets `sentence`
/// events; every channel gets `delta`s.
public enum Channel: String {
    case cli
    case voice
    case textBar = "text-bar"
}

/// One NDJSON event from POST /v1/turns, mirroring runtime.Event:
/// `{"kind": "...", "text": "...", "approval_id": "...", "action": "...",
/// "risk": "...", "payload_hash": "...", "read_back": "...", "error": "...",
/// "step_id": "...", "tool": "...", "label": "...", "status": "...",
/// "warnings": ["..."], "confirm_phrase": "...",
/// "artifact": {"type": "...", "to": [...], "cc": [...], "subject": "...", "body": "..."}}`.
///
/// `approval_required` carries `action`, `risk`, `payload_hash` (enough to
/// decide it via POST /v1/approvals/{id}/decision) and `read_back`, the
/// code-built text to show or speak before asking yes or no. It is
/// best-effort: it covers approvals this turn staged (a model tool call
/// queued while its model ran, or a Tier-0 write intent) and a spoken yes
/// that needs a tap. Refresh GET /v1/approvals on `done` for everything else.
/// It may also carry `warnings` (Slice W, D4c: code-built recipient
/// warnings such as a mail domain with no mail server; show them before the
/// CEO decides) and `confirm_phrase` (D5b: set, to "confirm send", when a
/// spoken yes on a send armed the two-step confirmation; saying that phrase
/// within 30s sends it, a tap still works).
///
/// `ack` is silent (Slice W, D3): the daemon's handoff is an `ack` with no
/// spoken text on every channel. Only a voice turn's `sentence` is ever
/// spoken (`spokenText(channel:)`), so the globe stays THINKING until the
/// first real model sentence.
///
/// `tool_start`/`tool_end` bracket one tool call the model made during this
/// turn (V-events): the same `step_id` on both, the function id in `tool`
/// (empty when the daemon didn't recognise it), a code-built `label` that
/// never contains the call's arguments, and on `tool_end` a `status` of
/// ok, queued, denied or error. The label is fixed text by construction;
/// show it as plain text anyway, like every other string here.
///
/// `artifact` follows a level-D draft call that ran successfully during this
/// turn (gmail.draft_message, gmail.draft_for_review), or a display.show
/// call (something the model chose to put on screen): the `step_id` and
/// `tool` of that call's step, and `artifact`, the draft or display itself
/// as the model wrote it (see TurnArtifact). It arrives between that step's
/// `tool_start` and `tool_end`, never for a denied, queued or failed call.
///
/// Every stream ends with exactly one terminal event, `done` or `error`;
/// nothing follows it (the daemon's turnSink closes after it).
public struct TurnEvent: Decodable, Equatable {
    public enum Kind: Equatable {
        case ack, delta, sentence, approvalRequired
        /// This turn is waiting for another turn's model call to finish
        /// (runtime.EventQueued): at most once, after ack, before any delta.
        case queued
        /// A tool call started / ended (see the type's doc comment).
        case toolStart, toolEnd
        /// A draft the model just made, or a display it chose to show (see
        /// the type's doc comment).
        case artifact
        /// The router's handoff acknowledgement on a non-voice channel
        /// (Design §11.4 step 6, internal/nervous/nervous.go's emitHandoff
        /// doc comment): the daemon sends a silent `ack` for this instead
        /// (Slice W D3; no code path emits the literal "handoff" kind), but
        /// this decodes it the moment one does, matching the Go CLI's
        /// own R-26 treatment (`cmd_ask.go`: an unrecognized-but-documented
        /// future kind, safely ignored rather than erroring).
        case handoff
        case done, error
        case unknown(String)

        init(_ raw: String) {
            switch raw {
            case "ack": self = .ack
            case "delta": self = .delta
            case "sentence": self = .sentence
            case "approval_required": self = .approvalRequired
            case "queued": self = .queued
            case "tool_start": self = .toolStart
            case "tool_end": self = .toolEnd
            case "artifact": self = .artifact
            case "handoff": self = .handoff
            case "done": self = .done
            case "error": self = .error
            default: self = .unknown(raw)
            }
        }
    }

    public var kind: Kind
    public var text: String?
    public var approvalID: String?
    public var action: String?
    public var risk: String?
    public var payloadHash: String?
    /// For `approval_required`: approvals.ReadBack, built by the daemon's
    /// code from the envelope's payload. Nil from an older daemon.
    public var readBack: String?
    public var error: String?
    /// For `tool_start`/`tool_end`: the step both events share.
    public var stepID: String?
    /// For `tool_start`/`tool_end`: the function id, e.g. "gcal.list_events".
    public var tool: String?
    /// For `tool_start`/`tool_end`: what to show while it runs.
    public var label: String?
    /// For `tool_end`: "ok", "queued", "denied" or "error" (kept as the raw
    /// string, so a future status decodes too).
    public var status: String?
    /// For `artifact`: what the draft call made. Nil on every other kind,
    /// and from an older daemon.
    public var artifact: TurnArtifact?
    /// For `approval_required`: recipient warnings (Slice W, D4c), plain
    /// text, blank entries dropped. Empty when absent, from an older daemon,
    /// or when the field has the wrong shape (a non-string entry is skipped).
    public var warnings: [String]
    /// For `approval_required`: the phrase that completes a spoken two-step
    /// send ("confirm send", Slice W D5b). Nil when absent or blank.
    public var confirmPhrase: String?

    public init(kind: Kind, text: String? = nil, approvalID: String? = nil,
                action: String? = nil, risk: String? = nil, payloadHash: String? = nil,
                readBack: String? = nil, error: String? = nil,
                stepID: String? = nil, tool: String? = nil, label: String? = nil, status: String? = nil,
                artifact: TurnArtifact? = nil, warnings: [String] = [], confirmPhrase: String? = nil) {
        self.kind = kind
        self.text = text
        self.approvalID = approvalID
        self.action = action
        self.risk = risk
        self.payloadHash = payloadHash
        self.readBack = readBack
        self.error = error
        self.stepID = stepID
        self.tool = tool
        self.label = label
        self.status = status
        self.artifact = artifact
        self.warnings = warnings
        self.confirmPhrase = confirmPhrase
    }

    private enum CodingKeys: String, CodingKey {
        case kind, text, error, action, risk, tool, label, status, artifact, warnings
        case confirmPhrase = "confirm_phrase"
        case approvalID = "approval_id"
        case payloadHash = "payload_hash"
        case readBack = "read_back"
        case stepID = "step_id"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = Kind(try c.decode(String.self, forKey: .kind))
        text = try c.decodeIfPresent(String.self, forKey: .text)
        approvalID = try c.decodeIfPresent(String.self, forKey: .approvalID)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        risk = try c.decodeIfPresent(String.self, forKey: .risk)
        payloadHash = try c.decodeIfPresent(String.self, forKey: .payloadHash)
        readBack = try c.decodeIfPresent(String.self, forKey: .readBack)
        error = try c.decodeIfPresent(String.self, forKey: .error)
        stepID = try c.decodeIfPresent(String.self, forKey: .stepID)
        tool = try c.decodeIfPresent(String.self, forKey: .tool)
        label = try c.decodeIfPresent(String.self, forKey: .label)
        status = try c.decodeIfPresent(String.self, forKey: .status)
        // A malformed artifact object drops only the artifact, never the
        // event (the kind still tells the client a draft was made).
        artifact = (try? c.decodeIfPresent(TurnArtifact.self, forKey: .artifact)) ?? nil
        // Lenient like `artifact`: a malformed field drops only itself.
        warnings = Self.lenientStrings(c, .warnings)
        let phrase = ((try? c.decodeIfPresent(String.self, forKey: .confirmPhrase)) ?? nil)?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        confirmPhrase = (phrase?.isEmpty ?? true) ? nil : phrase
    }

    /// A list of strings, leniently: a missing field or any other shape is
    /// [], a non-string entry is skipped, entries are trimmed and blank ones
    /// dropped.
    private static func lenientStrings(_ c: KeyedDecodingContainer<CodingKeys>, _ key: CodingKeys) -> [String] {
        guard let list = try? c.decodeIfPresent([LenientString].self, forKey: key) else { return [] }
        return list.compactMap { $0.value?.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
    }

    /// The text the voice path speaks for this event: a voice turn's
    /// `sentence`, and nothing else. An `ack` (the daemon's silent handoff,
    /// Slice W D3) or a `handoff` never speaks, even if a daemon put text on
    /// it, so no filler is queued ahead of the model's first real sentence.
    public func spokenText(channel: Channel) -> String? {
        guard channel == .voice, kind == .sentence, let t = text,
              !t.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        return t
    }

    /// For `approval_required`: the queued action's name (e.g.
    /// "gmail.send_message") — `action`, or `text` from a daemon that only
    /// sent the action there. Nil when neither is set.
    public var approvalAction: String? {
        for v in [action, text] {
            if let s = v?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty { return s }
        }
        return nil
    }

    /// Decodes one NDJSON line; nil for a line that isn't a valid event
    /// (the Go client skips those too).
    public static func decode(line: Data) -> TurnEvent? {
        try? JSONDecoder().decode(TurnEvent.self, from: line)
    }
}

/// The `artifact` object of an `artifact` event, mirroring runtime.Artifact:
/// an email draft `{"type": "email_draft", "to": [...], "cc": [...],
/// "subject": "...", "body": "..."}`, something the model chose to show
/// (display.show) `{"type": "display", "title": "...", "body": "..."}`, or
/// a relayed comment or other tool result a connector explicitly flagged
/// relay: true (docs/slices/UI.md Phase 6, U1-A) `{"type": "note", "title":
/// "...", "body": "...", "source": "...", "simulated": true}`, plain text.
/// The daemon copies the values from the call's own arguments (or, for a
/// note, the connector's own result) and caps them (draft strings at 20000
/// bytes, lists at 50; a display's title at 120 characters and body at
/// 4000; a note's the same as a draft's). Every field but `type` may be
/// missing (an unknown type decodes with just its type): lists then read as
/// empty, strings as "" and `simulated` as false. Entries of `to`/`cc` that
/// aren't strings are skipped, and a single string reads as a list of one,
/// so one odd value never loses the whole artifact.
public struct TurnArtifact: Decodable, Equatable {
    public static let emailDraftType = "email_draft"
    public static let displayType = "display"
    public static let noteType = "note"

    public var type: String
    /// A display's or a note's heading; "" for a draft (or when absent).
    public var title: String
    public var to: [String]
    public var cc: [String]
    public var subject: String
    public var body: String
    /// A note's own source (e.g. an issue identifier); "" for every other
    /// type (or when absent).
    public var source: String
    /// A note's own flag: true when the connector generated the content
    /// itself rather than relaying a real reply; false for every other type
    /// (or when absent). Never inferred client-side -- the daemon is the
    /// only source of truth for it.
    public var simulated: Bool

    public init(type: String, title: String = "", to: [String] = [], cc: [String] = [],
                subject: String = "", body: String = "", source: String = "", simulated: Bool = false) {
        self.type = type
        self.title = title
        self.to = to
        self.cc = cc
        self.subject = subject
        self.body = body
        self.source = source
        self.simulated = simulated
    }

    /// True for an email draft (`type == "email_draft"`).
    public var emailDraft: Bool { type == Self.emailDraftType }

    /// True for something the model chose to show (`type == "display"`).
    public var isDisplay: Bool { type == Self.displayType }

    /// True for a relayed comment or other flagged tool result
    /// (`type == "note"`).
    public var isNote: Bool { type == Self.noteType }

    private enum CodingKeys: String, CodingKey { case type, title, to, cc, subject, body, source, simulated }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        type = try c.decode(String.self, forKey: .type)
        title = (try? c.decodeIfPresent(String.self, forKey: .title)) ?? ""
        to = Self.strings(c, .to)
        cc = Self.strings(c, .cc)
        subject = (try? c.decodeIfPresent(String.self, forKey: .subject)) ?? ""
        body = (try? c.decodeIfPresent(String.self, forKey: .body)) ?? ""
        source = (try? c.decodeIfPresent(String.self, forKey: .source)) ?? ""
        simulated = (try? c.decodeIfPresent(Bool.self, forKey: .simulated)) ?? false
    }

    private static func strings(_ c: KeyedDecodingContainer<CodingKeys>, _ key: CodingKeys) -> [String] {
        if let one = try? c.decodeIfPresent(String.self, forKey: key) { return one.isEmpty ? [] : [one] }
        guard let list = try? c.decodeIfPresent([LenientString].self, forKey: key) else { return [] }
        return list.compactMap { $0.value }.filter { !$0.isEmpty }
    }

}

/// One list entry: its string, or nil for any other JSON value.
struct LenientString: Decodable {
    let value: String?
    init(from decoder: Decoder) throws {
        value = try? decoder.singleValueContainer().decode(String.self)
    }
}

/// Splits a byte stream into newline-delimited records as the bytes arrive,
/// holding back an incomplete trailing line until the rest of it shows up.
/// Blank lines are dropped, surrounding whitespace (including a CR) trimmed.
public struct NDJSONLineSplitter {
    private var pending: [UInt8] = []
    /// Same ceiling the Go client's bufio.Scanner uses (4 MiB).
    public static let maxLine = 4 << 20

    public init() {}

    public mutating func feed(_ data: Data) throws -> [Data] {
        var out: [Data] = []
        for byte in data {
            if byte == 10 {
                if let l = Self.trimmed(pending) { out.append(l) }
                pending.removeAll(keepingCapacity: true)
            } else {
                pending.append(byte)
                if pending.count > Self.maxLine {
                    throw WaterClientError.protocolError("NDJSON line exceeds \(Self.maxLine) bytes")
                }
            }
        }
        return out
    }

    /// The final unterminated line, if any, at end of stream.
    public mutating func flush() -> [Data] {
        defer { pending.removeAll() }
        return Self.trimmed(pending).map { [$0] } ?? []
    }

    private static func trimmed(_ b: [UInt8]) -> Data? {
        func ws(_ c: UInt8) -> Bool { c == 32 || c == 9 || c == 13 || c == 10 }
        guard let s = b.firstIndex(where: { !ws($0) }), let e = b.lastIndex(where: { !ws($0) }) else { return nil }
        return Data(b[s...e])
    }
}
