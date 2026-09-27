import Foundation
import Testing
@testable import WaterClientCore

/// The `water://` proxy's allowlist (Slice V-ui, docs/slices/V.md §5): the
/// only thing standing between the workspace web view — which renders
/// attacker-controlled text — and every other route on the daemon.
@Suite struct WorkspaceAllowlistTests {
    private func decide(_ method: String, _ url: String) -> WorkspaceAllowlist.Decision {
        guard let u = URL(string: url) else { return .deny("URL(string:) refused it") }
        return WorkspaceAllowlist.decide(method: method, url: u)
    }

    private func allowed(_ method: String, _ url: String) -> String? {
        if case .allow(_, let path) = decide(method, url) { return path }
        return nil
    }

    // MARK: allowed

    @Test func everyRouteTheUICallsIsAllowed() {
        let cases: [(String, String, String)] = [
            ("GET", "water://app/ui/", "/ui/"),
            ("GET", "water://app/ui/index.html", "/ui/index.html"),
            ("GET", "water://app/ui/app.css", "/ui/app.css"),
            ("GET", "water://app/ui/dom.js", "/ui/dom.js"),
            ("GET", "water://app/ui/api.js", "/ui/api.js"),
            ("GET", "water://app/ui/app.js", "/ui/app.js"),
            ("GET", "water://app/v1/today", "/v1/today"),
            ("GET", "water://app/v1/decisions", "/v1/decisions"),
            ("POST", "water://app/v1/decisions/card-0123abcd/stage", "/v1/decisions/card-0123abcd/stage"),
            ("POST", "water://app/v1/decisions/card-0123abcd/dismiss", "/v1/decisions/card-0123abcd/dismiss"),
            ("GET", "water://app/v1/approvals", "/v1/approvals"),
            ("GET", "water://app/v1/approvals?status=pending&limit=100", "/v1/approvals?status=pending&limit=100"),
            ("GET", "water://app/v1/approvals?status=decided&limit=500", "/v1/approvals?status=decided&limit=500"),
            ("GET", "water://app/v1/approvals?kind=gmail.send_message", "/v1/approvals?kind=gmail.send_message"),
            ("GET", "water://app/v1/approvals/env_ab12", "/v1/approvals/env_ab12"),
            ("POST", "water://app/v1/approvals/env_ab12/decision", "/v1/approvals/env_ab12/decision"),
            ("POST", "water://app/v1/approvals/env_ab12/edit", "/v1/approvals/env_ab12/edit"),
            ("POST", "water://app/v1/approvals/env_ab12/request-changes", "/v1/approvals/env_ab12/request-changes"),
            ("GET", "water://app/v1/threads", "/v1/threads"),
            ("POST", "water://app/v1/threads", "/v1/threads"),
            ("POST", "water://app/v1/threads/anchor", "/v1/threads/anchor"),
            ("GET", "water://app/v1/threads/thr_00ff", "/v1/threads/thr_00ff"),
            ("POST", "water://app/v1/threads/thr_00ff/messages", "/v1/threads/thr_00ff/messages"),
            ("POST", "water://app/v1/tasks/task_1a2b/cancel", "/v1/tasks/task_1a2b/cancel"),
            ("GET", "water://app/v1/meetings?limit=30", "/v1/meetings?limit=30"),
            ("GET", "water://app/v1/meetings", "/v1/meetings"),
            ("GET", "water://app/v1/meetings/ms_77", "/v1/meetings/ms_77"),
            // Slice UI Phase 2.
            ("GET", "water://app/v1/workspaces", "/v1/workspaces"),
            ("GET", "water://app/v1/dashboards", "/v1/dashboards"),
        ]
        for (m, url, want) in cases {
            #expect(allowed(m, url) == want, "\(m) \(url)")
        }
    }

    @Test func aFragmentIsNotForwarded() {
        #expect(allowed("GET", "water://app/ui/#/approvals/env_1") == "/ui/")
    }

    // MARK: denied: routes the UI must never reach

