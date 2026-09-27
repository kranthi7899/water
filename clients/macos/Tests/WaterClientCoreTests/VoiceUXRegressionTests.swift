import Foundation
import Testing
@testable import WaterClientCore

/// Voice UX fixes (2026-09-26): each test here reproduces a bug the owner
/// hit, using only API that existed before the fix, so it fails on the old
/// code and passes on the new.
@Suite struct VoiceUXRegressionTests {
    /// A scheduler that remembers each timer's delay and fires those due
    /// by a given time, like the real clock would.
    final class Clock: VoiceScheduler {
        var pending: [(at: TimeInterval, work: () -> Void)] = []
        var now: TimeInterval = 0
        func after(_ seconds: TimeInterval, _ work: @escaping () -> Void) { pending.append((now + seconds, work)) }
        func advance(to t: TimeInterval) {
            now = t
            let due = pending.filter { $0.at <= t }
            pending.removeAll { $0.at <= t }
            for d in due { d.work() }
        }
    }

    final class WarmingCapture: SpeechCapture {
        var isWarmingUp = true
        var sink: ((SpeechResult) -> Void)?
        var cancels = 0
        func start(onResult: @escaping (SpeechResult) -> Void) throws { sink = onResult }
        func endAudio() {}
        func cancel() { cancels += 1 }
    }

    /// Space: a hold released while Parakeet's models are still loading
    /// (measured: 24.5s on the first run after a build, ~1.2s in a warm
    /// process) must wait for the final result. Before the fix the 1.5s
    /// backstop fired first: "Didn't catch anything" and the queued audio
    /// was cancelled.
    @Test func holdReleasedWhileTheRecognizerWarmsUpIsNotDropped() {
        let perms = FakePermissions()
        let capture = WarmingCapture()
        let clock = Clock()
        let session = VoiceSession(permissions: perms, capture: capture, scheduler: clock)
        var transcripts: [String] = [], failures: [String] = []
        session.onTranscript = { transcripts.append($0) }
        session.onFailure = { failures.append($0) }

        session.startHold()
        perms.grant()
        #expect(session.state == .listening)
        session.endHold()
        clock.advance(to: 1.5) // the old backstop
        clock.advance(to: 10)  // still loading
        #expect(failures.isEmpty)
        #expect(capture.cancels == 0)
        capture.isWarmingUp = false
        capture.sink?(.final("what is on my calendar"))
        #expect(transcripts == ["what is on my calendar"])
        #expect(failures.isEmpty)
    }

    /// Esc right after Space came up (the capture is still finishing) must
    /// cancel that capture too; before the fix only a key still held was
    /// cancelled, so the transcript went out after voice mode had closed.
    @Test func escAfterReleaseStillCancelsTheCapture() {
        var m = VoiceMode()
        _ = m.toggle(now: Date())
        _ = m.spaceDown(now: Date())
        _ = m.spaceUp(now: Date())
        #expect(m.esc(now: Date()).first == .cancelHold)
    }

    /// The glass tab: a model-driven `display` artifact shows on voice.
    @Test func displayArtifactShowsOnVoice() throws {
        let line = #"{"kind":"artifact","tool":"display.show","artifact":{"type":"display","body":"Cash: $4.2M"}}"#
        let e = try #require(TurnEvent.decode(line: Data(line.utf8)))
        let item = try #require(GlassItem.shouldShow(channel: .voice, event: e))
        #expect(item.readBack == "Cash: $4.2M")
        #expect(GlassItem.shouldShow(channel: .textBar, event: e) == nil)
    }

    // MARK: the warm-up wait's edges

    func session(_ capture: WarmingCapture, _ clock: Clock, _ perms: FakePermissions) -> VoiceSession {
        VoiceSession(permissions: perms, capture: capture, scheduler: clock)
    }

    @Test func warmingUpIsAnnouncedAndBounded() {
        let perms = FakePermissions(), capture = WarmingCapture(), clock = Clock()
        let s = session(capture, clock, perms)
        var warming = 0, failures: [String] = []
        s.onWarmingUp = { warming += 1 }
        s.onFailure = { failures.append($0) }
        s.startHold(); perms.grant(); s.endHold()
        #expect(warming == 1)
        clock.advance(to: s.warmupBackstop - 1)
        #expect(failures.isEmpty && s.state == .finishing)
        clock.advance(to: s.warmupBackstop)
        #expect(failures == ["Didn't catch anything — try again."])
        #expect(s.state == .idle)
    }

    /// Models that finish loading mid-hold still get the long wait: the
    /// queued audio is decoded after the load.
    @Test func aHoldThatStartedWarmStillWaits() {
        let perms = FakePermissions(), capture = WarmingCapture(), clock = Clock()
        let s = session(capture, clock, perms)
        var transcripts: [String] = []
        s.onTranscript = { transcripts.append($0) }
        s.startHold(); perms.grant()
        capture.isWarmingUp = false
        s.endHold()
        clock.advance(to: 3)
        capture.sink?(.final("hello"))
        #expect(transcripts == ["hello"])
    }

    /// A warm recognizer keeps the short backstop (no added latency).
    @Test func aWarmCaptureKeepsTheShortBackstop() {
        let perms = FakePermissions(), capture = WarmingCapture(), clock = Clock()
        capture.isWarmingUp = false
        let s = session(capture, clock, perms)
        var warming = 0, transcripts: [String] = []
        s.onWarmingUp = { warming += 1 }
        s.onTranscript = { transcripts.append($0) }
        s.startHold(); perms.grant()
        capture.sink?(.partial("quick one"))
        s.endHold()
        clock.advance(to: s.backstop)
        #expect(transcripts == ["quick one"])
        #expect(warming == 0)
    }
}
