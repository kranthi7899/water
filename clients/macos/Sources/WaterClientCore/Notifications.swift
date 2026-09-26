import Foundation

// Native notifications (Slice V-notify, docs/slices/V.md §7.4). The daemon's
// needs-you service records each decision or approval that crosses the
// threshold once, ever; the app's notifier polls GET /v1/notifications,
// shows banners, and marks each delivered once the OS accepted it. These
// routes are native-only: they are not in WorkspaceAllowlist.routes, so the
// workspace page can never reach them.
//
// Everything here is pure; the app's Notifier does the I/O.

/// One undelivered notification, as GET /v1/notifications returns it. Title
/// and body come from the record (a decision's lead can quote an email
/// subject), so they are untrusted text, shown only as plain strings.
public struct PendingNotification: Equatable {
    public var id: String
    public var recordType: String
    public var recordID: String
    public var title: String
    public var body: String

    public init(id: String, recordType: String, recordID: String, title: String, body: String) {
        self.id = id
        self.recordType = recordType
        self.recordID = recordID
        self.title = title
        self.body = body
    }

    /// Parses the list body; nil if it isn't a JSON array. A row without an
    /// id is dropped.
    public static func parseList(_ data: Data) -> [PendingNotification]? {
        guard let rows = (try? JSONSerialization.jsonObject(with: data)) as? [Any] else { return nil }
        return rows.compactMap { row in
            guard let o = row as? [String: Any], let id = o["id"] as? String, !id.isEmpty else { return nil }
            return PendingNotification(id: id, recordType: o["record_type"] as? String ?? "",
                                       recordID: o["record_id"] as? String ?? "",
                                       title: o["title"] as? String ?? "", body: o["body"] as? String ?? "")
        }
    }
}

/// What a tapped notification anchors a thread to. Only decisions and
/// approvals notify, and only a plain daemon id (`[A-Za-z0-9_-]+`, at most
/// 128) is accepted, so nothing else can reach the anchor request or the
/// page.
public struct NotificationAnchor: Equatable {
    public enum Kind: String, Equatable {
        case decision
        case approval
    }

    public let type: Kind
    public let id: String

    public init(type: Kind, id: String) {
        self.type = type
        self.id = id
    }

    public init?(recordType: String, recordID: String) {
        guard let k = Kind(rawValue: recordType), WorkspaceAllowlist.isID(recordID) else { return nil }
        self.init(type: k, id: recordID)
    }

    /// The banner's userInfo: the record type and id, nothing else.
    public var userInfo: [String: String] { ["record_type": type.rawValue, "record_id": id] }

    /// Reads a tapped banner's userInfo back, re-validating it.
    public init?(userInfo: [AnyHashable: Any]) {
        guard let t = userInfo["record_type"] as? String, let id = userInfo["record_id"] as? String else { return nil }
        self.init(recordType: t, recordID: id)
    }

    /// The workspace view that lists this record, for when its thread
    /// can't be opened.
    public var fallbackView: String {
        switch type {
        case .decision: return "decisions"
        case .approval: return "approvals"
        }
    }
}

/// One banner to post. `notificationIDs` are the daemon notifications it
/// covers: one for a record banner, the remainder for the "and N more"
/// summary. Each is marked delivered only after the OS accepted the banner.
public struct NotificationBanner: Equatable {
    public var identifier: String
    public var title: String
    public var body: String
    /// Nil for the summary, or for a record that isn't a decision/approval
    /// with a plain id; a tap then opens Today.
    public var anchor: NotificationAnchor?
    public var notificationIDs: [String]

    public var userInfo: [String: String] { anchor?.userInfo ?? [:] }
}

/// Decides what each poll shows. It dedupes by notification id (and by
/// record, should the daemon ever list one record twice), shows at most
/// `maxBannersPerPoll` record banners plus one "and N more" summary, and
/// remembers what is in flight, posted or marked so a slow or failed mark
/// never shows a banner twice.
public struct NotificationPlanner {
    public static let maxBannersPerPoll = 3
    public static let maxTitleChars = 120
    public static let maxBodyChars = 240

    public struct Plan: Equatable {
        public var banners: [NotificationBanner] = []
        /// Posted earlier but not confirmed delivered: mark again, don't show.
        public var remark: [String] = []
        /// Notification ids that aren't plain ids: never shown or marked.
        public var rejected: [String] = []
    }

    private var inFlight: Set<String> = []
    private var posted: Set<String> = []
    private var marked: Set<String> = []

    public init() {}

    public mutating func plan(_ list: [PendingNotification]) -> Plan {
        var out = Plan()
        var seenIDs = Set<String>()
        var fresh: [PendingNotification] = []
        for n in list {
            guard seenIDs.insert(n.id).inserted else { continue }
            guard WorkspaceAllowlist.isID(n.id) else { out.rejected.append(n.id); continue }
            if marked.contains(n.id) || inFlight.contains(n.id) { continue }
            if posted.contains(n.id) { out.remark.append(n.id); continue }
            fresh.append(n)
        }

        // Group by record, keeping first-seen (oldest) order.
        var groups: [(first: PendingNotification, ids: [String])] = []
        var byRecord: [String: Int] = [:]
        for n in fresh {
            let key = n.recordType + "\u{0}" + n.recordID
            if let i = byRecord[key] {
                groups[i].ids.append(n.id)
            } else {
                byRecord[key] = groups.count
                groups.append((n, [n.id]))
            }
        }

        for g in groups.prefix(Self.maxBannersPerPoll) {
            let n = g.first
            let title = n.title.trimmingCharacters(in: .whitespacesAndNewlines)
            out.banners.append(NotificationBanner(
                identifier: n.id,
                title: Self.clip(title.isEmpty ? "Water: something needs you" : title, Self.maxTitleChars),
                body: Self.clip(n.body.trimmingCharacters(in: .whitespacesAndNewlines), Self.maxBodyChars),
                anchor: NotificationAnchor(recordType: n.recordType, recordID: n.recordID),
                notificationIDs: g.ids))
        }
        let rest = groups.dropFirst(Self.maxBannersPerPoll)
        if !rest.isEmpty {
            let count = rest.count
            out.banners.append(NotificationBanner(
                identifier: "more-" + rest.first!.first.id,
                title: count == 1 ? "And 1 more needs you" : "And \(count) more need you",
                body: "Open the workspace to see them.",
                anchor: nil,
                notificationIDs: rest.flatMap(\.ids)))
        }
        for b in out.banners { inFlight.formUnion(b.notificationIDs) }
        return out
    }

