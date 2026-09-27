import Foundation
import Testing
@testable import WaterClientCore

/// V-events: the daemon's new event kinds and fields decode, and events
/// from an older daemon still decode with the new fields nil.
struct TurnEventTests {
    private func decode(_ s: String) throws -> TurnEvent {
        try #require(TurnEvent.decode(line: Data(s.utf8)))
    }

    @Test func queuedDecodes() throws {
        let e = try decode(#"{"kind":"queued","text":"waiting for the previous turn to finish"}"#)
        #expect(e.kind == .queued)
        #expect(e.text == "waiting for the previous turn to finish")
    }

    @Test func toolStartDecodes() throws {
        let e = try decode(#"{"kind":"tool_start","step_id":"stp_1","tool":"gcal.list_events","label":"Checking your calendar"}"#)
        #expect(e == TurnEvent(kind: .toolStart, stepID: "stp_1", tool: "gcal.list_events", label: "Checking your calendar"))
        #expect(e.status == nil)
    }

    @Test func toolEndDecodesEveryStatus() throws {
        for status in ["ok", "queued", "denied", "error", "some_future_status"] {
            let e = try decode(#"{"kind":"tool_end","step_id":"stp_1","tool":"gcal.list_events","label":"Checking your calendar","status":"\#(status)"}"#)
            #expect(e.kind == .toolEnd)
            #expect(e.stepID == "stp_1" && e.tool == "gcal.list_events" && e.label == "Checking your calendar")
            #expect(e.status == status)
        }
        // An unrecognised function: the daemon leaves tool empty.
        let unknown = try decode(#"{"kind":"tool_end","step_id":"stp_2","label":"Using a tool","status":"denied"}"#)
        #expect(unknown.tool == nil && unknown.label == "Using a tool")
    }

    @Test func approvalRequiredDecodesReadBack() throws {
        let line = #"{"kind":"approval_required","text":"gcal.create_event","approval_id":"env_1","action":"gcal.create_event","risk":"medium","payload_hash":"ab12","read_back":"Create event 'Sync' starting 2026-09-25T15:00:00Z. Say yes to create it or no to cancel."}"#
        let e = try decode(line)
        #expect(e == TurnEvent(kind: .approvalRequired, text: "gcal.create_event", approvalID: "env_1",
                               action: "gcal.create_event", risk: "medium", payloadHash: "ab12",
                               readBack: "Create event 'Sync' starting 2026-09-25T15:00:00Z. Say yes to create it or no to cancel."))
    }

    @Test func olderEventsDecodeWithNewFieldsNil() throws {
        let e = try decode(#"{"kind":"approval_required","approval_id":"env_3"}"#)
        #expect(e.kind == .approvalRequired && e.approvalID == "env_3")
        #expect(e.readBack == nil && e.stepID == nil && e.tool == nil && e.label == nil && e.status == nil)
        let d = try decode(#"{"kind":"delta","text":"hi"}"#)
        #expect(d == TurnEvent(kind: .delta, text: "hi"))
    }

    @Test func artifactDecodesEmailDraft() throws {
        let e = try decode(#"{"kind":"artifact","step_id":"stp_1","tool":"gmail.draft_message","artifact":{"type":"email_draft","to":["dana@acme.com","lee@acme.com"],"cc":["sam@acme.com"],"subject":"Q3","body":"Numbers attached."}}"#)
        #expect(e == TurnEvent(kind: .artifact, stepID: "stp_1", tool: "gmail.draft_message",
                               artifact: TurnArtifact(type: "email_draft", to: ["dana@acme.com", "lee@acme.com"],
                                                      cc: ["sam@acme.com"], subject: "Q3", body: "Numbers attached.")))
        #expect(e.artifact?.emailDraft == true)
        #expect(e.label == nil && e.status == nil && e.text == nil)
    }

    @Test func artifactWithMissingFieldsAndUnknownType() throws {
        // The daemon omits empty fields (no cc on a gmail draft).
        let d = try decode(#"{"kind":"artifact","step_id":"stp_1","tool":"gmail.draft_for_review","artifact":{"type":"email_draft","to":["a@x.com"],"subject":"S"}}"#)
        #expect(d.artifact == TurnArtifact(type: "email_draft", to: ["a@x.com"], subject: "S"))
        // An unknown type decodes with just its type.
        let u = try decode(#"{"kind":"artifact","step_id":"stp_2","artifact":{"type":"calendar_event","start":"2026-09-26"}}"#)
        #expect(u.kind == .artifact && u.artifact == TurnArtifact(type: "calendar_event"))
        #expect(u.artifact?.emailDraft == false)
    }

    @Test func artifactReadsLooseListsAndSurvivesAMalformedObject() throws {
        let e = try decode(#"{"kind":"artifact","artifact":{"type":"email_draft","to":"a@x.com","cc":["",7,"b@x.com",null],"subject":5,"body":"B"}}"#)
        #expect(e.artifact == TurnArtifact(type: "email_draft", to: ["a@x.com"], cc: ["b@x.com"], subject: "", body: "B"))
        // No type, or not an object: the event still decodes, without an artifact.
        for bad in [#"{"to":["a@x.com"]}"#, #""oops""#, "[1,2]"] {
            let b = try decode(#"{"kind":"artifact","step_id":"stp_3","artifact":\#(bad)}"#)
            #expect(b.kind == .artifact && b.stepID == "stp_3" && b.artifact == nil)
        }
    }

    @Test func artifactDecodesDisplayWithTitle() throws {
        // display.show's wire shape: type "display", title and plain-text body.
        let e = try decode(#"{"kind":"artifact","step_id":"stp_4","tool":"display.show","artifact":{"type":"display","title":"Runway","body":"Cash: $4.2M\nRunway: 14 months"}}"#)
        #expect(e == TurnEvent(kind: .artifact, stepID: "stp_4", tool: "display.show",
                               artifact: TurnArtifact(type: "display", title: "Runway", body: "Cash: $4.2M\nRunway: 14 months")))
        #expect(e.artifact?.isDisplay == true && e.artifact?.emailDraft == false)
        #expect(e.artifact?.to == [] && e.artifact?.subject == "")
    }

    // docs/slices/UI.md Phase 6, U1-A: a relayed comment or other flagged
    // tool result's wire shape -- type "note", title, body, source and
    // simulated. Simulated is the field the round-trip matters most for
    // (it drives the "(simulated)" badge, GlassTabTests): it must decode as
    // a real Bool, not silently coerced or dropped.
    @Test func artifactDecodesNoteWithSourceAndSimulated() throws {
        let e = try decode(#"{"kind":"artifact","step_id":"stp_5","tool":"linear.create_comment","artifact":{"type":"note","title":"CRA-3","body":"(simulated) Relayed: Nina, via Slack: it's fixed","source":"linear:CRA-3","simulated":true}}"#)
        #expect(e == TurnEvent(kind: .artifact, stepID: "stp_5", tool: "linear.create_comment",
                               artifact: TurnArtifact(type: "note", title: "CRA-3", body: "(simulated) Relayed: Nina, via Slack: it's fixed",
                                                      source: "linear:CRA-3", simulated: true)))
        #expect(e.artifact?.isNote == true && e.artifact?.isDisplay == false && e.artifact?.emailDraft == false)
        #expect(e.artifact?.simulated == true)
    }

    @Test func artifactNoteSimulatedAndSourceDefaultFalseAndEmptyWhenAbsent() throws {
        // A note with no simulated/source key (an older daemon, or a real,
        // non-simulated relay): simulated reads false, source "" -- never
        // an error, and never a stray true.
        let e = try decode(#"{"kind":"artifact","artifact":{"type":"note","title":"CRA-3","body":"Nina: fixed for real"}}"#)
        #expect(e.artifact == TurnArtifact(type: "note", title: "CRA-3", body: "Nina: fixed for real"))
        #expect(e.artifact?.simulated == false && e.artifact?.source == "")
        // A non-bool simulated value never loses the artifact; it just
        // reads as the default, false.
        let m = try decode(#"{"kind":"artifact","artifact":{"type":"note","title":"t","body":"b","simulated":"yes"}}"#)
        #expect(m.artifact?.isNote == true && m.artifact?.simulated == false)
    }

    @Test func artifactTitleAbsentOrMalformedReadsEmpty() throws {
        // An email draft has no title: absent decodes as "".
        let d = try decode(#"{"kind":"artifact","artifact":{"type":"email_draft","subject":"S","body":"B"}}"#)
        #expect(d.artifact?.title == "" && d.artifact?.isDisplay == false)
        #expect(d.artifact == TurnArtifact(type: "email_draft", subject: "S", body: "B"))
        // A non-string title never loses the artifact.
        let m = try decode(#"{"kind":"artifact","artifact":{"type":"display","title":7,"body":"B"}}"#)
        #expect(m.artifact == TurnArtifact(type: "display", title: "", body: "B"))
    }

    @Test func otherKindsHaveNoArtifact() throws {
        #expect(try decode(#"{"kind":"tool_end","step_id":"stp_1","status":"ok"}"#).artifact == nil)
        #expect(try decode(#"{"kind":"delta","text":"hi"}"#).artifact == nil)
    }

    @Test func unknownKindsStillDecode() throws {
        #expect(try decode(#"{"kind":"tool_progress","step_id":"stp_1"}"#).kind == .unknown("tool_progress"))
    }
}
