import Foundation

/// GET /v1/voice/profile's JSON (internal/gateway/router.go's
/// handleVoiceProfile, R-15): `{"name":"...", "handoff":["..."],
/// "tts":{"voice":"...", "rate_wpm":185}}`. Mirrors the Go CLI's own
/// VoiceProfileResult (internal/cli/daemonclient.go) exactly, so every
/// client sounds the same regardless of which tier answered (Design's
/// one-voice contract, §8.4).
public struct VoiceProfile: Decodable, Equatable {
    public struct TTS: Decodable, Equatable {
        /// A system voice name (the same name space `say -v`/
        /// internal/voice.OS.SetVoice already uses) — empty means "leave
        /// whatever the client already resolved alone."
        public var voice: String
        /// Approximate words per minute — the same number `say -r` takes
        /// directly. Non-positive means "leave the resolved rate alone."
        public var rateWPM: Int

        enum CodingKeys: String, CodingKey {
            case voice
            case rateWPM = "rate_wpm"
        }

        public init(voice: String, rateWPM: Int) {
            self.voice = voice
            self.rateWPM = rateWPM
        }
    }

    public var name: String
    /// Handoff phrases (`style.yaml`'s `voice.handoff`), chosen by the
    /// daemon itself for spoken handoffs; a macOS client has no use for
    /// these yet (it decodes and ignores the `handoff` event kind, see
    /// Events.swift) but the field is kept so the fixture matches the real
    /// server response byte-for-byte.
    public var handoff: [String]
    public var tts: TTS

    public init(name: String, handoff: [String], tts: TTS) {
        self.name = name
        self.handoff = handoff
        self.tts = tts
    }
}

extension UnixSocketClient {
    /// GET /v1/voice/profile. Non-200 (e.g. 404 from a twin with no
    /// style.yaml loaded, or a very old daemon predating R-15) throws
    /// `.http`, same convention as this file's other calls — callers that
    /// want "best effort, fall back silently" (R-27's launch-time fetch)
    /// should wrap this in `try?`.
    public func fetchVoiceProfile(token: String) throws -> VoiceProfile {
        let r = try send(HTTPRequest(method: "GET", path: "/v1/voice/profile", token: token))
        guard r.status == 200 else {
            throw WaterClientError.http(status: r.status, body: String(decoding: r.body, as: UTF8.self))
        }
        return try JSONDecoder().decode(VoiceProfile.self, from: r.body)
    }
}

/// Maps `style.yaml`'s `tts.rate_wpm` (an approximate words-per-minute
/// figure — the same number `internal/voice.OS`'s `say -r` takes directly)
/// onto `AVSpeechUtterance.rate`'s normalized scale
/// (`AVSpeechUtteranceMinimumSpeechRate`...`MaximumSpeechRate`, 0.0...1.0).
/// AVFoundation documents no official wpm<->rate conversion, so this picks
/// two anchor points spanning a realistic push-to-talk speaking range —
/// 90 wpm (slow) at the minimum rate, 280 wpm (fast) at the maximum rate —
/// and interpolates linearly between them, clamped at the edges. Those
/// anchors were chosen deliberately: they place `style.yaml`'s own default
/// (185 wpm) almost exactly on `AVSpeechUtteranceDefaultSpeechRate` (0.5),
/// since (185 - 90) / (280 - 90) = 95 / 190 = 0.5 exactly — so a twin that
/// never overrides the default rate sounds like AVSpeechUtterance's own
/// out-of-the-box default, not a client-side reinterpretation of it.
///
/// Kept in WaterClientCore (not the AVFoundation-importing Water target) so
/// it's testable with swift-testing alone; `minRate`/`maxRate` are plain
/// Doubles equal to AVFoundation's real constants, duplicated here rather
/// than importing AVFoundation just for two numbers.
public enum TTSRateMapping {
    public static let minWPM: Double = 90
    public static let maxWPM: Double = 280
    /// == AVSpeechUtteranceMinimumSpeechRate.
    public static let minRate: Double = 0.0
    /// == AVSpeechUtteranceMaximumSpeechRate.
    public static let maxRate: Double = 1.0

    /// `wpm` is clamped to `minWPM...maxWPM` before interpolating, so a
    /// twin's own extreme override never produces an out-of-range
    /// `AVSpeechUtterance.rate`.
    public static func rate(forWPM wpm: Int) -> Double {
        let w = min(max(Double(wpm), minWPM), maxWPM)
        let frac = (w - minWPM) / (maxWPM - minWPM)
        return minRate + frac * (maxRate - minRate)
    }
}