    @Test func dangerousAndUnusedRoutesAreDenied() {
        let cases: [(String, String)] = [
            ("POST", "water://app/v1/tools/invoke"),
            ("GET", "water://app/v1/tools/invoke"),
            ("POST", "water://app/v1/tools"),
            ("POST", "water://app/v1/quick/invoke"),
            ("GET", "water://app/v1/twinlink/messages"),
            ("POST", "water://app/v1/twinlink/outbox"),
            ("POST", "water://app/v1/twinlink/receive"),
            ("POST", "water://app/v1/turns"),
            ("POST", "water://app/v1/turns/t1/partial"),
            ("GET", "water://app/v1/state"),
            ("GET", "water://app/v1/health"),
            ("GET", "water://app/v1/clients"),
            ("POST", "water://app/v1/clients"),
            ("GET", "water://app/v1/tokens"),
            ("POST", "water://app/v1/token"),
            ("POST", "water://app/v1/intents/promote"),
            ("POST", "water://app/v1/intents/reload"),
            ("POST", "water://app/v1/decisions/card-1/email"),
            ("POST", "water://app/v1/meetings/start"),
            ("POST", "water://app/v1/meetings/ms_1/stop"),
            ("POST", "water://app/v1/meetings/ms_1/segments"),
            ("GET", "water://app/v1/voice/profile"),
            ("GET", "water://app/"),
            ("GET", "water://app"),
            ("GET", "water://app/ui"),
        ]
        for (m, url) in cases {
            #expect(!decide(m, url).isAllowed, "\(m) \(url) must be denied")
        }
    }

    @Test func wrongMethodOnAnAllowedPathIsDenied() {
        #expect(!decide("POST", "water://app/v1/today").isAllowed)
        #expect(!decide("GET", "water://app/v1/decisions/card-1/stage").isAllowed)
        #expect(!decide("DELETE", "water://app/v1/threads/thr_1").isAllowed)
        #expect(!decide("PUT", "water://app/v1/threads").isAllowed)
        #expect(!decide("HEAD", "water://app/ui/").isAllowed)
        #expect(!decide("POST", "water://app/ui/index.html").isAllowed)
        #expect(!decide("get", "water://app/v1/today").isAllowed)
        #expect(!decide("POST", "water://app/v1/workspaces").isAllowed)
        #expect(!decide("POST", "water://app/v1/dashboards").isAllowed)
    }

    @Test func wrongOriginIsDenied() {
        #expect(!decide("GET", "https://app/v1/today").isAllowed)
        #expect(!decide("GET", "water://evil/v1/today").isAllowed)
        #expect(!decide("GET", "water://App.example/v1/today").isAllowed)
        #expect(!decide("GET", "water://user:pw@app/v1/today").isAllowed)
        #expect(!decide("GET", "water://user@app/v1/today").isAllowed)
        #expect(!decide("GET", "water://app:8080/v1/today").isAllowed)
        #expect(!decide("GET", "file:///v1/today").isAllowed)
    }

    // MARK: denied: traversal and encoding

    @Test func traversalIsDenied() {
        let cases = [
            "water://app/ui/../v1/tools/invoke",
            "water://app/ui/./index.html",
            "water://app/v1/threads/../tools/invoke",
            "water://app/v1/./today",
            "water://app/v1/threads/..",
            "water://app/v1/approvals/..",
            "water://app/ui/..",
        ]
        for url in cases {
            #expect(!decide("GET", url).isAllowed, "\(url)")
            #expect(!decide("POST", url).isAllowed, "\(url)")
        }
    }

    @Test func percentEncodingIsDenied() {
        let cases = [
            "water://app/ui/%2e%2e/v1/tools/invoke",
            "water://app/ui/%2E%2E/v1/tools/invoke",
            "water://app/ui/.%2e/v1/today",
            "water://app/v1/threads/%2e%2e/%2e%2e/v1/tools/invoke",
            "water://app/v1/approvals/env_1%2fdecision",
            "water://app/v1/approvals/env_1%2Fdecision",
            "water://app/v1/threads/thr_1%00",
            "water://app/v1/threads/thr%5f1",
            "water://app/v1/threads/%252e%252e",
            "water://app/%76%31/today",
            "water://app/ui/app%2ejs",
            "water://app/v1/tools%2finvoke",
        ]
        for url in cases {
            #expect(!decide("GET", url).isAllowed, "\(url)")
            #expect(!decide("POST", url).isAllowed, "\(url)")
        }
    }

