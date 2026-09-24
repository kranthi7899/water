// Tests use swift-testing (`import Testing`), not XCTest: XCTest ships only
// with full Xcode, and this package is built with the Command Line Tools
// alone. `swift test` runs them the same way.
import Foundation
import Testing
@testable import WaterClientCore

@Suite struct HTTPRequestTests {
    @Test func postTurnBytes() throws {
        let req = try HTTPRequest.json("POST", "/v1/turns", token: "abc123",
                                       ["channel": "text-bar", "prompt": "hi", "clear": false])
        let body = #"{"channel":"text-bar","clear":false,"prompt":"hi"}"#
        let want = "POST /v1/turns HTTP/1.1\r\n"
            + "Host: water\r\n"
            + "Authorization: Bearer abc123\r\n"
            + "Content-Type: application/json\r\n"
            + "Content-Length: \(body.utf8.count)\r\n"
            + "Connection: close\r\n"
            + "\r\n"
            + body
        #expect(String(decoding: try req.serialized(), as: UTF8.self) == want)
    }

    @Test func getWithoutTokenOrBody() throws {
        let got = String(decoding: try HTTPRequest(method: "GET", path: "/v1/health").serialized(), as: UTF8.self)
        #expect(got == "GET /v1/health HTTP/1.1\r\nHost: water\r\nContent-Type: application/json\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
        #expect(!got.contains("Authorization"))
    }

    @Test func contentLengthCountsUTF8Bytes() throws {
        let s = String(decoding: try HTTPRequest.json("POST", "/v1/turns", token: "t", ["prompt": "café ☕"]).serialized(), as: UTF8.self)
        let body = s.components(separatedBy: "\r\n\r\n")[1]
        #expect(body.utf8.count != body.count)
        #expect(s.contains("Content-Length: \(body.utf8.count)\r\n"))
    }

    @Test func rejectsHeaderInjection() {
        #expect(throws: WaterClientError.self) { try HTTPRequest(method: "GET", path: "/v1/state", token: "a\r\nX-Evil: 1").serialized() }
        #expect(throws: WaterClientError.self) { try HTTPRequest(method: "GET", path: "/v1/state\r\nX: y").serialized() }
        #expect(throws: WaterClientError.self) { try HTTPRequest(method: "GET", path: "no-slash").serialized() }
    }
}

@Suite struct HTTPResponseParserTests {
    private func parse(_ raw: String, splitEvery n: Int) throws -> (Int?, [String: String], String, Bool) {
        let p = HTTPResponseParser()
        var body = Data()
        p.onBody = { body.append($0) }
        let bytes = Array(raw.utf8)
        var i = 0
        while i < bytes.count {
            try p.feed(Data(bytes[i..<min(i + n, bytes.count)]))
            i += n
        }
        if !p.isComplete { try p.finish() }
        return (p.status, p.headers, String(decoding: body, as: UTF8.self), p.isComplete)
    }

    @Test(arguments: [1, 2, 3, 7, 1000])
    func contentLength(split n: Int) throws {
        let raw = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 15\r\n\r\n{\"status\":\"ok\"}"
        let (status, headers, body, done) = try parse(raw, splitEvery: n)
        #expect(status == 200)
        #expect(headers["content-type"] == "application/json")
        #expect(body == #"{"status":"ok"}"#)
        #expect(done)
    }

    @Test(arguments: [1, 2, 5, 13, 1000])
    func chunked(split n: Int) throws {
        let raw = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nX-Water-Task-Id: task_1\r\n\r\n"
            + "5\r\nhello\r\n"
            + "7;ext=1\r\n, world\r\n"
            + "0\r\n\r\n"
        let (status, headers, body, done) = try parse(raw, splitEvery: n)
        #expect(status == 200)
        #expect(headers["x-water-task-id"] == "task_1")
        #expect(body == "hello, world")
        #expect(done)
    }

    @Test func readUntilClose() throws {
        let (status, _, body, done) = try parse("HTTP/1.1 401 Unauthorized\r\n\r\ninvalid token\n", splitEvery: 4)
        #expect(status == 401)
        #expect(body == "invalid token\n")
        #expect(done)
    }

    @Test func truncatedBodyIsAnError() throws {
        let p = HTTPResponseParser()
        try p.feed(Data("HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nabc".utf8))
        #expect(throws: WaterClientError.self) { try p.finish() }
    }

    @Test func badChunkSize() {
        let p = HTTPResponseParser()
        #expect(throws: WaterClientError.self) {
            try p.feed(Data("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n".utf8))
        }
    }
}

@Suite struct NDJSONTests {
    static let stream = #"{"kind":"ack"}"# + "\n"
        + #"{"kind":"delta","text":"Hel"}"# + "\n"
        + #"{"kind":"delta","text":"lo, world."}"# + "\r\n"
        + "\n"
        + #"{"kind":"sentence","text":"Hello, world."}"# + "\n"
        + #"{"kind":"approval_required","approval_id":"env_1"}"# + "\n"
        + #"{"kind":"done","text":"Hello, world."}"#   // no trailing newline

    @Test(arguments: [1, 2, 3, 10, 17, 100_000])
    func reassemblesSplitObjects(split n: Int) throws {
        let bytes = Array(Self.stream.utf8)
        var s = NDJSONLineSplitter()
        var events: [TurnEvent] = []
        var i = 0
        while i < bytes.count {
            for l in try s.feed(Data(bytes[i..<min(i + n, bytes.count)])) {
                events.append(try #require(TurnEvent.decode(line: l)))
            }
            i += n
        }
        for l in s.flush() { events.append(try #require(TurnEvent.decode(line: l))) }
        #expect(events == [
            TurnEvent(kind: .ack),
            TurnEvent(kind: .delta, text: "Hel"),
            TurnEvent(kind: .delta, text: "lo, world."),
            TurnEvent(kind: .sentence, text: "Hello, world."),
            TurnEvent(kind: .approvalRequired, approvalID: "env_1"),
            TurnEvent(kind: .done, text: "Hello, world."),
        ])
    }

    @Test func multibyteSplitAcrossChunks() throws {
        let line = Array((#"{"kind":"delta","text":"naïve — ☕"}"# + "\n").utf8)
        var s = NDJSONLineSplitter()
        var got: [Data] = []
        for b in line { got += try s.feed(Data([b])) }
        #expect(got.count == 1)
        #expect(TurnEvent.decode(line: got[0])?.text == "naïve — ☕")
    }

    @Test func unknownKindAndErrorField() {
        #expect(TurnEvent.decode(line: Data(#"{"kind":"future_thing"}"#.utf8))?.kind == .unknown("future_thing"))
        #expect(TurnEvent.decode(line: Data(#"{"kind":"error","error":"boom"}"#.utf8))?.error == "boom")
        #expect(TurnEvent.decode(line: Data("not json".utf8)) == nil)
    }
}
