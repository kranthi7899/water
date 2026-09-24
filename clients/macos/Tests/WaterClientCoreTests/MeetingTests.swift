import Darwin
import Foundation
import Testing
@testable import WaterClientCore

@Suite struct MeetingRequestTests {
    // 2026-09-24T12:30:05.250Z
    static let at = Date(timeIntervalSince1970: 1_790_253_005.25)

    @Test func postSegmentBytes() throws {
        let req = try HTTPRequest.meetingSegment(sessionID: "mtg_0123456789abcdef01234567", channel: .system,
                                                 text: "Budget is 40k", at: Self.at, token: "abc123")
        let body = #"{"at":"2026-09-24T12:30:05.250Z","channel":"system","text":"Budget is 40k"}"#
        let want = "POST /v1/meetings/mtg_0123456789abcdef01234567/segments HTTP/1.1\r\n"
            + "Host: water\r\n"
            + "Authorization: Bearer abc123\r\n"
            + "Content-Type: application/json\r\n"
            + "Content-Length: \(body.utf8.count)\r\n"
            + "Connection: close\r\n"
            + "\r\n"
            + body
        #expect(String(decoding: try req.serialized(), as: UTF8.self) == want)
    }

    @Test func micChannelAndStartStopBodies() throws {
        let seg = try HTTPRequest.meetingSegment(sessionID: "mtg_1", channel: .mic, text: "hi", at: Self.at, token: "t")
        #expect(String(decoding: seg.body!, as: UTF8.self).contains(#""channel":"mic""#))
        #expect(String(decoding: try HTTPRequest.meetingStart(token: "t").body!, as: UTF8.self) == "{}")
        #expect(String(decoding: try HTTPRequest.meetingStart(token: "t", eventID: "ev_9").body!, as: UTF8.self) == #"{"event_id":"ev_9"}"#)
        let stop = try HTTPRequest.meetingStop(sessionID: "mtg_1", token: "t")
        #expect(stop.path == "/v1/meetings/mtg_1/stop")
        #expect(String(decoding: stop.body!, as: UTF8.self) == "{}")
    }

    @Test func sessionIDCannotEscapeThePath() {
        for bad in ["", "../turns", "mtg_1/stop", "mtg 1", "mtg_1?x=1", "mtg_1\r\nX: y"] {
            #expect(throws: WaterClientError.self) {
                try HTTPRequest.meetingSegment(sessionID: bad, channel: .mic, text: "x", at: Self.at, token: "t")
            }
        }
    }

    @Test func piecesTrimAndSplitUnderTheDaemonCap() {
        #expect(MeetingSegments.pieces("  \n ") == [])
        #expect(MeetingSegments.pieces("  hello there \n") == ["hello there"])
        #expect(MeetingSegments.pieces("aa bb cc dd", limit: 5) == ["aa bb", "cc dd"])
        #expect(MeetingSegments.pieces("abcdefgh ij", limit: 3) == ["abc", "def", "gh", "ij"])
        let long = Array(repeating: "naïve", count: 3000).joined(separator: " ")
        let ps = MeetingSegments.pieces(long)
        #expect(ps.count == 3) // ~21 KB of UTF-8
        #expect(ps.allSatisfy { $0.utf8.count <= MeetingSegments.maxText })
        #expect(ps.joined(separator: " ") == long)
    }
}

@Suite struct MeetingSocketTests {
    @Test func postSegmentAgainstCannedServer() throws {
        let server = try CannedServer(pieces: ["HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 12\r\n\r\n{\"ok\":true}\n"])
        try UnixSocketClient(socketPath: server.path).postMeetingSegment(
            sessionID: "mtg_abc", channel: .mic, text: "let's ship Friday", at: MeetingRequestTests.at, token: "tok_m")
        server.wait()
        let req = String(decoding: server.request, as: UTF8.self)
        #expect(req.hasPrefix("POST /v1/meetings/mtg_abc/segments HTTP/1.1\r\n"))
        #expect(req.contains("Authorization: Bearer tok_m\r\n"))
        #expect(req.hasSuffix(#"{"at":"2026-09-24T12:30:05.250Z","channel":"mic","text":"let's ship Friday"}"#))
    }

    @Test func stoppedSessionSurfaces409() throws {
        let server = try CannedServer(pieces: ["HTTP/1.1 409 Conflict\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 24\r\n\r\nmeeting session ended\n\n\n"])
        #expect(throws: WaterClientError.http(status: 409, body: "meeting session ended\n\n\n")) {
            try UnixSocketClient(socketPath: server.path).postMeetingSegment(sessionID: "mtg_abc", channel: .system, text: "x", token: "t")
        }
    }

    @Test func startParsesSessionAndStopParsesEnd() throws {
        let startBody = #"{"session_id":"mtg_0123456789abcdef01234567","started_at":"2026-09-24T12:30:05.123456789-04:00"}"#
        let s1 = try CannedServer(pieces: ["HTTP/1.1 200 OK\r\nContent-Length: \(startBody.utf8.count)\r\n\r\n" + startBody])
        let session = try UnixSocketClient(socketPath: s1.path).startMeeting(token: "t")
        s1.wait()
        #expect(session.id == "mtg_0123456789abcdef01234567")
        #expect(session.startedAt != nil)
        #expect(String(decoding: s1.request, as: UTF8.self).hasPrefix("POST /v1/meetings/start HTTP/1.1\r\n"))

        let stopBody = #"{"ended_at":"2026-09-24T16:45:00Z","ok":true,"session_id":"mtg_1"}"#
        let s2 = try CannedServer(pieces: ["HTTP/1.1 200 OK\r\nContent-Length: \(stopBody.utf8.count)\r\n\r\n" + stopBody])
        let ended = try UnixSocketClient(socketPath: s2.path).stopMeeting(sessionID: "mtg_1", token: "t")
        #expect(ended == Date(timeIntervalSince1970: 1_790_268_300))
    }

    /// Opt-in, against the real running daemon: start a session, post one
    /// segment on each channel, stop it, and check a post after the stop is
    /// refused. WATER_LIVE_DAEMON_TEST=1 ./test.sh
    ///
    /// Note: any segments call taints the daemon's shared session token until
    /// it restarts (meeting text is untrusted), by design.
    @Test(.enabled(if: ProcessInfo.processInfo.environment["WATER_LIVE_DAEMON_TEST"] == "1"))
    func liveDaemonMeetingRoundTrip() throws {
        let clients = ClientsFile.defaultPath()
        let token = try #require(ClientsFile.token(named: "macos-client", path: clients) ?? ClientsFile.token(named: "cli", path: clients))
        let c = UnixSocketClient(socketPath: UnixSocketClient.defaultSocketPath())
        c.readTimeout = 15
        let s = try c.startMeeting(token: token)
        #expect(s.id.hasPrefix("mtg_"))
        try c.postMeetingSegment(sessionID: s.id, channel: .mic, text: "live test: mic segment", token: token)
        try c.postMeetingSegment(sessionID: s.id, channel: .system, text: "live test: system segment", token: token)
        #expect(try c.stopMeeting(sessionID: s.id, token: token) != nil)
        let err = #expect(throws: WaterClientError.self) {
            try c.postMeetingSegment(sessionID: s.id, channel: .mic, text: "after stop", token: token)
        }
        if case .http(let status, _)? = err { #expect(status == 409) } else { Issue.record("want HTTP 409, got \(String(describing: err))") }
    }
}