    @Test func doubleSlashesAndEmptySegmentsAreDenied() {
        let cases: [(String, String)] = [
            ("GET", "water://app//v1/today"),
            ("GET", "water://app/v1//today"),
            ("GET", "water://app/v1/today/"),
            ("GET", "water://app/v1/threads//"),
            ("POST", "water://app/v1/threads//messages"),
            ("GET", "water://app/ui//index.html"),
            ("GET", "water://app/ui/sub/index.html"),
            ("GET", "water://app/ui//"),
        ]
        for (m, url) in cases {
            #expect(!decide(m, url).isAllowed, "\(m) \(url)")
        }
    }

    @Test func badIDsAndAssetNamesAreDenied() {
        #expect(!decide("GET", "water://app/v1/threads/thr.1").isAllowed)
        #expect(!decide("GET", "water://app/v1/threads/thr:1").isAllowed)
        #expect(!decide("GET", "water://app/v1/threads/" + String(repeating: "a", count: 129)).isAllowed)
        #expect(decide("GET", "water://app/v1/threads/" + String(repeating: "a", count: 128)).isAllowed)
        #expect(!decide("GET", "water://app/ui/app.json").isAllowed)
        #expect(!decide("GET", "water://app/ui/app.js.map").isAllowed)
        #expect(!decide("GET", "water://app/ui/.js").isAllowed)
        #expect(!decide("GET", "water://app/ui/app").isAllowed)
        #expect(!decide("GET", "water://app/ui/a..js").isAllowed)
        #expect(!decide("GET", "water://app/ui/.htaccess").isAllowed)
    }

    @Test func backslashesAndOddCharactersAreDenied() {
        // URL(string:) refuses some of these outright; the raw-path entry
        // point is what the handler reaches after URLComponents, so test it
        // directly as well.
        for raw in ["/ui/..\\v1\\tools", "/v1\\tools\\invoke", "/v1/threads/thr_1;x", "/v1/today\u{0}",
                    "/v1/threads/thr_é", "/v1/today\n", "v1/today", "", "/v1/threads/thr_1?x"] {
            #expect(!WorkspaceAllowlist.decide(method: "GET", rawPath: raw, rawQuery: nil).isAllowed, "\(raw.debugDescription)")
            #expect(!WorkspaceAllowlist.decide(method: "POST", rawPath: raw, rawQuery: nil).isAllowed, "\(raw.debugDescription)")
        }
    }

    // MARK: denied: query-string tricks

    @Test func queryOnlyWhereTheRouteTakesOne() {
        #expect(!decide("GET", "water://app/v1/today?x=1").isAllowed)
        #expect(!decide("GET", "water://app/v1/decisions?status=all").isAllowed)
        #expect(!decide("GET", "water://app/ui/?v=1").isAllowed)
        #expect(!decide("GET", "water://app/ui/app.js?v=1").isAllowed)
        #expect(!decide("POST", "water://app/v1/threads/thr_1/messages?x=y").isAllowed)
        #expect(!decide("GET", "water://app/v1/approvals/env_1?status=all").isAllowed)
        #expect(!decide("GET", "water://app/v1/workspaces?x=1").isAllowed)
        #expect(!decide("GET", "water://app/v1/dashboards?x=1").isAllowed)
    }

    @Test func queryTricksAreDenied() {
        let cases = [
            "water://app/v1/approvals?status=pending&status=all", // duplicate key
            "water://app/v1/approvals?token=abc", // unknown key
            "water://app/v1/approvals?status", // key without value
            "water://app/v1/approvals?status=pending&", // trailing empty pair
            "water://app/v1/approvals?&status=pending",
            "water://app/v1/approvals?status=%2e%2e", // escapes in values
            "water://app/v1/approvals?status=a%26token%3Dx",
            "water://app/v1/approvals?status=pending/../../tools",
            "water://app/v1/approvals?status=pending?limit=1",
            "water://app/v1/approvals?limit=1=2",
            "water://app/v1/approvals?limit=" + String(repeating: "9", count: 65),
            "water://app/v1/meetings?status=all", // key valid elsewhere, not here
            "water://app/v1/meetings?limit=30;x",
            "water://app/v1/meetings?limit=30+1",
        ]
        for url in cases {
            #expect(!decide("GET", url).isAllowed, "\(url)")
        }
    }

