import AVFoundation
import FluidAudio
import Foundation
import WaterClientCore

/// `SpeechOutput`'s second conformer: Kokoro-82M via FluidAudio's
/// `KokoroAneManager` (Core ML, resident on the Neural Engine — see
/// docs/slices/V.md §4). Unlike `AppleSpeechOutput`'s `AVSpeechSynthesizer`
/// (synthesize-and-play as one call), `KokoroAneManager.synthesize` can run
/// well ahead of playback, so the real work happens in `prepare` — `play`
/// only starts an `AVAudioPlayer` on the WAV `Data` `prepare` already
/// produced.
///
/// This type is *not* engine selection: it assumes it has already been
/// chosen and is simply instantiated and used. Choosing between this and
/// `AppleSpeechOutput` (Apple Silicon + models ready vs. Intel/models
/// missing), the one-time download-consent dialog, and falling back to
/// Apple Speech permanently on any Kokoro error are all V-6's job. Left to
/// its own devices, a `prepare` failure here (model load or synthesis) just
/// hands back `nil`, which `SentenceSpeechQueue` already treats as "skip
/// this sentence" — never a crash, never a silent retry loop — so this
/// conformer is safe to use standalone even before V-6 exists.
final class KokoroSpeaker: NSObject, SpeechOutput, SpeechLevelSource {
    /// Actor-isolated so two sentences preparing at once (`SentenceSpeechQueue`
    /// keeps one sentence of lookahead synthesizing while the current one
    /// plays, i.e. two concurrent `prepare` calls) can't race on "is a load
    /// already in flight" — actor isolation serializes the check-then-set
    /// around the one `Task` that actually calls `initialize()`, and lets a
    /// second, overlapping caller just await that same task instead of
    /// starting a redundant load.
    private actor InitGate {
        private var task: Task<Void, Error>?

        func ensureInitialized(_ manager: KokoroAneManager) async throws {
            if let task {
                try await task.value
                return
            }
            let started = Task { try await manager.initialize() }
            task = started
            do {
                try await started.value
            } catch {
                // Let the next `prepare` retry rather than permanently
                // remembering a transient failure (e.g. a download blip).
                task = nil
                throw error
            }
        }
    }

    private let manager: KokoroAneManager
    private let initGate = InitGate()
    /// From `VoiceProfile.TTS.kokoroVoice` (style.yaml's optional
    /// `kokoro_voice` field, R-... / V-7) when the daemon set one; `nil`
    /// leaves `KokoroAneManager` to use its own built-in default voice for
    /// this variant (`af_heart` for English — see `KokoroAneManager.init`'s
    /// `defaultVoice` parameter). `defaultVoice` is a private var on the
    /// manager with no public getter, so there is nothing for this type to
    /// read back even if it wanted to; `nil` is exactly the "let Kokoro
    /// pick" signal `synthesize(voice:)` already documents, not a
    /// workaround for a missing accessor.
    ///
    /// `var`, not `let`: `applyVoiceProfile` (below) updates it after
    /// construction, the same way `AppleSpeechOutput.applyVoiceProfile`
    /// updates its own `ttsVoice` — AppDelegate's `fetchVoiceProfile` fetches
    /// the daemon's profile asynchronously, after both conformers already
    /// exist.
    private var voice: String?

    /// `player`/`pendingDone` are touched only from the main thread: `play`
    /// and `stopCurrent` are called there by `SentenceSpeechQueue` (the
    /// protocol's whole contract is main-thread-only), and
    /// `AVAudioPlayerDelegate`'s callbacks land on the same run loop that
    /// started playback. Mirrors `AppleSpeechOutput`'s single `pendingDone`
    /// (no per-utterance table needed, since only one `play` is ever in
    /// flight).
    private var player: AVAudioPlayer?
    private var pendingDone: (() -> Void)?

    /// Main thread, ~30Hz while a sentence plays: its loudness, 0...1, from
    /// `AVAudioPlayer`'s own meter (`averagePower`, V-hud's blob).
    var onLevel: ((Float) -> Void)?
    private var meterTimer: Timer?

    init(voice: String? = nil, manager: KokoroAneManager = SpeechModels.makeKokoroManager()) {
        self.voice = voice
        self.manager = manager
    }

