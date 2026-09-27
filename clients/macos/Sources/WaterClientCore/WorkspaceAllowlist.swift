import Foundation

/// Decides which requests from the workspace web view (Slice V-ui,
/// docs/slices/V.md §5) the native `water://` scheme handler may proxy into
/// the daemon's Unix socket. The web view's own JavaScript is the untrusted
/// side here: it renders attacker-controlled text (decision evidence, email
/// bodies, meeting notes), so the handler forwards only the exact routes the
/// UI uses (§5a/§5b) and denies everything else — `/v1/tools/invoke`,
/// `/v1/quick/invoke`, any `/v1/twinlink/` route, token/client routes, and
/// every daemon route the UI simply doesn't call.
///
/// It is deliberately strict rather than clever:
/// - The URL must be exactly `water://app/...`: no user, password or port.
/// - The path is judged in its raw, percent-encoded form, and any `%` at all
///   is refused. Every id the daemon mints is `[A-Za-z0-9_-]` (`env_…`,
///   `thr_…`, `card-…`, `task_…`), so nothing legitimate needs an escape, and
///   refusing them outright closes `%2e%2e`, `%2f`, `%00` and double-encoding
///   in one rule instead of decoding and re-checking.
/// - `.` and `..` segments, empty segments (`//`), backslashes, and control
///   or non-ASCII characters are refused.
/// - A query string is allowed only on the two list routes that take one,
///   only with their known keys (each at most once), and only with values
///   from `[A-Za-z0-9._-]`. Any other route with a query is refused.
/// - The path forwarded to the daemon is rebuilt from the pieces that passed,
///   never copied from the input.
public enum WorkspaceAllowlist {
    public static let scheme = "water"
    public static let host = "app"
    /// The daemon's `gateway.UIPrefix`: where the embedded web UI's static
    /// assets are served.
    public static let uiPrefix = "/ui/"
    /// What the workspace window loads.
    public static let startURL = URL(string: "water://app/ui/")!

    public enum Decision: Equatable {
        /// Forward `method` and `path` (path plus `?query` when kept) as-is.
        case allow(method: String, path: String)
        case deny(String)

        public var isAllowed: Bool { if case .allow = self { return true } else { return false } }
    }

    private enum Segment {
        case lit(String)
        case id
    }

    private struct Route {
        let method: String
        let segments: [Segment]
        /// The query keys this route accepts; empty means no query at all.
        let queryKeys: Set<String>
    }

    private static func route(_ method: String, _ path: String, query: Set<String> = []) -> Route {
        let segs: [Segment] = path.split(separator: "/", omittingEmptySubsequences: false).dropFirst().map {
            $0 == "{id}" ? .id : .lit(String($0))
        }
        return Route(method: method, segments: segs, queryKeys: query)
    }

