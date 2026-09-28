import Foundation
import Testing
@testable import WaterClientCore

@Suite struct NotificationPlannerTests {
    private func n(_ id: String, _ type: String = "decision", _ rid: String? = nil,
                   title: String = "T", body: String = "B") -> PendingNotification {
        PendingNotification(id: id, recordType: type, recordID: rid ?? "card-\(id)", title: title, body: body)
    }

    // MARK: parsing

    @Test func parsesTheDaemonList() throws {
        let json = #"""
        [{"id":"ntf_1","record_type":"decision","record_id":"card-ab","title":"Lead","body":"Severity 3",
          "created_at":"2026-09-25T10:00:00Z","delivered_at":null},
         {"id":"ntf_2","record_type":"approval","record_id":"env_9","title":"gmail.send_message","body":"Pending"}]
        """#
        let list = try #require(PendingNotification.parseList(Data(json.utf8)))
        #expect(list == [
            PendingNotification(id: "ntf_1", recordType: "decision", recordID: "card-ab", title: "Lead", body: "Severity 3"),
            PendingNotification(id: "ntf_2", recordType: "approval", recordID: "env_9", title: "gmail.send_message", body: "Pending"),
        ])
        #expect(PendingNotification.parseList(Data("{}".utf8)) == nil)
        #expect(PendingNotification.parseList(Data("nope".utf8)) == nil)
        // A row without an id is dropped, not fatal.
        #expect(PendingNotification.parseList(Data(#"[{"title":"x"}]"#.utf8)) == [])
    }

    // MARK: anchor mapping

    @Test func anchorAcceptsOnlyDecisionOrApprovalAndPlainIDs() {
        #expect(NotificationAnchor(recordType: "decision", recordID: "card-0123abcd") == NotificationAnchor(type: .decision, id: "card-0123abcd"))
        #expect(NotificationAnchor(recordType: "approval", recordID: "env_ab12")?.type == .approval)
        for t in ["message", "meeting", "thread", "Decision", "", "decision ", "approval\n"] {
            #expect(NotificationAnchor(recordType: t, recordID: "card-1") == nil, "type \(t)")
        }
        for bad in ["", "card 1", "../tools/invoke", "card-1/x", "card%2F1", "card-1?x=1", "é", "card-1\n",
                    "<script>", "a.b", String(repeating: "a", count: 129)] {
            #expect(NotificationAnchor(recordType: "decision", recordID: bad) == nil, "id \(bad)")
        }
    }

    @Test func anchorUserInfoCarriesOnlyTypeAndID() throws {
        let a = try #require(NotificationAnchor(recordType: "approval", recordID: "env_1"))
        #expect(a.userInfo == ["record_type": "approval", "record_id": "env_1"])
        #expect(NotificationAnchor(userInfo: a.userInfo as [AnyHashable: Any]) == a)
        // Tampered or partial userInfo gives no anchor.
        #expect(NotificationAnchor(userInfo: ["record_type": "thread", "record_id": "thr_1"]) == nil)
        #expect(NotificationAnchor(userInfo: ["record_type": "decision", "record_id": "../x"]) == nil)
        #expect(NotificationAnchor(userInfo: ["record_type": "decision"]) == nil)
        #expect(NotificationAnchor(userInfo: ["record_type": "decision", "record_id": 5]) == nil)
        #expect(NotificationAnchor(userInfo: [:]) == nil)
    }

    @Test func anchorRequestAndFallbackView() throws {
        let d = NotificationAnchor(type: .decision, id: "card-1")
        let r = try NotificationActions.anchorThread(d, token: "tok")
        #expect(r.method == "POST")
        #expect(r.path == "/v1/threads/anchor")
        #expect(r.token == "tok")
        #expect(String(decoding: try #require(r.body), as: UTF8.self) == #"{"anchor_id":"card-1","anchor_type":"decision"}"#)
        #expect(d.fallbackView == "decisions")
        #expect(NotificationAnchor(type: .approval, id: "env_1").fallbackView == "approvals")
        #expect(throws: WaterClientError.self) {
            _ = try NotificationActions.anchorThread(NotificationAnchor(type: .decision, id: "../x"), token: "t")
        }
    }

    // MARK: requests

    @Test func listAndDeliveredRequests() throws {
        let l = NotificationActions.list(limit: 50, token: "t")
        #expect(l.method == "GET" && l.path == "/v1/notifications?undelivered=1&limit=50" && l.body == nil)
        let d = try NotificationActions.delivered(id: "ntf_ab", token: "t")
        #expect(d.method == "POST" && d.path == "/v1/notifications/ntf_ab/delivered")
        for bad in ["", "ntf/../x", "ntf 1", "ntf%2F"] {
            #expect(throws: WaterClientError.self) { _ = try NotificationActions.delivered(id: bad, token: "t") }
        }
    }

    // MARK: tap target

    @Test func tapOpensTheAnchoredThread() {
        let a = NotificationAnchor(type: .decision, id: "card-1")
        let ok = #"{"thread":{"id":"thr_ab12","anchor_type":"decision","anchor_id":"card-1","title":"x"},"created":true}"#
        #expect(NotificationActions.tapTarget(anchor: a, status: 200, body: Data(ok.utf8)) == .thread("thr_ab12"))
    }

    @Test func tapFallsBackToTheRecordsView() {
        let a = NotificationAnchor(type: .approval, id: "env_1")
        // 404: the record is gone (a dismissed card, an expired envelope).
        #expect(NotificationActions.tapTarget(anchor: a, status: 404, body: nil) == .view("approvals", id: "env_1"))
        // The daemon unreachable, a 500, or a body that isn't this anchor's thread.
        #expect(NotificationActions.tapTarget(anchor: a, status: nil, body: nil) == .view("approvals", id: "env_1"))
        #expect(NotificationActions.tapTarget(anchor: a, status: 500, body: Data("x".utf8)) == .view("approvals", id: "env_1"))
        let other = #"{"thread":{"id":"thr_1","anchor_type":"approval","anchor_id":"env_2"}}"#
        #expect(NotificationActions.tapTarget(anchor: a, status: 200, body: Data(other.utf8)) == .view("approvals", id: "env_1"))
        let badID = #"{"thread":{"id":"thr_1'); alert(1); ('","anchor_type":"approval","anchor_id":"env_1"}}"#
        #expect(NotificationActions.tapTarget(anchor: a, status: 200, body: Data(badID.utf8)) == .view("approvals", id: "env_1"))
        // No anchor at all (the summary banner, or tampered userInfo): Today.
        #expect(NotificationActions.tapTarget(anchor: nil, status: nil, body: nil) == .view("today", id: nil))
    }

    // MARK: planning

    @Test func capsAtThreeAndSummarizesTheRest() {
        var p = NotificationPlanner()
        let list = (1...5).map { n("ntf_\($0)") }
        let plan = p.plan(list)
        #expect(plan.banners.count == 4)
        #expect(plan.banners.prefix(3).map(\.notificationIDs) == [["ntf_1"], ["ntf_2"], ["ntf_3"]])
        let more = plan.banners[3]
        #expect(more.anchor == nil)
        #expect(more.notificationIDs == ["ntf_4", "ntf_5"])
        #expect(more.title == "And 2 more need you")
        #expect(more.userInfo.isEmpty)
        #expect(plan.remark.isEmpty)
    }

    @Test func exactlyThreeOrFewerHaveNoSummary() {
        var p = NotificationPlanner()
        #expect(p.plan((1...3).map { n("ntf_\($0)") }).banners.count == 3)
        var q = NotificationPlanner()
        #expect(q.plan([n("ntf_a")]).banners.count == 1)
        var r = NotificationPlanner()
        #expect(r.plan([]).banners.isEmpty)
        var s = NotificationPlanner()
        let four = s.plan((1...4).map { n("ntf_\($0)") })
        #expect(four.banners.last?.title == "And 1 more needs you")
    }

    @Test func recordBannerCarriesTextAndOnlyTypeAndIDInUserInfo() throws {
        var p = NotificationPlanner()
        let b = try #require(p.plan([n("ntf_1", "decision", "card-9", title: "<b>Approve Q3?</b>", body: "Severity 3")]).banners.first)
        #expect(b.identifier == "ntf_1")
        #expect(b.title == "<b>Approve Q3?</b>") // plain text, shown as is
        #expect(b.body == "Severity 3")
        #expect(b.anchor == NotificationAnchor(type: .decision, id: "card-9"))
        #expect(b.userInfo == ["record_type": "decision", "record_id": "card-9"])
    }

    @Test func longTextIsClippedAndEmptyTitleGetsADefault() throws {
        var p = NotificationPlanner()
        let long = String(repeating: "x", count: 1000)
        let b = try #require(p.plan([n("ntf_1", title: "", body: long)]).banners.first)
        #expect(b.title == "Water: something needs you")
        #expect(b.body.count <= NotificationPlanner.maxBodyChars)
        #expect(b.body.hasSuffix("…"))
    }

    @Test func aBadAnchorStillBannersButOpensToday() throws {
        var p = NotificationPlanner()
        let b = try #require(p.plan([n("ntf_1", "meeting", "mtg_1")]).banners.first)
        #expect(b.anchor == nil)
        #expect(b.userInfo.isEmpty)
    }

    @Test func aBadNotificationIDIsRejectedNotShown() {
        var p = NotificationPlanner()
        let plan = p.plan([n("ntf/../x"), n("ntf_ok")])
        #expect(plan.banners.map(\.notificationIDs) == [["ntf_ok"]])
        #expect(plan.rejected == ["ntf/../x"])
    }

    @Test func dedupesWithinAPollAndAcrossPolls() {
        var p = NotificationPlanner()
        let first = p.plan([n("ntf_1"), n("ntf_1"), n("ntf_2")])
        #expect(first.banners.map(\.notificationIDs) == [["ntf_1"], ["ntf_2"]])
        // In flight: the next poll shows nothing again.
        #expect(p.plan([n("ntf_1"), n("ntf_2")]).banners.isEmpty)
        // Posted but not yet marked (the mark failed): never re-shown, re-marked.
        p.posted(first.banners[0])
        let again = p.plan([n("ntf_1"), n("ntf_2")])
        #expect(again.banners.isEmpty)
        #expect(again.remark == ["ntf_1"])
        // Marked: not shown and not re-marked, even if a stale list repeats it.
        p.marked("ntf_1")
        let stale = p.plan([n("ntf_1")])
        #expect(stale.banners.isEmpty && stale.remark.isEmpty)
    }

    @Test func aFailedPostIsRetriedNextPoll() {
        var p = NotificationPlanner()
        let first = p.plan([n("ntf_1")])
        p.postFailed(first.banners[0])
        #expect(p.plan([n("ntf_1")]).banners.map(\.notificationIDs) == [["ntf_1"]])
    }

    @Test func summaryPostedMarksEveryCoveredID() {
        var p = NotificationPlanner()
        let plan = p.plan((1...5).map { n("ntf_\($0)") })
        p.posted(plan.banners[3])
        #expect(p.plan([n("ntf_4"), n("ntf_5")]).remark == ["ntf_4", "ntf_5"])
    }

    @Test func aDuplicateRecordUnderAnotherIDIsShownOnce() {
        var p = NotificationPlanner()
        let plan = p.plan([n("ntf_1", "decision", "card-1"), n("ntf_2", "decision", "card-1")])
        #expect(plan.banners.count == 1)
        #expect(plan.banners[0].notificationIDs == ["ntf_1", "ntf_2"])
    }
}

/// The notifier's socket calls, against a canned daemon.
@Suite struct NotificationSocketTests {
    private func reply(_ status: String, _ body: String) -> String {
        "HTTP/1.1 \(status)\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\n\r\n" + body
    }

    @Test func fetchesTheUndeliveredList() throws {
        let server = try CannedServer(pieces: [reply("200 OK", #"[{"id":"ntf_1","record_type":"decision","record_id":"card-1","title":"T","body":"B"}]"#)])
        let list = try UnixSocketClient(socketPath: server.path).fetchUndeliveredNotifications(token: "tok")
        server.wait()
        #expect(list.map(\.id) == ["ntf_1"])
        let req = String(decoding: server.request, as: UTF8.self)
        #expect(req.hasPrefix("GET /v1/notifications?undelivered=1&limit=50 HTTP/1.1\r\n"))
        #expect(req.contains("Authorization: Bearer tok\r\n"))
    }

    @Test func marksDeliveredAndSurfacesA404() throws {
        let ok = try CannedServer(pieces: [reply("200 OK", #"{"id":"ntf_1"}"#)])
        try UnixSocketClient(socketPath: ok.path).markNotificationDelivered(id: "ntf_1", token: "t")
        ok.wait()
        #expect(String(decoding: ok.request, as: UTF8.self).hasPrefix("POST /v1/notifications/ntf_1/delivered HTTP/1.1\r\n"))

        let gone = try CannedServer(pieces: [reply("404 Not Found", "no notification")])
        #expect(throws: WaterClientError.http(status: 404, body: "no notification")) {
            try UnixSocketClient(socketPath: gone.path).markNotificationDelivered(id: "ntf_1", token: "t")
        }
    }

    @Test func tapAnchorsThenOpensTheThreadOrFallsBack() throws {
        let a = NotificationAnchor(type: .decision, id: "card-1")
        let body = #"{"thread":{"id":"thr_9","anchor_type":"decision","anchor_id":"card-1"},"created":false}"#
        let server = try CannedServer(pieces: [reply("200 OK", body)])
        #expect(UnixSocketClient(socketPath: server.path).notificationTapTarget(a, token: "t") == .thread("thr_9"))
        server.wait()
        let req = String(decoding: server.request, as: UTF8.self)
        #expect(req.hasPrefix("POST /v1/threads/anchor HTTP/1.1\r\n"))
        #expect(req.hasSuffix(#"{"anchor_id":"card-1","anchor_type":"decision"}"#))

        let gone = try CannedServer(pieces: [reply("404 Not Found", "no decision card-1")])
        #expect(UnixSocketClient(socketPath: gone.path).notificationTapTarget(a, token: "t") == .view("decisions", id: "card-1"))
        // No daemon at all.
        #expect(UnixSocketClient(socketPath: "/nonexistent/water.sock").notificationTapTarget(a, token: "t") == .view("decisions", id: "card-1"))
        // No token: never sent, still lands somewhere useful.
        #expect(UnixSocketClient(socketPath: "/nonexistent/water.sock").notificationTapTarget(a, token: nil) == .view("decisions", id: "card-1"))
    }
}
