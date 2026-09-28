import Foundation
import Testing
@testable import WaterClientCore

/// Voice mode (owner brief 2026-09-26): ⌃⌥V toggles it, Space is held to
/// talk, Esc or 60s of true idleness leaves it.
@Suite struct VoiceModeTests {
    let t0 = Date(timeIntervalSinceReferenceDate: 1_000_000)
    func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    func armed() -> VoiceMode {
        var m = VoiceMode()
        _ = m.toggle(now: t0)
        return m
    }

    // MARK: toggle

    @Test func startsOffAndCapturesNothing() {
        let m = VoiceMode()
        #expect(m.state == .off)
        #expect(!m.capturesKeys)
    }

    @Test func toggleOnRegistersKeysAndShowsGlobe() {
        var m = VoiceMode()
        let fx = m.toggle(now: t0)
        #expect(fx == [.registerCaptureKeys, .showGlobe])
        #expect(m.state == .armed)
        #expect(m.capturesKeys)
    }

    @Test func toggleOffUnregistersAndHides() {
        var m = armed()
        let fx = m.toggle(now: at(1))
        #expect(fx == [.unregisterCaptureKeys, .hideGlobe, .exited(.toggle)])
        #expect(m.state == .off)
        #expect(!m.capturesKeys)
    }

    @Test func toggleWhileHoldingCancelsTheHoldFirst() {
        var m = armed()
        _ = m.spaceDown(now: at(1))
        let fx = m.toggle(now: at(2))
        #expect(fx == [.cancelHold, .unregisterCaptureKeys, .hideGlobe, .exited(.toggle)])
        #expect(m.state == .off)
    }

    @Test func reEntryAfterExitWorks() {
        var m = armed()
        _ = m.esc(now: at(1))
        #expect(m.toggle(now: at(2)) == [.registerCaptureKeys, .showGlobe])
        #expect(m.spaceDown(now: at(3)) == [.startHold])
        #expect(m.spaceUp(now: at(4)) == [.endHold])
        #expect(m.toggle(now: at(5)) == [.unregisterCaptureKeys, .hideGlobe, .exited(.toggle)])
        #expect(m.toggle(now: at(6)) == [.registerCaptureKeys, .showGlobe])
    }

    // MARK: space

    @Test func spaceDownStartsAndUpEndsTheHold() {
        var m = armed()
        #expect(m.spaceDown(now: at(1)) == [.startHold])
        #expect(m.state == .holding)
        #expect(m.spaceUp(now: at(2)) == [.endHold])
        #expect(m.state == .armed)
    }

    @Test func keyRepeatWhileHeldIsIgnored() {
        var m = armed()
        _ = m.spaceDown(now: at(1))
        #expect(m.spaceDown(now: at(1.1)) == [])
        #expect(m.spaceDown(now: at(1.2)) == [])
        #expect(m.state == .holding)
        #expect(m.spaceUp(now: at(2)) == [.endHold])
    }

    @Test func spaceIsInertWhenOff() {
        var m = VoiceMode()
        #expect(m.spaceDown(now: t0) == [])
        #expect(m.spaceUp(now: t0) == [])
        #expect(m.esc(now: t0) == [])
        #expect(m.state == .off)
    }

    @Test func strayReleaseWithNoHoldIsIgnored() {
        var m = armed()
        #expect(m.spaceUp(now: at(1)) == [])
        #expect(m.state == .armed)
    }

    @Test func spaceDuringAReplyStartsANewHold() {
        // Barge-in: the hold path itself silences and cancels the old turn.
        var m = armed()
        m.turnStarted(1, now: at(1))
        m.speechStarted(now: at(2))
        #expect(m.state == .processing)
        #expect(m.spaceDown(now: at(3)) == [.startHold])
        #expect(m.state == .holding)
    }

    // MARK: esc

    /// Esc is a hard stop from every state (2026-09-26 fixes): it always
    /// cancels the capture, the turn and the speech, then leaves.
    static let escEffects: [VoiceMode.Effect] = [.cancelHold, .cancelTurn, .stopSpeech, .unregisterCaptureKeys, .hideGlobe, .exited(.escape)]

    @Test func escExitsWhenArmed() {
        var m = armed()
        #expect(m.esc(now: at(1)) == Self.escEffects)
        #expect(m.state == .off)
    }

