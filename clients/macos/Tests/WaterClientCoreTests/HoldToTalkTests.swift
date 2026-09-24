import Foundation
import Testing
@testable import WaterClientCore

/// Test doubles for VoiceSession's seams. Everything runs on the test's own
/// thread: the fakes record calls and the test fires results, permission
/// answers and timers by hand, in whatever order a race would produce.
final class FakePermissions: VoicePermissionGate {
    var pending: [(String?) -> Void] = []
    func request(_ done: @escaping (String?) -> Void) { pending.append(done) }
    func grant() { pending.removeFirst()(nil) }
    func deny(_ m: String) { pending.removeFirst()(m) }
}

final class FakeCapture: SpeechCapture {
    var starts = 0, ends = 0, cancels = 0
    var live = false
    var startError: Error?
    var sinks: [(SpeechResult) -> Void] = []
    func start(onResult: @escaping (SpeechResult) -> Void) throws {
        if let startError { throw startError }
        starts += 1
        live = true
        sinks.append(onResult)
    }
    func endAudio() { ends += 1; live = false }
    func cancel() { cancels += 1; live = false }
    /// Delivers a result through capture n's callback (0-based), even a stale one.
    func emit(_ r: SpeechResult, capture n: Int? = nil) { sinks[n ?? sinks.count - 1](r) }
}

final class FakeScheduler: VoiceScheduler {
    var timers: [() -> Void] = []
    func after(_ seconds: TimeInterval, _ work: @escaping () -> Void) { timers.append(work) }
    func fire(_ i: Int) { timers[i]() }
}

final class Harness {
    let perms = FakePermissions()
    let capture = FakeCapture()
    let clock = FakeScheduler()
    let session: VoiceSession
    var listening = 0
    var partials: [String] = []
    var transcripts: [String] = []
    var failures: [String] = []

    init() {
        session = VoiceSession(permissions: perms, capture: capture, scheduler: clock)
        session.onListening = { [unowned self] in self.listening += 1 }
        session.onPartial = { [unowned self] in self.partials.append($0) }
        session.onTranscript = { [unowned self] in self.transcripts.append($0) }
        session.onFailure = { [unowned self] in self.failures.append($0) }
    }
}

@Suite struct VoiceSessionTests {
    @Test func holdSpeakRelease() {
        let h = Harness()
        h.session.startHold()
        #expect(h.session.state == .starting)
        h.perms.grant()
        #expect(h.session.state == .listening && h.capture.live && h.listening == 1)
        h.capture.emit(.partial("send"))
        h.session.endHold()
        #expect(h.session.state == .finishing && h.capture.ends == 1)
        h.capture.emit(.final("send it"))
        #expect(h.transcripts == ["send it"] && h.session.state == .idle)
        #expect(h.partials.contains("send"))
    }

    /// mac-1: the key comes up while the permission step is still pending
    /// (the user let go to click Allow). The mic must never open.
    @Test func releaseDuringPendingPermissionNeverOpensMic() {
        let h = Harness()
        h.session.startHold()
        h.session.endHold()
        h.perms.grant()
        #expect(h.capture.starts == 0)
        #expect(!h.capture.live)
        #expect(h.session.state == .idle)
        #expect(h.listening == 0)
        #expect(h.failures.count == 1) // the "hold while you talk" hint
        // And the next hold works normally.
        h.session.startHold()
        h.perms.grant()
        #expect(h.session.state == .listening)
    }

    @Test func deniedPermissionFailsToIdle() {
        let h = Harness()
        h.session.startHold()
        h.perms.deny("no mic")
        #expect(h.session.state == .idle && h.failures == ["no mic"] && h.capture.starts == 0)
    }

    @Test func captureStartErrorFailsToIdle() {
        let h = Harness()
        h.capture.startError = CaptureStartError("no input")
        h.session.startHold()
        h.perms.grant()
        #expect(h.session.state == .idle && h.failures == ["no input"] && h.listening == 0)
    }

