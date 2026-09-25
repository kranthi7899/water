import Foundation

/// The daemon's required shape for a client-generated turn id
/// (internal/gateway/router.go's `turnPartialIDPattern`:
/// `^[A-Za-z0-9_-]{8,64}$`, checked against `POST /v1/turns/{id}/partial`'s
/// path parameter). A plain `UUID().uuidString` (36 hex digits and hyphens,
/// uppercase) already satisfies this with no trimming or reformatting
/// needed.
public enum TurnID {
    public static func generate() -> String { UUID().uuidString }

    public static func isValid(_ s: String) -> Bool {
        guard (8...64).contains(s.count) else { return false }
        return s.unicodeScalars.allSatisfy {
            ("a"..."z").contains($0) || ("A"..."Z").contains($0) || ("0"..."9").contains($0) || $0 == "_" || $0 == "-"
        }
    }
}

/// A per-turn-id, fixed-window rate limit on how many partials this client
/// sends: the plan's own "at most 5/s" client-side cap, well under the
/// daemon's independent 20/s-per-turn-id limit
/// (`internal/gateway/router.go`'s `partialLimiter`, `maxPartialsPerSecond`).
/// Shaped like that server-side limiter (a counter that resets whenever a
/// new one-second window starts) but evaluated against an injected clock,
/// so tests don't need to sleep for real seconds.
public final class PartialRateLimiter {
    public let maxPerSecond: Int
    private var windowStart: TimeInterval = -.infinity
    private var count = 0

    public init(maxPerSecond: Int = 5) { self.maxPerSecond = maxPerSecond }

    /// Reports whether one more send is allowed at `now`, and records it if
    /// so. `now` is any monotonically-increasing seconds value; production
    /// uses `Date().timeIntervalSinceReferenceDate`, tests use their own.
    public func allow(now: TimeInterval) -> Bool {
        if now - windowStart >= 1.0 {
            windowStart = now
            count = 0
        }
        guard count < maxPerSecond else { return false }
        count += 1
        return true
    }
}

/// What `PartialStreamer` needs to actually send one partial —
/// `UnixSocketClient` conforms below. A protocol so tests can substitute a
/// fake that just counts calls, with no real socket.
public protocol PartialTransport {
    /// Posts one partial transcript for `turnID` and returns the daemon's
    /// raw HTTP status (202 on success, 404 from a daemon that predates
    /// R-19's endpoint, or anything else) rather than throwing on a non-200
    /// status: `PartialStreamer` needs to see 404 specifically, and every
    /// other status is equally none of its business (fire-and-forget).
    /// Throws only on a transport-level failure (daemon unreachable, bad
    /// turn id, etc.) — `PartialStreamer` treats that the same as any other
    /// status it doesn't recognize: ignored.
    func postPartial(turnID: String, text: String, seq: Int, channel: Channel, token: String) throws -> Int
}

extension UnixSocketClient: PartialTransport {
    /// `POST /v1/turns/{id}/partial` (the server route R-19 built; this is
    /// R-27's client use of it). Body: `{"text": "...", "seq": N,
    /// "channel": "voice"}`, exactly what `internal/gateway/router.go`'s
    /// `handleTurnPartial` decodes.
    public func postPartial(turnID: String, text: String, seq: Int, channel: Channel, token: String) throws -> Int {
        guard TurnID.isValid(turnID) else { throw WaterClientError.invalidRequest("bad turn id") }
        let req = try HTTPRequest.json("POST", "/v1/turns/\(turnID)/partial", token: token,
                                       ["text": text, "seq": seq, "channel": channel.rawValue])
        return try send(req).status
    }
}

/// Streams `SFSpeechRecognizer`'s intermediate (non-final) results to
/// `POST /v1/turns/{id}/partial` for one push-to-talk turn (R-27, backing
/// R-19's speculative-prefetch endpoint server-side): fire-and-forget, at
/// most 5/s, never blocking the caller and never surfacing a failure — the
/// plan treats this as best-effort speculative work that is never required
/// for correctness. The real, authoritative turn is always sent separately
/// through `POST /v1/turns`, carrying this instance's `turnID` as
/// `turn_id` so the daemon can correlate it with whatever speculative work
/// it already did.
///
/// Every mutable access — the rate-limit decision, the sequence counter and
/// the `disabled` flag — runs inside one `executor` closure per `post`
/// call. The default executor is a private serial `DispatchQueue`, so in
/// production every access is naturally single-threaded with no extra
/// locking; a test injects a synchronous executor (`{ $0() }`) to make the
/// whole thing deterministic with no real concurrency involved.
public final class PartialStreamer {
    public let turnID: String
    private let channel: Channel
    private let tokenProvider: () throws -> String
    private let transport: PartialTransport
    private let limiter: PartialRateLimiter
    private let clock: () -> TimeInterval
    private let executor: (@escaping () -> Void) -> Void
    private var seq = 0

    /// Set once a `POST /v1/turns/{id}/partial` reply is 404 (a daemon
    /// built before R-19); every later `post` call then becomes a no-op for
    /// the rest of this session, per the plan's own stated rule. Reflects
    /// state as of the last `post` call the executor has actually run.
    public private(set) var disabled = false

    /// - executor: nil (the production default) gives this instance its own
    ///   private serial `DispatchQueue`, created here rather than shared
    ///   across every `PartialStreamer`, so one turn's backlog can never
    ///   delay another's. Tests pass a synchronous executor (`{ $0() }`) to
    ///   make behavior deterministic.
    public init(turnID: String = TurnID.generate(),
                channel: Channel,
                tokenProvider: @escaping () throws -> String,
                transport: PartialTransport,
                maxPerSecond: Int = 5,
                clock: @escaping () -> TimeInterval = { Date().timeIntervalSinceReferenceDate },
                executor: ((@escaping () -> Void) -> Void)? = nil) {
        self.turnID = turnID
        self.channel = channel
        self.tokenProvider = tokenProvider
        self.transport = transport
        self.limiter = PartialRateLimiter(maxPerSecond: maxPerSecond)
        self.clock = clock
        if let executor {
            self.executor = executor
        } else {
            let queue = DispatchQueue(label: "water.partials.\(turnID)", qos: .utility)
            self.executor = { queue.async(execute: $0) }
        }
    }

    /// Best-effort: posts one partial transcript, subject to the 5/s cap
    /// and the 404-disables-the-session rule above. Never throws, never
    /// blocks the caller (the network call itself always runs inside
    /// `executor`), and never retries a failed or dropped send — the next
    /// recognition update supersedes it anyway.
    public func post(_ text: String) {
        executor { [weak self] in
            guard let self, !self.disabled else { return }
            guard self.limiter.allow(now: self.clock()) else { return }
            self.seq += 1
            let seq = self.seq
            do {
                let token = try self.tokenProvider()
                let status = try self.transport.postPartial(turnID: self.turnID, text: text, seq: seq, channel: self.channel, token: token)
                if status == 404 { self.disabled = true }
            } catch {
                // Best-effort: never retried, never surfaced to the caller.
            }
        }
    }
}