    /// Synthesizes `text` ahead of playback. Model load happens at most
    /// once (see `InitGate`); a load or synthesis failure resolves `ready`
    /// with `nil` rather than throwing further — see the type doc comment.
    ///
    /// `KokoroAneManager.initialize` already logs its own one-line warning
    /// when it detects the known Apple BNNS crash bug on affected OS
    /// builds (`isBnnsCrashProneOS`, macOS 26.4–26.5 / iOS 26.4+); this
    /// machine's 15.6.1 isn't in that range, so nothing further is done
    /// about it here, but it's worth remembering once engine selection
    /// (V-6) starts deciding whether to route to Kokoro at all after a
    /// future OS upgrade.
    func prepare(_ text: String, ready: @escaping (SpeechToken?) -> Void) {
        let manager = self.manager
        let initGate = self.initGate
        let voice = self.voice
        Task {
            do {
                try await initGate.ensureInitialized(manager)
                let data = try await manager.synthesize(text: text, voice: voice)
                await MainActor.run { ready(data) }
            } catch {
                await MainActor.run { ready(nil) }
            }
        }
    }

    /// `synthesize` returns one-shot 24 kHz mono 16-bit PCM *WAV* data
    /// (`KokoroAneManager.synthesize`'s doc comment) — a self-describing
    /// container `AVAudioPlayer(data:)` plays directly, unlike raw PCM
    /// samples, which would need an explicit `AVAudioFormat` and an
    /// `AVAudioEngine` player node. There's no existing precedent in this
    /// codebase for playing synthesized audio (`AppleSpeechOutput` hands
    /// its utterance straight to `AVSpeechSynthesizer`, which plays it
    /// itself), so this is the simplest correct fit for one-shot WAV data.
    func play(_ token: SpeechToken, done: @escaping () -> Void) {
        guard let data = token as? Data else { return done() }
        do {
            let newPlayer = try AVAudioPlayer(data: data)
            newPlayer.delegate = self
            newPlayer.isMeteringEnabled = onLevel != nil
            pendingDone = done
            player = newPlayer
            guard newPlayer.play() else {
                player = nil
                pendingDone = nil
                done()
                return
            }
            startMetering()
        } catch {
            // Decode failure on Kokoro's own WAV output — shouldn't happen,
            // but never crash: drop this sentence and let the queue move
            // on, same as a `prepare` failure.
            player = nil
            pendingDone = nil
            done()
        }
    }

    /// Stops whatever is playing right now. Safe to call with nothing in
    /// flight (matches the protocol's contract).
    func stopCurrent() {
        stopMetering()
        player?.stop()
        player = nil
        pendingDone = nil
    }

    /// A display-rate timer reading the playing sentence's meter. Runs only
    /// while a player exists; stopped on finish, error and `stopCurrent`.
    private func startMetering() {
        stopMetering()
        guard onLevel != nil else { return }
        let t = Timer(timeInterval: 1.0 / 30, repeats: true) { [weak self] _ in
            guard let self, let player = self.player, player.isPlaying else { return }
            player.updateMeters()
            self.onLevel?(AudioLevel.level(decibels: player.averagePower(forChannel: 0)))
        }
        RunLoop.main.add(t, forMode: .common)
        meterTimer = t
    }

    private func stopMetering() {
        meterTimer?.invalidate()
        meterTimer = nil
    }

    /// Applies `GET /v1/voice/profile` (R-27, fetched once at launch —
    /// AppDelegate's `fetchVoiceProfile`): an absent or empty
    /// `tts.kokoro_voice` leaves whatever voice this instance already has
    /// (the constructor argument, or Kokoro's own built-in default via
    /// `nil`) untouched. Mirrors `AppleSpeechOutput.applyVoiceProfile`'s
    /// shape exactly, so `EngineSelector`/AppDelegate can apply the same
    /// fetched profile to whichever `SpeechOutput` conformer is actually in
    /// use without needing to know which one it is.
    func applyVoiceProfile(_ profile: VoiceProfile) {
        if let kokoroVoice = profile.tts.kokoroVoice, !kokoroVoice.isEmpty {
            voice = kokoroVoice
        }
    }
}

extension KokoroSpeaker: AVAudioPlayerDelegate {
    func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        stopMetering()
        let done = pendingDone
        pendingDone = nil
        self.player = nil
        done?()
    }

    func audioPlayerDecodeErrorDidOccur(_ player: AVAudioPlayer, error: Error?) {
        stopMetering()
        let done = pendingDone
        pendingDone = nil
        self.player = nil
        done?()
    }
}

/// A speech engine whose output level can drive the Activity HUD's blob
/// (V-hud). Kept out of `SpeechOutput` (WaterClientCore), which stays
/// about ordering and barge-in only.
protocol SpeechLevelSource: AnyObject {
    /// Main thread; 0...1.
    var onLevel: ((Float) -> Void)? { get set }
}
