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
/// "step_id": "...", "tool": "...", "label": "...", "status": "..."}`.
///
/// `approval_required` carries `action`, `risk`, `payload_hash` (enough to
/// decide it via POST /v1/approvals/{id}/decision) and `read_back`, the
/// code-built text to show or speak before asking yes or no. It is
/// best-effort: it covers approvals this turn staged (a model tool call
/// queued while its model ran, or a Tier-0 write intent) and a spoken yes
/// that needs a tap. Refresh GET /v1/approvals on `done` for everything else.
///
/// `tool_start`/`tool_end` bracket one tool call the model made during this
/// turn (V-events): the same `step_id` on both, the function id in `tool`
/// (empty when the daemon didn't recognise it), a code-built `label` that
/// never contains the call's arguments, and on `tool_end` a `status` of
/// ok, queued, denied or error. The label is fixed text by construction;
/// show it as plain text anyway, like every other string here.
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
        /// The router's handoff acknowledgement on a non-voice channel
        /// (Design §11.4 step 6, internal/nervous/nervous.go's emitHandoff
        /// doc comment): today the daemon still sends a zero-text `ack` for
        /// this instead (no code path emits the literal "handoff" kind yet),
        /// but this decodes it the moment one does, matching the Go CLI's
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

    public init(kind: Kind, text: String? = nil, approvalID: String? = nil,
                action: String? = nil, risk: String? = nil, payloadHash: String? = nil,
                readBack: String? = nil, error: String? = nil,
                stepID: String? = nil, tool: String? = nil, label: String? = nil, status: String? = nil) {
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
    }

    private enum CodingKeys: String, CodingKey {
        case kind, text, error, action, risk, tool, label, status
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
