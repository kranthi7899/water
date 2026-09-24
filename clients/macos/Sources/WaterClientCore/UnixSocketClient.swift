import Darwin
import Foundation

public enum WaterClientError: Error, LocalizedError, Equatable {
    case daemonNotRunning(String)
    case socket(String)
    case invalidRequest(String)
    case protocolError(String)
    case http(status: Int, body: String)
    case noToken(String)
    case cancelled

    public var errorDescription: String? {
        switch self {
        case .daemonNotRunning:
            // Same wording as the Go CLI's errDaemonNotRunning.
            return "water daemon is not running. Start it with `water daemon` (or install it to start automatically: `water daemon install`)"
        case .socket(let m): return "water daemon socket: \(m)"
        case .invalidRequest(let m): return "invalid request: \(m)"
        case .protocolError(let m): return "water daemon protocol error: \(m)"
        case .http(let status, let body):
            let b = body.trimmingCharacters(in: .whitespacesAndNewlines)
            return "water daemon: HTTP \(status)\(b.isEmpty ? "" : ": " + b)"
        case .noToken(let m): return "no client token: \(m)"
        case .cancelled: return "cancelled"
        }
    }
}

/// Lets another thread abort an in-flight request by shutting its socket
/// down; the blocked read then returns and the call throws `.cancelled`.
/// The daemon sees the disconnect and cancels the turn's context.
public final class CancelToken {
    private let lock = NSLock()
    private var fd: Int32 = -1
    private var cancelled = false

    public init() {}

    public var isCancelled: Bool { lock.lock(); defer { lock.unlock() }; return cancelled }

    public func cancel() {
        lock.lock(); defer { lock.unlock() }
        cancelled = true
        if fd >= 0 { _ = Darwin.shutdown(fd, SHUT_RDWR) }
    }

    func attach(_ fd: Int32) -> Bool {
        lock.lock(); defer { lock.unlock() }
        if cancelled { return false }
        self.fd = fd
        return true
    }

    func detach() {
        lock.lock(); defer { lock.unlock() }
        fd = -1
    }
}

public struct HTTPResponse {
    public var status: Int
    public var headers: [String: String]
    public var body: Data
}

/// A minimal, blocking HTTP/1.1 client over a Unix domain socket, using only
/// POSIX socket calls. One request per connection. Call it off the main thread.
public final class UnixSocketClient {
    public let socketPath: String
    /// Per-read timeout (SO_RCVTIMEO). A model turn can think for a while
    /// between deltas, so this is generous; the Go client uses 5 minutes.
    public var readTimeout: TimeInterval = 300

    public init(socketPath: String) {
        self.socketPath = socketPath
    }

    /// `$WATER_HOME/run/water.sock`, else `~/.water/run/water.sock` — the same
    /// resolution as internal/config.Home + gateway.Paths.SocketPath.
    public static func defaultSocketPath(environment: [String: String] = ProcessInfo.processInfo.environment) -> String {
        waterHome(environment: environment) + "/run/water.sock"
    }

    public static func waterHome(environment: [String: String] = ProcessInfo.processInfo.environment) -> String {
        if let h = environment["WATER_HOME"], !h.isEmpty { return h }
        return NSHomeDirectory() + "/.water"
    }

    /// Sends one request and streams the response: `onHead` once with the
    /// status and headers, then `onBody` for each run of body bytes as it is
    /// read. Returns the status code.
    @discardableResult
    public func perform(_ request: HTTPRequest,
                        cancel: CancelToken? = nil,
                        onHead: ((Int, [String: String]) -> Void)? = nil,
                        onBody: @escaping (Data) throws -> Void) throws -> Int {
        let bytes = try request.serialized()
        let fd = try connectSocket()
        defer { Darwin.close(fd) }
        if let cancel {
            guard cancel.attach(fd) else { throw WaterClientError.cancelled }
        }
        defer { cancel?.detach() }

        try writeAll(fd, bytes, cancel: cancel)

        let parser = HTTPResponseParser()
        var bodyError: Error?
        parser.onHead = onHead
        parser.onBody = { d in
            guard bodyError == nil else { return }
            do { try onBody(d) } catch { bodyError = error }
        }
        var chunk = [UInt8](repeating: 0, count: 16 * 1024)
        while !parser.isComplete {
            let n = chunk.withUnsafeMutableBytes { Darwin.read(fd, $0.baseAddress, $0.count) }
            if n < 0 {
                if errno == EINTR { continue }
                if cancel?.isCancelled == true { throw WaterClientError.cancelled }
                if errno == EAGAIN || errno == EWOULDBLOCK { throw WaterClientError.socket("read timed out") }
                throw WaterClientError.socket("read: \(String(cString: strerror(errno)))")
            }
            if n == 0 {
                if cancel?.isCancelled == true { throw WaterClientError.cancelled }
                try parser.finish()
                break
            }
            try parser.feed(Data(chunk[0..<n]))
            if let bodyError { throw bodyError }
        }
        if let bodyError { throw bodyError }
        guard let status = parser.status else { throw WaterClientError.protocolError("no status") }
        return status
    }

