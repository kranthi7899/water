import Foundation
import Testing
@testable import WaterClientCore

/// The Activity HUD's one state machine (docs/slices/V.md §7.4 V-hud),
/// driven on a fake clock: every input takes `now`, so auto-dismiss and the
/// thinking pulse are tested without sleeping.
@Suite struct ActivityModelTests {
    let t0 = Date(timeIntervalSince1970: 1_000_000)
    func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    func ev(_ kind: TurnEvent.Kind, text: String? = nil, approvalID: String? = nil, action: String? = nil,
            risk: String? = nil, payloadHash: String? = nil, readBack: String? = nil,
            stepID: String? = nil, tool: String? = nil, label: String? = nil, status: String? = nil) -> TurnEvent {
        TurnEvent(kind: kind, text: text, approvalID: approvalID, action: action, risk: risk,
                  payloadHash: payloadHash, readBack: readBack, stepID: stepID, tool: tool, label: label, status: status)
    }

    func approval(_ id: String, hash: String = "h1") -> TurnEvent {
        ev(.approvalRequired, approvalID: id, action: "gcal.create_event", risk: "medium",
           payloadHash: hash, readBack: "Create “Sync” on Monday at 10:00.")
    }

    // MARK: lifecycle

    @Test func startsHiddenAndIdle() {
        let m = ActivityModel()
        #expect(!m.isVisible)
        #expect(m.phase == .idle)
        #expect(!m.isExpanded)
        #expect(m.steps.isEmpty && m.approvals.isEmpty)
    }

    /// A reflex (Tier-0) turn: ack, sentence, done. No tool_*, no
    /// approval_required, so the HUD never expands, and it goes away 4s
    /// after done.
    @Test func reflexTurnNeverExpandsAndDismissesAfterDone() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        #expect(m.isVisible && !m.isExpanded && m.phase == .thinking)
        m.event(ev(.ack), turn: turn, now: at(0.1))
        m.event(ev(.delta, text: "You have two meetings."), turn: turn, now: at(0.2))
        #expect(m.phase == .responding && !m.isExpanded)
        m.event(ev(.done), turn: turn, now: at(0.3))
        #expect(!m.isExpanded)
        #expect(m.phase == .idle)
        #expect(m.dismissAt == at(0.3 + ActivityModel.dismissDelay))

