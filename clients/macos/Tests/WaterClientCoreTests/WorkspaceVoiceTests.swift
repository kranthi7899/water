import Foundation
import Testing
@testable import WaterClientCore

/// V-ui2: the workspace bar's held mic. The page asks native code to run a
/// voice turn into one thread; these pin which transcript goes where and
/// what the turn request carries.
@Suite struct WorkspaceVoiceTests {
    @Test func aPageHoldSendsExactlyOneTranscriptToItsThread() {
        var target = WorkspaceVoiceTarget()
        target.micDown(thread: "thr_0a1b")
        #expect(target.take() == "thr_0a1b")
        // Taken once: a later transcript (a hotkey hold) goes nowhere.
        #expect(target.take() == nil)
    }

    @Test func aHotkeyHoldOrAFailureClearsThePageThread() {
        var target = WorkspaceVoiceTarget()
        target.micDown(thread: "thr_0a1b")
        target.clear() // the voice hotkey went down, or the capture failed
        #expect(target.take() == nil)
    }

    @Test func aMalformedThreadIsNeverATarget() {
        var target = WorkspaceVoiceTarget()
        target.micDown(thread: "thr_0a1b")
        target.micDown(thread: "env_1") // replaces, and is refused
        #expect(target.thread == nil)
        #expect(target.take() == nil)
    }

    private func body(_ req: HTTPRequest) throws -> [String: Any] {
        let data = try #require(req.body)
        let obj = try JSONSerialization.jsonObject(with: data)
        return try #require(obj as? [String: Any])
    }

    @Test func theTurnRequestCarriesTheThread() throws {
        let req = try UnixSocketClient.turnRequest(channel: .voice, prompt: "what about the budget", token: "t",
                                                   turnID: "turn-1", threadID: "thr_0a1b")
        #expect(req.method == "POST" && req.path == "/v1/turns")
        let b = try body(req)
        #expect(b["thread_id"] as? String == "thr_0a1b")
        #expect(b["channel"] as? String == "voice")
        #expect(b["prompt"] as? String == "what about the budget")
        #expect(b["turn_id"] as? String == "turn-1")
        #expect(b["meeting_id"] == nil)
    }

    @Test func aThreadTurnNeverAlsoCarriesAMeeting() throws {
        let b = try body(UnixSocketClient.turnRequest(channel: .voice, prompt: "p", token: "t",
                                                      meetingID: "ms_1", threadID: "thr_0a1b"))
        #expect(b["thread_id"] as? String == "thr_0a1b")
        #expect(b["meeting_id"] == nil)
    }

    @Test func withoutAThreadTheRequestIsUnchanged() throws {
        let b = try body(UnixSocketClient.turnRequest(channel: .textBar, prompt: "p", token: "t", meetingID: "ms_1"))
        #expect(b["thread_id"] == nil)
        #expect(b["meeting_id"] as? String == "ms_1")
        #expect(Set(b.keys) == ["channel", "prompt", "clear", "meeting_id"])
        // A malformed thread id is dropped, never sent.
        let bad = try body(UnixSocketClient.turnRequest(channel: .voice, prompt: "p", token: "t", threadID: "thr_../x"))
        #expect(bad["thread_id"] == nil)
    }
}
