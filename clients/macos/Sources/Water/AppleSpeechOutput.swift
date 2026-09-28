import AVFoundation
import Foundation
import WaterClientCore

/// `SpeechOutput`'s first conformer: the exact `AVSpeechSynthesizer` logic
/// that used to live directly on `VoiceController`, moved behind the
/// protocol so `SentenceSpeechQueue` can drive it — behavior is unchanged
/// from before this refactor (same utterance construction, same voice/rate
/// application, same `stopSpeaking(at: .immediate)`).
///
/// `AVSpeechSynthesizer` only exposes "synthesize and play" as one call, so
/// there's nothing useful to do ahead of time: `prepare` just hands the
/// text straight back as the token, and all the real work happens in
/// `play`. A conformer that can synthesize ahead of playback (e.g. a future
/// Kokoro-backed `KokoroSpeaker`, using FluidAudio's `KokoroAneManager` —
/// see docs/slices/V.md §4 and the `isBnnsCrashProneOS` note on that type
/// for a real Apple BNNS bug on macOS 26.4-26.5 that doesn't affect this
/// machine's 15.6.1) would do the actual synthesis in `prepare` instead.
///
/// `SentenceSpeechQueue` guarantees only one `play` is ever in flight at a
/// time, so a single `pendingDone` (rather than a per-utterance table) is
/// enough to route `AVSpeechSynthesizerDelegate`'s callback back to the
/// right completion.
final class AppleSpeechOutput: NSObject, SpeechOutput, SpeechLevelSource {
    // `nonisolated(unsafe)`: every touch of `synth` in this file is on the
    // main thread (SpeechOutput's whole contract is main-thread-only, see
    // the protocol doc comment), same as the plain AVSpeechSynthesizer this
    // replaced on VoiceController before this refactor — this only silences
    // the compiler's generic "NSObject subclass with a non-Sendable stored
    // property" concurrency warning, it changes no actual behavior.
    private nonisolated(unsafe) let synth = AVSpeechSynthesizer()
    private var pendingDone: (() -> Void)?
    /// Set by `applyVoiceProfile`, from `GET /v1/voice/profile` (R-27).
    /// Nil leaves `play` on `AVSpeechUtterance`'s own defaults, exactly as
    /// before this profile existed.
    private var ttsVoice: AVSpeechSynthesisVoice?
    private var ttsRate: Float?
    /// Main thread, once per spoken word: a value from a fixed, gentle
    /// wave (`AudioLevel.wordPulse`). AVSpeechSynthesizer has no audio
    /// meter, so this shows *that* it's speaking, not how loud — a known
    /// limitation (V-hud), unlike KokoroSpeaker's real meter.
    var onLevel: ((Float) -> Void)?
    private var wordIndex = 0

    override init() {
        super.init()
        synth.delegate = self
    }

    func prepare(_ text: String, ready: @escaping (SpeechToken?) -> Void) {
        ready(text)
    }

    /// Speaks one reply sentence's already-"prepared" text (see the type
    /// doc comment on why `prepare` above does nothing).
    func play(_ token: SpeechToken, done: @escaping () -> Void) {
        guard let text = token as? String, !text.isEmpty else { return done() }
        let utterance = AVSpeechUtterance(string: text)
        if let ttsVoice { utterance.voice = ttsVoice }
        if let ttsRate { utterance.rate = ttsRate }
        pendingDone = done
        wordIndex = 0
        synth.speak(utterance)
    }

    /// Stops the current utterance and drops every queued one.
    func stopCurrent() {
        pendingDone = nil
        synth.stopSpeaking(at: .immediate)
    }

    /// Applies `GET /v1/voice/profile` (R-27, fetched once at launch): an
    /// empty `tts.voice`, an unmatched voice name, or a non-positive
    /// `tts.rate_wpm` leaves the corresponding setting untouched, so `play`
    /// falls back to `AVSpeechUtterance`'s own defaults exactly as it did
    /// before this profile existed. `tts.voice` is matched
    /// case-insensitively against `AVSpeechSynthesisVoice.speechVoices()`'s
    /// `.name` — the same voice-name space `say -v`/`internal/voice.OS`
    /// already uses, since the daemon serves one TTS profile to every
    /// client. Never throws; meant to be called best-effort.
    func applyVoiceProfile(_ profile: VoiceProfile) {
        if !profile.tts.voice.isEmpty,
           let match = AVSpeechSynthesisVoice.speechVoices().first(where: {
               $0.name.compare(profile.tts.voice, options: .caseInsensitive) == .orderedSame
           }) {
            ttsVoice = match
        }
        if profile.tts.rateWPM > 0 {
            ttsRate = Float(TTSRateMapping.rate(forWPM: profile.tts.rateWPM))
        }
    }
}

extension AppleSpeechOutput: AVSpeechSynthesizerDelegate {
    func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, willSpeakRangeOfSpeechString characterRange: NSRange,
                           utterance: AVSpeechUtterance) {
        guard let onLevel else { return }
        let v = AudioLevel.wordPulse(wordIndex)
        wordIndex += 1
        // The delegate's queue isn't documented as main; SpeechOutput's
        // contract is main-thread-only, so hop if needed.
        if Thread.isMainThread { onLevel(v) } else { DispatchQueue.main.async { onLevel(v) } }
    }

    func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didFinish utterance: AVSpeechUtterance) {
        let done = pendingDone
        pendingDone = nil
        done?()
    }

    func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didCancel utterance: AVSpeechUtterance) {
        // No `done()` here: a cancel only ever comes from `stopCurrent`,
        // which already cleared `pendingDone` and whose caller
        // (`SentenceSpeechQueue.stop`) already reset its own state without
        // waiting for this callback.
        pendingDone = nil
    }
}