    /// A buffered request: the whole response body in memory.
    public func send(_ request: HTTPRequest, cancel: CancelToken? = nil) throws -> HTTPResponse {
        var headers: [String: String] = [:]
        var body = Data()
        let status = try perform(request, cancel: cancel, onHead: { _, h in headers = h }, onBody: { body.append($0) })
        return HTTPResponse(status: status, headers: headers, body: body)
    }

    /// GET /v1/health (unauthenticated liveness probe).
    public func health() throws -> [String: Any] {
        let r = try send(HTTPRequest(method: "GET", path: "/v1/health"))
        guard r.status == 200 else { throw WaterClientError.http(status: r.status, body: String(decoding: r.body, as: UTF8.self)) }
        return (try? JSONSerialization.jsonObject(with: r.body)) as? [String: Any] ?? [:]
    }

    /// POST /v1/turns, calling `onEvent` for each NDJSON event as soon as
    /// its line is complete — the same contract as the Go client's Turn.
    /// A non-200 reply throws `.http` with the daemon's error text (401 for a
    /// token the daemon doesn't know).
    ///
    /// - meetingID: a meeting session's id (Slice M). The daemon extends the
    ///   turn with that session's recent transcript (and taints the turn);
    ///   an unknown id is silently ignored. Nil or empty sends none.
    /// - onTaskID: called once, before any event, with the daemon's
    ///   `X-Water-Task-Id` — the id `cancelTask(id:token:)` takes. Closing
    ///   the stream (`cancel`) also cancels the turn; this is for a caller
    ///   that wants to cancel from somewhere other than the reading thread.
    ///
    /// The stream ends after exactly one terminal event (`done` or `error`):
    /// the daemon returns right after it, which ends the chunked body, so
    /// this returns without waiting for the socket to close.
    public func streamTurn(channel: Channel, prompt: String, token: String, clear: Bool = false,
                           meetingID: String? = nil,
                           cancel: CancelToken? = nil,
                           onTaskID: ((String) -> Void)? = nil,
                           onEvent: @escaping (TurnEvent) -> Void) throws {
        var body: [String: Any] = ["channel": channel.rawValue, "prompt": prompt, "clear": clear]
        if let m = meetingID?.trimmingCharacters(in: .whitespacesAndNewlines), !m.isEmpty { body["meeting_id"] = m }
        let req = try HTTPRequest.json("POST", "/v1/turns", token: token, body)
        var status = 0
        var errBody = Data()
        var splitter = NDJSONLineSplitter()
        try perform(req, cancel: cancel, onHead: { s, h in
            status = s
            // The parser lowercases header names.
            if s == 200, let id = h["x-water-task-id"], !id.isEmpty { onTaskID?(id) }
        }, onBody: { d in
            if status != 200 {
                if errBody.count < 64 * 1024 { errBody.append(d) }
                return
            }
            for line in try splitter.feed(d) {
                if let e = TurnEvent.decode(line: line) { onEvent(e) }
            }
        })
        if status != 200 {
            throw WaterClientError.http(status: status, body: String(decoding: errBody, as: UTF8.self))
        }
        for line in splitter.flush() {
            if let e = TurnEvent.decode(line: line) { onEvent(e) }
        }
    }

    /// POST /v1/tasks/{id}/cancel: cancels a running turn by the id
    /// `streamTurn` reported through `onTaskID`. 404 (`.http`) once the
    /// turn has already ended.
    public func cancelTask(id: String, token: String) throws {
        let ok = !id.isEmpty && id.unicodeScalars.allSatisfy {
            ("a"..."z").contains($0) || ("A"..."Z").contains($0) || ("0"..."9").contains($0) || $0 == "_" || $0 == "-"
        }
        guard ok else { throw WaterClientError.invalidRequest("bad task id") }
        let r = try send(try HTTPRequest.json("POST", "/v1/tasks/\(id)/cancel", token: token, [:]))
        if r.status != 200 {
            throw WaterClientError.http(status: r.status, body: String(decoding: r.body, as: UTF8.self))
        }
    }

    // MARK: - POSIX plumbing

    private func connectSocket() throws -> Int32 {
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let pathBytes = Array(socketPath.utf8)
        let capacity = MemoryLayout.size(ofValue: addr.sun_path)
        guard pathBytes.count < capacity else { throw WaterClientError.socket("socket path too long: \(socketPath)") }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: pathBytes)
            raw[pathBytes.count] = 0
        }
        addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)

        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw WaterClientError.socket("socket: \(String(cString: strerror(errno)))") }
        var one: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        var tv = timeval(tv_sec: Int(readTimeout), tv_usec: 0)
        _ = setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if rc != 0 {
            let e = errno
            Darwin.close(fd)
            if e == ENOENT || e == ECONNREFUSED { throw WaterClientError.daemonNotRunning(socketPath) }
            throw WaterClientError.socket("connect \(socketPath): \(String(cString: strerror(e)))")
        }
        return fd
    }

    private func writeAll(_ fd: Int32, _ data: Data, cancel: CancelToken?) throws {
        try data.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
            var off = 0
            while off < raw.count {
                let n = Darwin.write(fd, raw.baseAddress! + off, raw.count - off)
                if n < 0 {
                    if errno == EINTR { continue }
                    if cancel?.isCancelled == true { throw WaterClientError.cancelled }
                    throw WaterClientError.socket("write: \(String(cString: strerror(errno)))")
                }
                off += n
            }
        }
    }
}