    @Test func anEmptyQueryIsDroppedNotForwarded() {
        #expect(allowed("GET", "water://app/v1/approvals?") == "/v1/approvals")
    }

    @Test func theForwardedPathIsRebuiltNotCopied() {
        // Same text in, but the output is built from the matched pieces.
        #expect(allowed("GET", "water://app/v1/approvals?limit=5&status=all") == "/v1/approvals?limit=5&status=all")
    }

    // MARK: response headers

    @Test func onlySafeResponseHeadersAreForwarded() {
        let daemon = [
            "content-type": "application/x-ndjson",
            "content-security-policy": "default-src 'self'",
            "x-content-type-options": "nosniff",
            "x-water-task-id": "task_1",
            "x-water-thread-id": "thr_1",
            "transfer-encoding": "chunked",
            "content-length": "12",
            "set-cookie": "a=b",
            "authorization": "Bearer secret",
            "www-authenticate": "Bearer",
        ]
        let got = WorkspaceProxy.responseHeaders(from: daemon)
        #expect(got["Content-Type"] == "application/x-ndjson")
        #expect(got["Content-Security-Policy"] == "default-src 'self'")
        #expect(got["X-Content-Type-Options"] == "nosniff")
        #expect(got["X-Water-Task-Id"] == "task_1")
        #expect(got["X-Water-Thread-Id"] == "thr_1")
        #expect(got.count == 5)
        for (k, v) in got {
            #expect(!k.lowercased().contains("auth") && !k.lowercased().contains("cookie"))
            #expect(!v.contains("secret"))
        }
    }

    // MARK: script messages

    /// V-ui2: approval edit is reachable, POST only and only on one
    /// approval id; the page still has no turn route of any kind.
    @Test func approvalEditIsPostOnlyOnOneApproval() {
        #expect(allowed("POST", "water://app/v1/approvals/env_1/edit") == "/v1/approvals/env_1/edit")
        #expect(!decide("GET", "water://app/v1/approvals/env_1/edit").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/edit").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/env_1/edit/x").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/env_1/edit?x=1").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/env%2F1/edit").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/../edit").isAllowed)
        #expect(!decide("POST", "water://app/v1/decisions/card-1/edit").isAllowed)
        #expect(!decide("POST", "water://app/v1/threads/thr_1/edit").isAllowed)
        for url in ["water://app/v1/turns", "water://app/v1/turns?thread_id=thr_1", "water://app/v1/turns/thr_1"] {
            #expect(!decide("POST", url).isAllowed, "\(url)")
        }
    }

    /// Phase 3a: request-changes is reachable, POST only, on one approval
    /// id, and takes no query.
    @Test func requestChangesIsPostOnlyOnOneApproval() {
        #expect(allowed("POST", "water://app/v1/approvals/env_1/request-changes") == "/v1/approvals/env_1/request-changes")
        #expect(!decide("GET", "water://app/v1/approvals/env_1/request-changes").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/request-changes").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/env_1/request-changes/x").isAllowed)
        #expect(!decide("POST", "water://app/v1/approvals/env_1/request-changes?x=1").isAllowed)
        #expect(!decide("POST", "water://app/v1/decisions/card-1/request-changes").isAllowed)
    }

    // MARK: script messages