    /// The OS accepted the banner: its ids now only need marking.
    public mutating func posted(_ banner: NotificationBanner) {
        inFlight.subtract(banner.notificationIDs)
        posted.formUnion(banner.notificationIDs)
    }

    /// The OS refused it (or permission is missing): try again next poll.
    public mutating func postFailed(_ banner: NotificationBanner) {
        inFlight.subtract(banner.notificationIDs)
    }

    /// The daemon confirmed delivery.
    public mutating func marked(_ id: String) {
        inFlight.remove(id)
        posted.remove(id)
        marked.insert(id)
    }

    static func clip(_ s: String, _ max: Int) -> String {
        s.count <= max ? s : String(s.prefix(max - 1)) + "…"
    }
}

/// Where a tapped banner goes in the workspace.
public enum NotificationTapTarget: Equatable {
    /// `window.water.open('threads', id)`.
    case thread(String)
    /// `window.water.open(view, id)`; id nil opens the view's list.
    case view(String, id: String?)
}

public enum NotificationActions {
    /// Enough for any realistic backlog; the planner shows 3 plus a summary.
    public static let pollLimit = 50

    public static func list(limit: Int = pollLimit, token: String) -> HTTPRequest {
        HTTPRequest(method: "GET", path: "/v1/notifications?undelivered=1&limit=\(max(1, limit))", token: token)
    }

    public static func delivered(id: String, token: String) throws -> HTTPRequest {
        guard WorkspaceAllowlist.isID(id) else { throw WaterClientError.invalidRequest("bad notification id") }
        return HTTPRequest(method: "POST", path: "/v1/notifications/\(id)/delivered", token: token)
    }

    /// POST /v1/threads/anchor: get or create the record's thread.
    public static func anchorThread(_ anchor: NotificationAnchor, token: String) throws -> HTTPRequest {
        guard WorkspaceAllowlist.isID(anchor.id) else { throw WaterClientError.invalidRequest("bad anchor id") }
        return try HTTPRequest.json("POST", "/v1/threads/anchor", token: token,
                                    ["anchor_type": anchor.type.rawValue, "anchor_id": anchor.id])
    }

    /// Where a tap lands, from the anchor request's result (`status` nil when
    /// the daemon couldn't be reached). Only a 200 naming a thread for this
    /// exact anchor, with a plain id, opens the thread. Anything else (a 404
    /// because the card was dismissed or the envelope expired, an error, an
    /// odd body) opens the record's own view instead.
    public static func tapTarget(anchor: NotificationAnchor?, status: Int?, body: Data?) -> NotificationTapTarget {
        guard let anchor else { return .view("today", id: nil) }
        let fallback = NotificationTapTarget.view(anchor.fallbackView, id: anchor.id)
        guard status == 200, let body,
              let o = (try? JSONSerialization.jsonObject(with: body)) as? [String: Any],
              let t = o["thread"] as? [String: Any],
              let id = t["id"] as? String, WorkspaceAllowlist.isID(id),
              t["anchor_type"] as? String == anchor.type.rawValue,
              t["anchor_id"] as? String == anchor.id
        else { return fallback }
        return .thread(id)
    }
}

extension UnixSocketClient {
    /// GET /v1/notifications?undelivered=1.
    public func fetchUndeliveredNotifications(token: String, limit: Int = NotificationActions.pollLimit) throws -> [PendingNotification] {
        let r = try send(NotificationActions.list(limit: limit, token: token))
        guard r.status == 200 else {
            throw WaterClientError.http(status: r.status, body: String(decoding: r.body.prefix(4096), as: UTF8.self))
        }
        guard let list = PendingNotification.parseList(r.body) else { throw WaterClientError.protocolError("bad notifications body") }
        return list
    }

    /// POST /v1/notifications/{id}/delivered (idempotent on the daemon).
    public func markNotificationDelivered(id: String, token: String) throws {
        let r = try send(try NotificationActions.delivered(id: id, token: token))
        guard r.status == 200 else {
            throw WaterClientError.http(status: r.status, body: String(decoding: r.body.prefix(4096), as: UTF8.self))
        }
    }

    /// POST /v1/threads/anchor, resolved to where a tap should land. Never
    /// throws: an unreachable daemon falls back like any other failure.
    public func notificationTapTarget(_ anchor: NotificationAnchor?, token: String?) -> NotificationTapTarget {
        guard let anchor, let token, let req = try? NotificationActions.anchorThread(anchor, token: token) else {
            return NotificationActions.tapTarget(anchor: anchor, status: nil, body: nil)
        }
        guard let r = try? send(req) else { return NotificationActions.tapTarget(anchor: anchor, status: nil, body: nil) }
        return NotificationActions.tapTarget(anchor: anchor, status: r.status, body: r.body)
    }
}