    /// Exactly the daemon routes the web UI calls (internal/webui/static/
    /// api.js, pinned there by internal/gateway/webui_test.go's
    /// TestUIOnlyCallsAllowlistedRoutes). Adding a route here is a
    /// deliberate security decision: never add `/v1/tools`, `/v1/quick`,
    /// `/v1/twinlink`, `/v1/turns` or any token/client route.
    private static let routes: [Route] = [
        route("GET", "/v1/today"),
        route("GET", "/v1/decisions"),
        route("POST", "/v1/decisions/{id}/stage"),
        route("POST", "/v1/decisions/{id}/dismiss"),
        route("GET", "/v1/approvals", query: ["status", "kind", "limit"]),
        route("GET", "/v1/approvals/{id}"),
        route("POST", "/v1/approvals/{id}/decision"),
        // V-ui2: approval edit (voids the envelope, stages a new pending
        // one; nothing runs). Added by hand here, in api.js and in the Go
        // pin test.
        route("POST", "/v1/approvals/{id}/edit"),
        // Phase 3a: a person-request approval's "Request changes" button.
        // Added by hand here, in api.js and in the Go pin test.
        route("POST", "/v1/approvals/{id}/request-changes"),
        route("GET", "/v1/threads"),
        route("POST", "/v1/threads"),
        route("POST", "/v1/threads/anchor"),
        route("GET", "/v1/threads/{id}"),
        route("POST", "/v1/threads/{id}/messages"),
        route("POST", "/v1/tasks/{id}/cancel"),
        // Phase 3d: ?upcoming=1 switches this same route to future events
        // from the events table instead of recent meeting_sessions rows
        // (docs/slices/UI.md Phase 3d) -- no new route.
        route("GET", "/v1/meetings", query: ["limit", "upcoming"]),
        route("GET", "/v1/meetings/{id}"),
        // Slice UI Phase 2: the sidebar's Dashboards page and Workspaces
        // disclosure. Both are read-only lists with no id segment.
        route("GET", "/v1/workspaces"),
        route("GET", "/v1/dashboards"),
        // Phase 4: one dashboard's computed tiles.
        route("GET", "/v1/dashboards/{id}"),
        // Phase 3b: the per-action stage route (two dynamic segments; the
        // route() helper only recognises the literal "{id}" token, so it is
        // repeated) and the related-data panel behind "View related data
        // (N)". The old, function-keyed decisions/{id}/stage route above is
        // kept for one release, unchanged.
        route("POST", "/v1/decisions/{id}/actions/{id}/stage"),
        route("GET", "/v1/decisions/{id}/related"),
        // Phase 3c: the Drafts editor (U10-A, a real drafts table --
        // Save/"Send for approval" -- rather than a pending envelope being
        // the draft). Added by hand here, in api.js and in the Go pin test.
        route("GET", "/v1/drafts"),
        route("GET", "/v1/drafts/{id}"),
        route("POST", "/v1/drafts/{id}"),
        route("POST", "/v1/drafts/{id}/submit"),
    ]

