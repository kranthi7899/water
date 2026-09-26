import Foundation
import Testing
@testable import WaterClientCore

/// A test double for `SpeechOutput`, in the same style as `FakeCapture`/
/// `FakeScheduler` in HoldToTalkTests.swift: everything runs synchronously
/// on the test's own thread, recording each `prepare`/`play` call and
/// letting the test resolve them by hand, in whatever order a real engine's
/// timing would produce.
final class FakeSpeechOutput: SpeechOutput {
    struct Prepare { let text: String; let ready: (SpeechToken?) -> Void }
    struct Play { let text: String; let done: () -> Void }

    private(set) var prepares: [Prepare] = []
    private(set) var plays: [Play] = []
    private(set) var stopCurrentCalls = 0

    func prepare(_ text: String, ready: @escaping (SpeechToken?) -> Void) {
        prepares.append(Prepare(text: text, ready: ready))
    }

    func play(_ token: SpeechToken, done: @escaping () -> Void) {
        plays.append(Play(text: token as! String, done: done))
    }

    func stopCurrent() { stopCurrentCalls += 1 }

    /// Resolves prepare call `n` (0-based): succeeding hands back the
    /// sentence's own text as its token (good enough — these tests only
    /// ever check "which sentence played", not token identity), failing
    /// hands back `nil`.
    func resolvePrepare(_ n: Int, succeeds: Bool = true) {
        let p = prepares[n]
        p.ready(succeeds ? p.text : nil)
    }

    func finishPlay(_ n: Int) { plays[n].done() }

    var playedTexts: [String] { plays.map(\.text) }
}

