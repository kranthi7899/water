import Foundation
import Testing
@testable import WaterClientCore

/// `VoiceTurnTrace`/`VoiceLatencyStats`/`VoiceTurnAggregator` (V-8): the
/// pure trace/stats computation behind the real per-turn log line
/// (AppDelegate) and `--voice-bench`'s aggregate report (VoiceBench).
/// Everything here uses fixed `Date`s constructed by hand rather than the
/// real clock, so the numbers are exact and never flaky.
// `timeIntervalSinceReferenceDate: 0` (not `timeIntervalSince1970:`, whose
// ~1.7e9-second magnitude makes the rounding noise below far worse) keeps
// every timestamp's magnitude near zero. A little sub-millisecond noise
// still survives the sec<->ms round-trip through `Date`'s own arithmetic
// (ordinary double rounding, not a bug in `VoiceTurnTrace`), so `msRounded`
// below compares at millisecond precision — exactly what `logLine` itself
// reports.
private let t0 = Date(timeIntervalSinceReferenceDate: 0)
private func at(_ ms: Double) -> Date { t0.addingTimeInterval(ms / 1000) }
private func msRounded(_ intervals: [(from: VoiceTurnTrace.Checkpoint, to: VoiceTurnTrace.Checkpoint, ms: Double)]) -> [Double] {
    intervals.map { $0.ms.rounded() }
}

@Suite struct VoiceTraceTests {
    @Test func intervalsChainConsecutiveMarksInDeclarationOrder() {
        var trace = VoiceTurnTrace()
        trace.mark(.keyUp, at: at(0))
        trace.mark(.sttFinal, at: at(10))
        trace.mark(.requestSent, at: at(15))
        trace.mark(.ack, at: at(200))
        trace.mark(.firstSentence, at: at(540))
        trace.mark(.firstAudio, at: at(548))
        trace.mark(.done, at: at(2400))

        let intervals = trace.intervals()
        #expect(intervals.map(\.from) == [.keyUp, .sttFinal, .requestSent, .ack, .firstSentence, .firstAudio])
        #expect(intervals.map(\.to) == [.sttFinal, .requestSent, .ack, .firstSentence, .firstAudio, .done])
        #expect(msRounded(intervals) == [10, 5, 185, 340, 8, 1852])
        #expect(trace.totalMs?.rounded() == 2400)
    }

