import Foundation
import Testing
@testable import WaterClientCore

/// Counts calls a PartialStreamer makes through PartialTransport, without a
/// real socket — mirrors this test file's need for a fast, deterministic
/// fake (the codebase's existing CannedServer, used below for one
/// real-socket test, is a real listener per instance and not suited to
/// asserting "no more than N calls a second").
final class CountingTransport: PartialTransport, @unchecked Sendable {
    private(set) var calls: [(turnID: String, text: String, seq: Int, channel: Channel, token: String)] = []
    /// Status to return for the Nth call (1-based); calls past the list use
    /// the last entry. Defaults to always-202.
    var statuses: [Int] = [202]

    func postPartial(turnID: String, text: String, seq: Int, channel: Channel, token: String) throws -> Int {
        calls.append((turnID, text, seq, channel, token))
        let i = min(calls.count, statuses.count) - 1
        return statuses[i]
    }
}

@Suite struct PartialsTests {
    // MARK: - turn id

    @Test func generatedTurnIDIsValid() {
        for _ in 0..<20 {
            #expect(TurnID.isValid(TurnID.generate()))
        }
    }

    @Test func turnIDValidationMatchesServerPattern() {
        // internal/gateway/router.go: ^[A-Za-z0-9_-]{8,64}$
        #expect(TurnID.isValid("abcdefgh"))                 // exactly 8
        #expect(TurnID.isValid(String(repeating: "a", count: 64))) // exactly 64
        #expect(TurnID.isValid("E621E1F8-C36C-495A-93FC-0C247A3E6E5F")) // a real UUID
        #expect(!TurnID.isValid("short"))                   // < 8
        #expect(!TurnID.isValid(String(repeating: "a", count: 65))) // > 64
        #expect(!TurnID.isValid("has a space"))
        #expect(!TurnID.isValid("has/slash"))
        #expect(!TurnID.isValid(""))
    }

    // MARK: - rate limiter (pure, fake clock)

    @Test func rateLimiterAllowsUpToFivePerSecond() {
        let limiter = PartialRateLimiter(maxPerSecond: 5)
        var allowed = 0
        for i in 0..<50 {
            if limiter.allow(now: Double(i) * 0.001) { allowed += 1 } // all within the same 1s window
        }
        #expect(allowed == 5)
    }

    @Test func rateLimiterResetsEachWindow() {
        let limiter = PartialRateLimiter(maxPerSecond: 5)
        for i in 0..<5 { #expect(limiter.allow(now: Double(i) * 0.01)) }
        #expect(!limiter.allow(now: 0.5))
        #expect(limiter.allow(now: 1.2)) // a full second later: new window
    }

    // MARK: - PartialStreamer, synchronous executor (deterministic)

    /// A synchronous "executor" (runs the work item immediately, on the
    /// calling thread) so tests can assert exact call counts without real
    /// dispatch-queue timing.
    private static let syncExecutor: (@escaping () -> Void) -> Void = { $0() }

    @Test func postsAreRateLimitedToFivePerSecond() {
        let transport = CountingTransport()
        var now: TimeInterval = 0
        let streamer = PartialStreamer(channel: .voice, tokenProvider: { "tok" }, transport: transport,
                                        clock: { now }, executor: Self.syncExecutor)
        // 50 rapid-fire "recognition updates" within under a second.
        for i in 0..<50 {
            now = Double(i) * 0.01 // 10ms apart, all inside one second
            streamer.post("partial \(i)")
        }
        #expect(transport.calls.count == 5)
        #expect(transport.calls.map(\.seq) == [1, 2, 3, 4, 5])

        // A second later, sending resumes.
        now = 1.5
        streamer.post("final-ish text")
        #expect(transport.calls.count == 6)
    }

    @Test func sendsLatestTextAndIncrementingSeq() {
        let transport = CountingTransport()
        var now: TimeInterval = 0
        let streamer = PartialStreamer(channel: .voice, tokenProvider: { "tok" }, transport: transport,
                                        clock: { now }, executor: Self.syncExecutor)
        streamer.post("a")
        now = 1.1
        streamer.post("ab")
        now = 2.2
        streamer.post("abc")
        #expect(transport.calls.map(\.text) == ["a", "ab", "abc"])
        #expect(transport.calls.map(\.seq) == [1, 2, 3])
        #expect(transport.calls.allSatisfy { $0.turnID == streamer.turnID })
    }

    @Test func a404DisablesPartialsForTheRestOfTheSession() {
        let transport = CountingTransport()
        transport.statuses = [404]
        var now: TimeInterval = 0
        let streamer = PartialStreamer(channel: .voice, tokenProvider: { "tok" }, transport: transport,
                                        clock: { now }, executor: Self.syncExecutor)
        streamer.post("first")
        #expect(transport.calls.count == 1)
        #expect(streamer.disabled)

        // Later posts, even in fresh rate-limit windows, never call the
        // transport again.
        now = 5
        streamer.post("second")
        now = 10
        streamer.post("third")
        #expect(transport.calls.count == 1)
    }

    @Test func aNon404FailureKeepsStreamingPartials() {
        let transport = CountingTransport()
        transport.statuses = [202, 500, 202]
        var now: TimeInterval = 0
        let streamer = PartialStreamer(channel: .voice, tokenProvider: { "tok" }, transport: transport,
                                        clock: { now }, executor: Self.syncExecutor)
        streamer.post("a")
        now = 1.1
        streamer.post("b") // a 500 - not 404 - doesn't disable
        now = 2.2
        streamer.post("c")
        #expect(transport.calls.count == 3)
        #expect(!streamer.disabled)
    }

    @Test func transportErrorIsIgnoredAndNeverRetried() {
        struct FailingTransport: PartialTransport {
            func postPartial(turnID: String, text: String, seq: Int, channel: Channel, token: String) throws -> Int {
                throw WaterClientError.socket("no route to daemon")
            }
        }
        let streamer = PartialStreamer(channel: .voice, tokenProvider: { "tok" }, transport: FailingTransport(),
                                        clock: { 0 }, executor: Self.syncExecutor)
        streamer.post("a") // must not throw or crash
        #expect(!streamer.disabled)
    }

    // MARK: - real socket round trip (one call)

    @Test func postPartialOverCannedServer() throws {
        let server = try CannedServer(pieces: ["HTTP/1.1 202 Accepted\r\nContent-Type: application/json\r\nContent-Length: 20\r\n\r\n" + #"{"state":"listening"}"#])
        let status = try UnixSocketClient(socketPath: server.path)
            .postPartial(turnID: "abcdefgh12345678", text: "what's on my calen", seq: 3, channel: .voice, token: "tok_1")
        server.wait()
        #expect(status == 202)
        let req = String(decoding: server.request, as: UTF8.self)
        #expect(req.hasPrefix("POST /v1/turns/abcdefgh12345678/partial HTTP/1.1\r\n"))
        #expect(req.hasSuffix(#"{"channel":"voice","seq":3,"text":"what's on my calen"}"#))
    }

    @Test func postPartialRejectsInvalidTurnIDLocally() {
        #expect(throws: WaterClientError.self) {
            _ = try UnixSocketClient(socketPath: "/tmp/doesnt-matter.sock")
                .postPartial(turnID: "../etc/passwd", text: "x", seq: 1, channel: .voice, token: "t")
        }
    }
}