    @Test func onlyTheExactMicHoldMessagesAreRecognised() {
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_0a1b2c"]) == .micDown(thread: "thr_0a1b2c"))
        #expect(WorkspaceMessage.parse(["type": "mic-up"]) == .micUp)
        // The old toggle message is gone.
        #expect(WorkspaceMessage.parse(["type": "mic"]) == nil)
        // mic-down needs exactly a well-formed thread id, nothing else.
        #expect(WorkspaceMessage.parse(["type": "mic-down"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": ""]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_ABCD"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_12g"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "env_0a1b"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_0a/../x"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": " thr_0a"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_0a\n"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_" + String(repeating: "a", count: 65)]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": 12]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": ["thr_0a"]]) == nil)
        #expect(WorkspaceMessage.parse(["type": "mic-down", "thread": "thr_0a", "prompt": "send it"]) == nil)
        // mic-up carries nothing.
        #expect(WorkspaceMessage.parse(["type": "mic-up", "thread": "thr_0a"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "MIC-UP"]) == nil)
        #expect(WorkspaceMessage.parse(["type": "open", "url": "https://evil"]) == nil)
        #expect(WorkspaceMessage.parse(["type": 1]) == nil)
        #expect(WorkspaceMessage.parse("mic-up") == nil)
        #expect(WorkspaceMessage.parse([["type": "mic-up"]]) == nil)
        #expect(WorkspaceMessage.parse(NSNull()) == nil)
        #expect(WorkspaceMessage.isThreadID("thr_" + String(repeating: "f", count: 64)))
    }

    @Test func localErrorsCarryARestrictiveCSP() {
        let h = WorkspaceProxy.localErrorHeaders()
        #expect(h["Content-Security-Policy"]?.hasPrefix("default-src 'none'") == true)
        #expect(h["X-Content-Type-Options"] == "nosniff")
    }

    @Test func openTargetsAreKnownViewsAndDaemonIDs() {
        #expect(WorkspaceAllowlist.isOpenTarget(view: "approvals", id: "env_0a1b"))
        #expect(WorkspaceAllowlist.isOpenTarget(view: "threads", id: "thr_12"))
        #expect(WorkspaceAllowlist.isOpenTarget(view: "drafts", id: "env_0a1b"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "settings", id: "env_1"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "approvals", id: ""))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "approvals", id: "env_1');alert(1);//"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "approvals", id: "../x"))
        // No id opens the view's list (a tapped "and N more" banner opens Today).
        #expect(WorkspaceAllowlist.isOpenTarget(view: "today", id: nil))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "settings", id: nil))
        // Slice UI Phase 2: the two new page views.
        #expect(WorkspaceAllowlist.isOpenTarget(view: "dashboards", id: nil))
        #expect(WorkspaceAllowlist.isOpenTarget(view: "dashboards", id: "finance"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "dashboards", id: "../x"))
        #expect(WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: nil))
    }

    /// Slice UI Phase 2: a "workspaces" open target is either a bare
    /// workspace id, or exactly two slash-separated segments ("<id>/<sub>",
    /// the page's own hash-route shape) each independently checked with
    /// `isID` — never the joined string, which always fails `isID` once it
    /// contains a "/". A bad segment is denied regardless of which of the
    /// two positions it is in.
    @Test func workspacesOpenTargetsAreValidatedSegmentBySegment() {
        #expect(WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "finance"))
        #expect(WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "finance/overview"))
        #expect(WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "water-team_1/sub-page_2"))
        // A bad first segment (each still exactly two segments).
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "../overview"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "fin ance/overview"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "/overview"))
        // A bad second segment (each still exactly two segments).
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "finance/.."))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "finance/"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "finance/sub page"))
        // More than two segments, or an empty compound id, is denied too.
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: "finance/overview/extra"))
        #expect(!WorkspaceAllowlist.isOpenTarget(view: "workspaces", id: ""))
    }

    /// V-notify's routes are native-only: the page can never list or mark
    /// notifications, in any form.
    @Test func notificationRoutesAreNeverReachableFromThePage() {
        let cases: [(String, String)] = [
            ("GET", "water://app/v1/notifications"),
            ("GET", "water://app/v1/notifications?undelivered=1"),
            ("GET", "water://app/v1/notifications?undelivered=1&limit=50"),
            ("POST", "water://app/v1/notifications/ntf_1/delivered"),
            ("GET", "water://app/v1/notifications/ntf_1"),
            ("POST", "water://app/v1/notifications"),
        ]
        for (m, url) in cases {
            #expect(!decide(m, url).isAllowed, "\(m) \(url) must be denied")
        }
    }
}
