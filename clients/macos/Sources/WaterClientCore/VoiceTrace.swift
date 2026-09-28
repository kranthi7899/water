import Foundation

// Latency instrumentation for a voice turn (Slice V, task V-8;
// docs/slices/V.md §4: "a trace logged per voice turn (key-up -> STT final
// -> request sent -> ack -> first sentence -> first audio -> done), plus a
// --voice-bench mode"). Kept Foundation-only, like HoldToTalk.swift and
// SpeechOutput.swift, so it's testable without AppKit/AVFoundation/
// FluidAudio and shared between the real per-turn trace (AppDelegate,
// Sources/Water) and `--voice-bench`'s synthetic turns (VoiceBench,
// Sources/Water).

/// One voice turn's timestamped checkpoints, in the exact order and naming
/// docs/slices/V.md §4 specifies.
public struct VoiceTurnTrace {
    /// `CaseIterable`'s order is this declaration order, which `intervals()`
    /// relies on to know what "next" means.
    public enum Checkpoint: String, CaseIterable, Hashable {
        /// The push-to-talk hotkey came up (AppDelegate's
        /// `hotkeys.onVoiceKeyUp`) — the start of the trace.
        case keyUp = "key_up"
        /// The on-device recognizer delivered its final transcript
        /// (`VoiceSession.onTranscript`).
        case sttFinal = "stt_final"
        /// This turn's `POST /v1/turns` was sent.
        case requestSent = "request_sent"
        /// The daemon's `ack` event for this turn arrived.
        case ack
        /// The first `sentence` event for this turn arrived.
        case firstSentence = "first_sentence"
        /// The first reply sentence was handed to `SpeechOutput.play` —
        /// hand-off to whatever engine is behind `SentenceSpeechQueue`, not
        /// the engine's own internal "audio actually started" signal.
        /// `SpeechOutput` has no protocol-level hook for that (neither
        /// `AppleSpeechOutput`'s `AVSpeechSynthesizerDelegate` nor
        /// `KokoroSpeaker`'s `AVAudioPlayer` wiring surfaces a "did start"
        /// callback today — see docs/known-gaps.md), so hand-off-to-the-
        /// engine is the honest, engine-agnostic proxy for this checkpoint.
        case firstAudio = "first_audio"
        /// The turn's terminal `done` event arrived.
        case done
    }

    private var timestamps: [Checkpoint: Date] = [:]

    public init() {}

    /// Records `checkpoint` at `at` (defaulting to now) — but only the
    /// first time it's marked; a later mark of the same checkpoint on the
    /// same trace is ignored (a repeated `sentence` event, say, must never
    /// overwrite "first sentence"). Returns whether this call actually
    /// recorded it.
    @discardableResult
    public mutating func mark(_ checkpoint: Checkpoint, at date: Date = Date()) -> Bool {
        guard timestamps[checkpoint] == nil else { return false }
        timestamps[checkpoint] = date
        return true
    }

    public func timestamp(_ checkpoint: Checkpoint) -> Date? { timestamps[checkpoint] }

    /// One entry per pair of *consecutively recorded* checkpoints, in
    /// `Checkpoint.allCases` order. A checkpoint this trace never marked
    /// (e.g. a turn whose daemon never sent `ack`) is skipped rather than
    /// breaking the chain — the interval then spans whichever two
    /// checkpoints actually bracket the gap (e.g. `request_sent ->
    /// first_sentence` directly).
    public func intervals() -> [(from: Checkpoint, to: Checkpoint, ms: Double)] {
        var result: [(Checkpoint, Checkpoint, Double)] = []
        var last: (Checkpoint, Date)?
        for c in Checkpoint.allCases {
            guard let t = timestamps[c] else { continue }
            if let (lastCheckpoint, lastTime) = last {
                result.append((lastCheckpoint, c, t.timeIntervalSince(lastTime) * 1000))
            }
            last = (c, t)
        }
        return result
    }

    /// The span from the earliest checkpoint recorded to the latest, in
    /// milliseconds — nil with fewer than two recorded.
    public var totalMs: Double? {
        let present = Checkpoint.allCases.compactMap { timestamps[$0] }
        guard present.count >= 2, let first = present.first, let last = present.last else { return nil }
        return last.timeIntervalSince(first) * 1000
    }

    /// One structured line for `print`, matching this codebase's only
    /// existing logging convention (plain `print`, space-separated
    /// `key=value` pairs — see SelfTest.swift): e.g. `voice_trace turn=3
    /// key_up->stt_final=12ms stt_final->request_sent=4ms
    /// request_sent->ack=180ms ack->first_sentence=340ms
    /// first_sentence->first_audio=6ms first_audio->done=1900ms
    /// total=2440ms`.
    public func logLine(turnID: String) -> String {
        var parts = ["voice_trace turn=\(turnID)"]
        for (from, to, ms) in intervals() {
            parts.append("\(from.rawValue)->\(to.rawValue)=\(Int(ms.rounded()))ms")
        }
        if let total = totalMs {
            parts.append("total=\(Int(total.rounded()))ms")
        }
        return parts.joined(separator: " ")
    }
}

/// One checkpoint-to-checkpoint interval's aggregate stats across many
/// voice turns.
public struct VoiceLatencyStats: Equatable {
    public let count: Int
    public let minMs: Double
    public let medianMs: Double
    public let p90Ms: Double
    public let maxMs: Double

    /// nil for an empty sample. `median`/`p90` use linear interpolation
    /// between the two nearest ranks (the common "numpy default" method) —
    /// simple, dependency-free, and the interpolation choice doesn't matter
    /// much at the sample sizes `--voice-bench` targets (N>=30).
    public init?(_ valuesMs: [Double]) {
        guard !valuesMs.isEmpty else { return nil }
        let sorted = valuesMs.sorted()
        count = sorted.count
        minMs = sorted.first!
        maxMs = sorted.last!
        medianMs = Self.percentile(sorted, 0.5)
        p90Ms = Self.percentile(sorted, 0.9)
    }

    private static func percentile(_ sorted: [Double], _ p: Double) -> Double {
        guard sorted.count > 1 else { return sorted[0] }
        let rank = p * Double(sorted.count - 1)
        let lower = Int(rank.rounded(.down))
        let upper = Int(rank.rounded(.up))
        guard lower != upper else { return sorted[lower] }
        let frac = rank - Double(lower)
        return sorted[lower] + (sorted[upper] - sorted[lower]) * frac
    }
}

/// Turns a batch of per-turn traces into one row per interval label
/// ("key_up->stt_final", etc.), preserving `Checkpoint.allCases` order.
/// Traces that skip a checkpoint (see `intervals()`) contribute to whatever
/// label their actual interval spans — a turn missing `ack` feeds
/// `request_sent->first_sentence` instead of `request_sent->ack` and
/// `ack->first_sentence`, never a mismatched or padded value under either.
public enum VoiceTurnAggregator {
    public static func aggregate(_ traces: [VoiceTurnTrace]) -> [(label: String, stats: VoiceLatencyStats)] {
        var buckets: [String: [Double]] = [:]
        var order: [String] = []
        for trace in traces {
            for (from, to, ms) in trace.intervals() {
                let label = "\(from.rawValue)->\(to.rawValue)"
                if buckets[label] == nil {
                    buckets[label] = []
                    order.append(label)
                }
                buckets[label]?.append(ms)
            }
        }
        return order.compactMap { label in
            guard let stats = VoiceLatencyStats(buckets[label] ?? []) else { return nil }
            return (label, stats)
        }
    }
}
