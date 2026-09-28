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
/// Gets audio the same way `OnDeviceSpeechCapture` does: a `MicTap` (this
/// project's one AVAudioEngine-input-tap wrapper, see AudioCapture.swift).
/// Each hold is one `ParakeetSession` (below), which owns its own
/// `SlidingWindowAsrManager`.
///
/// Why a fresh manager per hold: as of FluidAudio 0.17.4 a
/// `SlidingWindowAsrManager` makes its audio input `AsyncStream` once, in
/// `init`, and both `finish()` and `cancel()` end that stream for good
/// (`reset()` doesn't recreate it). A manager kept for the capture's
/// lifetime is therefore dead after its first `finish()`/`cancel()`: every
/// later `streamAudio` is dropped and `finish()` returns "" forever. The
/// expensive part — the Core ML models (`AsrModels`) — is loaded once per
/// process by `ModelGate` and shared; a new manager only wraps them
/// (`loadModels(_:)` just stores references), so per-hold setup is cheap.
///
/// This is not engine selection or the model-download consent flow (V-6):
/// like `KokoroSpeaker`, any load/streaming/finish failure here just reports
/// `.error(...)` through the ordinary `SpeechCapture` contract, which
/// `VoiceSession` already turns into a user-facing failure and a clean
/// return to idle — never a crash, never a silent retry loop.
final class ParakeetCapture: SpeechCapture, LevelReportingCapture {
    /// Single-flight load of Parakeet's `AsrModels`, shared by every
    /// session. Mirrors `KokoroSpeaker.InitGate`, including its
    /// retry-after-failure behavior (a transient failure isn't remembered —
    /// the next hold tries again).
    actor ModelGate {
        private let directory: URL
        private var task: Task<AsrModels, Error>?
        /// Set once the models are loaded; readable synchronously (from the
        /// main thread) through `isLoaded`, for `isWarmingUp`.
        private nonisolated let loaded = LoadedFlag()

        init(directory: URL) { self.directory = directory }

        nonisolated var isLoaded: Bool { loaded.value }

        func models() async throws -> AsrModels {
            if let task { return try await task.value }
            // Water's own models folder. The files are already there:
            // EngineSelector only picks FluidAudio once they are, and its
            // download flow calls `load()` first.
            let directory = self.directory
            let started = Task { try await AsrModels.downloadAndLoad(to: directory) }
            task = started
            do {
                let models = try await started.value
                loaded.value = true
                return models
            } catch {
                task = nil
                throw error
            }
        }
    }

    /// A lock-guarded Bool shared across threads.
    final class LoadedFlag: @unchecked Sendable {
        private let lock = NSLock()
        private var v = false
        var value: Bool {
            get { lock.lock(); defer { lock.unlock() }; return v }
            set { lock.lock(); v = newValue; lock.unlock() }
        }
    }

    /// True until Parakeet's models are loaded (the prewarm at launch, or a
    /// first hold's own load): a hold's audio then waits in its session's
    /// pipe, and `VoiceSession` waits for the final instead of giving up at
    /// its usual 1.5s backstop (docs/slices/V.md §10).
    var isWarmingUp: Bool { !gate.isLoaded }

    private let mic: MicTap
    /// Where audio comes from: the mic, or (for `--asr-selftest`) a file.
    private let source: PCMSource
    private let gate: ModelGate

    /// The current hold's session, and the `onResult` it reports to.
    /// Main thread only. Each session keeps its own `onResult` (it's
    /// `VoiceSession`'s generation-tagged closure, see HoldToTalk.swift's
    /// `startCapture()`), and a cancelled session delivers nothing at all,
    /// so an old hold can never reach a newer one's closure.
    private var session: ParakeetSession?
    private var onResult: ((SpeechResult) -> Void)?

    var onLevel: ((Float) -> Void)? {
        get { mic.onLevel } set { mic.onLevel = newValue }
    }

    /// `source` defaults to the mic; `gate` to the process-wide Parakeet
    /// models in Water's folder.
    init(source: PCMSource? = nil, gate: ModelGate = ParakeetCapture.sharedGate) {
        let mic = MicTap()
        self.mic = mic
        self.source = source ?? mic
        self.gate = gate
        // A hold is short: if the input device changes mid-hold, end it with
        // a clear error rather than splice two audio formats into one
        // session — matches `OnDeviceSpeechCapture`.
        mic.onInterrupted = { [weak self] message in
            guard let self else { return }
            let onResult = self.onResult
            self.cancel()
            onResult?(.error(message))
        }
    }

    static let sharedGate = ModelGate(directory: SpeechModels.parakeetDirectory)

    /// Loads the models now (at most once; see `ModelGate`). The download
    /// flow awaits it inside its progress window.
    func load() async throws {
        _ = try await gate.models()
    }

    /// `load()` in the background, so the first hold after launch doesn't
    /// wait for it. A failure is ignored here; the next hold retries and
    /// reports it.
    func prewarm() {
        Task { try? await self.load() }
    }

    func start(onResult: @escaping (SpeechResult) -> Void) throws {
        cancel()
        let gate = self.gate
        let session = ParakeetSession(models: { try await gate.models() })
        // Mirrors `OnDeviceSpeechCapture`: open the mic first, and only keep
        // the session once that succeeds, so a synchronous mic failure here
        // leaves nothing behind.
        do {
            try source.start { buffer in session.feed(buffer) }
        } catch {
            session.cancel()
            throw error
        }
        self.session = session
        self.onResult = onResult
        session.run(
            onPartial: { text in onResult(.partial(text)) },
            onDone: { result in
                switch result {
                case .success(let text): onResult(.final(text))
                case .failure(let error): onResult(.error(ParakeetSession.describe(error)))
                }
            })
    }

