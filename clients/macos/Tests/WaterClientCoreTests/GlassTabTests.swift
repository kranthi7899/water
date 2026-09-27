import Foundation
import Testing
@testable import WaterClientCore

@Suite struct GlassTabTests {
    static let mail = EmailFields(to: ["dana@example.com"], cc: ["ops@example.com"],
                                  subject: "Re: Board deck timing", body: "Thursday works.")

    // MARK: what shows

    @Test func onlyVoiceShowsAnything() {
        for ch in [Channel.cli, .textBar] {
            #expect(GlassItem.decide(channel: ch, kind: .approvalRequired, approvalID: "env_1",
                                     action: "gmail.send_message", readBack: "Send?", payloadHash: "h") == nil)
            #expect(GlassItem.decide(channel: ch, kind: .artifact, tool: "gmail.draft_message",
                                     artifactType: "email_draft", email: Self.mail) == nil)
        }
    }

    @Test func approvalRequiredOnVoiceIsAnApprovalItem() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gcal.create_event", risk: "medium",
                                                 readBack: "Create event 'Sync' at 3pm. Say yes or no.", payloadHash: "sha256:x"))
        #expect(item.kind == .approval(id: "env_1", action: "gcal.create_event", risk: "medium",
                                       readBack: "Create event 'Sync' at 3pm. Say yes or no.", payloadHash: "sha256:x"))
        #expect(item.email == nil)
        #expect(item.readBack == "Create event 'Sync' at 3pm. Say yes or no.")
        #expect(item.canDecide)
        #expect(!item.wantsPayload) // not an outward message: read-back only
    }

    @Test func outwardMessageApprovalWantsItsPayload() throws {
        for a in ["gmail.send_message", "twinlink.send_message"] {
            let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                     action: a, readBack: "Send?", payloadHash: "h"))
            #expect(item.wantsPayload)
        }
    }

    @Test func approvalWithoutAnIDOrWithABadIDShowsNothing() {
        #expect(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: nil, readBack: "x") == nil)
        #expect(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "../x", readBack: "x") == nil)
    }

    @Test func approvalWithoutAHashCannotBeDecided() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "Send?"))
        #expect(!item.canDecide)
    }

    @Test func emailDraftArtifactOnVoiceIsADraft() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .artifact, tool: "gmail.draft_for_review",
                                                 artifactType: "email_draft", email: Self.mail))
        #expect(item.kind == .draft)
        #expect(item.email == Self.mail)
        #expect(!item.isApproval && !item.canDecide)
        #expect(item.title == "Draft for your review")
        #expect(item.readBack == "Draft to dana@example.com, subject 'Re: Board deck timing'")
    }

    @Test func otherArtifactTypesAndOtherKindsShowNothing() {
        #expect(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "calendar_event", email: Self.mail) == nil)
        #expect(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "email_draft", email: nil) == nil)
        for k: TurnEvent.Kind in [.ack, .delta, .sentence, .toolStart, .toolEnd, .done, .error, .queued, .unknown("x")] {
            #expect(GlassItem.decide(channel: .voice, kind: k, approvalID: "env_1", readBack: "x",
                                     artifactType: "email_draft", email: Self.mail) == nil)
        }
    }

    @Test func shouldShowReadsTheTurnEvents() throws {
        let line = #"{"kind":"artifact","step_id":"stp_1","tool":"gmail.draft_message","artifact":{"type":"email_draft","to":["dana@example.com"],"cc":[],"subject":"Re: Board deck timing","body":"Thursday."}}"#
        let e = try #require(TurnEvent.decode(line: Data(line.utf8)))
        let item = try #require(GlassItem.shouldShow(channel: .voice, event: e))
        #expect(item.kind == .draft && item.tool == "gmail.draft_message")
        #expect(item.email == EmailFields(to: ["dana@example.com"], subject: "Re: Board deck timing", body: "Thursday."))
        #expect(item.title == "Email draft")
        #expect(GlassItem.shouldShow(channel: .textBar, event: e) == nil)

        let other = try #require(TurnEvent.decode(line: Data(#"{"kind":"artifact","tool":"x.y","artifact":{"type":"doc"}}"#.utf8)))
        #expect(GlassItem.shouldShow(channel: .voice, event: other) == nil)
        let bare = try #require(TurnEvent.decode(line: Data(#"{"kind":"artifact","tool":"gmail.draft_message"}"#.utf8)))
        #expect(GlassItem.shouldShow(channel: .voice, event: bare) == nil)

        let ap = try #require(TurnEvent.decode(line: Data(#"{"kind":"approval_required","approval_id":"env_9","action":"gmail.send_message","risk":"high","payload_hash":"sha256:z","read_back":"Send email to dana@example.com"}"#.utf8)))
        let a = try #require(GlassItem.shouldShow(channel: .voice, event: ap))
        #expect(a.approvalID == "env_9" && a.payloadHash == "sha256:z" && a.risk == "high")
        #expect(a.wantsPayload && a.title == "Send this email?")
        #expect(GlassItem.shouldShow(channel: .cli, event: ap) == nil)
        for k in [#"{"kind":"tool_start","step_id":"s","tool":"gmail.draft_message","label":"Drafting"}"#, #"{"kind":"done"}"#] {
            #expect(GlassItem.shouldShow(channel: .voice, event: try #require(TurnEvent.decode(line: Data(k.utf8)))) == nil)
        }
    }

    // MARK: payload layout

    @Test func gmailSendPayloadIsLaidOutAsAMessage() throws {
        let e = try #require(GlassItem.emailFields(action: "gmail.send_message", payload: [
            "to": ["dana@example.com"], "cc": ["a@b.c"], "subject": "Hi", "body": "Line 1\nLine 2",
        ]))
        #expect(e == EmailFields(to: ["dana@example.com"], cc: ["a@b.c"], subject: "Hi", body: "Line 1\nLine 2"))
        // A plain string recipient works too, and cc is optional.
        let s = try #require(GlassItem.emailFields(action: "gmail.send_message", payload: ["to": "x@y.z", "subject": "s", "body": "b"]))
        #expect(s.to == ["x@y.z"] && s.cc.isEmpty)
    }

    /// Anything the layout wouldn't show (bcc, an html attachment) sends the
    /// tab back to the read-back, which names every bound field.
    @Test func extraOrMalformedPayloadFieldsFallBackToTheReadBack() {
        #expect(GlassItem.emailFields(action: "gmail.send_message",
                                      payload: ["to": ["a@b.c"], "subject": "s", "body": "b", "html_attachment": "<p>"]) == nil)
        #expect(GlassItem.emailFields(action: "gmail.send_message",
                                      payload: ["to": ["a@b.c"], "bcc": ["x@y.z"], "subject": "s", "body": "b"]) == nil)
        #expect(GlassItem.emailFields(action: "gmail.send_message", payload: ["to": [1], "subject": "s", "body": "b"]) == nil)
        #expect(GlassItem.emailFields(action: "gmail.send_message", payload: ["subject": "s", "body": "b"]) == nil)
        #expect(GlassItem.emailFields(action: "gmail.send_message", payload: ["to": ["a@b.c"], "subject": 3, "body": "b"]) == nil)
        #expect(GlassItem.emailFields(action: "gcal.create_event", payload: ["to": ["a@b.c"], "subject": "s", "body": "b"]) == nil)
    }

    /// Every recipient of an outward message must be on screen before the
    /// CEO can approve it. A list longer than the layout can show in full
    /// falls back to the read-back, which names every address.
    @Test func tooManyRecipientsFallBackToTheReadBack() throws {
        let eight = (0..<8).map { "p\($0)@example.com" }
        let e = try #require(GlassItem.emailFields(action: "gmail.send_message",
                                                   payload: ["to": Array(eight.prefix(6)), "cc": Array(eight.suffix(2)), "subject": "s", "body": "b"]))
        #expect(e.to.count + e.cc.count == GlassItem.maxLaidOutRecipients)
        #expect(GlassItem.emailFields(action: "gmail.send_message",
                                      payload: ["to": eight, "cc": ["one-more@example.com"], "subject": "s", "body": "b"]) == nil)
        #expect(GlassItem.emailFields(action: "gmail.send_message",
                                      payload: ["to": (0..<60).map { "p\($0)@x.y" }, "subject": "s", "body": "b"]) == nil)
        let long = String(repeating: "a", count: GlassItem.maxLaidOutRecipientChars) + "@example.com"
        #expect(GlassItem.emailFields(action: "gmail.send_message", payload: ["to": [long], "subject": "s", "body": "b"]) == nil)
        #expect(GlassItem.emailFields(action: "twinlink.send_message",
                                      payload: ["to_twin": long, "subject": "s", "payload": "p"]) == nil)

        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "r", payloadHash: "h"))
        let body = try JSONSerialization.data(withJSONObject: [
            "id": "env_1", "status": "pending", "action": "gmail.send_message", "payload_hash": "h2", "read_back": "every address",
            "payload": ["to": eight + ["ninth@example.com"], "subject": "s", "body": "b"],
        ])
        let next = try #require(item.applyingApproval(body: body))
        #expect(next.email == nil)
        #expect(next.readBack == "every address")
    }

    /// An approval's recipients are drawn in full (never cut with "…"); a
    /// read-only draft may truncate them.
    @Test func approvalRecipientsAreNeverTruncated() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "r", payloadHash: "h"))
        #expect(!item.truncatesRecipients)
        let draft = try #require(GlassItem.decide(channel: .voice, kind: .artifact, tool: "gmail.draft_message",
                                                  artifactType: "email_draft", email: Self.mail))
        #expect(draft.truncatesRecipients)
    }

    @Test func twinMessagePayloadIsLabelledAsATwin() throws {
        let e = try #require(GlassItem.emailFields(action: "twinlink.send_message",
                                                   payload: ["to_twin": "counterparty", "subject": "Re: budget", "payload": "We have $42k."]))
        #expect(e.to == ["twin: counterparty"])
        #expect(e.body == "We have $42k.")
        #expect(GlassItem.emailFields(action: "twinlink.send_message",
                                      payload: ["to_twin": "c", "subject": "s", "payload": "p", "in_reply_to": "tl_1"]) == nil)
    }

    @Test func rereadBindsTextAndHashFromTheSameResponse() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "old", payloadHash: "h1"))
        let body = try JSONSerialization.data(withJSONObject: [
            "id": "env_1", "status": "pending", "action": "gmail.send_message", "payload_hash": "h2",
            "read_back": "Send email to dana@example.com", "risk": "high",
            "payload": ["to": ["dana@example.com"], "subject": "Re: Board deck timing", "body": "Thursday."],
        ])
        let next = try #require(item.applyingApproval(body: body))
        #expect(next.payloadHash == "h2")
        #expect(next.readBack == "Send email to dana@example.com")
        #expect(next.risk == "high")
        #expect(next.email?.subject == "Re: Board deck timing")
        #expect(!next.wantsPayload)
    }

    @Test func rereadThatIsGoneDecidedOrSomeoneElseResolves() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "r", payloadHash: "h"))
        #expect(item.applyingApproval(body: nil) == nil)
        let decided = try JSONSerialization.data(withJSONObject: ["id": "env_1", "status": "approved"])
        #expect(item.applyingApproval(body: decided) == nil)
        let other = try JSONSerialization.data(withJSONObject: ["id": "env_2", "status": "pending"])
        #expect(item.applyingApproval(body: other) == nil)
        #expect(item.applyingApproval(body: Data("not json".utf8)) == nil)
    }

    @Test func rereadWithAHiddenFieldDropsTheMessageLayout() throws {
        var item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "r", payloadHash: "h"))
        item.email = Self.mail
        let body = try JSONSerialization.data(withJSONObject: [
            "id": "env_1", "status": "pending", "action": "gmail.send_message", "payload_hash": "h3", "read_back": "all fields",
            "payload": ["to": ["a@b.c"], "subject": "s", "body": "b", "html_attachment": "<p>x</p>"],
        ])
        let next = try #require(item.applyingApproval(body: body))
        #expect(next.email == nil)
        #expect(next.readBack == "all fields")
    }

    @Test func stringsAndListsAreCapped() {
        let long = String(repeating: "é", count: 30_000)
        let e = EmailFields(to: (0..<80).map { "p\($0)@x.y" } + ["  "], subject: long, body: long)
        #expect(e.to.count == GlassItem.maxList)
        #expect(e.subject.utf8.count <= GlassItem.maxString)
        #expect(e.body.utf8.count <= GlassItem.maxString)
        #expect(e.body.utf8.count > GlassItem.maxString - 64)
    }

    // MARK: when it hides

    @Test func draftLingersThenHides() throws {
        let t0 = Date(timeIntervalSince1970: 1000)
        var s = GlassTabState()
        s.show(try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "email_draft", email: Self.mail)), now: t0)
        s.tick(now: t0.addingTimeInterval(GlassItem.linger - 1))
        #expect(s.isVisible)
        s.tick(now: t0.addingTimeInterval(GlassItem.linger))
        #expect(!s.isVisible)
    }

    @Test func approvalStaysUntilResolved() throws {
        let t0 = Date(timeIntervalSince1970: 1000)
        var s = GlassTabState()
        let a = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                              action: "gcal.create_event", readBack: "r", payloadHash: "h"))
        s.show(a, now: t0)
        s.tick(now: t0.addingTimeInterval(3600))
        #expect(s.isVisible)
        // A draft doesn't cover a waiting approval.
        s.show(try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "email_draft", email: Self.mail)), now: t0)
        #expect(s.item?.approvalID == "env_1")
        s.resolved(id: "env_other")
        #expect(s.isVisible)
        s.submitting(id: "env_1")
        #expect(s.item?.canDecide == false)
        s.failed(id: "env_1", message: "Try again")
        #expect(s.item?.note == "Try again" && s.item?.canDecide == true)
        s.approvalReread(id: "env_1", item: nil)
        #expect(!s.isVisible)
    }

    @Test func rereadKeepsTheClickStateAndResolvedHides() throws {
        var s = GlassTabState()
        let a = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                              action: "gcal.create_event", readBack: "r", payloadHash: "h"))
        s.show(a, now: Date())
        s.failed(id: "env_1", message: "n")
        var fresh = a
        fresh.readBack = "r2"
        s.approvalReread(id: "env_1", item: fresh)
        #expect(s.item?.readBack == "r2" && s.item?.note == "n")
        s.resolved(id: "env_1")
        #expect(!s.isVisible)
    }

    // MARK: display artifacts and notices (2026-09-26 fixes)

    @Test func displayArtifactOnVoiceIsPlainTitleAndBody() throws {
        let line = #"{"kind":"artifact","step_id":"stp_2","tool":"display.show","artifact":{"type":"display","title":"Runway","body":"Cash: $4.2M\nBurn: $300k/mo"}}"#
        let e = try #require(TurnEvent.decode(line: Data(line.utf8)))
        let item = try #require(GlassItem.shouldShow(channel: .voice, event: e))
        #expect(item.kind == .display(title: "Runway"))
        #expect(item.title == "Runway")
        #expect(item.readBack == "Cash: $4.2M\nBurn: $300k/mo")
        #expect(item.email == nil && !item.isApproval && !item.canDecide)
        #expect(item.action == "display.show")
        for ch in [Channel.cli, .textBar] { #expect(GlassItem.shouldShow(channel: ch, event: e) == nil) }
    }

    @Test func displayNeedsSomethingToShow() throws {
        #expect(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "display", title: "  ", body: "\n") == nil)
        let bodyOnly = try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "display", body: "x"))
        #expect(bodyOnly.title == "Water")
        let titleOnly = try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "display", title: "Steps"))
        #expect(titleOnly.readBack.isEmpty && titleOnly.title == "Steps")
        // Only a real artifact event shows it.
        #expect(GlassItem.decide(channel: .voice, kind: .delta, artifactType: "display", title: "t", body: "b") == nil)
    }

    @Test func displayTitleIsOneCappedLine() throws {
        let long = String(repeating: "t", count: 500)
        let item = try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "display",
                                                 title: long + "\nsecond line", body: "b"))
        #expect(item.title.count == GlassItem.maxTitle)
        #expect(!item.title.contains("second"))
    }

    // MARK: note artifacts (docs/slices/UI.md Phase 6, U1-A)

    @Test func noteArtifactOnVoiceIsTitleSourceAndSimulatedBadge() throws {
        let line = #"{"kind":"artifact","step_id":"stp_3","tool":"linear.create_comment","artifact":{"type":"note","title":"CRA-3","body":"(simulated) Relayed: Nina, via Slack: it's fixed","source":"linear:CRA-3","simulated":true}}"#
        let e = try #require(TurnEvent.decode(line: Data(line.utf8)))
        let item = try #require(GlassItem.shouldShow(channel: .voice, event: e))
        #expect(item.kind == .note(title: "CRA-3", source: "linear:CRA-3", simulated: true))
        #expect(item.title == "CRA-3")
        #expect(item.readBack == "(simulated) Relayed: Nina, via Slack: it's fixed")
        #expect(item.isSimulatedNote)
        #expect(item.email == nil && !item.isApproval && !item.canDecide)
        #expect(item.action == "linear.create_comment")
        for ch in [Channel.cli, .textBar] { #expect(GlassItem.shouldShow(channel: ch, event: e) == nil) }
    }

    @Test func noteArtifactNotSimulatedHasNoBadge() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "note",
                                                 title: "CRA-3", body: "Nina: fixed for real", source: "linear:CRA-3", simulated: false))
        #expect(item.kind == .note(title: "CRA-3", source: "linear:CRA-3", simulated: false))
        #expect(!item.isSimulatedNote)
    }

    @Test func noteNeedsSomethingToShow() throws {
        #expect(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "note", title: "  ", body: "\n") == nil)
        let bodyOnly = try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "note", body: "x"))
        #expect(bodyOnly.title == "Water")
        let titleOnly = try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "note", title: "Steps"))
        #expect(titleOnly.readBack.isEmpty && titleOnly.title == "Steps")
        // Only a real artifact event shows it.
        #expect(GlassItem.decide(channel: .voice, kind: .delta, artifactType: "note", title: "t", body: "b") == nil)
    }

    @Test func noteTitleAndSourceAreOneCappedLine() throws {
        let long = String(repeating: "t", count: 500)
        let item = try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "note",
                                                 title: long + "\nsecond line", body: "b", source: long + "\nsecond line"))
        #expect(item.title.count == GlassItem.maxTitle)
        #expect(!item.title.contains("second"))
        guard case .note(_, let source, _) = item.kind else { #expect(Bool(false), "not a note"); return }
        #expect(source.count == GlassItem.maxTitle)
        #expect(!source.contains("second"))
    }

    /// Only a `note` reports `isSimulatedNote`; every other kind (including
    /// a non-simulated note, and an approval's own unrelated `risk`) is
    /// false, so the badge is never mistakenly shown elsewhere.
    @Test func isSimulatedNoteIsFalseForEveryOtherKind() throws {
        let approval = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                     action: "gcal.create_event", risk: "high", readBack: "r", payloadHash: "h"))
        #expect(!approval.isSimulatedNote)
        let draft = try #require(GlassItem.decide(channel: .voice, kind: .artifact, tool: "gmail.draft_message", artifactType: "email_draft", email: Self.mail))
        #expect(!draft.isSimulatedNote)
        #expect(!GlassItem.notice("n").isSimulatedNote)
    }

    @Test func noticeIsPlainAndLingers() {
        let t0 = Date(timeIntervalSince1970: 1000)
        let n = GlassItem.notice("  Hold Space to talk. Esc to leave voice mode. ")
        #expect(n.kind == .notice && n.readBack == "Hold Space to talk. Esc to leave voice mode.")
        #expect(n.title == "Water" && n.action == nil && !n.isApproval)
        var s = GlassTabState()
        s.show(n, now: t0)
        s.tick(now: t0.addingTimeInterval(GlassItem.linger - 0.5))
        #expect(s.isVisible)
        s.tick(now: t0.addingTimeInterval(GlassItem.linger))
        #expect(!s.isVisible)
    }

    @Test func nonApprovalItemsLingerAboutTenSeconds() {
        #expect(GlassItem.linger == 10)
    }

    @Test func hoverPausesTheLingerCount() throws {
        let t0 = Date(timeIntervalSince1970: 1000)
        func at(_ x: TimeInterval) -> Date { t0.addingTimeInterval(x) }
        var s = GlassTabState()
        s.show(try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "display", title: "t", body: "b")), now: t0)
        s.hover(true, now: at(8))
        s.tick(now: at(60))
        #expect(s.isVisible) // paused with 2s left
        s.hover(false, now: at(61))
        s.tick(now: at(61 + GlassItem.lingerAfterHover - 0.1))
        #expect(s.isVisible) // at least lingerAfterHover after leaving
        s.tick(now: at(61 + GlassItem.lingerAfterHover))
        #expect(!s.isVisible)
    }

    @Test func hoverBeforeAnItemShowsStillPauses() throws {
        let t0 = Date(timeIntervalSince1970: 1000)
        var s = GlassTabState()
        s.show(GlassItem.notice("one"), now: t0)
        s.hover(true, now: t0)
        s.show(GlassItem.notice("two"), now: t0.addingTimeInterval(1))
        s.tick(now: t0.addingTimeInterval(100))
        #expect(s.item?.readBack == "two")
        s.hover(false, now: t0.addingTimeInterval(100))
        s.tick(now: t0.addingTimeInterval(100 + GlassItem.linger))
        #expect(!s.isVisible)
    }

    /// Every item can be closed; closing an approval only hides the tab
    /// (nothing is decided) and a draft/display/notice never covers it.
    @Test func approvalCanBeClosedAndIsNeverCoveredOrTimedOut() throws {
        let t0 = Date(timeIntervalSince1970: 1000)
        var s = GlassTabState()
        let a = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                              action: "gcal.create_event", readBack: "r", payloadHash: "h"))
        s.show(a, now: t0)
        s.show(GlassItem.notice("hint"), now: t0)
        s.show(try #require(GlassItem.decide(channel: .voice, kind: .artifact, artifactType: "display", title: "t", body: "b")), now: t0)
        s.tick(now: t0.addingTimeInterval(3600))
        #expect(s.item?.approvalID == "env_1")
        #expect(s.item?.submitting == false && s.item?.note == nil)
        s.close()
        #expect(!s.isVisible)
    }
}
