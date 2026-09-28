import Foundation

// The two approval calls the Activity HUD makes (V-hud), and nothing else:
// GET /v1/approvals/{id} (re-read an open card) and
// POST /v1/approvals/{id}/decision (a click on Approve or Reject). Both are
// existing routes; the daemon's gate is still the only thing that executes,
// and only for an envelope whose payload_hash matches what was shown.

/// An approval envelope's current state, from GET /v1/approvals/{id} (or a
/// decision's `envelope`). Only the fields the HUD uses.
public struct ApprovalState: Equatable {
    public var id: String
    public var status: String
    public var action: String?
    public var risk: String?
    public var readBack: String?
    public var payloadHash: String?

    public init(id: String, status: String, action: String? = nil, risk: String? = nil,
                readBack: String? = nil, payloadHash: String? = nil) {
        self.id = id
        self.status = status
        self.action = action
        self.risk = risk
        self.readBack = readBack
        self.payloadHash = payloadHash
    }

    public var isPending: Bool { status == "pending" }

    public var card: ApprovalCard {
        ApprovalCard(id: id, action: action, risk: risk, readBack: readBack, payloadHash: payloadHash)
    }

    /// Parses an ApprovalView object; nil without an id and a status.
    public static func parse(_ object: [String: Any]) -> ApprovalState? {
        func str(_ k: String) -> String? {
            guard let s = object[k] as? String, !s.isEmpty else { return nil }
            return s
        }
        guard let id = str("id"), let status = str("status") else { return nil }
        return ApprovalState(id: id, status: status, action: str("action"), risk: str("risk"),
                             readBack: str("read_back"), payloadHash: str("payload_hash"))
    }

    public static func parse(_ data: Data) -> ApprovalState? {
        guard let o = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else { return nil }
        return parse(o)
    }
}

/// What POST /v1/approvals/{id}/decision answered (gateway.DecisionResult).
public struct DecisionOutcome: Equatable {
    public var envelope: ApprovalState?
    public var answer: String?
    public var executed: Bool
    public var error: String?
    public var outcomeUnknown: Bool

    public static func parse(_ data: Data) -> DecisionOutcome? {
        guard let o = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else { return nil }
        let env = (o["envelope"] as? [String: Any]).flatMap { ApprovalState.parse($0) }
        let err = (o["error"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        return DecisionOutcome(envelope: env, answer: o["answer"] as? String,
                               executed: o["executed"] as? Bool ?? false, error: err,
                               outcomeUnknown: o["outcome_unknown"] as? Bool ?? false)
    }
}

public enum ApprovalActions {
    /// The reply a click sends. The daemon matches it deterministically
    /// (approvals.Match); anything but yes is a final denial.
    public static func reply(approve: Bool) -> String { approve ? "yes" : "no" }

    static func checkID(_ id: String) throws {
        guard WorkspaceAllowlist.isID(id) else { throw WaterClientError.invalidRequest("bad approval id") }
    }

    public static func get(id: String, token: String) throws -> HTTPRequest {
        try checkID(id)
        return HTTPRequest(method: "GET", path: "/v1/approvals/\(id)", token: token)
    }

    /// Approve or Reject, bound to the hash of the payload the card showed.
    /// A missing hash is refused here: the daemon would 409 it anyway, and
    /// a decision must never be sent for text nobody saw.
    public static func decide(id: String, payloadHash: String?, approve: Bool, token: String) throws -> HTTPRequest {
        try checkID(id)
        guard let h = payloadHash, !h.isEmpty else {
            throw WaterClientError.invalidRequest("approval \(id) has no payload hash")
        }
        return try HTTPRequest.json("POST", "/v1/approvals/\(id)/decision", token: token,
                                    ["payload_hash": h, "reply": reply(approve: approve)])
    }

    /// One short sentence for a failed click, shown on the card as plain
    /// text. Never the daemon's raw body.
    public static func failureMessage(_ error: Error) -> String {
        switch error as? WaterClientError {
        case .http(status: 409, body: _)?:
            return "This changed since it was shown. Check it again before deciding."
        case .http(status: 404, body: _)?:
            return "This approval no longer exists."
        case .daemonNotRunning?, .socket?:
            return "Couldn't reach Water. Is `water daemon` running?"
        default:
            return "That didn't go through. Try again, or open it in the workspace."
        }
    }
}

extension ActivityModel {
    /// Applies a re-read of one open card: gone (nil, e.g. a 404) or no
    /// longer pending resolves it; still pending refreshes its hash and
    /// read-back.
    public mutating func approvalReread(id: String, state: ApprovalState?, now: Date) {
        guard let state, state.id == id, state.isPending else {
            approvalResolved(id, now: now)
            return
        }
        approvalRefreshed(state.card)
    }
}

extension UnixSocketClient {
    /// GET /v1/approvals/{id}: nil on 404.
    public func fetchApproval(id: String, token: String) throws -> ApprovalState? {
        let r = try send(try ApprovalActions.get(id: id, token: token))
        if r.status == 404 { return nil }
        guard r.status == 200 else {
            throw WaterClientError.http(status: r.status, body: String(decoding: r.body.prefix(4096), as: UTF8.self))
        }
        guard let s = ApprovalState.parse(r.body) else { throw WaterClientError.protocolError("bad approval body") }
        return s
    }

    /// POST /v1/approvals/{id}/decision. A non-200 (409 for a stale hash,
    /// 404) throws `.http`.
    public func decideApproval(id: String, payloadHash: String?, approve: Bool, token: String) throws -> DecisionOutcome {
        let r = try send(try ApprovalActions.decide(id: id, payloadHash: payloadHash, approve: approve, token: token))
        guard r.status == 200 else {
            throw WaterClientError.http(status: r.status, body: String(decoding: r.body.prefix(4096), as: UTF8.self))
        }
        guard let o = DecisionOutcome.parse(r.body) else { throw WaterClientError.protocolError("bad decision body") }
        return o
    }
}
