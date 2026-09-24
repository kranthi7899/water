import Darwin
import Foundation
import Testing
@testable import WaterClientCore

/// A one-shot Unix-socket HTTP server, built on the same POSIX calls as the
/// client: accepts one connection, captures the request, replies with canned
/// bytes (optionally in several pieces with pauses, to exercise streaming).
final class CannedServer: @unchecked Sendable {
    let path: String
    private let fd: Int32
    private let done = DispatchSemaphore(value: 0)
    private(set) var request = Data()

    init(pieces: [String], pause: TimeInterval = 0) throws {
        path = "/tmp/water-test-\(getpid())-\(UInt32.random(in: 0...UInt32.max)).sock"
        unlink(path)
        fd = socket(AF_UNIX, SOCK_STREAM, 0)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let p = Array(path.utf8)
        withUnsafeMutableBytes(of: &addr.sun_path) { $0.copyBytes(from: p); $0[p.count] = 0 }
        let listenFD = fd
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(listenFD, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard rc == 0, listen(fd, 1) == 0 else { throw WaterClientError.socket("test server bind/listen failed") }

        Thread.detachNewThread { [self] in
            let c = accept(listenFD, nil, nil)
            guard c >= 0 else { done.signal(); return }
            var one: Int32 = 1
            setsockopt(c, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
            // Read the head, then Content-Length bytes of body.
            var buf = [UInt8]()
            var tmp = [UInt8](repeating: 0, count: 4096)
            while true {
                let n = tmp.withUnsafeMutableBytes { read(c, $0.baseAddress, $0.count) }
                if n <= 0 { break }
                buf += tmp[0..<n]
                if let end = findHeadEnd(buf) {
                    let head = String(decoding: buf[0..<end], as: UTF8.self)
                    let cl = head.components(separatedBy: "\r\n")
                        .first { $0.lowercased().hasPrefix("content-length:") }
                        .flatMap { Int($0.split(separator: ":")[1].trimmingCharacters(in: .whitespaces)) } ?? 0
                    if buf.count >= end + 4 + cl { break }
                }
            }
            request = Data(buf)
            for piece in pieces {
                _ = Array(piece.utf8).withUnsafeBytes { write(c, $0.baseAddress, $0.count) }
                if pause > 0 { Thread.sleep(forTimeInterval: pause) }
            }
            close(c)
            done.signal()
        }
    }

    func wait() { _ = done.wait(timeout: .now() + 5) }

    deinit {
        close(fd)
        unlink(path)
    }
}

@Suite struct SocketClientTests {
    @Test func streamTurnAgainstCannedChunkedServer() throws {
        func chunk(_ s: String) -> String { String(Array(s.utf8).count, radix: 16) + "\r\n" + s + "\r\n" }
        let lines = [
            #"{"kind":"ack"}"# + "\n",
            #"{"kind":"delta","text":"Two meetings "}"# + "\n",
            #"{"kind":"delta","text":"today."}"# + "\n" + #"{"kind":"sentence","text":"Two meetings today."}"# + "\n",
            #"{"kind":"approval_required","approval_id":"env_9"}"# + "\n",
            #"{"kind":"done","text":"Two meetings today."}"# + "\n",
        ]
        // One NDJSON line is split across two HTTP chunks too.
        let split = lines[1]
        let mid = split.index(split.startIndex, offsetBy: 10)
        let pieces = ["HTTP/1.1 200 OK\r\nContent-Type: application/x-ndjson\r\nX-Water-Task-Id: task_x\r\nTransfer-Encoding: chunked\r\n\r\n",
                      chunk(lines[0]), chunk(String(split[..<mid])), chunk(String(split[mid...])),
                      chunk(lines[2]), chunk(lines[3]), chunk(lines[4]), "0\r\n\r\n"]
        let server = try CannedServer(pieces: pieces, pause: 0.03)

        var events: [TurnEvent] = []
        var firstDeltaAt: Date?
        let start = Date()
        try UnixSocketClient(socketPath: server.path).streamTurn(channel: .voice, prompt: "what's on today?", token: "tok_1") { e in
            if e.kind == .delta, firstDeltaAt == nil { firstDeltaAt = Date() }
            events.append(e)
        }
        let end = Date()
        server.wait()
        #expect(events.map(\.kind) == [.ack, .delta, .delta, .sentence, .approvalRequired, .done])
        #expect(events[1].text == "Two meetings ")
        #expect(events[4].approvalID == "env_9")
        // Streamed, not buffered: the first delta was delivered well before
        // the response finished arriving.
        let first = try #require(firstDeltaAt)
        #expect(end.timeIntervalSince(first) > 0.08)
        _ = start

        let req = String(decoding: server.request, as: UTF8.self)
        #expect(req.hasPrefix("POST /v1/turns HTTP/1.1\r\n"))
        #expect(req.contains("Authorization: Bearer tok_1\r\n"))
        #expect(req.hasSuffix(#"{"channel":"voice","clear":false,"prompt":"what's on today?"}"#))
    }

    @Test func unauthorizedSurfacesStatusAndBody() throws {
        let server = try CannedServer(pieces: ["HTTP/1.1 401 Unauthorized\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 14\r\n\r\ninvalid token\n"])
        #expect(throws: WaterClientError.http(status: 401, body: "invalid token\n")) {
            try UnixSocketClient(socketPath: server.path).streamTurn(channel: .textBar, prompt: "x", token: "bad") { _ in }
        }
    }

    @Test func healthBuffered() throws {
        let server = try CannedServer(pieces: ["HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 30\r\n\r\n", #"{"status":"ok","twin":"ceo"}"# + "\n\n"])
        let h = try UnixSocketClient(socketPath: server.path).health()
        #expect(h["status"] as? String == "ok")
        #expect(h["twin"] as? String == "ceo")
    }

    @Test func missingSocketIsDaemonNotRunning() {
        let path = "/tmp/water-no-such-\(getpid()).sock"
        #expect(throws: WaterClientError.daemonNotRunning(path)) {
            try UnixSocketClient(socketPath: path).health()
        }
    }

    @Test func cancelUnblocksARead() throws {
        // The server sends the head and then stalls far longer than we wait.
        let server = try CannedServer(pieces: ["HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n", "0\r\n\r\n"], pause: 3)
        let cancel = CancelToken()
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.2) { cancel.cancel() }
        let start = Date()
        #expect(throws: WaterClientError.cancelled) {
            try UnixSocketClient(socketPath: server.path).streamTurn(channel: .cli, prompt: "x", token: "t", cancel: cancel) { _ in }
        }
        #expect(Date().timeIntervalSince(start) < 2)
    }

    @Test func defaultSocketPathHonorsWaterHome() {
        #expect(UnixSocketClient.defaultSocketPath(environment: ["WATER_HOME": "/x/y"]) == "/x/y/run/water.sock")
        #expect(UnixSocketClient.defaultSocketPath(environment: [:]) == NSHomeDirectory() + "/.water/run/water.sock")
    }

    /// Opt-in probe of the real running daemon (/v1/health needs no token):
    /// WATER_LIVE_DAEMON_TEST=1 swift test
    @Test(.enabled(if: ProcessInfo.processInfo.environment["WATER_LIVE_DAEMON_TEST"] == "1"))
    func liveDaemonHealth() throws {
        let h = try UnixSocketClient(socketPath: UnixSocketClient.defaultSocketPath()).health()
        #expect(h["status"] as? String == "ok")
    }
}