        m.tick(now: at(4.2))
        #expect(m.isVisible) // not yet
        m.tick(now: at(4.3))
        #expect(!m.isVisible)
        #expect(m.dismissAt == nil)
    }

    @Test func toolTurnExpandsOnFirstToolStart() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.ack), turn: turn, now: at(0.1))
        #expect(!m.isExpanded)
        m.event(ev(.toolStart, stepID: "stp_1", tool: "linear.list_issues", label: "Checking tickets in Linear"),
                turn: turn, now: at(0.5))
        #expect(m.isExpanded)
        #expect(m.steps == [ActivityStep(id: "stp_1", label: "Checking tickets in Linear", state: .running)])

        m.event(ev(.toolEnd, stepID: "stp_1", tool: "linear.list_issues", label: "Checking tickets in Linear", status: "ok"),
                turn: turn, now: at(1))
        #expect(m.steps.map(\.state) == [.ok])

        m.event(ev(.toolStart, stepID: "stp_2", label: "Checking your calendar"), turn: turn, now: at(1.1))
        m.event(ev(.toolEnd, stepID: "stp_2", label: "Checking your calendar", status: "denied"), turn: turn, now: at(1.2))
        #expect(m.steps.map(\.state) == [.ok, .denied])

        m.event(ev(.done), turn: turn, now: at(2))
        #expect(m.isExpanded) // steps stay listed until it dismisses
        m.tick(now: at(6))
        #expect(!m.isVisible)
        #expect(m.steps.isEmpty) // collapsed back
    }

    @Test func quickToolsCountAsSteps() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.toolStart, stepID: "stp_q", tool: "quick.calendar", label: "Checking your calendar"), turn: turn, now: at(0.1))
        #expect(m.isExpanded)
    }

    @Test func toolEndStatusesMap() {
        #expect(ActivityStep.State(status: "ok") == .ok)
        #expect(ActivityStep.State(status: "queued") == .queued)
        #expect(ActivityStep.State(status: "denied") == .denied)
        #expect(ActivityStep.State(status: "error") == .error)
        #expect(ActivityStep.State(status: nil) == .ok)
        #expect(ActivityStep.State(status: "later_status") == .other("later_status"))
    }

    /// A tool_end whose tool_start was missed (e.g. the stream attached
    /// late) still shows as a finished step rather than being dropped.
    @Test func toolEndWithoutStartIsAdded() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.toolEnd, stepID: "stp_9", label: "Reading the budget sheet", status: "ok"), turn: turn, now: at(0.1))
        #expect(m.steps == [ActivityStep(id: "stp_9", label: "Reading the budget sheet", state: .ok)])
    }

    @Test func stepEventsWithoutAStepIDAreIgnored() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.toolStart, label: "x"), turn: turn, now: at(0.1))
        m.event(ev(.toolStart, stepID: "  ", label: "x"), turn: turn, now: at(0.1))
        #expect(m.steps.isEmpty && !m.isExpanded)
    }

    @Test func duplicateToolStartDoesNotAddASecondRow() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.toolStart, stepID: "stp_1", label: "A"), turn: turn, now: at(0.1))
        m.event(ev(.toolStart, stepID: "stp_1", label: "A"), turn: turn, now: at(0.2))
        #expect(m.steps.count == 1)
    }

    @Test func emptyLabelFallsBackToGenericText() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.toolStart, stepID: "stp_1", label: ""), turn: turn, now: at(0.1))
        #expect(m.steps.first?.label == ActivityModel.genericStepLabel)
    }

    // MARK: approvals

    @Test func approvalPinsUntilResolvedThenCollapses() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(approval("apr_1"), turn: turn, now: at(0.5))
        #expect(m.isExpanded && m.isPinned)
        #expect(m.approvals == [ApprovalCard(id: "apr_1", action: "gcal.create_event", risk: "medium",
                                             readBack: "Create “Sync” on Monday at 10:00.", payloadHash: "h1")])
        m.event(ev(.done), turn: turn, now: at(1))
        #expect(m.phase == .needsYou)
        #expect(m.dismissAt == nil)

        // Pinned: an hour later it's still there.
        m.tick(now: at(3600))
        #expect(m.isVisible && m.isExpanded)

        m.approvalResolved("apr_1", now: at(3601))
        #expect(!m.isPinned)
        #expect(m.phase == .idle)
        #expect(m.dismissAt == at(3601 + ActivityModel.dismissDelay))
        m.tick(now: at(3605))
        #expect(!m.isVisible && !m.isExpanded)
    }

    @Test func approvalWithoutAnIDIsIgnored() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.approvalRequired, action: "gmail.send_message"), turn: turn, now: at(0.1))
        #expect(m.approvals.isEmpty)
    }

    /// A second approval_required for the same envelope (e.g. a richer
    /// event after a bare one) updates the card, never duplicates it.
    @Test func repeatedApprovalUpdatesTheSameCard() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.approvalRequired, approvalID: "apr_1"), turn: turn, now: at(0.1))
        m.event(approval("apr_1", hash: "h2"), turn: turn, now: at(0.2))
        #expect(m.approvals.count == 1)
        #expect(m.approvals[0].payloadHash == "h2")
        #expect(m.approvals[0].readBack == "Create “Sync” on Monday at 10:00.")
        // A later bare event doesn't wipe what's known.
        m.event(ev(.approvalRequired, approvalID: "apr_1"), turn: turn, now: at(0.3))
        #expect(m.approvals[0].payloadHash == "h2")
    }

    /// Approvals survive a new turn (they're still open in the daemon);
    /// the steps of the old turn don't.
    @Test func approvalsSurviveANewTurnButStepsDoNot() {
        var m = ActivityModel()
        let t1 = m.turnSent(now: at(0))
        m.event(ev(.toolStart, stepID: "stp_1", label: "A"), turn: t1, now: at(0.1))
        m.event(approval("apr_1"), turn: t1, now: at(0.2))
        m.event(ev(.done), turn: t1, now: at(0.3))
        let t2 = m.turnSent(now: at(10))
        #expect(m.steps.isEmpty)
        #expect(m.approvals.map(\.id) == ["apr_1"])
        #expect(m.phase == .thinking)
        m.event(ev(.done), turn: t2, now: at(11))
        #expect(m.phase == .needsYou && m.dismissAt == nil)
    }

    /// An approval resolved elsewhere (a spoken yes in another turn) is
    /// removed by the HUD's re-read, the same input as a click.
    @Test func resolvingAnUnknownApprovalIsANoOp() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(approval("apr_1"), turn: turn, now: at(0.1))
        m.approvalResolved("apr_nope", now: at(1))
        #expect(m.approvals.map(\.id) == ["apr_1"])
    }

    @Test func submittingAndFailureNotesOnACard() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(approval("apr_1"), turn: turn, now: at(0.1))
        m.approvalSubmitting("apr_1")
        #expect(m.approvals[0].submitting)
        m.approvalFailed("apr_1", message: "It changed since it was shown. Review it again.")
        #expect(!m.approvals[0].submitting)
        #expect(m.approvals[0].note == "It changed since it was shown. Review it again.")
        // Still pinned: a failed click resolves nothing.
        #expect(m.isPinned)
    }

    @Test func approvalRefreshedReplacesHashAndReadBack() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(approval("apr_1"), turn: turn, now: at(0.1))
        m.approvalRefreshed(ApprovalCard(id: "apr_1", action: "gcal.create_event", risk: "medium",
                                         readBack: "new text", payloadHash: "h9"))
        #expect(m.approvals[0].payloadHash == "h9" && m.approvals[0].readBack == "new text")
        m.approvalRefreshed(ApprovalCard(id: "apr_x", action: nil, risk: nil, readBack: nil, payloadHash: "z"))
        #expect(m.approvals.count == 1) // refresh never adds a card
    }

    // MARK: phases

    @Test func listeningThinkingRespondingIdleDrivenByInputs() {
        var m = ActivityModel()
        m.holdStarted(now: at(0))
        #expect(m.isVisible && m.phase == .listening && !m.isExpanded)
        m.holdEnded(now: at(2))
        #expect(m.phase == .thinking)
        let turn = m.turnSent(now: at(2.3))
        #expect(m.phase == .thinking)
        m.event(ev(.sentence, text: "Here you go."), turn: turn, now: at(3))
        #expect(m.phase == .responding)
        m.speechStarted(now: at(3.1))
        m.event(ev(.done), turn: turn, now: at(3.2))
        // Still speaking: done alone doesn't end the interaction.
        #expect(m.phase == .responding)
        #expect(m.dismissAt == nil)
        m.speechIdle(now: at(6))
        #expect(m.phase == .idle)
        #expect(m.dismissAt == at(6 + ActivityModel.dismissDelay))
    }

    @Test func speechIdleBeforeDoneWaitsForDone() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.speechStarted(now: at(1))
        m.speechIdle(now: at(2)) // between sentences, the queue drained
        #expect(m.phase == .responding)
        #expect(m.dismissAt == nil)
        m.event(ev(.done), turn: turn, now: at(3))
        #expect(m.phase == .idle && m.dismissAt == at(3 + ActivityModel.dismissDelay))
    }

    @Test func aStaleTurnsEventsAreIgnored() {
        var m = ActivityModel()
        let old = m.turnSent(now: at(0))
        let cur = m.turnSent(now: at(1))
        #expect(old != cur)
        m.event(ev(.toolStart, stepID: "stp_old", label: "Old"), turn: old, now: at(1.1))
        m.event(approval("apr_old"), turn: old, now: at(1.2))
        m.event(ev(.done), turn: old, now: at(1.3))
        #expect(m.steps.isEmpty && m.approvals.isEmpty)
        #expect(m.phase == .thinking) // the old done didn't end the new turn
        m.turnEnded(turn: old, now: at(1.4))
        #expect(m.phase == .thinking)
    }

    /// A hold during a running turn is barge-in: that turn's late events
    /// are stale, and a hold that then hears nothing still settles.
    @Test func holdDuringATurnRetiresIt() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.speechStarted(now: at(0.5))
        m.holdStarted(now: at(1))
        #expect(m.phase == .listening)
        m.event(ev(.toolStart, stepID: "stp_late", label: "Late"), turn: turn, now: at(1.1))
        m.event(ev(.done), turn: turn, now: at(1.2))
        #expect(m.steps.isEmpty && m.phase == .listening)
        m.holdEnded(now: at(2))
        m.cancelled(now: at(2.1))
        #expect(m.phase == .idle && m.dismissAt == at(2.1 + ActivityModel.dismissDelay))
    }

    @Test func errorEndsTheTurnLikeDone() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.error), turn: turn, now: at(1))
        #expect(m.phase == .idle && m.dismissAt == at(1 + ActivityModel.dismissDelay))
    }

    /// onFinish without a terminal event (a dropped socket) still ends it.
    @Test func turnEndedWithoutTerminalEventEndsTheTurn() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.turnEnded(turn: turn, now: at(1))
        #expect(m.phase == .idle && m.dismissAt != nil)
        // Idempotent: a later done doesn't move the deadline.
        m.event(ev(.done), turn: turn, now: at(2))
        #expect(m.dismissAt == at(1 + ActivityModel.dismissDelay))
    }

    @Test func cancelledHoldGoesIdleAndDismisses() {
        var m = ActivityModel()
        m.holdStarted(now: at(0))
        m.holdEnded(now: at(0.2))
        m.cancelled(now: at(0.3))
        #expect(m.phase == .idle && m.dismissAt == at(0.3 + ActivityModel.dismissDelay))
    }

    /// A hold that ends and never produces a turn or a failure (it
    /// shouldn't happen, but a stuck "thinking" blob would sit on screen
    /// forever) gives up after `stallTimeout`.
    @Test func holdThatNeverSendsGivesUp() {
        var m = ActivityModel()
        m.holdStarted(now: at(0))
        m.holdEnded(now: at(1))
        m.tick(now: at(1 + ActivityModel.stallTimeout - 0.1))
        #expect(m.phase == .thinking)
        m.tick(now: at(1 + ActivityModel.stallTimeout))
        #expect(m.phase == .idle && m.dismissAt != nil)
    }

    @Test func newInteractionCancelsAPendingDismiss() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.done), turn: turn, now: at(1))
        #expect(m.dismissAt != nil)
        m.holdStarted(now: at(2))
        #expect(m.dismissAt == nil)
        m.tick(now: at(10))
        #expect(m.isVisible && m.phase == .listening)
    }

    /// A dismissed HUD comes back for the next interaction, and an approval
    /// in that turn pins it.
    @Test func dismissedHUDReturnsForTheNextTurnsApproval() {
        var m = ActivityModel()
        let t1 = m.turnSent(now: at(0))
        m.event(ev(.done), turn: t1, now: at(0.1))
        m.tick(now: at(5))
        #expect(!m.isVisible)
        let t2 = m.turnSent(now: at(6))
        #expect(m.isVisible)
        m.event(approval("apr_1"), turn: t2, now: at(6.2))
        m.event(ev(.done), turn: t2, now: at(6.3))
        m.tick(now: at(100))
        #expect(m.isVisible && m.isPinned)
    }

    // MARK: level

    @Test func micLevelOnlyCountsWhileListening() {
        var m = ActivityModel()
        m.micLevel(0.8, now: at(0))
        #expect(m.displayLevel(now: at(0)) == 0)
        m.holdStarted(now: at(0))
        m.micLevel(0.8, now: at(0.1))
        #expect(abs(m.displayLevel(now: at(0.1)) - 0.8) < 0.0001)
        m.micLevel(7, now: at(0.2)) // clamped
        #expect(m.displayLevel(now: at(0.2)) == 1)
        m.micLevel(-1, now: at(0.3))
        #expect(m.displayLevel(now: at(0.3)) == 0)
    }

    @Test func ttsLevelOnlyCountsWhileSpeaking() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.ttsLevel(0.5, now: at(0.1))
        // Thinking uses the time pulse, not amplitude.
        m.event(ev(.sentence, text: "x"), turn: turn, now: at(0.2))
        m.speechStarted(now: at(0.3))
        m.ttsLevel(0.6, now: at(0.4))
        #expect(abs(m.displayLevel(now: at(0.4)) - 0.6) < 0.0001)
        m.speechIdle(now: at(1))
        #expect(m.displayLevel(now: at(1)) == 0)
    }

    /// A level that stops updating (a word tick from Apple speech, then
    /// silence) fades rather than freezing the blob open.
    @Test func levelDecaysWithoutUpdates() {
        var m = ActivityModel()
        m.holdStarted(now: at(0))
        m.micLevel(1, now: at(0))
        let later = m.displayLevel(now: at(0.5))
        #expect(later < 0.2)
        #expect(later >= 0)
    }

    @Test func thinkingPulseIsTimeDrivenAndBounded() {
        var m = ActivityModel()
        _ = m.turnSent(now: at(0))
        var seen: Set<Int> = []
        for i in 0..<60 {
            let v = m.displayLevel(now: at(Double(i) * 0.05))
            #expect(v >= 0 && v <= 1)
            seen.insert(Int((v * 10).rounded()))
        }
        #expect(seen.count > 3) // it actually moves
        // And amplitude inputs don't change it while thinking.
        m.micLevel(1, now: at(1))
        m.ttsLevel(1, now: at(1))
        #expect(m.displayLevel(now: at(1)) == ActivityModel.thinkingPulse(elapsed: 1))
    }

    @Test func idleAndNeedsYouHaveNoAmplitude() {
        var m = ActivityModel()
        #expect(m.displayLevel(now: at(0)) == 0)
        let turn = m.turnSent(now: at(0))
        m.event(approval("apr_1"), turn: turn, now: at(0.1))
        m.event(ev(.done), turn: turn, now: at(0.2))
        #expect(m.phase == .needsYou)
        #expect(m.displayLevel(now: at(0.3)) == 0)
    }

    @Test func ignoredKindsChangeNothing() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        let before = m
        m.event(ev(.ack), turn: turn, now: at(0.1))
        m.event(ev(.queued), turn: turn, now: at(0.1))
        m.event(ev(.handoff), turn: turn, now: at(0.1))
        m.event(ev(.unknown("future")), turn: turn, now: at(0.1))
        #expect(m == before)
    }

    // MARK: Esc leaves voice mode (2026-09-26 fixes)

    /// Esc cancels the turn (whose stream then never reports its end) and
    /// the speech: the globe must go at once, not linger on a turn that
    /// will never finish.
    @Test func dismissNowHidesAtOnceMidTurnAndDropsItsLateEvents() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(ev(.sentence, text: "Sure."), turn: turn, now: at(1))
        m.speechStarted(now: at(1))
        m.dismissNow(now: at(2))
        #expect(!m.isVisible)
        #expect(m.phase == .idle)
        #expect(m.dismissAt == nil)
        // A late event from the cancelled turn doesn't bring it back.
        m.event(ev(.delta, text: "more"), turn: turn, now: at(2.1))
        m.event(ev(.done), turn: turn, now: at(2.2))
        #expect(!m.isVisible)
    }

    @Test func dismissNowKeepsAPinnedApproval() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(approval("env_1"), turn: turn, now: at(1))
        m.dismissNow(now: at(2))
        #expect(m.isVisible && m.isPinned)
        #expect(m.phase == .needsYou)
        #expect(m.approvals.map(\.id) == ["env_1"])
    }

    /// The glass tab's close button on an approval only hides it here: the
    /// envelope stays pending in the workspace, but the globe stops pinning.
    @Test func approvalDismissedUnpinsWithoutDeciding() {
        var m = ActivityModel()
        let turn = m.turnSent(now: at(0))
        m.event(approval("env_1"), turn: turn, now: at(1))
        m.event(ev(.done), turn: turn, now: at(1.5))
        m.approvalResolved("env_1", now: at(2))
        #expect(!m.isPinned)
        m.tick(now: at(2 + ActivityModel.dismissDelay))
        #expect(!m.isVisible)
    }

    /// A hold released while the speech models load keeps the globe
    /// thinking past the usual stall guard, until the late transcript.
    @Test func warmingUpTranscriptKeepsThinkingPastTheStallGuard() {
        var m = ActivityModel()
        m.holdStarted(now: at(0))
        m.holdEnded(now: at(1))
        m.transcriptWarmingUp(now: at(1))
        m.tick(now: at(1 + ActivityModel.stallTimeout + 5))
        #expect(m.phase == .thinking && m.isVisible)
        _ = m.turnSent(now: at(30))
        #expect(m.phase == .thinking)
        // Without it, the usual guard applies.
        var n = ActivityModel()
        n.holdStarted(now: at(0))
        n.holdEnded(now: at(1))
        n.tick(now: at(1 + ActivityModel.stallTimeout))
        #expect(n.phase == .idle)
    }
}
