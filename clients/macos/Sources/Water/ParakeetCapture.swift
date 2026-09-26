import AVFoundation
import FluidAudio
import Foundation
import WaterClientCore

/// `SpeechCapture`'s second conformer: FluidAudio's Parakeet ASR
/// (`SlidingWindowAsrManager`), alongside the existing `OnDeviceSpeechCapture`
/// (`SFSpeechRecognizer`, in Voice.swift). `VoiceSession` (HoldToTalk.swift)
/// is unaware which one it's driving — that's the whole point of the
/// protocol — and is untouched by this type.
///
/// Gets audio the exact same way `OnDeviceSpeechCapture` does: a `MicTap`
/// (this project's one AVAudioEngine-input-tap wrapper, shared with meeting
/// capture — see AudioCapture.swift) whose buffers are handed straight to
/// the recognizer. There, that's `SFSpeechAudioBufferRecognitionRequest
/// .append`; here, it's `SlidingWindowAsrManager.streamAudio`.
///
/// One `SlidingWindowAsrManager` is held for this capture's whole lifetime
/// (constructor-injected, defaulted to a fresh one) rather than recreated
/// per hold, so Parakeet's model loads at most once across many holds;
/// loading is gated by `InitGate`, the same single-flight idiom
/// `KokoroSpeaker` uses for `KokoroAneManager.initialize()`.
///
/// This is not engine selection or the model-download consent flow (V-6):
/// like `KokoroSpeaker`, any load/streaming/finish failure here just reports
/// `.error(...)` through the ordinary `SpeechCapture` contract, which
/// `VoiceSession` already turns into a user-facing failure and a clean
/// return to idle — never a crash, never a silent retry loop. Falling back
/// to Apple Speech *permanently* for the install on such a failure is V-6's
/// job, not this conformer's.
final class ParakeetCapture: SpeechCapture {
    /// Single-flight guard around `manager.loadModels()`. `SlidingWindowAsrManager`
    /// is itself an actor, so two concurrent callers would simply serialize
    /// rather than race, but without this, a second `start()` issued before
    /// the first model load finished would re-run `loadModels()` (and its
    /// download-already-present check) for nothing. Mirrors
    /// `KokoroSpeaker.InitGate` exactly, including its retry-after-failure
    /// behavior (a transient failure, e.g. a network blip fetching models,
    /// isn't remembered — the next `start()` tries again).
    private actor InitGate {
        private var task: Task<Void, Error>?

        func ensureLoaded(_ manager: SlidingWindowAsrManager) async throws {
            if let task {
                try await task.value
                return
            }
            let started = Task { try await manager.loadModels() }
            task = started
            do {
                try await started.value
            } catch {
                task = nil
                throw error
            }
        }
    }

    private let manager: SlidingWindowAsrManager
    private let mic = MicTap()
    private let initGate = InitGate()

    /// This capture's own closure for whatever `start()` call is current.
    /// Matches `OnDeviceSpeechCapture`'s comment: it's `VoiceSession`'s own
    /// generation-tagged closure (see HoldToTalk.swift's `startCapture()`),
    /// so a late callback that still fires after this capture moved on can
    /// only ever reach a closure `VoiceSession` itself already drops as
    /// stale — never a later capture's result.
    private var onResult: ((SpeechResult) -> Void)?

    init(manager: SlidingWindowAsrManager = SlidingWindowAsrManager()) {
        self.manager = manager
        // A hold is short: if the input device changes mid-hold, end it with
        // a clear error rather than splice two audio formats into one
        // session — matches `OnDeviceSpeechCapture`.
        mic.onInterrupted = { [weak self] message in self?.onResult?(.error(message)) }
    }

    func start(onResult: @escaping (SpeechResult) -> Void) throws {
        cancel()
        // Mirrors `OnDeviceSpeechCapture`: open the mic first, and only keep
        // `onResult` once that succeeds, so a synchronous mic failure here
        // leaves nothing behind for a stale callback to use.
        try mic.start { [weak self] buffer in
            guard let self else { return }
            Task { await self.manager.streamAudio(buffer) }
        }
        self.onResult = onResult

        let manager = self.manager
        let initGate = self.initGate
        Task {
            do {
                try await initGate.ensureLoaded(manager)
                // A fresh sliding-window session per hold. `startStreaming()`
                // resets its own frame/token bookkeeping but — as of
                // FluidAudio 0.17.4 — not `volatileTranscript`/
                // `confirmedTranscript`; without this `reset()` first, a
                // hold that emits no tokens of its own (e.g. near-silence)
                // could have `finish()` fall back to a *previous* hold's
                // leftover transcript text instead of reporting empty.
                try await manager.reset()
                try await manager.startStreaming()
                let updates = await manager.transcriptionUpdates
                for await update in updates {
                    // Both volatile and confirmed segments are still
                    // in-progress from VoiceSession's point of view; only
                    // `finish()`'s return value (in `endAudio()` below) is
                    // the turn's one `.final` (see docs/slices/V.md §4).
                    await MainActor.run { onResult(.partial(update.text)) }
                }
            } catch {
                await MainActor.run {
                    onResult(.error((error as? LocalizedError)?.errorDescription ?? "\(error)"))
                }
            }
        }
    }

    func endAudio() {
        mic.stop()
        let manager = self.manager
        let onResult = self.onResult
        Task {
            do {
                let text = try await manager.finish()
                await MainActor.run { onResult?(.final(text)) }
            } catch {
                await MainActor.run {
                    onResult?(.error((error as? LocalizedError)?.errorDescription ?? "\(error)"))
                }
            }
        }
    }

    func cancel() {
        mic.stop()
        onResult = nil
        let manager = self.manager
        Task { await manager.cancel() }
    }
}
