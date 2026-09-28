import Foundation

/// One raw HTTP/1.1 request to the water daemon, serialized by hand (the
/// daemon listens on a Unix socket, which URLSession doesn't do cleanly).
/// Mirrors internal/cli/daemonclient.go: `Authorization: Bearer <token>` and
/// `Content-Type: application/json` on every request.
public struct HTTPRequest: Equatable {
    public var method: String
    public var path: String
    public var token: String?
    public var body: Data?

    public init(method: String, path: String, token: String? = nil, body: Data? = nil) {
        self.method = method
        self.path = path
        self.token = token
        self.body = body
    }

    /// A JSON-bodied request (the body is encoded with sorted keys so the
    /// bytes are deterministic).
    public static func json(_ method: String, _ path: String, token: String?, _ object: [String: Any]) throws -> HTTPRequest {
        let body = try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])
        return HTTPRequest(method: method, path: path, token: token, body: body)
    }

    /// The exact bytes written to the socket. `Connection: close` keeps the
    /// exchange one request per connection, so end-of-stream is unambiguous.
    public func serialized() throws -> Data {
        for (field, value) in [("method", method), ("path", path), ("token", token ?? "")] {
            // Check scalars, not Characters: "\r\n" is one grapheme cluster
            // and equals neither "\r" nor "\n" as a Character.
            if value.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7f }) {
                throw WaterClientError.invalidRequest("\(field) contains a control character")
            }
        }
        if method.isEmpty || method.contains(" ") { throw WaterClientError.invalidRequest("bad method") }
        if !path.hasPrefix("/") || path.contains(" ") { throw WaterClientError.invalidRequest("bad path") }

        var head = "\(method) \(path) HTTP/1.1\r\n"
        head += "Host: water\r\n"
        if let token, !token.isEmpty {
            head += "Authorization: Bearer \(token)\r\n"
        }
        head += "Content-Type: application/json\r\n"
        head += "Content-Length: \(body?.count ?? 0)\r\n"
        head += "Connection: close\r\n"
        head += "\r\n"
        var out = Data(head.utf8)
        if let body { out.append(body) }
        return out
    }
}

/// Incremental HTTP/1.1 response parser: feed it bytes as they arrive and it
/// reports the status/headers once, then body bytes as they become available.
/// Handles Content-Length, `Transfer-Encoding: chunked` (what Go's net/http
/// uses for the daemon's streamed /v1/turns reply) and read-until-close.
public final class HTTPResponseParser {
    public private(set) var status: Int?
    /// Header names are lowercased.
    public private(set) var headers: [String: String] = [:]
    public private(set) var isComplete = false

    public var onHead: ((Int, [String: String]) -> Void)?
    public var onBody: ((Data) -> Void)?

    private enum Chunk { case size, data(Int), dataCRLF, trailer }
    private enum State { case head, length(Int), chunked(Chunk), untilClose, done }

    private var state: State = .head
    private var buf: [UInt8] = []
    private static let maxHead = 64 * 1024

    public init() {}

    public func feed(_ data: Data) throws {
        buf.append(contentsOf: data)
        try drain()
    }

    /// Call at end of stream. Throws if the body was cut short.
    public func finish() throws {
        switch state {
        case .untilClose:
            state = .done
            isComplete = true
        case .done:
            break
        case .head:
            throw WaterClientError.protocolError("connection closed before a complete response head")
        default:
            throw WaterClientError.protocolError("connection closed mid-body")
        }
    }

    private func emit(_ n: Int) {
        guard n > 0 else { return }
        let piece = Data(buf[0..<n])
        buf.removeFirst(n)
        onBody?(piece)
    }

    private func line() -> [UInt8]? {
        guard let i = findCRLF(buf, from: 0) else { return nil }
        let l = Array(buf[0..<i])
        buf.removeFirst(i + 2)
        return l
    }

    private func drain() throws {
        while true {
            switch state {
            case .done:
                buf.removeAll()
                return
            case .head:
                guard let end = findHeadEnd(buf) else {
                    if buf.count > Self.maxHead { throw WaterClientError.protocolError("response head too large") }
                    return
                }
                let head = String(decoding: buf[0..<end], as: UTF8.self)
                buf.removeFirst(end + 4)
                try parseHead(head)
            case .length(let remaining):
                let n = min(remaining, buf.count)
                emit(n)
                if remaining - n == 0 {
                    state = .done
                    isComplete = true
                } else {
                    state = .length(remaining - n)
                    return
                }
            case .untilClose:
                emit(buf.count)
                return
            case .chunked(.size):
                guard let l = line() else { return }
                let sizeText = String(decoding: l, as: UTF8.self)
                    .split(separator: ";", maxSplits: 1).first.map(String.init)?
                    .trimmingCharacters(in: .whitespaces) ?? ""
                guard let size = Int(sizeText, radix: 16), size >= 0 else {
                    throw WaterClientError.protocolError("bad chunk size \(sizeText.debugDescription)")
                }
                state = size == 0 ? .chunked(.trailer) : .chunked(.data(size))
            case .chunked(.data(let remaining)):
                let n = min(remaining, buf.count)
                emit(n)
                if remaining - n == 0 {
                    state = .chunked(.dataCRLF)
                } else {
                    state = .chunked(.data(remaining - n))
                    return
                }
            case .chunked(.dataCRLF):
                guard buf.count >= 2 else { return }
                guard buf[0] == 13, buf[1] == 10 else { throw WaterClientError.protocolError("missing CRLF after chunk") }
                buf.removeFirst(2)
                state = .chunked(.size)
            case .chunked(.trailer):
                guard let l = line() else { return }
                if l.isEmpty {
                    state = .done
                    isComplete = true
                }
            }
        }
    }

    private func parseHead(_ head: String) throws {
        let lines = head.components(separatedBy: "\r\n")
        let statusParts = lines[0].split(separator: " ", maxSplits: 2)
        guard statusParts.count >= 2, statusParts[0].hasPrefix("HTTP/1."), let code = Int(statusParts[1]) else {
            throw WaterClientError.protocolError("bad status line \(lines[0].debugDescription)")
        }
        var hs: [String: String] = [:]
        for l in lines.dropFirst() where !l.isEmpty {
            guard let colon = l.firstIndex(of: ":") else { continue }
            let name = l[..<colon].trimmingCharacters(in: .whitespaces).lowercased()
            let value = l[l.index(after: colon)...].trimmingCharacters(in: .whitespaces)
            hs[name] = hs[name].map { $0 + ", " + value } ?? value
        }
        status = code
        headers = hs
        onHead?(code, hs)

        if code == 204 || code == 304 || (100..<200).contains(code) {
            state = .done
            isComplete = true
        } else if hs["transfer-encoding"]?.lowercased().contains("chunked") == true {
            state = .chunked(.size)
        } else if let cl = hs["content-length"], let n = Int(cl) {
            state = n == 0 ? .done : .length(n)
            if n == 0 { isComplete = true }
        } else {
            state = .untilClose
        }
    }
}

func findCRLF(_ b: [UInt8], from: Int) -> Int? {
    var i = from
    while i + 1 < b.count {
        if b[i] == 13 && b[i + 1] == 10 { return i }
        i += 1
    }
    return nil
}

func findHeadEnd(_ b: [UInt8]) -> Int? {
    var i = 0
    while i + 3 < b.count {
        if b[i] == 13 && b[i + 1] == 10 && b[i + 2] == 13 && b[i + 3] == 10 { return i }
        i += 1
    }
    return nil
}
