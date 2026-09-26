import Foundation

/// Pure amplitude helpers for the Activity HUD's blob (V-hud): the mic
/// tap's RMS, AVAudioPlayer's dB meter, and the fixed wave used for Apple
/// speech (which has no meter). Everything returns 0...1.
public enum AudioLevel {
    /// The quietest level that still registers; anything below is 0.
    public static let floorDB: Float = -50

    /// Root mean square of `count` samples starting at `samples`. 0 for an
    /// empty buffer; non-finite samples count as silence.
    public static func rms(_ samples: UnsafePointer<Float>, count: Int) -> Float {
        guard count > 0 else { return 0 }
        var sum: Double = 0
        for i in 0..<count {
            let s = Double(samples[i])
            if s.isFinite { sum += s * s }
        }
        return Float((sum / Double(count)).squareRoot())
    }

    public static func rms(_ samples: [Float]) -> Float {
        samples.withUnsafeBufferPointer { buf in
            guard let base = buf.baseAddress else { return 0 }
            return rms(base, count: buf.count)
        }
    }

    /// A linear RMS (1.0 = full scale) as a display level: decibels mapped
    /// from `floorDB`...0 onto 0...1, so speech at a normal distance moves
    /// the blob visibly instead of barely at all.
    public static func level(rms: Float) -> Float {
        guard rms.isFinite, rms > 0 else { return 0 }
        return level(decibels: 20 * log10(rms))
    }

    /// A dBFS reading (AVAudioPlayer.averagePower: -160...0) as 0...1.
    public static func level(decibels db: Float) -> Float {
        guard db.isFinite else { return 0 }
        return min(1, max(0, (db - floorDB) / -floorDB))
    }

    /// Apple speech has no meter (AVSpeechSynthesizer exposes none), so
    /// each spoken word (willSpeakRangeOfSpeechString) gets the next value
    /// of a fixed, gentle pattern. It shows the voice is speaking, not how
    /// loud it is.
    public static let wordWave: [Float] = [0.45, 0.62, 0.38, 0.55, 0.7, 0.42]

    public static func wordPulse(_ index: Int) -> Float {
        wordWave[((index % wordWave.count) + wordWave.count) % wordWave.count]
    }
}

/// Lets through at most one value per `interval` seconds (about 30Hz by
/// default): the mic tap delivers ~40+ buffers a second, and the HUD only
/// needs display rate. Not thread-safe; each audio thread owns one.
public struct LevelThrottle {
    public let interval: TimeInterval
    private var last: TimeInterval?

    public init(interval: TimeInterval = 1.0 / 30) {
        self.interval = interval
    }

    /// True when a value at time `now` (seconds, any monotonic clock)
    /// should be emitted.
    public mutating func allow(now: TimeInterval) -> Bool {
        if let last, now - last < interval, now >= last { return false }
        last = now
        return true
    }
}