    /// Judges a URL the web view asked for.
    public static func decide(method: String, url: URL) -> Decision {
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return .deny("unparseable URL") }
        guard c.scheme?.lowercased() == scheme else { return .deny("wrong scheme") }
        guard c.percentEncodedHost == host else { return .deny("wrong host") }
        guard c.percentEncodedUser == nil, c.percentEncodedPassword == nil, c.port == nil else {
            return .deny("user, password or port in URL")
        }
        return decide(method: method, rawPath: c.percentEncodedPath, rawQuery: c.percentEncodedQuery)
    }

    /// Judges a raw (still percent-encoded) path and query. `rawQuery` is
    /// the text after `?`, nil when the URL had no `?` at all.
    public static func decide(method: String, rawPath: String, rawQuery: String?) -> Decision {
        guard method == "GET" || method == "POST" else { return .deny("method \(method) not allowed") }
        guard rawPath.hasPrefix("/") else { return .deny("path must be absolute") }
        guard rawPath.unicodeScalars.allSatisfy(isPathScalar) else {
            return .deny("path has an escape, a control, non-ASCII or reserved character")
        }
        let segments = rawPath.split(separator: "/", omittingEmptySubsequences: false).dropFirst().map(String.init)

        // UI assets: `/ui/` itself, or one flat file in it.
        if rawPath == uiPrefix || rawPath.hasPrefix(uiPrefix) {
            guard method == "GET" else { return .deny("UI assets are GET only") }
            guard rawQuery == nil else { return .deny("no query on UI assets") }
            if rawPath == uiPrefix { return .allow(method: method, path: uiPrefix) }
            guard segments.count == 2, isAssetName(segments[1]) else { return .deny("not a UI asset path") }
            return .allow(method: method, path: uiPrefix + segments[1])
        }

        // Empty segments ("//", or a trailing "/") and dot segments.
        guard !segments.contains(where: { $0.isEmpty || $0 == "." || $0 == ".." }) else {
            return .deny("empty or dot path segment")
        }

        for r in routes where r.method == method && r.segments.count == segments.count {
            var ok = true
            for (want, got) in zip(r.segments, segments) {
                switch want {
                case .lit(let s): ok = s == got
                case .id: ok = isID(got)
                }
                if !ok { break }
            }
            guard ok else { continue }
            let path = "/" + segments.joined(separator: "/")
            guard let rawQuery else { return .allow(method: method, path: path) }
            guard !r.queryKeys.isEmpty else { return .deny("no query allowed on \(path)") }
            guard let q = canonicalQuery(rawQuery, allowed: r.queryKeys) else { return .deny("query not allowed") }
            return .allow(method: method, path: q.isEmpty ? path : path + "?" + q)
        }
        return .deny("\(method) \(rawPath) is not a workspace route")
    }

    // MARK: - pieces

    private static func isAlnum(_ s: Unicode.Scalar) -> Bool {
        ("a"..."z").contains(s) || ("A"..."Z").contains(s) || ("0"..."9").contains(s)
    }

    /// Unreserved characters plus "/" — no "%", "\", ";", "?" or "#".
    private static func isPathScalar(_ s: Unicode.Scalar) -> Bool {
        isAlnum(s) || s == "/" || s == "-" || s == "_" || s == "."
    }

    /// The page's views (`app.js` VIEWS), for `window.water.open(view, id)`
    /// calls made natively. Slice UI Phase 2 adds "dashboards" and
    /// "workspaces".
    public static let pageViews: Set<String> = ["today", "decisions", "drafts", "approvals", "threads", "meetings", "dashboards", "workspaces"]

    /// Whether native code may ask the page to open `view` at `id` (the
    /// Activity HUD's Edit, a tapped notification). Both travel as
    /// `callAsyncJavaScript` arguments, never string-built JS; this keeps
    /// them to known views and daemon ids anyway. A nil id opens the view's
    /// list (a tapped "and N more" notification opens Today).
    ///
    /// "workspaces" is the one view whose id can carry a second segment
    /// (the page's own "<id>/<sub>" hash-route shape, docs/slices/UI.md
    /// Phase 2): `id` there is either a bare workspace id, or exactly two
    /// slash-separated segments, each independently checked with `isID` —
    /// never the whole compound string, which `isID` would always reject
    /// once it contains a "/".
    public static func isOpenTarget(view: String, id: String?) -> Bool {
        guard pageViews.contains(view) else { return false }
        guard let id else { return true }
        guard view == "workspaces" else { return isID(id) }
        let parts = id.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
        switch parts.count {
        case 1: return isID(parts[0])
        case 2: return isID(parts[0]) && isID(parts[1])
        default: return false
        }
    }

    /// Every id the daemon hands the UI: `env_…`, `thr_…`, `card-…`, task ids.
    static func isID(_ s: String) -> Bool {
        !s.isEmpty && s.count <= 128 && s.unicodeScalars.allSatisfy { isAlnum($0) || $0 == "_" || $0 == "-" }
    }

    /// U16's fixed allowlist for `open-external`: the only hosts the native
    /// side will ever hand to `NSWorkspace`. Exact match only — no
    /// subdomain rule of any kind, the most conservative reading of "keep
    /// it conservative": a real subdomain that legitimately needs opening
    /// would have to be added here by hand, as its own owner decision,
    /// never inferred from one of these five.
    public static let externalHostAllowlist: Set<String> = [
        "linear.app", "mail.google.com", "calendar.google.com", "docs.google.com", "github.com",
    ]

    /// Resolves an `open-external` `WorkspaceMessage`'s own `url` field to
    /// something safe to open, or `nil` to open nothing. `source` and `id`
    /// are not used here at all — see `WorkspaceMessage`'s own doc comment
    /// for why the message carries a server-resolved `url` rather than
    /// making the native side re-derive one from `source`/`id` alone. This
    /// is the only check standing between the workspace page and
    /// `NSWorkspace`, so it is deliberately strict:
    ///   - `urlString` must parse as an absolute URL at all (a malformed or
    ///     non-URL string is refused, never crashes anything);
    ///   - its scheme must be exactly `https`;
    ///   - it must carry no userinfo (`user:pass@host`), which could hide
    ///     the real host from a casual read of the URL;
    ///   - its host must be an exact match in `externalHostAllowlist`.
    public static func resolvedExternalURL(source: String, id: String, urlString: String) -> URL? {
        guard let url = URL(string: urlString) else { return nil }
        guard url.scheme?.lowercased() == "https" else { return nil }
        guard url.user == nil, url.password == nil else { return nil }
        guard let host = url.host?.lowercased(), !host.isEmpty else { return nil }
        guard externalHostAllowlist.contains(host) else { return nil }
        return url
    }

    /// `name.html`, `name.css` or `name.js`, name from `[A-Za-z0-9_-]` — the
    /// only file types internal/webui serves.
    private static func isAssetName(_ s: String) -> Bool {
        let parts = s.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 2, ["html", "css", "js"].contains(parts[1]) else { return false }
        return isID(String(parts[0]))
    }

    /// Rebuilds a query from known keys, each at most once, with plain
    /// values. Nil means refuse the request.
    private static func canonicalQuery(_ raw: String, allowed: Set<String>) -> String? {
        guard raw.count <= 256 else { return nil }
        if raw.isEmpty { return "" }
        var seen = Set<String>()
        var out: [String] = []
        for pair in raw.split(separator: "&", omittingEmptySubsequences: false) {
            let kv = pair.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
            guard kv.count == 2 else { return nil }
            let k = String(kv[0]), v = String(kv[1])
            guard allowed.contains(k), !seen.contains(k) else { return nil }
            guard v.count <= 64, v.unicodeScalars.allSatisfy({ isAlnum($0) || $0 == "." || $0 == "_" || $0 == "-" }) else {
                return nil
            }
            seen.insert(k)
            out.append(k + "=" + v)
        }
        return out.joined(separator: "&")
    }
}