    /// mac-2: the first cycle's 1.5 s backstop must not deliver the second
    /// cycle early.
    @Test func staleBackstopDoesNotDeliverNextCapture() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.capture.emit(.partial("yes"))
        h.session.endHold()
        h.capture.emit(.final("yes"))
        #expect(h.transcripts == ["yes"])
        // Re-press within the first backstop's window.
        h.session.startHold(); h.perms.grant()
        h.capture.emit(.partial("send"))
        h.session.endHold()
        #expect(h.session.state == .finishing)
        h.clock.fire(0) // the first cycle's timer
        #expect(h.transcripts == ["yes"])
        #expect(h.session.state == .finishing)
        h.capture.emit(.final("send it"))
        #expect(h.transcripts == ["yes", "send it"])
        h.clock.fire(1) // the second cycle's timer, after delivery: no-op
        #expect(h.transcripts == ["yes", "send it"] && h.failures.isEmpty)
    }

    @Test func backstopDeliversWhenNoFinalArrives() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.capture.emit(.partial("hello there"))
        h.session.endHold()
        h.clock.fire(0)
        #expect(h.transcripts == ["hello there"] && h.session.state == .idle && h.capture.cancels == 1)
    }

    /// mac-2: a late callback from a cancelled capture must not touch the
    /// next one — no transcript overwrite, no partial, no failure.
    @Test func staleRecognizerCallbacksAreIgnored() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.session.endHold()
        h.capture.emit(.final("first"))
        h.session.startHold(); h.perms.grant()
        #expect(h.session.state == .listening)
        let partialsBefore = h.partials.count
        h.capture.emit(.error("cancelled"), capture: 0)
        h.capture.emit(.partial("ghost"), capture: 0)
        h.capture.emit(.final("ghost"), capture: 0)
        #expect(h.session.state == .listening && h.failures.isEmpty)
        #expect(h.partials.count == partialsBefore)
        h.capture.emit(.partial("real"))
        h.session.endHold()
        h.capture.emit(.final("real"))
        #expect(h.transcripts == ["first", "real"])
    }

    @Test func repeatedKeyDownWhileListeningIsNoOp() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.session.startHold()
        h.session.startHold()
        #expect(h.session.state == .listening && h.capture.starts == 1 && h.perms.pending.isEmpty)
    }

    @Test func releaseWithoutPressIsNoOp() {
        let h = Harness()
        h.session.endHold()
        #expect(h.session.state == .idle && h.failures.isEmpty)
    }

    @Test func recognizerErrorWhileListeningFails() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.capture.emit(.error("boom"))
        #expect(h.session.state == .idle && h.capture.cancels == 1)
        #expect(h.failures.count == 1 && h.failures[0].contains("boom"))
    }

    @Test func recognizerErrorWhileFinishingDeliversWhatItHas() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.capture.emit(.partial("almost"))
        h.session.endHold()
        h.capture.emit(.error("ended"))
        #expect(h.transcripts == ["almost"] && h.failures.isEmpty && h.session.state == .idle)
    }

    @Test func emptyTranscriptFails() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.session.endHold()
        h.capture.emit(.final("   "))
        #expect(h.transcripts.isEmpty && h.failures.count == 1 && h.session.state == .idle)
    }

    /// A menu-started (toggle) capture ends on a second toggle or a fresh
    /// hotkey press — never on a stray key release.
    @Test func toggleCaptureIgnoresKeyUpAndStopsOnPressOrToggle() {
        let h = Harness()
        h.session.toggle()
        h.session.toggle() // still starting: no-op
        h.perms.grant()
        #expect(h.session.state == .listening)
        h.session.endHold()
        #expect(h.session.state == .listening)
        h.session.toggle()
        #expect(h.session.state == .finishing)
        h.capture.emit(.final("from the menu"))
        #expect(h.transcripts == ["from the menu"])

        // Toggle, then a real hotkey press stops it; the matching release is a no-op.
        h.session.toggle(); h.perms.grant()
        h.session.startHold()
        #expect(h.session.state == .finishing)
        h.session.endHold()
        h.capture.emit(.final("stopped by key"))
        #expect(h.transcripts == ["from the menu", "stopped by key"])

        // Then a hold works normally.
        h.session.startHold(); h.perms.grant()
        h.session.endHold()
        h.capture.emit(.final("held"))
        #expect(h.transcripts.last == "held")
    }

    @Test func cancelStopsEverything() {
        let h = Harness()
        h.session.startHold(); h.perms.grant()
        h.session.cancel()
        #expect(h.session.state == .idle && !h.capture.live)
        h.capture.emit(.final("late"))
        #expect(h.transcripts.isEmpty && h.failures.isEmpty)
        // Cancel while permissions are pending: the grant must not open the mic.
        h.session.startHold()
        h.session.cancel()
        h.perms.grant()
        #expect(h.capture.starts == 1 && h.session.state == .idle)
    }
}

@Suite struct HoldKeyTrackerTests {
    let v: UInt16 = 9

    @Test func pressRepeatRelease() {
        var t = HoldKeyTracker(keyCode: v)
        #expect(t.keyDown(keyCode: v, isRepeat: false, matchesHotKey: true) == .press)
        #expect(t.keyDown(keyCode: v, isRepeat: true, matchesHotKey: false) == .swallow)
        #expect(t.keyUp(keyCode: v) == .release)
        #expect(!t.held)
    }

    /// mac-6: a plain 'v' typed anywhere (no hold in progress) passes through.
    @Test func plainVKeyUpPassesThrough() {
        var t = HoldKeyTracker(keyCode: v)
        #expect(t.keyDown(keyCode: v, isRepeat: false, matchesHotKey: false) == .pass)
        #expect(t.keyUp(keyCode: v) == .pass)
        #expect(t.keyDown(keyCode: v, isRepeat: true, matchesHotKey: false) == .pass)
    }

    @Test func otherKeysPassWhileHeld() {
        var t = HoldKeyTracker(keyCode: v)
        _ = t.keyDown(keyCode: v, isRepeat: false, matchesHotKey: true)
        #expect(t.keyDown(keyCode: 0, isRepeat: false, matchesHotKey: false) == .pass)
        #expect(t.keyUp(keyCode: 0) == .pass)
        #expect(t.held)
        // Release with the modifiers already up still counts (key code only).
        #expect(t.keyUp(keyCode: v) == .release)
    }
}
