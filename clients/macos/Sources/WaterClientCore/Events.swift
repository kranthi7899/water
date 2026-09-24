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
/// "risk": "...", "payload_hash": "...", "error": "..."}`.
///
/// `approval_required` carries `action`, `risk` and `payload_hash` (enough to
/// decide it via POST /v1/approvals/{id}/decision) and is best-effort: it
/// covers only approvals this turn's own model queued while it was running.
/// Refresh GET /v1/approvals on `done` for everything else.
///
/// Every stream ends with exactly one terminal event, `done` or `error`;
/// nothing follows it (the daemon's turnSink closes after it).
public struct TurnEvent: Decodable, Equatable {
    public enum Kind: Equatable {
        case ack, delta, sentence, approvalRequired, done, error
        case unknown(String)

        init(_ raw: String) {
            switch raw {
            case "ack": self = .ack
            case "delta": self = .delta
            case "sentence": self = .sentence
            case "approval_required": self = .approvalRequired
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
    public var error: String?

    public init(kind: Kind, text: String? = nil, approvalID: String? = nil,
                action: String? = nil, risk: String? = nil, payloadHash: String? = nil,
                error: String? = nil) {
        self.kind = kind
        self.text = text
        self.approvalID = approvalID
        self.action = action
        self.risk = risk
        self.payloadHash = payloadHash
        self.error = error
    }

    private enum CodingKeys: String, CodingKey {
        case kind, text, error, action, risk
        case approvalID = "approval_id"
        case payloadHash = "payload_hash"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = Kind(try c.decode(String.self, forKey: .kind))
        text = try c.decodeIfPresent(String.self, forKey: .text)
        approvalID = try c.decodeIfPresent(String.self, forKey: .approvalID)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        risk = try c.decodeIfPresent(String.self, forKey: .risk)
        payloadHash = try c.decodeIfPresent(String.self, forKey: .payloadHash)
        error = try c.decodeIfPresent(String.self, forKey: .error)
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