/// A message the workspace page posts to the native `water` script message
/// handler (`window.webkit.messageHandlers.water.postMessage(...)`). The
/// page is untrusted, so this is a closed set: anything that isn't exactly
/// one of these shapes is ignored (V-ui2: the bar's hold-to-talk mic).
public enum WorkspaceMessage: Equatable {
    /// The bar's mic was pressed: start listening, and send the transcript
    /// as a voice turn into `thread` (`POST /v1/turns` with `thread_id`,
    /// native only). Exactly `{type: "mic-down", thread: "thr_<hex>"}`.
    case micDown(thread: String)
    /// The bar's mic was released: stop listening and send. Exactly
    /// `{type: "mic-up"}`.
    case micUp
    /// "View related data"'s per-source open affordance (docs/slices/UI.md
    /// U16). Exactly `{type: "open-external", source, id, url}`.
    ///
    /// `url` is exactly what the daemon's own `GET
    /// /v1/decisions/{id}/related` already resolved server-side from the
    /// store record `source`/`id` refer to (a Linear/GitHub issue's own API
    /// URL, a Google Doc's own Drive URL, ...) — never page text, never
    /// anything a model wrote, and never re-derived here: the native side's
    /// only job for this case is `WorkspaceAllowlist.resolvedExternalURL`'s
    /// allowlist check, then handing the result to `NSWorkspace`. `source`
    /// and `id` travel alongside it only as the same opaque, shape-checked
    /// identifiers the related-data response itself carries (for logging or
    /// future use); they play no part in deciding what may be opened.
    case openExternal(source: String, id: String, url: String)

    public static let handlerName = "water"

    /// The longest `url` string this case accepts (comfortably above any
    /// real Linear/Drive/GitHub URL); a longer string is refused outright
    /// rather than parsed.
    private static let maxExternalURLLength = 2048

    /// `body` is what WebKit hands over (a JSON-like Foundation value).
    public static func parse(_ body: Any) -> WorkspaceMessage? {
        guard let dict = body as? [String: Any], let type = dict["type"] as? String else { return nil }
        switch type {
        case "mic-down":
            guard dict.count == 2, let thread = dict["thread"] as? String, isThreadID(thread) else { return nil }
            return .micDown(thread: thread)
        case "mic-up":
            guard dict.count == 1 else { return nil }
            return .micUp
        case "open-external":
            guard dict.count == 4,
                  let source = dict["source"] as? String, WorkspaceAllowlist.isID(source),
                  let id = dict["id"] as? String, WorkspaceAllowlist.isID(id),
                  let urlString = dict["url"] as? String, urlString.utf8.count <= maxExternalURLLength,
                  URL(string: urlString) != nil
            else { return nil }
            return .openExternal(source: source, id: id, url: urlString)
        default:
            return nil
        }
    }