@Suite struct SentenceSpeechQueueTests {
    @Test func singleSentencePlaysAfterItsPrepareResolves() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)

        q.enqueue("hello there")
        #expect(out.prepares.count == 1 && out.prepares[0].text == "hello there")
        #expect(out.plays.isEmpty) // not played until prepare resolves

        out.resolvePrepare(0)
        #expect(out.playedTexts == ["hello there"])

        out.finishPlay(0)
        // Idle again: nothing left prepared or preparing.
        #expect(out.prepares.count == 1 && out.plays.count == 1)
    }

    @Test func blankSentencesAreIgnored() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)
        q.enqueue("")
        q.enqueue("   \n\t")
        #expect(out.prepares.isEmpty)
    }

    /// The core overlap proof: the second sentence starts synthesizing
    /// while the first is still playing (not after it finishes), and the
    /// handoff to it happens the instant the first finishes, with no new
    /// prepare call needed at that moment.
    @Test func secondSentenceOverlapsFirstsPlaybackAndHandsOffWithNoGap() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)

        q.enqueue("one")
        out.resolvePrepare(0) // "one" starts playing
        #expect(out.playedTexts == ["one"])

        q.enqueue("two")
        // "two" must already be synthesizing — before "one" has finished.
        #expect(out.prepares.count == 2 && out.prepares[1].text == "two")
        #expect(out.plays.count == 1) // not played yet: "one" is still going

        out.resolvePrepare(1) // "two"'s synthesis finishes while "one" still plays
        #expect(out.plays.count == 1) // still just sitting ready, not played early

        out.finishPlay(0) // "one" ends
        // "two" plays immediately: no fresh prepare call was needed for it.
        #expect(out.playedTexts == ["one", "two"])
        #expect(out.prepares.count == 2)

        out.finishPlay(1)
        #expect(out.prepares.count == 2 && out.plays.count == 2) // idle
    }

    /// Lookahead is capped at one sentence deep: a third sentence enqueued
    /// while the second is still preparing must not start a third
    /// concurrent prepare call.
    @Test func lookaheadStaysOneSentenceDeep() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)

        q.enqueue("a")
        out.resolvePrepare(0) // "a" playing
        q.enqueue("b")
        #expect(out.prepares.count == 2) // "b" preparing as lookahead

        q.enqueue("c")
        // "c" must wait — only one sentence of lookahead at a time.
        #expect(out.prepares.count == 2)

        out.resolvePrepare(1) // "b" ready, "a" still playing
        #expect(out.prepares.count == 2) // "c" still hasn't started

        out.finishPlay(0) // "a" ends, "b" promoted to play with no gap
        #expect(out.playedTexts == ["a", "b"])
        // Only now does "c" start preparing, as the new lookahead.
        #expect(out.prepares.count == 3 && out.prepares[2].text == "c")

        out.resolvePrepare(2)
        out.finishPlay(1)
        #expect(out.playedTexts == ["a", "b", "c"])
        out.finishPlay(2)
    }

    /// Synthesis can be slower than playback: if the lookahead isn't ready
    /// by the time the current sentence finishes, there's a real gap — the
    /// queue plays it the instant it does become ready instead.
    @Test func playsLookaheadAssoonAsReadyWhenSlowerThanPlayback() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)

        q.enqueue("a")
        out.resolvePrepare(0)
        q.enqueue("b")
        #expect(out.prepares.count == 2)

        out.finishPlay(0) // "a" ends before "b" is ready: nothing to play yet
        #expect(out.plays.count == 1)

        out.resolvePrepare(1) // "b" finally ready
        #expect(out.playedTexts == ["a", "b"])
        out.finishPlay(1)
    }

    /// A sentence whose synthesis fails is dropped, whether it was the
    /// current sentence or the lookahead — never crashes, never blocks the
    /// rest of the queue.
    @Test func failedSynthesisSkipsThatSentence() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)

        // Failure as the very first ("current") sentence: falls through to
        // the next one already queued behind it.
        q.enqueue("bad")
        q.enqueue("good")
        #expect(out.prepares.count == 1)
        out.resolvePrepare(0, succeeds: false)
        #expect(out.prepares.count == 2 && out.prepares[1].text == "good")
        out.resolvePrepare(1)
        #expect(out.playedTexts == ["good"])
        out.finishPlay(0)

        // Failure as the lookahead, with a follow-up sentence already
        // queued behind it: the queue tries that one next as the new
        // lookahead, without disturbing what's currently playing.
        q.enqueue("x")
        out.resolvePrepare(2) // "x" playing
        q.enqueue("bad2")
        q.enqueue("y")
        #expect(out.prepares.count == 4) // "bad2" preparing, "y" still waiting
        out.resolvePrepare(3, succeeds: false)
        #expect(out.prepares.count == 5 && out.prepares[4].text == "y")
        out.resolvePrepare(4)
        out.finishPlay(1) // "x" ends
        #expect(out.playedTexts == ["good", "x", "y"])
        out.finishPlay(2)
    }

    /// A sentence that fails with nothing queued behind it leaves the
    /// queue cleanly idle, ready to take more work later.
    @Test func failedSynthesisWithNothingElseQueuedGoesIdle() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)
        q.enqueue("only")
        out.resolvePrepare(0, succeeds: false)
        #expect(out.plays.isEmpty)

        q.enqueue("later")
        #expect(out.prepares.count == 2 && out.prepares[1].text == "later")
        out.resolvePrepare(1)
        #expect(out.playedTexts == ["later"])
    }

    /// Barge-in: stops whatever is playing right now and drops everything
    /// queued or mid-synthesis, and a callback from before the stop that
    /// lands afterward (a late prepare or a late done) is ignored rather
    /// than starting or finishing anything.
    @Test func stopCutsCurrentAndDropsQueuedAndIgnoresStaleCallbacks() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)

        q.enqueue("a")
        out.resolvePrepare(0) // "a" playing
        q.enqueue("b")
        #expect(out.prepares.count == 2) // "b" preparing

        q.stop()
        #expect(out.stopCurrentCalls == 1)

        // "b"'s prepare resolving after the stop must not start a play.
        out.resolvePrepare(1)
        #expect(out.plays.count == 1)

        // "a"'s done firing after the stop must not be treated as a
        // finish that promotes anything.
        out.finishPlay(0)
        #expect(out.plays.count == 1)

        // The queue is fully usable again afterward.
        q.enqueue("fresh")
        #expect(out.prepares.count == 3 && out.prepares[2].text == "fresh")
        out.resolvePrepare(2)
        #expect(out.playedTexts == ["a", "fresh"])
    }

    @Test func stopWithNothingInFlightIsSafeAndLeavesQueueUsable() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)
        q.stop()
        #expect(out.stopCurrentCalls == 1)

        q.enqueue("after a stop")
        #expect(out.prepares.count == 1)
        out.resolvePrepare(0)
        #expect(out.playedTexts == ["after a stop"])
    }

    /// `onWillPlay` (V-8's "first audio" hook): fires exactly once per
    /// sentence actually played — including the overlapped hand-off, where
    /// no fresh `prepare` call happens — and not at all for a sentence
    /// whose synthesis failed.
    @Test func onWillPlayFiresOncePerSentencePlayedIncludingOverlapHandoff() {
        let out = FakeSpeechOutput()
        let q = SentenceSpeechQueue(output: out)
        var callCount = 0
        q.onWillPlay = { callCount += 1 }

        q.enqueue("one")
        out.resolvePrepare(0, succeeds: false) // never played: no call
        #expect(callCount == 0)

        q.enqueue("two")
        out.resolvePrepare(1)
        #expect(callCount == 1) // "two" started playing

        q.enqueue("three")
        out.resolvePrepare(2) // "three" ready as lookahead, not played yet
        #expect(callCount == 1)

        out.finishPlay(0) // "two" ends, "three" hands off with no new prepare
        #expect(callCount == 2)
        out.finishPlay(1)
    }
}
