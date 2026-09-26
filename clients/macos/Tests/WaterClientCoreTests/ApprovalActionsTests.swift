import Foundation
import Testing
@testable import WaterClientCore

@Suite struct ApprovalActionsTests {
    @Test func getBuildsTheExistingRoute() throws {
        let r = try ApprovalActions.get(id: "env_ab12", token: "tok")
        #expect(r.method == "GET")
        #expect(r.path == "/v1/approvals/env_ab12")
        #expect(r.token == "tok")
        #expect(r.body == nil)
    }

    @Test func decideSendsTheCardsHashAndAYesOrNo() throws {
        let yes = try ApprovalActions.decide(id: "env_1", payloadHash: "sha256:abc", approve: true, token: "t")
        #expect(yes.method == "POST")
        #expect(yes.path == "/v1/approvals/env_1/decision")
        let body = try #require(yes.body)
        #expect(String(decoding: body, as: UTF8.self) == #"{"payload_hash":"sha256:abc","reply":"yes"}"#)

        let no = try ApprovalActions.decide(id: "env_1", payloadHash: "sha256:abc", approve: false, token: "t")
        #expect(String(decoding: no.body!, as: UTF8.self) == #"{"payload_hash":"sha256:abc","reply":"no"}"#)
    }

    /// No hash, no decision: a click must be bound to what was shown.
    @Test func decideWithoutAHashIsRefused() {
        #expect(throws: WaterClientError.self) {
            _ = try ApprovalActions.decide(id: "env_1", payloadHash: nil, approve: true, token: "t")
        }
        #expect(throws: WaterClientError.self) {
            _ = try ApprovalActions.decide(id: "env_1", payloadHash: "", approve: true, token: "t")
        }
    }

    @Test func badIDsNeverReachThePath() {
        for bad in ["", "../tools/invoke", "env_1/decision", "env 1", "env%2F1", "env?x=1", "é", String(repeating: "a", count: 129)] {
            #expect(throws: WaterClientError.self) { _ = try ApprovalActions.get(id: bad, token: "t") }
            #expect(throws: WaterClientError.self) {
                _ = try ApprovalActions.decide(id: bad, payloadHash: "h", approve: true, token: "t")
            }
        }
    }

    @Test func parsesAnApprovalView() throws {
        let json = #"""
        {"id":"env_1","action":"gmail.send_message","risk":"high","status":"pending",
         "payload_hash":"h1","read_back":"Send to a@b.c: \"Hi\"","summary":"x","payload":{"to":"a@b.c"}}
        """#
        let s = try #require(ApprovalState.parse(Data(json.utf8)))
        #expect(s == ApprovalState(id: "env_1", status: "pending", action: "gmail.send_message", risk: "high",
                                   readBack: "Send to a@b.c: \"Hi\"", payloadHash: "h1"))
        #expect(s.isPending)
        #expect(ApprovalState.parse(Data(#"{"status":"pending"}"#.utf8)) == nil)
        #expect(ApprovalState.parse(Data("nope".utf8)) == nil)
    }

    @Test func parsesADecisionResult() throws {
        let json = #"{"envelope":{"id":"env_1","status":"executed","payload_hash":"h1"},"answer":"yes","executed":true}"#
        let o = try #require(DecisionOutcome.parse(Data(json.utf8)))
        #expect(o.envelope?.status == "executed")
        #expect(o.executed && o.answer == "yes" && o.error == nil && !o.outcomeUnknown)

        let failed = #"{"envelope":{"id":"env_1","status":"approved"},"executed":false,"error":"boom","outcome_unknown":true}"#
        let f = try #require(DecisionOutcome.parse(Data(failed.utf8)))
        #expect(f.error == "boom" && f.outcomeUnknown && !f.executed)
    }

    @Test func failureMessagesAreFixedText() {
        #expect(ApprovalActions.failureMessage(WaterClientError.http(status: 409, body: "<script>")).contains("changed"))
        #expect(!ApprovalActions.failureMessage(WaterClientError.http(status: 500, body: "<script>")).contains("script"))
        #expect(ApprovalActions.failureMessage(WaterClientError.http(status: 404, body: "")).contains("no longer"))
    }

    // MARK: re-read into the model

    func pinned(_ id: String) -> ActivityModel {
        var m = ActivityModel()
        let t = m.turnSent(now: Date(timeIntervalSince1970: 0))
        m.event(TurnEvent(kind: .approvalRequired, approvalID: id, action: "gcal.create_event",
                          risk: "medium", payloadHash: "h1", readBack: "old"),
                turn: t, now: Date(timeIntervalSince1970: 0))
        m.event(TurnEvent(kind: .done), turn: t, now: Date(timeIntervalSince1970: 1))
        return m
    }

    /// A spoken yes in another turn: the re-read finds it executed and the
    /// card goes, the same as after a click.
    @Test func rereadDecidedElsewhereResolvesTheCard() {
        for status in ["approved", "executed", "denied", "expired"] {
            var m = pinned("env_1")
            m.approvalReread(id: "env_1", state: ApprovalState(id: "env_1", status: status), now: Date(timeIntervalSince1970: 5))
            #expect(m.approvals.isEmpty, "status \(status)")
            #expect(m.phase == .idle)
            #expect(m.dismissAt != nil)
        }
    }

    @Test func rereadNotFoundResolvesTheCard() {
        var m = pinned("env_1")
        m.approvalReread(id: "env_1", state: nil, now: Date(timeIntervalSince1970: 5))
        #expect(m.approvals.isEmpty)
    }

    @Test func rereadStillPendingRefreshesIt() {
        var m = pinned("env_1")
        m.approvalSubmitting("env_1")
        m.approvalReread(id: "env_1",
                         state: ApprovalState(id: "env_1", status: "pending", readBack: "new", payloadHash: "h2"),
                         now: Date(timeIntervalSince1970: 5))
        #expect(m.approvals.count == 1)
        #expect(m.approvals[0].payloadHash == "h2" && m.approvals[0].readBack == "new")
        #expect(!m.approvals[0].submitting)
        #expect(m.isPinned)
    }
}