    /// `^thr_[0-9a-f]+$` (what the daemon's store mints), at most 64 hex
    /// digits. The daemon checks the same shape again on `thread_id`.
    public static func isThreadID(_ s: String) -> Bool {
        guard s.hasPrefix("thr_") else { return false }
        let hex = s.unicodeScalars.dropFirst(4)
        return !hex.isEmpty && hex.count <= 64 && hex.allSatisfy { ("0"..."9").contains($0) || ("a"..."f").contains($0) }
    }
}

/// Which workspace thread the next voice transcript belongs to (V-ui2).
/// Set only by the page's mic-down; any other capture (the voice hotkey,
/// the menu's Talk) clears it, so a hotkey question never lands in a thread
/// the page opened earlier. `take()` hands it out once, for exactly one
/// transcript.
public struct WorkspaceVoiceTarget: Equatable {
    public private(set) var thread: String?

    public init() {}

    /// The page's mic went down for `thread` (already shape-checked by
    /// `WorkspaceMessage.parse`).
    public mutating func micDown(thread: String) {
        self.thread = WorkspaceMessage.isThreadID(thread) ? thread : nil
    }

    /// A capture that didn't come from the page started, or the capture
    /// failed: nothing goes to a thread.
    public mutating func clear() { thread = nil }

    /// The transcript is ready: the thread it goes to (if any), once.
    public mutating func take() -> String? {
        defer { thread = nil }
        return thread
    }
}

/// Request-side limits and header handling for the `water://` proxy, kept
/// here (pure, tested) rather than in the AppKit target.
public enum WorkspaceProxy {
    /// The largest request body the web view may send. A staged email or a
    /// thread message is a few KB; this is headroom, not a target.
    public static let maxRequestBody = 1 << 20

    /// Response headers passed from the daemon back to the web view. Only
    /// these: never hop-by-hop framing (`transfer-encoding`,
    /// `content-length` — WebKit frames the body itself from `didReceive`),
    /// never `set-cookie`, and never anything that could echo credentials.
    /// The request's `Authorization` header is native-only and is never a
    /// response header, but this list also guarantees nothing named like it
    /// could be passed through.
    public static let forwardedResponseHeaders: [String] = [
        "content-type",
        "content-security-policy",
        "x-content-type-options",
        "referrer-policy",
        "x-frame-options",
        "cross-origin-opener-policy",
        "cross-origin-resource-policy",
        "cache-control",
        "x-water-task-id",
        "x-water-thread-id",
    ]

    /// Picks the response headers to hand WebKit from the daemon's
    /// (lowercased) headers, re-cased canonically.
    public static func responseHeaders(from daemon: [String: String]) -> [String: String] {
        var out: [String: String] = [:]
        for name in forwardedResponseHeaders {
            guard let v = daemon[name] else { continue }
            out[canonicalName(name)] = v
        }
        return out
    }

    /// The headers of a response the proxy produces itself (a denial, or a
    /// daemon that can't be reached): plain text, with the UI's own CSP
    /// shape so even an error page can run nothing.
    public static func localErrorHeaders() -> [String: String] {
        [
            "Content-Type": "text/plain; charset=utf-8",
            "Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
            "X-Content-Type-Options": "nosniff",
            "Cache-Control": "no-store",
        ]
    }

    static func canonicalName(_ lower: String) -> String {
        lower.split(separator: "-").map { part -> String in
            // "x-water-task-id" -> "X-Water-Task-Id"
            part.prefix(1).uppercased() + part.dropFirst()
        }.joined(separator: "-")
    }
}
