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
/// `{"kind": "...", "text": "...", "approval_id": "...", "error": "..."}`.
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
    public var error: String?

    public init(kind: Kind, text: String? = nil, approvalID: String? = nil, error: String? = nil) {
        self.kind = kind
        self.text = text
        self.approvalID = approvalID
        self.error = error
    }

    private enum CodingKeys: String, CodingKey {
        case kind, text, error
        case approvalID = "approval_id"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = Kind(try c.decode(String.self, forKey: .kind))
        text = try c.decodeIfPresent(String.self, forKey: .text)
        approvalID = try c.decodeIfPresent(String.self, forKey: .approvalID)
        error = try c.decodeIfPresent(String.self, forKey: .error)
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