    @Test func missingCheckpointsAreSkippedNotPadded() {
        // No `ack` this turn (the daemon didn't send one) and no `sttFinal`
        // (a toggle-started capture, say) — `intervals()` must bridge
        // straight from the checkpoints that *were* marked, never insert a
        // zero or a bogus pairing through the gap.
        var trace = VoiceTurnTrace()
        trace.mark(.keyUp, at: at(0))
        trace.mark(.requestSent, at: at(20))
        trace.mark(.firstSentence, at: at(300))
        trace.mark(.done, at: at(900))

        let intervals = trace.intervals()
        #expect(intervals.map { "\($0.from.rawValue)->\($0.to.rawValue)" } ==
                 ["key_up->request_sent", "request_sent->first_sentence", "first_sentence->done"])
        #expect(msRounded(intervals) == [20, 280, 600])
    }

    @Test func markIsIdempotentFirstWriteWins() {
        var trace = VoiceTurnTrace()
        #expect(trace.mark(.firstSentence, at: at(100)) == true)
        // A second `sentence` event landing later must not move "first
        // sentence" — the call returns false and the original timestamp
        // stands.
        #expect(trace.mark(.firstSentence, at: at(999)) == false)
        #expect(trace.timestamp(.firstSentence) == at(100))
    }

    @Test func totalMsIsNilWithFewerThanTwoCheckpoints() {
        var trace = VoiceTurnTrace()
        #expect(trace.totalMs == nil)
        trace.mark(.keyUp, at: at(0))
        #expect(trace.totalMs == nil)
        trace.mark(.done, at: at(50))
        #expect(trace.totalMs == 50)
    }

    @Test func logLineFormatsEveryIntervalAndTheTotal() {
        var trace = VoiceTurnTrace()
        trace.mark(.keyUp, at: at(0))
        trace.mark(.sttFinal, at: at(12))
        trace.mark(.done, at: at(512))
        #expect(trace.logLine(turnID: "7") ==
                 "voice_trace turn=7 key_up->stt_final=12ms stt_final->done=500ms total=512ms")
    }

    @Test func logLineWithNothingMarkedHasNoIntervalsOrTotal() {
        let trace = VoiceTurnTrace()
        #expect(trace.logLine(turnID: "1") == "voice_trace turn=1")
    }

    // MARK: - VoiceLatencyStats

    @Test func statsComputeMinMedianP90Max() {
        // 5 values: median is the middle one; p90 (linear interpolation,
        // rank = 0.9 * 4 = 3.6) sits 60% of the way from index 3 to index 4.
        let stats = VoiceLatencyStats([50, 10, 30, 20, 40])!
        #expect(stats.count == 5)
        #expect(stats.minMs == 10)
        #expect(stats.medianMs == 30)
        #expect(stats.p90Ms == 46)
        #expect(stats.maxMs == 50)
    }

    @Test func statsWithOneValueAreAllThatValue() {
        let stats = VoiceLatencyStats([42])!
        #expect(stats.minMs == 42 && stats.medianMs == 42 && stats.p90Ms == 42 && stats.maxMs == 42)
    }

    @Test func statsAreNilForAnEmptySample() {
        #expect(VoiceLatencyStats([]) == nil)
    }

    // MARK: - VoiceTurnAggregator

    @Test func aggregatorBucketsByLabelAcrossTraces() {
        var a = VoiceTurnTrace()
        a.mark(.keyUp, at: at(0))
        a.mark(.sttFinal, at: at(10))

        var b = VoiceTurnTrace()
        b.mark(.keyUp, at: at(0))
        b.mark(.sttFinal, at: at(30))

        let agg = VoiceTurnAggregator.aggregate([a, b])
        #expect(agg.count == 1)
        #expect(agg[0].label == "key_up->stt_final")
        #expect(agg[0].stats.minMs == 10)
        #expect(agg[0].stats.maxMs == 30)
        #expect(agg[0].stats.count == 2)
    }

    @Test func aggregatorNeverMixesDifferentlySpannedIntervalsUnderOneLabel() {
        // `a` has no `ack`, so its one interval is request_sent->first_sentence.
        // `b` has `ack`, so it contributes request_sent->ack and
        // ack->first_sentence instead — three distinct labels, never one
        // label with mismatched or padded samples.
        var a = VoiceTurnTrace()
        a.mark(.requestSent, at: at(0))
        a.mark(.firstSentence, at: at(100))

        var b = VoiceTurnTrace()
        b.mark(.requestSent, at: at(0))
        b.mark(.ack, at: at(20))
        b.mark(.firstSentence, at: at(120))

        let agg = Dictionary(uniqueKeysWithValues: VoiceTurnAggregator.aggregate([a, b]).map { ($0.label, $0.stats) })
        #expect(agg["request_sent->first_sentence"]?.count == 1)
        #expect(agg["request_sent->first_sentence"]?.minMs == 100)
        #expect(agg["request_sent->ack"]?.count == 1)
        #expect(agg["request_sent->ack"]?.minMs == 20)
        #expect(agg["ack->first_sentence"]?.count == 1)
        #expect(agg["ack->first_sentence"]?.minMs.rounded() == 100)
    }

    @Test func aggregatorPreservesCheckpointOrderAndSkipsEmptyLabels() {
        var trace = VoiceTurnTrace()
        trace.mark(.firstSentence, at: at(0))
        trace.mark(.firstAudio, at: at(5))
        trace.mark(.done, at: at(400))

        let labels = VoiceTurnAggregator.aggregate([trace]).map(\.label)
        #expect(labels == ["first_sentence->first_audio", "first_audio->done"])
    }
}