    @Test func escWhileHoldingCancelsTheHoldAndExits() {
        var m = armed()
        _ = m.spaceDown(now: at(1))
        #expect(m.esc(now: at(2)) == Self.escEffects)
        #expect(m.state == .off)
        // The Space release that follows (now delivered to the app) is not ours.
        #expect(m.spaceUp(now: at(3)) == [])
    }

    @Test func escWhileProcessingCancelsTheTurnAndSpeechAndExits() {
        var m = armed()
        m.turnStarted(1, now: at(1))
        m.speechStarted(now: at(2))
        #expect(m.esc(now: at(3)) == Self.escEffects)
        #expect(m.state == .off)
        // Re-entering starts clean: the cancelled turn and speech don't
        // hold the new session busy.
        _ = m.toggle(now: at(4))
        #expect(m.state == .armed)
        #expect(m.tick(now: at(64)) != [])
    }

    @Test func escWhileTheCaptureFinishesCancelsIt() {
        var m = armed()
        _ = m.spaceDown(now: at(1))
        _ = m.spaceUp(now: at(2)) // released: the capture is finishing
        #expect(m.esc(now: at(2.2)) == Self.escEffects)
    }

    @Test func escLeavesWhileAnApprovalIsPinnedButNeverDecidesIt() {
        // The effects have no approval in them: the tab and the workspace
        // keep the approval open.
        var m = armed()
        m.setApprovalPinned(true, now: at(1))
        #expect(m.esc(now: at(2)) == Self.escEffects)
        #expect(m.state == .off)
    }

    @Test func otherExitsDontCancelTheTurn() {
        var a = armed(); a.turnStarted(1, now: at(1))
        #expect(a.toggle(now: at(2)) == [.unregisterCaptureKeys, .hideGlobe, .exited(.toggle)])
    }

    // MARK: captured key routing

    /// CaptureKeys reports (key, pressed); `key(_:pressed:now:)` is the one
    /// router from those to the mode's inputs.
    @Test func capturedKeysRouteToTheHoldAndEsc() {
        var m = armed()
        #expect(m.key(.space, pressed: true, now: at(1)) == [.startHold])
        #expect(m.key(.space, pressed: true, now: at(1.1)) == []) // repeat
        #expect(m.key(.space, pressed: false, now: at(2)) == [.endHold])
        #expect(m.key(.escape, pressed: false, now: at(3)) == [])
        #expect(m.key(.escape, pressed: true, now: at(3)) == Self.escEffects)
        #expect(m.key(.space, pressed: true, now: at(4)) == [])
        #expect(m.key(.escape, pressed: true, now: at(4)) == [])
    }

    // MARK: timeout

    @Test func idleTimeoutAfterSixtySeconds() {
        var m = armed()
        #expect(m.tick(now: at(59.9)) == [])
        #expect(m.state == .armed)
        #expect(m.tick(now: at(60)) == [.unregisterCaptureKeys, .hideGlobe, .exited(.idleTimeout)])
        #expect(m.state == .off)
        #expect(m.tick(now: at(200)) == [])
    }

    @Test func eachSpacePressResetsTheTimer() {
        var m = armed()
        _ = m.spaceDown(now: at(50))
        _ = m.spaceUp(now: at(51))
        #expect(m.tick(now: at(100)) == [])
        #expect(m.tick(now: at(111)) != [])
    }

    @Test func neverTimesOutWhileHolding() {
        var m = armed()
        _ = m.spaceDown(now: at(1))
        #expect(m.tick(now: at(500)) == [])
        #expect(m.state == .holding)
    }

    @Test func neverTimesOutMidTurn() {
        var m = armed()
        m.turnStarted(7, now: at(1))
        #expect(m.tick(now: at(300)) == [])
        #expect(m.state == .processing)
        // A different turn's finish doesn't free it.
        m.turnFinished(6, now: at(301))
        #expect(m.tick(now: at(400)) == [])
        m.turnFinished(7, now: at(402))
        #expect(m.state == .armed)
        // The 60s count restarts from when it went idle.
        #expect(m.tick(now: at(461)) == [])
        #expect(m.tick(now: at(462)) != [])
    }

