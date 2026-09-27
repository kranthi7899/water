import Foundation
import Testing
@testable import WaterClientCore

/// Slice W, stream S5 (docs/slices/W.md §9): the silent handoff keeps the
/// globe thinking, `approval_required` carries `warnings` and
/// `confirm_phrase`, and the glass tab shows a warning banner and the
/// "confirm send" hint.
@Suite struct SliceWClientTests {
    let t0 = Date(timeIntervalSince1970: 2_000_000)
    func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    func decode(_ line: String) throws -> TurnEvent {
        try #require(TurnEvent.decode(line: Data(line.utf8)))
    }

    // MARK: decoding

    @Test func approvalRequiredDecodesWarningsAndConfirmPhrase() throws {
        let e = try decode(#"{"kind":"approval_required","approval_id":"env_1","action":"gmail.send_message","risk":"high","payload_hash":"h1","read_back":"Send email to a@b.co","warnings":["b.co has no mail server"],"confirm_phrase":"confirm send"}"#)
        #expect(e.kind == .approvalRequired)
        #expect(e.warnings == ["b.co has no mail server"])
        #expect(e.confirmPhrase == "confirm send")
    }

    @Test func oldLinesWithoutTheFieldsStillDecode() throws {
        let e = try decode(#"{"kind":"approval_required","approval_id":"env_1","action":"gmail.send_message","payload_hash":"h1"}"#)
        #expect(e.warnings.isEmpty)
        #expect(e.confirmPhrase == nil)
        #expect(e.approvalID == "env_1")
    }

    @Test func malformedWarningsDropOnlyThemselves() throws {
        let wrongType = try decode(#"{"kind":"approval_required","approval_id":"env_1","warnings":"oops","confirm_phrase":7}"#)
        #expect(wrongType.warnings.isEmpty)
        #expect(wrongType.confirmPhrase == nil)
        #expect(wrongType.approvalID == "env_1")
        let mixed = try decode(#"{"kind":"approval_required","approval_id":"env_1","warnings":["one",3,"  ",null,"two"],"confirm_phrase":"  "}"#)
        #expect(mixed.warnings == ["one", "two"])
        #expect(mixed.confirmPhrase == nil)
        let null = try decode(#"{"kind":"approval_required","approval_id":"env_1","warnings":null}"#)
        #expect(null.warnings.isEmpty)
    }

    // MARK: the silent handoff (D3)

    /// No spoken filler is queued for an `ack`, even one carrying text (the
    /// old daemon spoke "One moment." as a sentence; the new one sends a
    /// silent ack). Only a voice turn's real sentence speaks.
    @Test func onlyAVoiceSentenceIsSpoken() throws {
        #expect(try decode(#"{"kind":"ack","text":"One moment."}"#).spokenText(channel: .voice) == nil)
        #expect(try decode(#"{"kind":"ack"}"#).spokenText(channel: .voice) == nil)
        #expect(try decode(#"{"kind":"handoff","text":"One moment."}"#).spokenText(channel: .voice) == nil)
        #expect(try decode(#"{"kind":"delta","text":"Hello"}"#).spokenText(channel: .voice) == nil)
        #expect(try decode(#"{"kind":"sentence","text":"   "}"#).spokenText(channel: .voice) == nil)
        let s = try decode(#"{"kind":"sentence","text":"Your next meeting is at ten."}"#)
        #expect(s.spokenText(channel: .voice) == "Your next meeting is at ten.")
        #expect(s.spokenText(channel: .textBar) == nil)
        #expect(s.spokenText(channel: .cli) == nil)
    }

    @Test func ackAndHandoffKeepTheGlobeThinkingUntilTheFirstSentence() throws {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        #expect(m.phase == .thinking)
        m.event(try decode(#"{"kind":"ack"}"#), turn: turn, now: at(0.01))
        #expect(m.phase == .thinking)
        m.event(try decode(#"{"kind":"handoff","text":"One moment."}"#), turn: turn, now: at(0.02))
        #expect(m.phase == .thinking)
        // A web search shows its own look (2026-09-26 globe interaction),
        // still never responding before the first sentence.
        m.event(try decode(#"{"kind":"tool_start","step_id":"s1","tool":"research.web","label":"Searching the web"}"#), turn: turn, now: at(1))
        #expect(m.phase == .searching)
        m.event(try decode(#"{"kind":"sentence","text":"It is sunny in Dublin."}"#), turn: turn, now: at(3.4))
        #expect(m.phase == .responding)
    }

    // MARK: the glass tab

    static let warnedLine = #"{"kind":"approval_required","approval_id":"env_1","action":"gmail.send_message","risk":"high","payload_hash":"h1","read_back":"Send email to k@gmial.com","warnings":["gmial.com looks like gmail.com.","gmial.com has no mail server."]}"#
    static let armedLine = #"{"kind":"approval_required","approval_id":"env_2","action":"gmail.send_message","risk":"high","payload_hash":"h2","read_back":"Send email to dana@example.com","confirm_phrase":"confirm send"}"#

    @Test func warningsReachTheApprovalItem() throws {
        let item = try #require(GlassItem.shouldShow(channel: .voice, event: try decode(Self.warnedLine)))
        #expect(item.warnings == ["gmial.com looks like gmail.com.", "gmial.com has no mail server."])
        #expect(item.confirmHint == nil)
        // Approve stays under the existing rule (a hash, not in flight):
        // the daemon decides tap eligibility, and a tap is the intended path.
        #expect(item.canDecide)
    }

    @Test func confirmHintShowsOnlyWithAConfirmPhrase() throws {
        let armed = try #require(GlassItem.shouldShow(channel: .voice, event: try decode(Self.armedLine)))
        #expect(armed.confirmPhrase == "confirm send")
        #expect(armed.confirmHint == "Say \u{201C}confirm send\u{201D} or tap Approve")
        #expect(armed.canDecide)
        let plain = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_3",
                                                  action: "gmail.send_message", readBack: "r", payloadHash: "h"))
        #expect(plain.confirmHint == nil)
        #expect(plain.warnings.isEmpty)
    }

    /// Warnings make an envelope tap-only on the daemon; the tab never
    /// suggests the spoken phrase next to them.
    @Test func noConfirmHintNextToWarnings() throws {
        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "r", payloadHash: "h",
                                                 warnings: ["no mail server"], confirmPhrase: "confirm send"))
        #expect(item.confirmHint == nil)
    }

    @Test func onlyApprovalsCarryWarningsOrAPhrase() {
        let draft = GlassItem(kind: .draft, email: nil, readBack: "d", warnings: ["x"], confirmPhrase: "confirm send")
        #expect(draft.warnings.isEmpty && draft.confirmPhrase == nil && draft.confirmHint == nil)
        let n = GlassItem.notice("hi")
        #expect(n.warnings.isEmpty && n.confirmHint == nil)
    }

    @Test func warningsAndPhraseAreCapped() throws {
        let long = String(repeating: "w", count: 1_000)
        let many = (0..<20).map { "warning \($0)" }
        let item = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_1",
                                                 action: "gmail.send_message", readBack: "r", payloadHash: "h",
                                                 warnings: [long] + many,
                                                 confirmPhrase: "confirm send\nand then something else entirely that is long"))
        #expect(item.warnings.count == GlassItem.maxWarnings)
        #expect(item.warnings[0].count == GlassItem.maxWarningChars)
        #expect(item.confirmPhrase == "confirm send")
    }

    @Test func rereadTakesWarningsFromTheBodyWhenPresent() throws {
        let item = try #require(GlassItem.shouldShow(channel: .voice, event: try decode(Self.warnedLine)))
        // A body with `warnings` replaces them (the daemon recomputes them).
        let healed = try JSONSerialization.data(withJSONObject: [
            "id": "env_1", "status": "pending", "action": "gmail.send_message", "payload_hash": "h1",
            "read_back": "Send email to k@gmial.com", "warnings": ["gmial.com looks like gmail.com."],
        ])
        #expect(try #require(item.applyingApproval(body: healed)).warnings == ["gmial.com looks like gmail.com."])
        let cleared = try JSONSerialization.data(withJSONObject: [
            "id": "env_1", "status": "pending", "payload_hash": "h1", "warnings": [String](),
        ])
        #expect(try #require(item.applyingApproval(body: cleared)).warnings.isEmpty)
        // A body without the field (an older daemon) keeps the item's own.
        let old = try JSONSerialization.data(withJSONObject: ["id": "env_1", "status": "pending", "payload_hash": "h1"])
        #expect(try #require(item.applyingApproval(body: old)).warnings.count == 2)
    }

    @Test func rereadKeepsTheConfirmPhraseOnlyForTheSameHash() throws {
        let armed = try #require(GlassItem.shouldShow(channel: .voice, event: try decode(Self.armedLine)))
        let same = try JSONSerialization.data(withJSONObject: [
            "id": "env_2", "status": "pending", "action": "gmail.send_message", "payload_hash": "h2",
            "payload": ["to": ["dana@example.com"], "subject": "Re: timing", "body": "Thursday."],
        ])
        let kept = try #require(armed.applyingApproval(body: same))
        #expect(kept.confirmHint != nil)
        #expect(kept.email?.subject == "Re: timing")
        let edited = try JSONSerialization.data(withJSONObject: ["id": "env_2", "status": "pending", "payload_hash": "h9"])
        let dropped = try #require(armed.applyingApproval(body: edited))
        #expect(dropped.confirmPhrase == nil && dropped.confirmHint == nil)
        #expect(dropped.payloadHash == "h9")
        let noHash = try JSONSerialization.data(withJSONObject: ["id": "env_2", "status": "pending"])
        #expect(try #require(armed.applyingApproval(body: noHash)).confirmHint == nil)
        let decided = try JSONSerialization.data(withJSONObject: ["id": "env_2", "status": "approved", "payload_hash": "h2"])
        #expect(armed.applyingApproval(body: decided) == nil)
    }

    /// The spoken yes re-emits the same envelope with `confirm_phrase`: the
    /// tab gains the hint and keeps the message layout it already had.
    @Test func armingTheSameEnvelopeKeepsItsLayoutAndAddsTheHint() throws {
        var state = GlassTabState()
        var first = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_2",
                                                  action: "gmail.send_message", readBack: "r", payloadHash: "h2"))
        first.email = EmailFields(to: ["dana@example.com"], subject: "Re: timing", body: "Thursday.")
        state.show(first, now: at(0))
        let armed = try #require(GlassItem.shouldShow(channel: .voice, event: try decode(Self.armedLine)))
        state.show(armed, now: at(5))
        #expect(state.item?.confirmHint != nil)
        #expect(state.item?.email?.subject == "Re: timing")
        // A different hash is a different decision: no stale layout.
        let other = try #require(GlassItem.decide(channel: .voice, kind: .approvalRequired, approvalID: "env_2",
                                                  action: "gmail.send_message", readBack: "r2", payloadHash: "h3"))
        state.show(other, now: at(6))
        #expect(state.item?.email == nil)
        #expect(state.item?.confirmHint == nil)
    }
}
