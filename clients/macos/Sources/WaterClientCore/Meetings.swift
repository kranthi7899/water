import Foundation

/// Which side of a meeting a transcript segment came from, exactly as the
/// daemon's POST /v1/meetings/{id}/segments accepts it: `mic` is the CEO's
/// microphone, `system` is everyone else (the Mac's audio output). The daemon
/// treats both as untrusted content — a segment is never an instruction.
public enum MeetingChannel: String {
    case mic
    case system
}

public struct MeetingSession: Equatable {
    public let id: String
    public let startedAt: Date?

    public init(id: String, startedAt: Date?) {
        self.id = id
        self.startedAt = startedAt
    }
}

public enum MeetingSegments {
    /// The daemon's per-segment cap (internal/meetings.MaxSegmentText), in
    /// UTF-8 bytes after trimming.
    public static let maxText = 8 << 10

    /// Trims `text` and splits it at word boundaries into pieces the daemon
    /// accepts; empty text yields no pieces.
    public static func pieces(_ text: String, limit: Int = maxText) -> [String] {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return [] }
        if t.utf8.count <= limit { return [t] }
        var out: [String] = []
        var cur = ""
        func flush() {
            if !cur.isEmpty { out.append(cur) }
            cur = ""
        }
        for word in t.split(whereSeparator: { $0.isWhitespace }) {
            var w = String(word)
            // A single word over the limit is cut at character boundaries.
            while w.utf8.count > limit {
                flush()
                var head = ""
                for ch in w {
                    if head.utf8.count + String(ch).utf8.count > limit { break }
                    head.append(ch)
                }
                if head.isEmpty { head = String(w.removeFirst()) } else { w.removeFirst(head.count) }
                out.append(head)
            }
            if cur.isEmpty {
                cur = w
            } else if cur.utf8.count + 1 + w.utf8.count <= limit {
                cur += " " + w
            } else {
                flush()
                cur = w
            }
        }
        flush()
        return out
    }

    /// RFC 3339 with fractional seconds, which Go's time.Time decodes.
    public static func timestamp(_ d: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.string(from: d)
    }

    static func parseTimestamp(_ s: String?) -> Date? {
        guard let s else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: s)
    }

    /// Session ids go into the request path, so only the daemon's own id
    /// alphabet ("mtg_" + hex) is allowed through.
    static func checkID(_ id: String) throws {
        let ok = !id.isEmpty && id.unicodeScalars.allSatisfy {
            ("a"..."z").contains($0) || ("A"..."Z").contains($0) || ("0"..."9").contains($0) || $0 == "_" || $0 == "-"
        }
        if !ok { throw WaterClientError.invalidRequest("bad meeting session id") }
    }
}

extension HTTPRequest {
    public static func meetingStart(token: String, eventID: String? = nil) throws -> HTTPRequest {
        var body: [String: Any] = [:]
        if let eventID, !eventID.isEmpty { body["event_id"] = eventID }
        return try json("POST", "/v1/meetings/start", token: token, body)
    }

    /// One transcript segment: text only — audio never leaves the Mac.
    public static func meetingSegment(sessionID: String, channel: MeetingChannel, text: String, at: Date, token: String) throws -> HTTPRequest {
        try MeetingSegments.checkID(sessionID)
        return try json("POST", "/v1/meetings/\(sessionID)/segments", token: token,
                        ["at": MeetingSegments.timestamp(at), "channel": channel.rawValue, "text": text])
    }

    public static func meetingStop(sessionID: String, token: String) throws -> HTTPRequest {
        try MeetingSegments.checkID(sessionID)
        return try json("POST", "/v1/meetings/\(sessionID)/stop", token: token, [:])
    }
}

extension UnixSocketClient {
    /// POST /v1/meetings/start.
    public func startMeeting(token: String, eventID: String? = nil) throws -> MeetingSession {
        let obj = try meetingCall(try .meetingStart(token: token, eventID: eventID))
        guard let id = obj["session_id"] as? String, !id.isEmpty else {
            throw WaterClientError.protocolError("meeting start reply has no session_id")
        }
        return MeetingSession(id: id, startedAt: MeetingSegments.parseTimestamp(obj["started_at"] as? String))
    }

    /// POST /v1/meetings/{id}/segments. Non-200 throws `.http`: 400 for a bad
    /// segment, 404 for an unknown session, 409 once the session is stopped.
    public func postMeetingSegment(sessionID: String, channel: MeetingChannel, text: String,
                                   at: Date = Date(), token: String) throws {
        _ = try meetingCall(try .meetingSegment(sessionID: sessionID, channel: channel, text: text, at: at, token: token))
    }

    /// POST /v1/meetings/{id}/stop; returns the session's end time. A repeat
    /// stop succeeds and keeps the original end time.
    @discardableResult
    public func stopMeeting(sessionID: String, token: String) throws -> Date? {
        let obj = try meetingCall(try .meetingStop(sessionID: sessionID, token: token))
        return MeetingSegments.parseTimestamp(obj["ended_at"] as? String)
    }

    private func meetingCall(_ req: HTTPRequest) throws -> [String: Any] {
        var status = 0
        var body = Data()
        try perform(req, onHead: { s, _ in status = s }, onBody: { d in
            if body.count < 64 * 1024 { body.append(d) }
        })
        if status != 200 {
            throw WaterClientError.http(status: status, body: String(decoding: body, as: UTF8.self))
        }
        return (try? JSONSerialization.jsonObject(with: body)) as? [String: Any] ?? [:]
    }
}