    @Test func neverTimesOutWhileSpeaking() {
        var m = armed()
        m.speechStarted(now: at(1))
        #expect(m.tick(now: at(120)) == [])
        m.speechIdle(now: at(121))
        #expect(m.tick(now: at(180)) == [])
        #expect(m.tick(now: at(181)) != [])
    }

    @Test func neverTimesOutWhileAnApprovalIsPinned() {
        var m = armed()
        m.setApprovalPinned(true, now: at(1))
        #expect(m.state == .processing)
        #expect(m.tick(now: at(600)) == [])
        m.setApprovalPinned(true, now: at(601)) // unchanged: no reset
        m.setApprovalPinned(false, now: at(700))
        #expect(m.tick(now: at(759)) == [])
        #expect(m.tick(now: at(760)) != [])
    }

    /// A Space capture still finishing after release (its transcript queued
    /// behind a model warm-up, up to VoiceSession.warmupBackstop = 90s) is
    /// not idleness: voice mode must not time out under it (review fix).
    @Test func neverTimesOutWhileACaptureIsStillFinishing() {
        var m = armed()
        _ = m.spaceDown(now: at(1))
        _ = m.spaceUp(now: at(2))
        m.setCapturePending(true, now: at(2))
        #expect(m.state == .processing)
        #expect(m.tick(now: at(62)) == [])
        #expect(m.tick(now: at(91)) == [])
        m.setCapturePending(false, now: at(92)) // e.g. "Didn't catch anything"
        #expect(m.tick(now: at(151)) == [])
        #expect(m.tick(now: at(152)) == [.unregisterCaptureKeys, .hideGlobe, .exited(.idleTimeout)])
    }

    /// Esc cancels that capture, so it no longer counts after re-entry.
    @Test func escClearsAPendingCapture() {
        var m = armed()
        m.setCapturePending(true, now: at(1))
        _ = m.esc(now: at(2))
        _ = m.toggle(now: at(3))
        #expect(m.state == .armed)
        #expect(m.tick(now: at(63)) == [.unregisterCaptureKeys, .hideGlobe, .exited(.idleTimeout)])
    }

    @Test func aNewTurnReplacesTheOldOne() {
        // TurnRunner cancels the old stream, whose finish then never fires.
        var m = armed()
        m.turnStarted(1, now: at(1))
        m.turnStarted(2, now: at(2))
        m.turnFinished(2, now: at(3))
        #expect(m.state == .armed)
    }

    @Test func cancelledTurnsFreeTheMode() {
        var m = armed()
        m.turnStarted(1, now: at(1))
        m.turnsCancelled(now: at(2))
        #expect(m.state == .armed)
        #expect(m.tick(now: at(62)) != [])
    }

    @Test func busyStateCarriesAcrossEntry() {
        // A reply still speaking when voice mode opens keeps it from timing out.
        var m = VoiceMode()
        m.speechStarted(now: t0)
        _ = m.toggle(now: at(1))
        #expect(m.state == .processing)
        #expect(m.tick(now: at(100)) == [])
    }

    // MARK: failures and shutdown

    @Test func captureUnavailableExits() {
        var m = armed()
        #expect(m.captureUnavailable() == [.unregisterCaptureKeys, .hideGlobe, .exited(.captureUnavailable)])
        #expect(m.state == .off)
        #expect(m.captureUnavailable() == [])
    }

    @Test func shutdownAlwaysUnregistersWhenOn() {
        var m = armed()
        _ = m.spaceDown(now: at(1))
        #expect(m.shutdown() == [.cancelHold, .unregisterCaptureKeys, .hideGlobe, .exited(.shutdown)])
        var off = VoiceMode()
        #expect(off.shutdown() == [])
    }

    @Test func everyExitPathUnregisters() {
        var paths: [[VoiceMode.Effect]] = []
        var a = armed(); paths.append(a.toggle(now: at(1)))
        var b = armed(); paths.append(b.esc(now: at(1)))
        var c = armed(); paths.append(c.tick(now: at(61)))
        var d = armed(); paths.append(d.captureUnavailable())
        var e = armed(); paths.append(e.shutdown())
        for fx in paths {
            #expect(fx.contains(.unregisterCaptureKeys))
            #expect(fx.contains(.hideGlobe))
        }
        for m in [a, b, c, d, e] { #expect(!m.capturesKeys) }
    }
}
