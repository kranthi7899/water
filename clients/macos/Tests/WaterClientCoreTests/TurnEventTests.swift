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

    @Test func unknownKindsStillDecode() throws {
        #expect(try decode(#"{"kind":"tool_progress","step_id":"stp_1"}"#).kind == .unknown("tool_progress"))
    }
}