    /// Closes the mic; the session decodes everything already piped (in
    /// order), then `finish()`es its own manager and reports `.final`.
    func endAudio() {
        source.stop()
        session?.endAudio()
    }

    /// Stops the current hold now; it delivers nothing further.
    func cancel() {
        source.stop()
        session?.cancel()
        session = nil
        onResult = nil
    }
}

/// Something that delivers PCM buffers until stopped: `MicTap`, or a file
/// for the self-test. `onBuffer` may run on any thread.
protocol PCMSource: AnyObject {
    func start(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws
    func stop()
}

extension MicTap: PCMSource {}

/// One hold's recognition: a fresh `SlidingWindowAsrManager` over the
/// shared models, fed through one serial pipe.
///
/// - `feed(_:)` is synchronous and thread-safe (called from the audio
///   thread): it copies the buffer and yields it into an unbounded
///   `AsyncStream`. One worker task drains that stream and awaits
///   `streamAudio` for each buffer in turn, so buffers reach the recognizer
///   in capture order, and audio fed while the models are still loading or
///   the manager is starting just waits in the pipe — the first words of a
///   hold aren't lost.
/// - `endAudio()` ends the pipe; the worker streams what's left, then
///   calls `finish()` on this session's manager and reports the text.
/// - `cancel()` ends the pipe, cancels the worker and silences every
///   further callback for this session.
///
/// Callbacks run on the main thread, partials strictly before the one
/// final result (`onDone`).
final class ParakeetSession: @unchecked Sendable {
    private let models: () async throws -> AsrModels
    private let input: AsyncStream<AVAudioPCMBuffer>
    private let pipe: AsyncStream<AVAudioPCMBuffer>.Continuation
    private var worker: Task<Void, Never>?
    private let lock = NSLock()
    private var cancelled = false

    init(models: @escaping () async throws -> AsrModels) {
        self.models = models
        (input, pipe) = AsyncStream<AVAudioPCMBuffer>.makeStream(bufferingPolicy: .unbounded)
    }

    private var isCancelled: Bool {
        lock.lock(); defer { lock.unlock() }
        return cancelled
    }

    /// Queues one buffer (copied: the tap's buffer isn't ours to keep).
    func feed(_ buffer: AVAudioPCMBuffer) {
        guard let copy = ParakeetSession.copy(buffer) else { return }
        pipe.yield(copy)
    }

    func endAudio() { pipe.finish() }

    func cancel() {
        lock.lock()
        cancelled = true
        lock.unlock()
        pipe.finish()
        worker?.cancel()
    }

    /// Starts the worker. Call once.
    func run(onPartial: @escaping (String) -> Void,
             onDone: @escaping (Result<String, Error>) -> Void) {
        let deliver: (@escaping () -> Void) -> Void = { [weak self] work in
            DispatchQueue.main.async {
                guard let self, !self.isCancelled else { return }
                work()
            }
        }
        worker = Task { [input, models] in
            let result: Result<String, Error>
            do {
                result = .success(try await Self.recognize(input: input, models: models,
                                                            onPartial: { text in deliver { onPartial(text) } }))
            } catch {
                result = .failure(error)
            }
            deliver { onDone(result) }
        }
    }

    /// The recognition itself: load/start a fresh manager, stream the pipe
    /// in order, finish.
    private static func recognize(input: AsyncStream<AVAudioPCMBuffer>,
                                  models: () async throws -> AsrModels,
                                  onPartial: @escaping (String) -> Void) async throws -> String {
        let loaded = try await models()
        try Task.checkCancellation()
        let manager = SlidingWindowAsrManager()
        try await manager.loadModels(loaded)
        try await manager.startStreaming()
        // Subscribe before any audio flows, so no update is missed. Both
        // volatile and confirmed updates are in-progress text for
        // VoiceSession; only `finish()`'s return is the turn's `.final`
        // (docs/slices/V.md §4).
        let updates = await manager.transcriptionUpdates
        let partials = Task { for await update in updates { onPartial(update.text) } }
        do {
            for await buffer in input {
                await manager.streamAudio(buffer)
            }
            try Task.checkCancellation()
            let text = try await manager.finish()
            // `finish()` leaves the updates stream open; `cancel()` closes
            // it, and waiting for `partials` keeps every partial ahead of
            // the final.
            await manager.cancel()
            await partials.value
            return text
        } catch {
            await manager.cancel()
            partials.cancel()
            throw error
        }
    }

    static func describe(_ error: Error) -> String {
        (error as? LocalizedError)?.errorDescription ?? "\(error)"
    }

    /// A deep copy of `buffer` (same format and frames), or nil if one
    /// can't be allocated.
    static func copy(_ buffer: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
        guard let out = AVAudioPCMBuffer(pcmFormat: buffer.format, frameCapacity: max(buffer.frameLength, 1))
        else { return nil }
        out.frameLength = buffer.frameLength
        let src = UnsafeMutableAudioBufferListPointer(UnsafeMutablePointer(mutating: buffer.audioBufferList))
        let dst = UnsafeMutableAudioBufferListPointer(out.mutableAudioBufferList)
        for (s, d) in zip(src, dst) {
            guard let sData = s.mData, let dData = d.mData else { continue }
            memcpy(dData, sData, Int(min(s.mDataByteSize, d.mDataByteSize)))
        }
        return out
    }
}
